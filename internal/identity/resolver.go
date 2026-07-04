package identity

import (
	"fmt"
	"sort"

	"github.com/davetashner/alertlint/internal/model"
)

// Method confidences fixed by the tool (docs/specs/identity-resolution.md,
// strategy chain). Convention confidence is per-rule (default 0.8).
const (
	ConfidenceExact     = 1.0
	ConfidenceConfirmed = 0.95
)

// CI is one normalized CMDB configuration item from the inventory snapshot.
type CI struct {
	ID              string   `json:"ci_id"`
	Name            string   `json:"name"`
	Aliases         []string `json:"aliases,omitempty"`
	CriticalityTier *int     `json:"criticality_tier"` // nil when absent in the CMDB
	Status          string   `json:"status"`           // "operational" | "retired" | ...
}

// Inventory indexes a CI snapshot for exact-id and field lookups.
type Inventory struct {
	byID map[string]CI
	cis  []CI
}

// NewInventory builds lookup indexes over a CI snapshot.
func NewInventory(cis []CI) *Inventory {
	inv := &Inventory{byID: make(map[string]CI, len(cis)), cis: cis}
	for _, ci := range cis {
		inv.byID[ci.ID] = ci
	}
	return inv
}

// ByID returns the CI with the given canonical id.
func (inv *Inventory) ByID(id string) (CI, bool) {
	ci, ok := inv.byID[id]
	return ci, ok
}

// matchField returns all CIs whose lookup field (and optional secondary
// field) equals value exactly. Convention rules do their normalization in
// transforms; the inventory match itself is plain equality.
func (inv *Inventory) matchField(field, also, value string) []CI {
	var out []CI
	for _, ci := range inv.cis {
		if fieldEquals(ci, field, value) || (also != "" && fieldEquals(ci, also, value)) {
			out = append(out, ci)
		}
	}
	return out
}

func fieldEquals(ci CI, field, value string) bool {
	switch field {
	case "name":
		return ci.Name == value
	case "aliases":
		for _, a := range ci.Aliases {
			if a == value {
				return true
			}
		}
	}
	return false
}

// ArtifactRef is the stable identity of a source artifact — the mapping
// table's key (source, kind, key).
type ArtifactRef struct {
	Source string `json:"source"`
	Kind   string `json:"kind"`
	Key    string `json:"key"`
}

// DataClass tags an artifact with which provider interface produced it.
type DataClass string

const (
	ClassConfig  DataClass = "config"
	ClassHistory DataClass = "history"
	ClassAction  DataClass = "action"
)

// Artifact is one record to resolve.
type Artifact struct {
	Ref       ArtifactRef
	DataClass DataClass
	Hints     model.IdentityHints
}

// ConfirmedMapping is one entry of the confirmed-mappings file (strategy
// 2); the file loader ships with the identity-confirm command.
type ConfirmedMapping struct {
	Artifact ArtifactRef
	CIID     string
}

// ResolverConfig holds the org-specific knobs of the resolver itself.
type ResolverConfig struct {
	// CIIDTagKeys are tag keys whose value is an explicit CI identifier
	// (strategy 1), e.g. "ci_id".
	CIIDTagKeys []string
}

// MappingEvidence records what produced a mapping (REQ-NOISE-004 spirit).
type MappingEvidence struct {
	RuleID       string `json:"rule_id,omitempty"` // convention only
	Hint         string `json:"hint"`
	MatchedValue string `json:"matched_value,omitempty"`
}

// Mapping is one row of the join table the scoring stage uses.
type Mapping struct {
	Artifact   ArtifactRef     `json:"artifact"`
	DataClass  DataClass       `json:"data_class"`
	CIID       string          `json:"ci_id"`
	Method     string          `json:"method"` // exact | confirmed | convention
	Confidence float64         `json:"confidence"`
	Evidence   MappingEvidence `json:"evidence"`
}

// Identity finding subtypes emitted by the core strategies.
const (
	FindingStaleCIReference      = "stale_ci_reference"
	FindingDanglingCIReference   = "dangling_ci_reference"
	FindingAmbiguousCIReference  = "ambiguous_ci_reference"
	FindingAmbiguousConvention   = "ambiguous_convention_match"
	FindingStaleConfirmedMapping = "stale_confirmed_mapping"
)

// Finding is one identity finding (type: identity in the output contract).
type Finding struct {
	Subtype    string      `json:"subtype"`
	Artifact   ArtifactRef `json:"artifact"`
	Detail     string      `json:"detail"`
	Candidates []string    `json:"candidates,omitempty"` // CI ids, sorted
}

// Result is the resolver's output for one run.
type Result struct {
	Mappings []Mapping
	Findings []Finding
	// Unresolved artifacts matched no strategy; the fuzzy suggester (a
	// separate stage) consumes exactly this list. Nothing is dropped
	// silently (REQ-ID-003).
	Unresolved []Artifact
}

// Resolve runs strategies 1–3 in strictly decreasing confidence order for
// every artifact. The first strategy producing a scoring-eligible mapping
// wins. Deterministic: artifacts in input order, rules in file order.
func Resolve(artifacts []Artifact, inv *Inventory, confirmed []ConfirmedMapping, conv *Conventions, cfg ResolverConfig) Result {
	confirmedIdx := make(map[ArtifactRef]string, len(confirmed))
	for _, cm := range confirmed {
		confirmedIdx[cm.Artifact] = cm.CIID
	}

	var res Result
	for _, art := range artifacts {
		if done := resolveExact(art, inv, cfg, &res); done {
			continue
		}
		if done := resolveConfirmed(art, inv, confirmedIdx, &res); done {
			continue
		}
		if done := resolveConvention(art, inv, conv, &res); done {
			continue
		}
		res.Unresolved = append(res.Unresolved, art)
	}
	return res
}

// resolveExact implements strategy 1. Any explicit CI reference — valid or
// not — terminates the chain: a wrong explicit reference is a data-quality
// problem to surface, never to paper over with fuzzier strategies.
func resolveExact(art Artifact, inv *Inventory, cfg ResolverConfig, res *Result) bool {
	var refs []string
	var hint string
	for _, key := range cfg.CIIDTagKeys {
		if v, ok := art.Hints.Tags[key]; ok && v != "" {
			if !contains(refs, v) {
				refs = append(refs, v)
			}
			hint = key + "=" + v
		}
	}
	switch {
	case len(refs) == 0:
		return false // no explicit reference; chain continues
	case len(refs) > 1:
		sort.Strings(refs)
		res.Findings = append(res.Findings, Finding{
			Subtype:    FindingAmbiguousCIReference,
			Artifact:   art.Ref,
			Detail:     "artifact carries conflicting explicit CI references",
			Candidates: refs,
		})
		return true
	}
	ci, ok := inv.ByID(refs[0])
	switch {
	case !ok:
		res.Findings = append(res.Findings, Finding{
			Subtype:  FindingDanglingCIReference,
			Artifact: art.Ref,
			Detail:   fmt.Sprintf("explicit reference %q not found in CI inventory", refs[0]),
		})
	case ci.Status != "operational":
		res.Findings = append(res.Findings, Finding{
			Subtype:    FindingStaleCIReference,
			Artifact:   art.Ref,
			Detail:     fmt.Sprintf("referenced CI %q has status %q", ci.ID, ci.Status),
			Candidates: []string{ci.ID},
		})
	default:
		res.Mappings = append(res.Mappings, Mapping{
			Artifact: art.Ref, DataClass: art.DataClass, CIID: ci.ID,
			Method: "exact", Confidence: ConfidenceExact,
			Evidence: MappingEvidence{Hint: hint},
		})
	}
	return true
}

// resolveConfirmed implements strategy 2: pinned (source, kind, key)
// mappings from the confirmed-mappings file. A confirmed mapping whose CI
// vanished or retired is a stale_confirmed_mapping finding, not a join.
func resolveConfirmed(art Artifact, inv *Inventory, idx map[ArtifactRef]string, res *Result) bool {
	ciID, ok := idx[art.Ref]
	if !ok {
		return false
	}
	ci, exists := inv.ByID(ciID)
	if !exists || ci.Status != "operational" {
		res.Findings = append(res.Findings, Finding{
			Subtype:    FindingStaleConfirmedMapping,
			Artifact:   art.Ref,
			Detail:     fmt.Sprintf("confirmed mapping targets CI %q which is missing or not operational", ciID),
			Candidates: []string{ciID},
		})
		return true
	}
	res.Mappings = append(res.Mappings, Mapping{
		Artifact: art.Ref, DataClass: art.DataClass, CIID: ciID,
		Method: "confirmed", Confidence: ConfidenceConfirmed,
		Evidence: MappingEvidence{Hint: "confirmed-mappings entry"},
	})
	return true
}

// resolveConvention implements strategy 3: rules in file order, first rule
// whose transformed hint equal-matches exactly one CI wins. A multi-CI
// match emits ambiguous_convention_match and terminates the stage —
// deterministic rules must be deterministic in outcome.
func resolveConvention(art Artifact, inv *Inventory, conv *Conventions, res *Result) bool {
	if conv == nil {
		return false
	}
	for _, rule := range conv.Rules {
		if rule.Source != "*" && rule.Source != art.Ref.Source {
			continue
		}
		hintLabel, values := hintValues(rule, art.Hints)
		for _, raw := range values {
			transformed := rule.TransformValue(raw)
			matches := inv.matchField(rule.Lookup.Field, rule.Lookup.Also, transformed)
			if len(matches) == 0 {
				continue
			}
			if len(matches) > 1 {
				ids := make([]string, 0, len(matches))
				for _, ci := range matches {
					ids = append(ids, ci.ID)
				}
				sort.Strings(ids)
				res.Findings = append(res.Findings, Finding{
					Subtype:    FindingAmbiguousConvention,
					Artifact:   art.Ref,
					Detail:     fmt.Sprintf("rule %s matched %d CIs for value %q", rule.ID, len(matches), transformed),
					Candidates: ids,
				})
				return true
			}
			res.Mappings = append(res.Mappings, Mapping{
				Artifact: art.Ref, DataClass: art.DataClass, CIID: matches[0].ID,
				Method: "convention", Confidence: rule.Confidence,
				Evidence: MappingEvidence{
					RuleID:       rule.ID,
					Hint:         hintLabel + ":" + raw,
					MatchedValue: transformed,
				},
			})
			return true
		}
	}
	return false
}

// hintValues extracts the hint values a rule reads, in deterministic order.
func hintValues(rule Rule, hints model.IdentityHints) (label string, values []string) {
	switch rule.Match.Hint {
	case HintTag:
		if v, ok := hints.Tags[rule.Match.Key]; ok && v != "" {
			return "tag " + rule.Match.Key, []string{v}
		}
		return "tag " + rule.Match.Key, nil
	case HintServiceName:
		return "service_name", hints.Names
	case HintIntegrationLink:
		vals := make([]string, 0, len(hints.ExternalRefs))
		for _, er := range hints.ExternalRefs {
			vals = append(vals, er.NativeID)
		}
		return "integration_link", vals
	}
	return "", nil
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
