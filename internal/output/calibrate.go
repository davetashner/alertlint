package output

import (
	"encoding/json"
	"fmt"
	"sort"
)

// Calibration is a read-only report: observed distributions from a real
// corpus, side by side with the config constants that scored it, plus
// heuristic suggestions. It never writes configuration — adopting a
// suggestion is a deliberate scoring_config_version bump
// (docs/specs/scoring-engine.md open questions).

// CalibrationReport summarizes one corpus.
type CalibrationReport struct {
	Services int `json:"services"`
	Ranked   int `json:"ranked"`
	// PerTier keys are "tier1".."tierN" as observed in documents.
	PerTier map[string]*TierCalibration `json:"per_tier"`
	// NoiseEvidence aggregates finding-level facts across the corpus.
	NoiseEvidence NoiseEvidenceStats `json:"noise_evidence"`
}

// TierCalibration inverts each service's noise score against the budget
// that produced it, recovering the observed noisy-fires-per-week rate:
// burden = 1 − score/100; rate = burden × budget.
type TierCalibration struct {
	Services         int         `json:"services"`
	CurrentBudget    float64     `json:"current_budget_per_week"`
	ObservedRatePcts Percentiles `json:"observed_noisy_fires_per_week"`
	NoiseScorePcts   Percentiles `json:"noise_score"`
	// SuggestedBudget is the p75 of observed rates: the budget at which a
	// quarter of this tier's services would exceed budget — a starting
	// point for discussion, not a truth.
	SuggestedBudget float64 `json:"suggested_budget_per_week"`
	rates           []float64
	scores          []float64
}

// NoiseEvidenceStats aggregates noise-finding evidence.
type NoiseEvidenceStats struct {
	Findings          int         `json:"findings"`
	FireCountPcts     Percentiles `json:"fire_count"`
	OffHoursRatioPcts Percentiles `json:"off_hours_ratio"`
	MedianResolvePcts Percentiles `json:"median_time_to_resolve_s"`
	fireCounts        []float64
	offHours          []float64
	resolves          []float64
}

// Percentiles is the standard summary of one observed distribution.
type Percentiles struct {
	P50 float64 `json:"p50"`
	P75 float64 `json:"p75"`
	P90 float64 `json:"p90"`
	Max float64 `json:"max"`
}

// Calibrate builds the report from a corpus, given the tier budgets the
// documents were scored with (from the same scoring config the run used;
// the caller passes them so this package stays config-agnostic).
func Calibrate(dirs []string, tierBudgets map[string]float64) (CalibrationReport, error) {
	report := CalibrationReport{PerTier: map[string]*TierCalibration{}}
	kept, _, _, err := loadNewest(dirs)
	if err != nil {
		return report, err
	}
	for _, k := range kept {
		doc := k.doc
		report.Services++
		if doc.Scores.PriorityScore == nil || doc.Scores.Noise == nil {
			continue
		}
		report.Ranked++
		tierKey := fmt.Sprintf("tier%d", doc.Scores.CriticalityTier)
		tc := report.PerTier[tierKey]
		if tc == nil {
			tc = &TierCalibration{CurrentBudget: tierBudgets[tierKey]}
			report.PerTier[tierKey] = tc
		}
		tc.Services++
		tc.scores = append(tc.scores, *doc.Scores.Noise)
		if tc.CurrentBudget > 0 {
			burden := 1 - *doc.Scores.Noise/100
			tc.rates = append(tc.rates, burden*tc.CurrentBudget)
		}

		for _, f := range doc.Findings {
			if f.Type != "noise" {
				continue
			}
			var ev struct {
				FireCount            float64  `json:"fire_count"`
				OffHoursRatio        float64  `json:"off_hours_ratio"`
				MedianTimeToResolveS *float64 `json:"median_time_to_resolve_s"`
			}
			if json.Unmarshal(f.Evidence, &ev) != nil {
				continue
			}
			report.NoiseEvidence.Findings++
			report.NoiseEvidence.fireCounts = append(report.NoiseEvidence.fireCounts, ev.FireCount)
			report.NoiseEvidence.offHours = append(report.NoiseEvidence.offHours, ev.OffHoursRatio)
			if ev.MedianTimeToResolveS != nil {
				report.NoiseEvidence.resolves = append(report.NoiseEvidence.resolves, *ev.MedianTimeToResolveS)
			}
		}
	}

	for _, tc := range report.PerTier {
		tc.ObservedRatePcts = percentiles(tc.rates)
		tc.NoiseScorePcts = percentiles(tc.scores)
		tc.SuggestedBudget = tc.ObservedRatePcts.P75
	}
	report.NoiseEvidence.FireCountPcts = percentiles(report.NoiseEvidence.fireCounts)
	report.NoiseEvidence.OffHoursRatioPcts = percentiles(report.NoiseEvidence.offHours)
	report.NoiseEvidence.MedianResolvePcts = percentiles(report.NoiseEvidence.resolves)
	return report, nil
}

// percentiles uses the nearest-rank method over a sorted copy —
// deterministic and dependency-free.
func percentiles(values []float64) Percentiles {
	if len(values) == 0 {
		return Percentiles{}
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	at := func(p float64) float64 {
		rank := int(p*float64(len(sorted))+0.5) - 1
		if rank < 0 {
			rank = 0
		}
		if rank >= len(sorted) {
			rank = len(sorted) - 1
		}
		return sorted[rank]
	}
	return Percentiles{P50: at(0.50), P75: at(0.75), P90: at(0.90), Max: sorted[len(sorted)-1]}
}
