package score

import (
	"time"

	"github.com/davetashner/alertlint/internal/model"
)

// Class is the noise classification of a single fire.
type Class string

const (
	ClassNoise        Class = "noise"
	ClassActionable   Class = "actionable"
	ClassUnclear      Class = "unclear"      // noise-leaning, N-T3
	ClassUnclassified Class = "unclassified" // excluded from ratios, counted in evidence
)

// Rule confidences fixed by the decision table in
// docs/specs/scoring-engine.md but not exposed as config keys. They are
// part of scoring_config_version semantics: changing one requires a
// version bump exactly like a config constant.
const (
	confAckedAfterAutoResolve = 0.60 // N-D4
	confNeverAckedNoAutoClose = 0.45 // N-T2
	confHighReassignment      = 0.40 // N-T3
	confUnclassified          = 0.20 // N-T4
)

// Fire is one firing episode joined to its (optional) human-response trail
// — the scoring engine's per-fire input. Response is nil when no
// ActionProvider record could be joined to the event.
type Fire struct {
	Event    model.AlertEvent
	Response *model.ResponseRecord
}

// Evidence carries the raw facts the matched rule saw (REQ-NOISE-004).
// Values the rule did not consult are still populated when derivable — the
// skill reasons over evidence without recomputing (ADR 0003).
type Evidence struct {
	Disposition        *model.Disposition   `json:"disposition,omitempty"`
	DispositionNative  *string              `json:"disposition_native,omitempty"`
	AckDelaySeconds    *int64               `json:"ack_delay_seconds,omitempty"`
	CloseDelaySeconds  *int64               `json:"close_delay_seconds,omitempty"`
	AutoResolved       *bool                `json:"auto_resolved,omitempty"`
	AutoResolveSeconds *int64               `json:"auto_resolve_seconds,omitempty"`
	ReassignmentCount  *int64               `json:"reassignment_count,omitempty"`
	LinkedRecords      []model.LinkedRecord `json:"linked_records,omitempty"`
}

// FireClassification is the decision-table output for one fire.
type FireClassification struct {
	Class         Class    `json:"class"`
	RuleID        string   `json:"rule_id"`
	Confidence    float64  `json:"confidence"`
	LowConfidence bool     `json:"low_confidence"`
	Evidence      Evidence `json:"evidence"`
}

// Classify runs the fixed, ordered, first-match-wins decision table
// N-D1..N-T4 from docs/specs/scoring-engine.md. Deterministic: same fire
// and config always produce the same classification (ADR 0003).
func Classify(f Fire, cfg Config) FireClassification {
	ev := gatherEvidence(f)
	resp := f.Response

	disposition := model.DispositionUnknown
	if resp != nil {
		disposition = resp.Disposition
	}
	acked := resp != nil && resp.AckedAt != nil
	closed := resp != nil && resp.ClosedAt != nil
	autoResolved := f.Event.AutoResolved != nil && *f.Event.AutoResolved

	// N-D1: explicit no-action close code — the primary REQ-NOISE-001 signal.
	if disposition == model.DispositionNoAction {
		return result(ClassNoise, "N-D1", cfg.Confidence.DispositionNoAction, cfg, ev)
	}

	// N-D2: a change or incident record is linked to the fire.
	if resp != nil {
		for _, lr := range resp.LinkedRecords {
			if lr.Kind == model.LinkedChange || lr.Kind == model.LinkedIncident {
				return result(ClassActionable, "N-D2", cfg.Confidence.LinkedChangeOrIncident, cfg, ev)
			}
		}
	}

	// N-D3: acked, manually closed, substantive disposition code present.
	if acked && closed && !autoResolved && substantive(disposition) {
		return result(ClassActionable, "N-D3", cfg.Confidence.AckedManualClose, cfg, ev)
	}

	// N-D4: auto-resolved and acked after the auto-resolve — a human
	// arrived to a self-closed alert.
	if autoResolved && acked && f.Event.ResolvedAt != nil && resp.AckedAt.After(*f.Event.ResolvedAt) {
		return result(ClassNoise, "N-D4", confAckedAfterAutoResolve, cfg, ev)
	}

	neverAcked := !ackedWithin(f, time.Duration(cfg.Noise.NeverAckedGraceMinutes)*time.Minute)
	noCode := disposition == model.DispositionUnknown

	// N-T1: the REQ-NOISE-003 ambiguity default — never acked, fast
	// auto-resolve, no disposition code. Scored noise, tagged
	// low_confidence so the skill can adjudicate self-healing vs. noise.
	if neverAcked && noCode &&
		autoResolvedWithin(f.Event, time.Duration(cfg.Noise.FastAutoResolveMinutes)*time.Minute) {
		return result(ClassNoise, "N-T1", cfg.Confidence.AmbiguityDefault, cfg, ev)
	}

	// N-T2: never acked, no auto-resolve, closed without a disposition code.
	if neverAcked && !autoResolved && closed && noCode {
		return result(ClassNoise, "N-T2", confNeverAckedNoAutoClose, cfg, ev)
	}

	// N-T3: acked but heavily reassigned before close — noise-leaning.
	if acked && resp.ReassignmentCount >= int64(cfg.Noise.HighReassignmentCount) {
		return result(ClassUnclear, "N-T3", confHighReassignment, cfg, ev)
	}

	// N-T4: anything else.
	return result(ClassUnclassified, "N-T4", confUnclassified, cfg, ev)
}

func result(class Class, rule string, confidence float64, cfg Config, ev Evidence) FireClassification {
	return FireClassification{
		Class:         class,
		RuleID:        rule,
		Confidence:    confidence,
		LowConfidence: cfg.LowConfidence(confidence),
		Evidence:      ev,
	}
}

// substantive dispositions indicate a deliberate human close: everything in
// the taxonomy except no_action (N-D1's business), auto_closed (no human),
// and unknown (no signal).
func substantive(d model.Disposition) bool {
	switch d {
	case model.DispositionActionTaken, model.DispositionEscalated,
		model.DispositionDuplicate, model.DispositionKnownIssue:
		return true
	}
	return false
}

// ackedWithin reports whether the fire was acknowledged within grace of
// firing. No response or no ack timestamp means never acked.
func ackedWithin(f Fire, grace time.Duration) bool {
	if f.Response == nil || f.Response.AckedAt == nil {
		return false
	}
	return !f.Response.AckedAt.After(f.Event.FiredAt.Add(grace))
}

// autoResolvedWithin reports whether the event auto-resolved within limit
// of firing.
func autoResolvedWithin(e model.AlertEvent, limit time.Duration) bool {
	if e.AutoResolved == nil || !*e.AutoResolved || e.ResolvedAt == nil {
		return false
	}
	return !e.ResolvedAt.After(e.FiredAt.Add(limit))
}

func gatherEvidence(f Fire) Evidence {
	var ev Evidence
	ev.AutoResolved = f.Event.AutoResolved
	if f.Event.AutoResolved != nil && *f.Event.AutoResolved && f.Event.ResolvedAt != nil {
		secs := int64(f.Event.ResolvedAt.Sub(f.Event.FiredAt) / time.Second)
		ev.AutoResolveSeconds = &secs
	}
	if r := f.Response; r != nil {
		d := r.Disposition
		ev.Disposition = &d
		ev.DispositionNative = r.DispositionNative
		if r.AckedAt != nil {
			secs := int64(r.AckedAt.Sub(f.Event.FiredAt) / time.Second)
			ev.AckDelaySeconds = &secs
		}
		if r.ClosedAt != nil {
			secs := int64(r.ClosedAt.Sub(f.Event.FiredAt) / time.Second)
			ev.CloseDelaySeconds = &secs
		}
		count := r.ReassignmentCount
		ev.ReassignmentCount = &count
		ev.LinkedRecords = r.LinkedRecords
	}
	return ev
}
