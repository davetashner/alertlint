package score

import (
	"fmt"

	"github.com/davetashner/alertlint/internal/archetype"
)

// CoverageScore aggregates required-signal satisfaction across the
// service's applicable archetypes (docs/specs/scoring-engine.md):
//
//	coverage_score = 100 × (present_required_signals / applicable_required_signals)
//
// Suppressed archetypes contribute nothing. Zero applicable signals means
// the sub-score is not_applicable: the second return is false and the
// composite stage redistributes the weight — never a fake score.
func CoverageScore(results []archetype.Result) (float64, bool) {
	var present, applicable int
	for _, r := range results {
		if !r.Applies {
			continue
		}
		for _, s := range r.Signals {
			applicable++
			if s.Satisfied {
				present++
			}
		}
	}
	if applicable == 0 {
		return 0, false
	}
	return 100 * float64(present) / float64(applicable), true
}

// CoverageFindingConfidence derives a coverage finding's confidence from
// the archetype applicability confidence, capped to the low-confidence
// ceiling when the service's identity mapping is partial: unseen telemetry
// may exist behind unmapped artifacts, so the finding is forced into the
// skill's low-confidence triage queue rather than asserted firmly
// (identity-resolution.md, "Downstream scoring transparency").
func CoverageFindingConfidence(applicabilityConfidence float64, partialMapping bool, cfg Config) float64 {
	if partialMapping && applicabilityConfidence > cfg.LowConfidenceCeiling {
		return cfg.LowConfidenceCeiling
	}
	return applicabilityConfidence
}

// TierKey is the config key form of a criticality tier ("tier1".."tierN").
func TierKey(tier int) string { return fmt.Sprintf("tier%d", tier) }

// CriticalitySource records where a service's tier came from.
type CriticalitySource string

const (
	CriticalityFromCMDB    CriticalitySource = "cmdb"
	CriticalityFromDefault CriticalitySource = "default"
)

// ResolveTier maps a CMDB criticality tier (nil = absent) onto the config
// tier key that drives budgets and priority multipliers (REQ-CRIT-001..003).
// Missing or unknown tiers fall to the configurable default with
// source=default and needsFinding=true — the service is neither hidden nor
// over-prioritized, and the gap is surfaced as a missing_criticality
// finding by the caller.
func ResolveTier(cmdbTier *int, cfg Config) (tierKey string, source CriticalitySource, needsFinding bool) {
	if cmdbTier != nil {
		key := TierKey(*cmdbTier)
		if _, ok := cfg.Criticality.Multiplier[key]; ok {
			return key, CriticalityFromCMDB, false
		}
		// Tier present but outside the configured scale — same treatment
		// as absent: default + finding, never a guess.
		return cfg.Criticality.DefaultTier, CriticalityFromDefault, true
	}
	return cfg.Criticality.DefaultTier, CriticalityFromDefault, true
}
