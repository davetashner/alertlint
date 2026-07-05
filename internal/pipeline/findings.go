package pipeline

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/davetashner/alertlint/internal/archetype"
	"github.com/davetashner/alertlint/internal/identity"
	"github.com/davetashner/alertlint/internal/model"
	"github.com/davetashner/alertlint/internal/output"
	"github.com/davetashner/alertlint/internal/score"
)

// findingAssembler renders engine results into contract findings with the
// per-type required evidence keys (output-contract.md evidence table).
type findingAssembler struct {
	window output.Window
	cfg    score.Config
}

// noiseEvidence carries the contract's required noise keys.
type noiseEvidence struct {
	FireCount            int               `json:"fire_count"`
	WindowDays           int               `json:"window_days"`
	AckedCount           int               `json:"acked_count"`
	NoActionCount        int               `json:"no_action_count"`
	AutoResolvedCount    int               `json:"auto_resolved_count"`
	MedianTimeToResolveS *int64            `json:"median_time_to_resolve_s"`
	DispositionCounts    map[string]int    `json:"disposition_counts"`
	NoiseRatio           float64           `json:"noise_ratio"`
	ClassCounts          score.ClassCounts `json:"class_counts"`
	OffHoursFireCount    int               `json:"off_hours_fire_count"`
	OffHoursRatio        float64           `json:"off_hours_ratio"`
}

func (fa findingAssembler) noise(nf score.NoiseFinding, fires []score.Fire, classifications []score.FireClassification, cfg model.AlertConfig) output.Finding {
	ev := noiseEvidence{
		FireCount:         len(fires),
		WindowDays:        fa.window.Days,
		DispositionCounts: map[string]int{},
		NoiseRatio:        nf.Ratio,
		ClassCounts:       nf.Counts,
	}
	var resolveSecs []int64
	for _, f := range fires {
		if fa.cfg.OffHours.IsOffHours(f.Event.FiredAt) {
			ev.OffHoursFireCount++
		}
		if f.Response != nil && f.Response.AckedAt != nil {
			ev.AckedCount++
		}
		disp := "none"
		if f.Response != nil {
			disp = string(f.Response.Disposition)
		}
		ev.DispositionCounts[disp]++
		if disp == string(model.DispositionNoAction) {
			ev.NoActionCount++
		}
		if f.Event.AutoResolved != nil && *f.Event.AutoResolved {
			ev.AutoResolvedCount++
			if f.Event.ResolvedAt != nil {
				resolveSecs = append(resolveSecs, int64(f.Event.ResolvedAt.Sub(f.Event.FiredAt)/time.Second))
			}
		}
	}
	if len(resolveSecs) > 0 {
		sort.Slice(resolveSecs, func(i, j int) bool { return resolveSecs[i] < resolveSecs[j] })
		med := resolveSecs[(len(resolveSecs)-1)/2]
		ev.MedianTimeToResolveS = &med
	}
	if len(fires) > 0 {
		ev.OffHoursRatio = float64(ev.OffHoursFireCount) / float64(len(fires))
	}

	src, native := sourceOf(cfg, nf.AlertID)
	subject := output.Subject{Source: src, NativeID: native}
	rationale := fmt.Sprintf("%.0f%% of the confidence-weighted evidence over %d fires in %dd says noise (%d no-action, %d auto-resolved, %d acked).",
		nf.Ratio*100, len(fires), fa.window.Days, ev.NoActionCount, ev.AutoResolvedCount, ev.AckedCount)
	return output.Finding{
		ID:         output.FindingID("noise", subject, fa.window),
		Type:       "noise",
		Severity:   "high",
		Confidence: string(nf.Band),
		Subject:    subject,
		Rationale:  rationale,
		Evidence:   mustJSON(ev),
	}
}

// thresholdEvidence carries the contract's required threshold keys.
type thresholdEvidence struct {
	FireCount        int                     `json:"fire_count"`
	WindowDays       int                     `json:"window_days"`
	NoActionRatio    float64                 `json:"no_action_ratio"`
	CurrentThreshold *float64                `json:"current_threshold"`
	CurrentDurationS *int64                  `json:"current_duration_s"`
	RuleID           string                  `json:"rule_id"`
	Stats            score.ThresholdEvidence `json:"stats"`
}

func (fa findingAssembler) threshold(tf score.ThresholdFinding, cfg model.AlertConfig) output.Finding {
	src, native := sourceOf(cfg, tf.AlertID)
	subject := output.Subject{Source: src, NativeID: native}
	ev := thresholdEvidence{
		FireCount:        tf.Evidence.ClassifiedFireCount,
		WindowDays:       fa.window.Days,
		NoActionRatio:    tf.Evidence.NoActionRatio,
		CurrentThreshold: cfg.Threshold,
		CurrentDurationS: cfg.DurationS,
		RuleID:           tf.RuleID,
		Stats:            tf.Evidence,
	}
	return output.Finding{
		ID:         output.FindingID("threshold-"+tf.RuleID, subject, fa.window),
		Type:       "threshold",
		Severity:   tf.Severity,
		Confidence: string(tf.Band),
		Subject:    subject,
		Rationale:  tf.Rationale,
		Evidence:   mustJSON(ev),
	}
}

// coverageEvidence carries the contract's required coverage keys.
type coverageEvidence struct {
	Archetype          string   `json:"archetype"`
	ArchetypeSource    string   `json:"archetype_source"`
	MissingSignal      string   `json:"missing_signal"`
	ApplicabilityBasis string   `json:"applicability_basis"`
	MatchedArtifacts   []string `json:"matched_artifacts,omitempty"`
	AbsenceSeverity    string   `json:"absence_severity"`
}

func (fa findingAssembler) coverage(ar archetype.Result, sig archetype.SignalResult, partialMapping bool) output.Finding {
	signal := sig.SignalID
	subject := output.Subject{Signal: &signal}
	basis := ar.Provenance
	if basis == "" {
		basis = fmt.Sprintf("telemetry matched applies_when on %d artifact(s)", len(ar.MatchedArtifacts))
	}
	conf := score.CoverageFindingConfidence(ar.Confidence, partialMapping, fa.cfg)
	return output.Finding{
		ID:         output.FindingID("coverage-"+ar.ArchetypeID, subject, fa.window),
		Type:       "coverage",
		Severity:   sig.AbsenceSeverity,
		Confidence: string(fa.cfg.BandOf(conf)),
		Subject:    subject,
		Rationale: fmt.Sprintf("Service matches the %s archetype but has no %s alert (%s anchor).",
			ar.ArchetypeID, sig.SignalID, sig.Anchor),
		Evidence: mustJSON(coverageEvidence{
			Archetype:          ar.ArchetypeID,
			ArchetypeSource:    string(ar.Source),
			MissingSignal:      sig.SignalID,
			ApplicabilityBasis: basis,
			MatchedArtifacts:   ar.MatchedArtifacts,
			AbsenceSeverity:    sig.AbsenceSeverity,
		}),
	}
}

// suppressedArchetype records a negative override in the output — the
// suppression is visible, never a silent drop (archetype-library.md §4).
func (fa findingAssembler) suppressedArchetype(ar archetype.Result) output.Finding {
	signal := "(suppressed)"
	subject := output.Subject{Signal: &signal}
	basis := ar.Provenance
	if basis == "" {
		basis = "negative override without provenance"
	}
	return output.Finding{
		ID:         output.FindingID("coverage-suppressed-"+ar.ArchetypeID, subject, fa.window),
		Type:       "coverage",
		Severity:   "low",
		Confidence: "high",
		Subject:    subject,
		Rationale: fmt.Sprintf("Archetype %s was suppressed by a %s override; its coverage requirements were not evaluated.",
			ar.ArchetypeID, ar.Source),
		Evidence: mustJSON(coverageEvidence{
			Archetype:          ar.ArchetypeID,
			ArchetypeSource:    string(ar.Source),
			MissingSignal:      "(suppressed)",
			ApplicabilityBasis: basis,
		}),
	}
}

// identityEvidence carries the contract's required identity keys.
type identityEvidence struct {
	UnresolvedArtifact map[string]string `json:"unresolved_artifact"`
	Candidates         []map[string]any  `json:"candidates"`
	Reason             string            `json:"reason"`
	Subtype            string            `json:"subtype,omitempty"`
}

func (fa findingAssembler) missingCriticality(ci identity.CI) output.Finding {
	src := "servicenow"
	subject := output.Subject{Source: &src, NativeID: &ci.ID}
	return output.Finding{
		ID:         output.FindingID("identity-criticality", subject, fa.window),
		Type:       "identity",
		Severity:   "medium",
		Confidence: "high",
		Subject:    subject,
		Rationale: fmt.Sprintf("CI %s (%s) has no usable criticality tier in the CMDB; the configured default tier was applied (REQ-CRIT-003).",
			ci.ID, ci.Name),
		Evidence: mustJSON(identityEvidence{
			UnresolvedArtifact: map[string]string{"source": "servicenow", "kind": "ci", "native_id": ci.ID, "native_name": ci.Name},
			Candidates:         []map[string]any{},
			Reason:             "criticality_missing",
		}),
	}
}

func (fa findingAssembler) unresolvedArtifact(art identity.Artifact, candidates []identity.Candidate) output.Finding {
	subject := output.Subject{Source: &art.Ref.Source, NativeID: &art.Ref.Key}
	cands := make([]map[string]any, 0, len(candidates))
	for _, c := range candidates {
		cands = append(cands, map[string]any{
			"ci_id": c.CIID, "ci_name": c.CI, "match_score": c.Score, "method": "fuzzy",
		})
	}
	reason := "no_ci_reference"
	rationale := fmt.Sprintf("%s %s %q could not be attributed to any CI; its data joins no service until mapped.",
		art.Ref.Source, art.Ref.Kind, art.Ref.Key)
	if len(cands) > 0 {
		reason = "ambiguous_candidates"
		rationale = fmt.Sprintf("%s %s %q has no CI reference; fuzzy match suggests %d candidate(s). Confirmation required before its data can join scoring.",
			art.Ref.Source, art.Ref.Kind, art.Ref.Key, len(cands))
	}
	return output.Finding{
		ID:         output.FindingID("identity-unresolved", subject, fa.window),
		Type:       "identity",
		Severity:   "low",
		Confidence: "high",
		Subject:    subject,
		Rationale:  rationale,
		Evidence: mustJSON(identityEvidence{
			UnresolvedArtifact: map[string]string{"source": art.Ref.Source, "kind": art.Ref.Kind, "native_id": art.Ref.Key},
			Candidates:         cands,
			Reason:             reason,
		}),
	}
}

func (fa findingAssembler) identityFinding(f identity.Finding) output.Finding {
	subject := output.Subject{Source: &f.Artifact.Source, NativeID: &f.Artifact.Key}
	candidates := make([]map[string]any, 0, len(f.Candidates))
	for _, c := range f.Candidates {
		candidates = append(candidates, map[string]any{"ci_id": c})
	}
	return output.Finding{
		ID:         output.FindingID("identity-"+f.Subtype, subject, fa.window),
		Type:       "identity",
		Severity:   "medium",
		Confidence: "high",
		Subject:    subject,
		Rationale:  f.Detail,
		Evidence: mustJSON(identityEvidence{
			UnresolvedArtifact: map[string]string{"source": f.Artifact.Source, "kind": f.Artifact.Kind, "native_id": f.Artifact.Key},
			Candidates:         candidates,
			Reason:             "ambiguous_candidates",
			Subtype:            f.Subtype,
		}),
	}
}

func sortFindings(findings []output.Finding) {
	sort.Slice(findings, func(i, j int) bool {
		if findings[i].Type != findings[j].Type {
			return findings[i].Type < findings[j].Type
		}
		return findings[i].ID < findings[j].ID
	})
}

func sourceOf(cfg model.AlertConfig, alertID string) (*string, *string) {
	if cfg.Source.Provider == "" {
		id := alertID
		return nil, &id
	}
	src := cfg.Source.Provider
	native := cfg.SourceRef.Kind + ":" + cfg.SourceRef.NativeID
	return &src, &native
}

func mustJSON(v any) json.RawMessage {
	buf, err := json.Marshal(v)
	if err != nil {
		panic(err) // structs above are always marshalable
	}
	return buf
}
