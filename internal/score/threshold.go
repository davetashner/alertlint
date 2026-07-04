package score

import (
	"sort"
	"time"

	"github.com/davetashner/alertlint/internal/adapter"
)

// Threshold-rule confidences: behavior-inferred heuristics are firm
// pattern matches over measured statistics, but the *diagnosis* is
// inference — medium band, above the low-confidence ceiling, below firm
// disposition evidence. Version-bump gated like every scoring constant.
const (
	confThresholdRule = 0.70
)

// ThresholdFinding is one type:threshold finding for an alert.
type ThresholdFinding struct {
	AlertID       string            `json:"alert_id"`
	RuleID        string            `json:"rule_id"` // TH-1..TH-4
	Severity      string            `json:"severity"`
	Confidence    float64           `json:"confidence"`
	Band          Band              `json:"band"`
	LowConfidence bool              `json:"low_confidence"`
	Rationale     string            `json:"rationale"`
	Evidence      ThresholdEvidence `json:"evidence"`
}

// ThresholdEvidence carries the measured statistics that matched.
type ThresholdEvidence struct {
	FiresPerWeek        float64 `json:"fires_per_week"`
	NoActionRatio       float64 `json:"no_action_ratio,omitempty"`
	P50AutoResolveMin   float64 `json:"p50_auto_resolve_minutes,omitempty"`
	BurstDays           int     `json:"burst_days,omitempty"`
	MaxFiresInOneDay    int     `json:"max_fires_in_one_day,omitempty"`
	ClassifiedFireCount int     `json:"classified_fire_count"`
}

// AlertThresholdInput is the per-alert statistics bundle the heuristics
// read. It reuses the noise classifications (the engine computes
// statistics once; both sub-scores read them, keeping evidence
// consistent across findings).
type AlertThresholdInput struct {
	AlertID         string
	Fires           []Fire
	Classifications []FireClassification
}

// ThresholdForAlert evaluates TH-1..TH-4 for one scoreable alert. Rules
// are independent: an alert can match several; all matches are emitted.
func ThresholdForAlert(in AlertThresholdInput, window adapter.TimeWindow, cfg Config) []ThresholdFinding {
	weeks := WindowWeeks(window)
	stats, allClassifiedNoise := computeStats(in, weeks, cfg.Threshold.BurstFiresPerDay)
	var out []ThresholdFinding

	emit := func(rule, severity, rationale string) {
		out = append(out, ThresholdFinding{
			AlertID:       in.AlertID,
			RuleID:        rule,
			Severity:      severity,
			Confidence:    confThresholdRule,
			Band:          cfg.BandOf(confThresholdRule),
			LowConfidence: cfg.LowConfidence(confThresholdRule),
			Rationale:     rationale,
			Evidence:      stats,
		})
	}

	// TH-1 chatty-no-action: threshold too tight or duration too short.
	if stats.FiresPerWeek >= cfg.Threshold.ChattyFiresPerWeek &&
		stats.NoActionRatio >= cfg.Threshold.ChattyNoActionRatio {
		emit("TH-1", "high", "fires far more often than it is acted on — threshold too tight or for-duration too short")
	}

	// TH-2 flappy: condition recovers before a human could act.
	if stats.P50AutoResolveMin > 0 &&
		stats.P50AutoResolveMin <= cfg.Threshold.FlapP50AutoResolveMinutes &&
		stats.FiresPerWeek >= cfg.Threshold.FlapFiresPerWeek {
		emit("TH-2", "medium", "typically auto-resolves within minutes of firing — evaluation duration too short")
	}

	// TH-3 bursty: missing dedup/grouping/inhibition.
	if stats.BurstDays >= cfg.Threshold.BurstMinDays {
		emit("TH-3", "medium", "fires in daily bursts — needs dedup, grouping, or inhibition rather than a threshold change")
	}

	// TH-4 never-actioned: no operational value as tuned. Reuses noise
	// classes: every classified fire is noise-classed, at any confidence.
	if stats.ClassifiedFireCount >= cfg.ColdStart.MinFiresToScore && allClassifiedNoise {
		emit("TH-4", "high", "every classified fire in the window was noise — the alert provides no operational value as tuned")
	}
	return out
}

// ThresholdScore computes the service threshold sub-score:
//
//	flagged_weight  = Σ over flagged alerts (1.0 if any high match, else 0.5)
//	threshold_score = 100 × (1 − min(1, flagged_weight / scoreable_alert_count))
//
// Zero scoreable alerts means the sub-score is unavailable; callers
// handle weight redistribution (composite stage).
func ThresholdScore(findings []ThresholdFinding, scoreableAlerts int) (float64, bool) {
	if scoreableAlerts == 0 {
		return 0, false
	}
	weight := map[string]float64{} // alert id -> flagged weight
	for _, f := range findings {
		w := 0.5
		if f.Severity == "high" {
			w = 1.0
		}
		if w > weight[f.AlertID] {
			weight[f.AlertID] = w
		}
	}
	var flagged float64
	for _, w := range weight {
		flagged += w
	}
	burden := flagged / float64(scoreableAlerts)
	if burden > 1 {
		burden = 1
	}
	return 100 * (1 - burden), true
}

func computeStats(in AlertThresholdInput, weeks, burstFiresPerDay float64) (ThresholdEvidence, bool) {
	stats := ThresholdEvidence{}

	// Fires per week over all fires joined to the alert.
	stats.FiresPerWeek = float64(len(in.Fires)) / weeks

	// No-action ratio and never-actioned reuse the decision-table output:
	// noise-classed / classified (noise + actionable), any confidence.
	var noise, classified int
	for _, c := range in.Classifications {
		switch c.Class {
		case ClassNoise:
			noise++
			classified++
		case ClassActionable:
			classified++
		}
	}
	stats.ClassifiedFireCount = classified
	if classified > 0 {
		stats.NoActionRatio = float64(noise) / float64(classified)
	}
	allClassifiedNoise := classified > 0 && noise == classified

	// p50 auto-resolve duration over auto-resolved fires.
	var autoMins []float64
	for _, f := range in.Fires {
		if f.Event.AutoResolved != nil && *f.Event.AutoResolved && f.Event.ResolvedAt != nil {
			autoMins = append(autoMins, f.Event.ResolvedAt.Sub(f.Event.FiredAt).Minutes())
		}
	}
	if len(autoMins) > 0 {
		sort.Float64s(autoMins)
		stats.P50AutoResolveMin = autoMins[(len(autoMins)-1)/2] // lower median: deterministic
	}

	// Burst detection: fires per UTC calendar day.
	perDay := map[string]int{}
	for _, f := range in.Fires {
		perDay[f.Event.FiredAt.UTC().Format(time.DateOnly)]++
	}
	for _, n := range perDay {
		if n > stats.MaxFiresInOneDay {
			stats.MaxFiresInOneDay = n
		}
		if float64(n) >= burstFiresPerDay {
			stats.BurstDays++
		}
	}
	return stats, allClassifiedNoise
}
