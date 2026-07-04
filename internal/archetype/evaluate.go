package archetype

import (
	"sort"
)

// Artifact is one entry of the normalized telemetry inventory the adapter
// layer produces (docs/specs/archetype-library.md §3 step 2): a monitor's
// metric references, query strings, monitor type, tags, and
// adapter-assigned signal classes.
type Artifact struct {
	ID            string            // stable artifact id (source_ref native id)
	SignalClasses []string          // normalized classes, e.g. "http_server"
	MonitorType   string            // normalized monitor-type enum value
	MetricRefs    []string          // metric names and query strings, verbatim
	Tags          map[string]string // resource tags, verbatim
}

// tagString renders a tag pair in the "key=value" form the library's tag
// predicates use (e.g. "alertlint:archetype=rest-api").
func tagMatches(a Artifact, want string) bool {
	for k, v := range a.Tags {
		if k+"="+v == want {
			return true
		}
	}
	return false
}

// matchNode evaluates one node against one artifact and reports the
// strongest strength among matched leaf predicates ("" when no strength
// annotation or no match).
func matchNode(n *Node, a Artifact) (bool, string) {
	if n.Kind != "" {
		ok := matchLeaf(n, a)
		if !ok {
			return false, ""
		}
		return true, n.Strength
	}
	nested := Combinator{Any: n.Any, All: n.All, None: n.None}
	return matchCombinator(&nested, a)
}

func matchLeaf(n *Node, a Artifact) bool {
	switch n.Kind {
	case "signal_class":
		for _, sc := range a.SignalClasses {
			if sc == n.Equals {
				return true
			}
		}
	case "monitor_type":
		for _, mt := range n.In {
			if a.MonitorType == mt {
				return true
			}
		}
	case "metric_pattern":
		for _, ref := range a.MetricRefs {
			if n.re.MatchString(ref) {
				return true
			}
		}
	case "tag":
		return tagMatches(a, n.Equals)
	}
	return false
}

// matchCombinator applies any/all/none semantics. Strength propagates as
// the strongest strength among leaves that contributed to the match.
func matchCombinator(c *Combinator, a Artifact) (bool, string) {
	strongest := ""
	upgrade := func(s string) {
		if s == "strong" || (s == "weak" && strongest == "") {
			strongest = s
		}
	}

	if len(c.Any) > 0 {
		matched := false
		for i := range c.Any {
			if ok, s := matchNode(&c.Any[i], a); ok {
				matched = true
				upgrade(s)
			}
		}
		if !matched {
			return false, ""
		}
	}
	for i := range c.All {
		ok, s := matchNode(&c.All[i], a)
		if !ok {
			return false, ""
		}
		upgrade(s)
	}
	for i := range c.None {
		if ok, _ := matchNode(&c.None[i], a); ok {
			return false, ""
		}
	}
	if len(c.Any) == 0 && len(c.All) == 0 && len(c.None) == 0 {
		return false, "" // empty combinator matches nothing, never everything
	}
	return true, strongest
}

// Source records how an archetype's applicability was determined.
type Source string

const (
	SourceInferred  Source = "inferred"  // path A
	SourceEnriched  Source = "enriched"  // path C override
	SourceConfirmed Source = "confirmed" // path D override
)

// SignalResult is the satisfaction verdict for one required signal.
type SignalResult struct {
	SignalID        string   `json:"signal_id"`
	Anchor          string   `json:"anchor"`
	AbsenceSeverity string   `json:"absence_severity"`
	Satisfied       bool     `json:"satisfied"`
	SatisfiedBy     []string `json:"satisfied_by,omitempty"` // artifact ids, sorted
}

// Result is the full applicability + coverage outcome for one archetype.
type Result struct {
	ArchetypeID string `json:"archetype_id"`
	Applies     bool   `json:"applies"`
	// Suppressed is true when a negative override turned the archetype
	// off; recorded, never a silent drop (spec §4).
	Suppressed bool    `json:"suppressed,omitempty"`
	Source     Source  `json:"source"`
	Confidence float64 `json:"confidence,omitempty"`
	// MatchedArtifacts is the applicability evidence for inferred results:
	// which inventory artifacts matched applies_when. Sorted.
	MatchedArtifacts []string       `json:"matched_artifacts,omitempty"`
	Provenance       string         `json:"provenance,omitempty"` // override provenance text
	Signals          []SignalResult `json:"signals,omitempty"`
}

// Evaluate runs the full path-A pipeline for one service: overrides first,
// inference for the rest, then signal satisfaction for every applicable
// archetype. Deterministic: archetypes in library order, artifacts in
// input order, all output lists sorted (ADR 0003 / ADR 0005).
func Evaluate(lib *Library, inventory []Artifact, overrides []Override) []Result {
	byArchetype := indexOverrides(overrides)
	results := make([]Result, 0, len(lib.Archetypes))

	for i := range lib.Archetypes {
		arch := &lib.Archetypes[i]
		r := Result{ArchetypeID: arch.ID}

		if ov, ok := byArchetype[arch.ID]; ok {
			r.Source = ov.sourceEnum()
			r.Provenance = ov.Provenance
			if ov.Applies {
				r.Applies = true
				if r.Source == SourceConfirmed {
					r.Confidence = ConfidenceConfirmed
				} else {
					r.Confidence = ConfidenceEnriched
				}
			} else {
				r.Suppressed = true
			}
		} else {
			matched, strongest := inferApplies(arch, inventory)
			if len(matched) > 0 {
				r.Applies = true
				r.Source = SourceInferred
				r.MatchedArtifacts = matched
				if strongest == "strong" {
					r.Confidence = ConfidenceStrong
				} else {
					r.Confidence = ConfidenceWeak
				}
			} else {
				r.Source = SourceInferred // evaluated, did not apply
			}
		}

		if r.Applies {
			r.Signals = checkSignals(arch, inventory)
		}
		results = append(results, r)
	}
	return results
}

func inferApplies(arch *Archetype, inventory []Artifact) (matched []string, strongest string) {
	for _, a := range inventory {
		if ok, s := matchCombinator(&arch.AppliesWhen, a); ok {
			matched = append(matched, a.ID)
			if s == "strong" || (s == "weak" && strongest == "") {
				strongest = s
			}
		}
	}
	sort.Strings(matched)
	if strongest == "" && len(matched) > 0 {
		strongest = "weak" // matched predicates without strength annotations
	}
	return matched, strongest
}

func checkSignals(arch *Archetype, inventory []Artifact) []SignalResult {
	out := make([]SignalResult, 0, len(arch.RequiredSignals))
	for i := range arch.RequiredSignals {
		sig := &arch.RequiredSignals[i]
		sr := SignalResult{
			SignalID:        sig.ID,
			Anchor:          sig.Anchor,
			AbsenceSeverity: sig.AbsenceSeverity,
		}
		for _, a := range inventory {
			if ok, _ := matchCombinator(&sig.SatisfiedBy, a); ok {
				sr.SatisfiedBy = append(sr.SatisfiedBy, a.ID)
			}
		}
		sort.Strings(sr.SatisfiedBy)
		sr.Satisfied = len(sr.SatisfiedBy) > 0
		out = append(out, sr)
	}
	return out
}
