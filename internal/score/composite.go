package score

// SubScore is one sub-score value with its availability. Unavailable means
// not_applicable coverage or zero scoreable alerts — never a zero score.
type SubScore struct {
	Value     float64 `json:"value"`
	Available bool    `json:"available"`
}

// CompositeInput carries the three sub-scores.
type CompositeInput struct {
	Noise     SubScore
	Coverage  SubScore
	Threshold SubScore
}

// CompositeResult is the composite block: score, partiality, and both
// weight sets (REQ-SCORE-004, REQ-SCORE-007 — a partially-scored service
// is visibly partial, never silently complete).
type CompositeResult struct {
	Composite float64 `json:"composite"`
	// Available is false when no sub-score is available at all; the
	// caller emits the service state instead of a score (REQ-HIST-004).
	Available    bool    `json:"available"`
	PartialScore bool    `json:"partial_score"`
	Configured   Weights `json:"configured_weights"`
	Effective    Weights `json:"effective_weights"`
}

// Composite computes the weighted mean of the available sub-scores,
// redistributing an unavailable sub-score's weight proportionally across
// the rest. Effective weights are recorded scaled back to the configured
// total, so consumers see exactly what each sub-score contributed.
func Composite(in CompositeInput, cfg Config) CompositeResult {
	res := CompositeResult{Configured: cfg.Weights}

	type part struct {
		score  float64
		weight float64
		out    *float64
	}
	parts := []part{
		{in.Noise.Value, cfg.Weights.Noise, &res.Effective.Noise},
		{in.Coverage.Value, cfg.Weights.Coverage, &res.Effective.Coverage},
		{in.Threshold.Value, cfg.Weights.Threshold, &res.Effective.Threshold},
	}
	available := []bool{in.Noise.Available, in.Coverage.Available, in.Threshold.Available}

	totalConfigured := cfg.Weights.Noise + cfg.Weights.Coverage + cfg.Weights.Threshold
	var availableWeight, weightedSum float64
	for i, p := range parts {
		if available[i] {
			availableWeight += p.weight
			// Separate statement prevents cross-architecture FMA fusion
			// (ADR 0005 determinism).
			product := p.score * p.weight
			weightedSum += product
		} else {
			res.PartialScore = true
		}
	}
	if availableWeight == 0 {
		return res // Available stays false: state, not score
	}
	res.Available = true
	res.Composite = weightedSum / availableWeight

	// Record effective weights scaled to the configured total.
	scale := totalConfigured / availableWeight
	for i, p := range parts {
		if available[i] {
			*p.out = p.weight * scale
		}
	}
	return res
}

// Priority is the criticality-weighted priority score — the ranking key
// of the worklist (REQ-SCORE-005):
//
//	priority = (100 − composite) × criticality.multiplier[tier]
//
// Only per-service facts and config constants enter (ADR 0001): the value
// is identical whether the service is scored alone or among 5,000 others.
// tierKey must come from ResolveTier, which guarantees a multiplier entry.
func Priority(composite float64, tierKey string, cfg Config) float64 {
	return (100 - composite) * cfg.Criticality.Multiplier[tierKey]
}
