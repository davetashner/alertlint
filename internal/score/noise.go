package score

import (
	"github.com/davetashner/alertlint/internal/adapter"
)

// ClassCounts is the per-class fire tally carried as finding evidence
// (docs/specs/scoring-engine.md, "Per-alert rollup").
type ClassCounts struct {
	Noise        int `json:"noise"`
	Actionable   int `json:"actionable"`
	Unclear      int `json:"unclear"`
	Unclassified int `json:"unclassified"`
}

// AlertNoise is the noise rollup for one scoreable alert.
type AlertNoise struct {
	AlertID string `json:"alert_id"`
	// Ratio is Σ(confidence of noise fires) / Σ(confidence of noise and
	// actionable fires). Unclear and unclassified fires are excluded from
	// the ratio but counted in evidence.
	Ratio float64 `json:"noise_ratio"`
	// HasRatio is false when no fire classified as noise or actionable —
	// there is no evidence either way and no finding can be emitted.
	HasRatio bool `json:"has_ratio"`
	// WeightedNoiseFires is Σ confidence over noise-classed fires — this
	// alert's contribution to the service noise burden.
	WeightedNoiseFires float64     `json:"weighted_noise_fires"`
	Counts             ClassCounts `json:"counts"`
	// Finding is non-nil when the alert crosses the noise threshold.
	Finding *NoiseFinding `json:"finding,omitempty"`
}

// NoiseFinding is the type:noise finding payload for a noisy alert.
type NoiseFinding struct {
	AlertID string  `json:"alert_id"`
	Ratio   float64 `json:"noise_ratio"`
	// Confidence is the confidence-weighted mean (Σc²/Σc) of the
	// noise-classed classifications that produced the verdict.
	Confidence    float64     `json:"confidence"`
	Band          Band        `json:"band"`
	LowConfidence bool        `json:"low_confidence"`
	Counts        ClassCounts `json:"counts"`
}

// noiseFindingRatioFloor: an alert is finding-worthy when at least half
// its confidence-weighted evidence says noise. Spec constant, version-bump
// gated like the table confidences.
const noiseFindingRatioFloor = 0.5

// NoiseForAlert rolls per-fire classifications up to one alert
// (docs/specs/scoring-engine.md "Per-alert rollup"). Deterministic in the
// order fires are supplied — and, because it is pure summation,
// order-independent.
func NoiseForAlert(alertID string, classifications []FireClassification, cfg Config) AlertNoise {
	out := AlertNoise{AlertID: alertID}
	var noiseConf, participatingConf, noiseConfSq float64
	participating := 0
	for _, c := range classifications {
		switch c.Class {
		case ClassNoise:
			out.Counts.Noise++
			noiseConf += c.Confidence
			noiseConfSq += c.Confidence * c.Confidence
			participatingConf += c.Confidence
			participating++
		case ClassActionable:
			out.Counts.Actionable++
			participatingConf += c.Confidence
			participating++
		case ClassUnclear:
			out.Counts.Unclear++
		case ClassUnclassified:
			out.Counts.Unclassified++
		}
	}
	out.WeightedNoiseFires = noiseConf
	if participatingConf == 0 {
		return out // no noise-or-actionable evidence: no ratio, no finding
	}
	out.HasRatio = true
	out.Ratio = noiseConf / participatingConf

	if out.Ratio >= noiseFindingRatioFloor && participating >= cfg.ColdStart.MinFiresToScore && noiseConf > 0 {
		conf := noiseConfSq / noiseConf // confidence-weighted mean of noise classifications
		out.Finding = &NoiseFinding{
			AlertID:       alertID,
			Ratio:         out.Ratio,
			Confidence:    conf,
			Band:          cfg.BandOf(conf),
			LowConfidence: cfg.LowConfidence(conf),
			Counts:        out.Counts,
		}
	}
	return out
}

// NoiseScore computes the service noise sub-score with tier normalization
// (REQ-SCORE-006, ADR 0001): only per-service facts and config constants —
// no corpus statistic ever enters, so the score is identical whether the
// service is scored alone or among 5,000 others.
//
//	noisy_fires_per_week = Σ weighted noise fires / window_weeks
//	burden               = noisy_fires_per_week / tier_noise_budget_per_week[tier]
//	noise_score          = 100 × (1 − min(1, burden))
//
// tier must exist in the budget table (config validation + upstream
// default-tier substitution guarantee it).
func NoiseScore(alerts []AlertNoise, tier string, window adapter.TimeWindow, cfg Config) float64 {
	var weighted float64
	for _, a := range alerts {
		weighted += a.WeightedNoiseFires
	}
	weeks := window.End.Sub(window.Start).Hours() / (24 * 7)
	noisyPerWeek := weighted / weeks
	burden := noisyPerWeek / cfg.Noise.TierNoiseBudgetPerWeek[tier]
	if burden > 1 {
		burden = 1
	}
	return 100 * (1 - burden)
}

// WindowWeeks exposes the week count used by NoiseScore for evidence
// blocks and tests.
func WindowWeeks(w adapter.TimeWindow) float64 {
	return w.End.Sub(w.Start).Hours() / (24 * 7)
}
