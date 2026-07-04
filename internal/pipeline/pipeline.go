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
	"os"
	"path/filepath"
	"sort"

	"github.com/davetashner/alertlint/internal/adapter"
	"github.com/davetashner/alertlint/internal/archetype"
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
	OutDir     string

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
	for _, cp := range opts.Registry.CIProviders() {
		for ci, err := range cp.FetchCIs(opts.Scope, opts.Window) {
			if err != nil {
				return res, fmt.Errorf("ci inventory from %s: %w", cp.ProviderID(), err)
			}
			cis = append(cis, ci)
		}
	}
	inventory := identity.NewInventory(cis)

	// 2. Pull canonical records; build the resolver's artifact list.
	pull, err := pullAll(opts)
	if err != nil {
		return res, err
	}

	// 3. Resolve artifacts to CIs.
	resolved := identity.Resolve(pull.artifacts, inventory, opts.Confirmed, opts.Convention, opts.Resolver)
	byCI := map[string][]identity.Mapping{}
	for _, m := range resolved.Mappings {
		byCI[m.CIID] = append(byCI[m.CIID], m)
	}

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
		doc := buildDocument(opts, ci, byCI[ciID], pull, resolved)
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
	unresolvedDoc := buildUnresolvedDocument(opts, resolved)
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

// pulled holds all canonical records of one run, keyed for joining.
type pulled struct {
	artifacts []identity.Artifact
	configs   map[identity.ArtifactRef]model.AlertConfig
	events    map[identity.ArtifactRef]model.AlertEvent
	responses map[identity.ArtifactRef]model.ResponseRecord
	sources   []output.SourceMeta
}

func pullAll(opts Options) (pulled, error) {
	p := pulled{
		configs:   map[identity.ArtifactRef]model.AlertConfig{},
		events:    map[identity.ArtifactRef]model.AlertEvent{},
		responses: map[identity.ArtifactRef]model.ResponseRecord{},
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

	for _, cp := range opts.Registry.ConfigProviders() {
		noteSource(cp)
		for rec, err := range cp.FetchConfigs(opts.Scope, opts.Window) {
			if err != nil {
				return p, fmt.Errorf("configs from %s: %w", cp.ProviderID(), err)
			}
			p.configs[add(rec.Envelope, identity.ClassConfig)] = rec
		}
	}
	for _, hp := range opts.Registry.HistoryProviders() {
		noteSource(hp)
		for rec, err := range hp.FetchEvents(opts.Scope, opts.Window) {
			if err != nil {
				return p, fmt.Errorf("events from %s: %w", hp.ProviderID(), err)
			}
			p.events[add(rec.Envelope, identity.ClassHistory)] = rec
		}
	}
	for _, ap := range opts.Registry.ActionProviders() {
		noteSource(ap)
		for rec, err := range ap.FetchResponses(opts.Scope, opts.Window) {
			if err != nil {
				return p, fmt.Errorf("responses from %s: %w", ap.ProviderID(), err)
			}
			p.responses[add(rec.Envelope, identity.ClassAction)] = rec
		}
	}
	sort.Slice(p.sources, func(i, j int) bool { return p.sources[i].Source < p.sources[j].Source })
	return p, nil
}
