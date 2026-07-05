// Package pipeline wires the full analyze flow: adapters → resolver →
// archetype/scoring → per-service output documents
// (docs/specs/output-contract.md; the composition mirrors the golden-test
// harness in internal/score).
//
// Determinism: CIs are processed in sorted id order, artifacts in adapter
// order (adapters guarantee deterministic iteration), and all derived
// collections are sorted before emission. Run timestamp and invocation id
// are inputs — the pipeline never reads the wall clock (ADR 0003).
package pipeline

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/davetashner/alertlint/internal/adapter"
	"github.com/davetashner/alertlint/internal/archetype"
	"github.com/davetashner/alertlint/internal/cache"
	"github.com/davetashner/alertlint/internal/identity"
	"github.com/davetashner/alertlint/internal/model"
	"github.com/davetashner/alertlint/internal/output"
	"github.com/davetashner/alertlint/internal/score"
)

// UnjoinedAlertID is the pseudo-alert that carries fires which could not
// be joined to any alert config: real firing burden never disappears from
// scoring just because the config join failed (REQ-ID-003 spirit).
const UnjoinedAlertID = "_unjoined_events"

// Options configures one pipeline run.
type Options struct {
	Registry   *adapter.Registry
	Scope      adapter.Scope
	Window     adapter.TimeWindow
	Config     score.Config
	Library    *archetype.Library
	Overrides  []archetype.Override
	Convention *identity.Conventions
	Confirmed  []identity.ConfirmedMapping
	Resolver   identity.ResolverConfig
	Fuzzy      identity.FuzzyConfig
	OutDir     string
	// Log, when non-nil, receives per-source pull summaries — long pulls
	// should never look hung. Counts only, no clock reads (ADR 0003).
	Log io.Writer

	// Cache, when non-nil, records every source's canonical records into
	// per-provider snapshot writers and seals manifests (ADR 0004). Raw
	// pages arrive via the adapters' Recorder hooks, wired by the CLI at
	// construction; the same Writer receives both.
	Cache map[string]*SourceCache

	// Run provenance — passed in, never read from the clock.
	RunMeta output.Run
}

// Result summarizes one run.
type Result struct {
	Documents  []string // written file paths, sorted
	Services   int
	Unresolved int
}

// Run executes the pipeline and writes one document per resolved CI plus
// the reserved _unresolved.json.
func Run(opts Options) (Result, error) {
	var res Result

	// 1. CI inventory.
	var cis []identity.CI
	ciCounts := map[string]int{}
	for _, cp := range opts.Registry.CIProviders() {
		for ci, err := range cp.FetchCIs(opts.Scope, opts.Window) {
			if err != nil {
				if sc := opts.Cache[cp.ProviderID()]; sc != nil {
					_, _ = sc.Writer.Seal(sc.Key, opts.RunMeta.ToolVersion, model.CanonicalSchemaVersion, cache.StatusFailed)
				}
				return res, fmt.Errorf("ci inventory from %s: %w", cp.ProviderID(), err)
			}
			if sc := opts.Cache[cp.ProviderID()]; sc != nil {
				if err := sc.Writer.WriteRecord("cis", ci); err != nil {
					return res, err
				}
			}
			ciCounts[cp.ProviderID()]++
			cis = append(cis, ci)
		}
	}
	if opts.Log != nil {
		providers := make([]string, 0, len(ciCounts))
		for prov := range ciCounts {
			providers = append(providers, prov)
		}
		sort.Strings(providers)
		for _, prov := range providers {
			fmt.Fprintf(opts.Log, "%s: %d cis\n", prov, ciCounts[prov])
		}
	}
	inventory := identity.NewInventory(cis)

	// 2. Pull canonical records; build the resolver's artifact list.
	pull, err := pullAll(opts)
	if err != nil {
		return res, err
	}

	// 3. Resolve artifacts to CIs; fuzzy-suggest over the unresolved
	// queue (strategy 4 — findings only, never mappings).
	resolved := identity.Resolve(pull.artifacts, inventory, opts.Confirmed, opts.Convention, opts.Resolver)
	suggestions := identity.Suggest(resolved.Unresolved, inventory, opts.Fuzzy)
	suggestedCounts := map[string]map[identity.DataClass]int{}
	for _, sg := range suggestions {
		for _, c := range sg.Candidates {
			if suggestedCounts[c.CIID] == nil {
				suggestedCounts[c.CIID] = map[identity.DataClass]int{}
			}
			suggestedCounts[c.CIID][sg.Artifact.DataClass]++
		}
	}
	byCI := map[string][]identity.Mapping{}
	for _, m := range resolved.Mappings {
		byCI[m.CIID] = append(byCI[m.CIID], m)
	}

	// Shared-monitor membership (ADR 0006): a config belongs to every CI
	// whose events reference it, in addition to the CI its own mapping
	// resolved to. Fires already attribute per event; membership restores
	// config context (cold-start, thresholds, archetypes) to each member.
	members := configMembership(byCI, pull)

	ciIDs := make([]string, 0, len(byCI))
	for id := range byCI {
		ciIDs = append(ciIDs, id)
	}
	sort.Strings(ciIDs)

	if err := os.MkdirAll(opts.OutDir, 0o755); err != nil {
		return res, err
	}

	// 4. Score and emit one document per CI.
	for _, ciID := range ciIDs {
		ci, _ := inventory.ByID(ciID)
		doc := buildDocument(opts, ci, byCI[ciID], pull, resolved, suggestedCounts[ciID], members)
		buf, err := output.Marshal(doc)
		if err != nil {
			return res, fmt.Errorf("marshal %s: %w", ciID, err)
		}
		path := filepath.Join(opts.OutDir, output.Filename(ci.Name, ci.ID))
		if err := os.WriteFile(path, buf, 0o644); err != nil {
			return res, err
		}
		res.Documents = append(res.Documents, path)
		res.Services++
	}

	// 5. The reserved unresolved document (never a silent drop).
	unresolvedDoc := buildUnresolvedDocument(opts, resolved, suggestions)
	buf, err := output.Marshal(unresolvedDoc)
	if err != nil {
		return res, err
	}
	path := filepath.Join(opts.OutDir, output.UnresolvedDocumentName)
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		return res, err
	}
	res.Documents = append(res.Documents, path)
	res.Unresolved = len(resolved.Unresolved)
	sort.Strings(res.Documents)
	return res, nil
}

// SourceCache pairs a provider's snapshot writer with its key so the
// pipeline can seal manifests after each source completes.
type SourceCache struct {
	Writer *cache.Writer
	Key    cache.Key
}

// configMembership maps each config artifact to the set of CIs that own
// it: the CI its own mapping resolved to plus every CI with at least one
// event referencing it by alert_ref id or name (ADR 0006).
func configMembership(byCI map[string][]identity.Mapping, pull pulled) map[identity.ArtifactRef]map[string]bool {
	members := map[identity.ArtifactRef]map[string]bool{}
	add := func(ref identity.ArtifactRef, ci string) {
		if members[ref] == nil {
			members[ref] = map[string]bool{}
		}
		members[ref][ci] = true
	}
	for ciID, mappings := range byCI {
		for _, m := range mappings {
			switch m.DataClass {
			case identity.ClassConfig:
				if _, ok := pull.configs[m.Artifact]; ok {
					add(m.Artifact, ciID)
				}
			case identity.ClassHistory:
				ev, ok := pull.events[m.Artifact]
				if !ok {
					continue
				}
				if ref, found := referencedConfig(ev, pull); found {
					add(ref, ciID)
				}
			}
		}
	}
	return members
}

// referencedConfig finds the pulled config an event's alert_ref points
// at: exact (provider, native id) first, then name (paging integrations
// often preserve only the monitor name). Deterministic: candidates are
// scanned in sorted key order.
func referencedConfig(ev model.AlertEvent, pull pulled) (identity.ArtifactRef, bool) {
	refs := make([]identity.ArtifactRef, 0, len(pull.configs))
	for ref := range pull.configs {
		refs = append(refs, ref)
	}
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].Source != refs[j].Source {
			return refs[i].Source < refs[j].Source
		}
		return refs[i].Key < refs[j].Key
	})
	if ev.AlertRef.NativeID != nil {
		for _, ref := range refs {
			if ref.Key == *ev.AlertRef.NativeID &&
				(ev.AlertRef.Provider == nil || ref.Source == *ev.AlertRef.Provider) {
				return ref, true
			}
		}
	}
	if ev.AlertRef.Name != nil {
		for _, ref := range refs {
			if pull.configs[ref].Name == *ev.AlertRef.Name {
				return ref, true
			}
		}
	}
	return identity.ArtifactRef{}, false
}

// pulled holds all canonical records of one run, keyed for joining.
type pulled struct {
	artifacts   []identity.Artifact
	configs     map[identity.ArtifactRef]model.AlertConfig
	events      map[identity.ArtifactRef]model.AlertEvent
	responses   map[identity.ArtifactRef]model.ResponseRecord
	maintenance []model.MaintenanceWindow
	sources     []output.SourceMeta
	counts      map[string]map[string]int // provider -> class -> records
}

func pullAll(opts Options) (pulled, error) {
	p := pulled{
		configs:   map[identity.ArtifactRef]model.AlertConfig{},
		events:    map[identity.ArtifactRef]model.AlertEvent{},
		responses: map[identity.ArtifactRef]model.ResponseRecord{},
		counts:    map[string]map[string]int{},
	}
	count := func(provider, class string) {
		if p.counts[provider] == nil {
			p.counts[provider] = map[string]int{}
		}
		p.counts[provider][class]++
	}
	add := func(env model.Envelope, class identity.DataClass) identity.ArtifactRef {
		ref := identity.ArtifactRef{Source: env.Source.Provider, Kind: env.SourceRef.Kind, Key: env.SourceRef.NativeID}
		p.artifacts = append(p.artifacts, identity.Artifact{Ref: ref, DataClass: class, Hints: env.IdentityHints})
		return ref
	}

	seenSource := map[string]bool{}
	noteSource := func(prov adapter.Provider) {
		if !seenSource[prov.ProviderID()] {
			seenSource[prov.ProviderID()] = true
			p.sources = append(p.sources, output.SourceMeta{
				Source:                 prov.ProviderID(),
				AdapterVersion:         opts.RunMeta.ToolVersion,
				CanonicalSchemaVersion: prov.SchemaVersion(),
				SnapshotKey:            fmt.Sprintf("%s/%s/%s_%s", prov.ProviderID(), opts.Scope.Tenant, opts.Window.Start.Format("2006-01-02"), opts.Window.End.Format("2006-01-02")),
			})
		}
	}

	record := func(provider, class string, rec any) error {
		sc := opts.Cache[provider]
		if sc == nil {
			return nil
		}
		return sc.Writer.WriteRecord(class, rec)
	}
	fail := func(provider string, err error) error {
		if sc := opts.Cache[provider]; sc != nil {
			// Failed pulls are sealed failed: never presented as usable
			// snapshots, but raw pages survive for regeneration.
			_, _ = sc.Writer.Seal(sc.Key, opts.RunMeta.ToolVersion, model.CanonicalSchemaVersion, cache.StatusFailed)
		}
		return err
	}

	for _, cp := range opts.Registry.ConfigProviders() {
		noteSource(cp)
		for rec, err := range cp.FetchConfigs(opts.Scope, opts.Window) {
			if err != nil {
				return p, fail(cp.ProviderID(), fmt.Errorf("configs from %s: %w", cp.ProviderID(), err))
			}
			if err := record(cp.ProviderID(), "configs", rec); err != nil {
				return p, err
			}
			count(cp.ProviderID(), "configs")
			p.configs[add(rec.Envelope, identity.ClassConfig)] = rec
		}
	}
	// History pulls collect first, then dedup: when paging history (any
	// non-config-source event) already covers a monitor via alert_ref,
	// monitor-side episodes for that monitor are dropped — paging history
	// is authoritative because it carries the response trail
	// (REQ-SRC-008; provider-adapters.md dedup rule).
	var allEvents []model.AlertEvent
	for _, hp := range opts.Registry.HistoryProviders() {
		noteSource(hp)
		for rec, err := range hp.FetchEvents(opts.Scope, opts.Window) {
			if err != nil {
				return p, fail(hp.ProviderID(), fmt.Errorf("events from %s: %w", hp.ProviderID(), err))
			}
			if err := record(hp.ProviderID(), "events", rec); err != nil {
				return p, err
			}
			count(hp.ProviderID(), "events")
			allEvents = append(allEvents, rec)
		}
	}
	// Paging providers are the ones that also carry response trails
	// (capability-discovered); their events are authoritative. Monitor
	// history from config-side providers referencing the same monitor —
	// by native id or by name, since paging integrations often preserve
	// only the name — is a duplicate stream and is dropped.
	// An event is monitor-side history when its alert_ref points back at
	// its own source (the monitor observing itself); everything else is
	// paging-system history and is authoritative. Paging integrations
	// often preserve only the monitor name, so paged keys cover both the
	// (provider, native_id) form and the name form.
	monitorSide := func(rec model.AlertEvent) bool {
		return rec.AlertRef.Provider != nil && *rec.AlertRef.Provider == rec.Source.Provider
	}
	pagedMonitors := map[string]bool{}
	for _, rec := range allEvents {
		if monitorSide(rec) {
			continue
		}
		if rec.AlertRef.Provider != nil && rec.AlertRef.NativeID != nil {
			pagedMonitors["id\x00"+*rec.AlertRef.Provider+"\x00"+*rec.AlertRef.NativeID] = true
		}
		if rec.AlertRef.Name != nil {
			pagedMonitors["name\x00"+*rec.AlertRef.Name] = true
		}
	}
	for _, rec := range allEvents {
		if monitorSide(rec) {
			dup := false
			if rec.AlertRef.NativeID != nil &&
				pagedMonitors["id\x00"+*rec.AlertRef.Provider+"\x00"+*rec.AlertRef.NativeID] {
				dup = true
			}
			if rec.AlertRef.Name != nil && pagedMonitors["name\x00"+*rec.AlertRef.Name] {
				dup = true
			}
			if dup {
				continue // monitor-side duplicate of a paged episode stream
			}
		}
		p.events[add(rec.Envelope, identity.ClassHistory)] = rec
	}
	for _, ap := range opts.Registry.ActionProviders() {
		noteSource(ap)
		for rec, err := range ap.FetchResponses(opts.Scope, opts.Window) {
			if err != nil {
				return p, fail(ap.ProviderID(), fmt.Errorf("responses from %s: %w", ap.ProviderID(), err))
			}
			if err := record(ap.ProviderID(), "responses", rec); err != nil {
				return p, err
			}
			count(ap.ProviderID(), "responses")
			p.responses[add(rec.Envelope, identity.ClassAction)] = rec
		}
	}
	for _, mp := range opts.Registry.MaintenanceProviders() {
		noteSource(mp)
		for mw, err := range mp.FetchMaintenance(opts.Scope, opts.Window) {
			if err != nil {
				return p, fail(mp.ProviderID(), fmt.Errorf("maintenance from %s: %w", mp.ProviderID(), err))
			}
			if err := record(mp.ProviderID(), "maintenance", mw); err != nil {
				return p, err
			}
			count(mp.ProviderID(), "maintenance")
			p.maintenance = append(p.maintenance, mw)
		}
	}
	for provider, sc := range opts.Cache {
		if _, err := sc.Writer.Seal(sc.Key, opts.RunMeta.ToolVersion, model.CanonicalSchemaVersion, cache.StatusComplete); err != nil {
			return p, fmt.Errorf("seal snapshot for %s: %w", provider, err)
		}
	}
	for i := range p.sources {
		if c := p.counts[p.sources[i].Source]; len(c) > 0 {
			p.sources[i].RecordCounts = c
		}
	}
	sort.Slice(p.sources, func(i, j int) bool { return p.sources[i].Source < p.sources[j].Source })
	if opts.Log != nil {
		for _, src := range p.sources {
			classes := make([]string, 0, len(src.RecordCounts))
			for class := range src.RecordCounts {
				classes = append(classes, class)
			}
			sort.Strings(classes)
			line := src.Source + ":"
			total := 0
			for _, class := range classes {
				line += fmt.Sprintf(" %d %s", src.RecordCounts[class], class)
				total += src.RecordCounts[class]
			}
			if total == 0 {
				line += " no records"
			}
			fmt.Fprintln(opts.Log, line)
		}
	}
	return p, nil
}
