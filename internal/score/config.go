package score

import (
	"fmt"
	"io"
	"os"
	"sort"

	"gopkg.in/yaml.v3"
)

// Band is the contract-level confidence band derived from numeric rule
// confidences (docs/specs/output-contract.md; ADR 0003 handoff signal).
type Band string

const (
	BandHigh   Band = "high"
	BandMedium Band = "medium"
	BandLow    Band = "low"
)

// Config is the versioned scoring parameter set
// (docs/specs/scoring-engine.md, "Scoring config"). Two runs with the same
// version and input must be byte-identical (REQ-SCORE-007), so every
// constant the engine uses lives here and nowhere else.
type Config struct {
	ScoringConfigVersion int             `yaml:"scoring_config_version"`
	Weights              Weights         `yaml:"weights"`
	WindowDays           int             `yaml:"window_days"`
	ColdStart            ColdStart       `yaml:"cold_start"`
	Noise                Noise           `yaml:"noise"`
	Threshold            Threshold       `yaml:"threshold"`
	Criticality          Criticality     `yaml:"criticality"`
	Confidence           Confidence      `yaml:"confidence"`
	LowConfidenceCeiling float64         `yaml:"low_confidence_ceiling"`
	ConfidenceBands      ConfidenceBands `yaml:"confidence_bands"`
}

// Weights are the sub-score weights (REQ-SCORE-004).
type Weights struct {
	Noise     float64 `yaml:"noise"`
	Coverage  float64 `yaml:"coverage"`
	Threshold float64 `yaml:"threshold"`
}

// ColdStart gates alerts into insufficient_data / dormant_healthy states
// before any scoring (REQ-HIST-002..004).
type ColdStart struct {
	MinAlertAgeDays int `yaml:"min_alert_age_days"`
	MinFiresToScore int `yaml:"min_fires_to_score"`
}

// Noise holds the noise-classification and budget parameters
// (REQ-NOISE-001..004, REQ-SCORE-006).
type Noise struct {
	FastAutoResolveMinutes int                `yaml:"fast_auto_resolve_minutes"`
	NeverAckedGraceMinutes int                `yaml:"never_acked_grace_minutes"`
	HighReassignmentCount  int                `yaml:"high_reassignment_count"`
	TierNoiseBudgetPerWeek map[string]float64 `yaml:"tier_noise_budget_per_week"`
}

// Threshold holds the TH-1..TH-4 heuristic parameters (REQ-THRESH-001).
type Threshold struct {
	ChattyFiresPerWeek        float64 `yaml:"chatty_fires_per_week"`
	ChattyNoActionRatio       float64 `yaml:"chatty_no_action_ratio"`
	FlapP50AutoResolveMinutes float64 `yaml:"flap_p50_auto_resolve_minutes"`
	FlapFiresPerWeek          float64 `yaml:"flap_fires_per_week"`
	BurstFiresPerDay          float64 `yaml:"burst_fires_per_day"`
	BurstMinDays              int     `yaml:"burst_min_days"`
}

// Criticality maps CMDB tiers to priority multipliers (REQ-SCORE-005,
// REQ-CRIT-001..003).
type Criticality struct {
	Multiplier  map[string]float64 `yaml:"multiplier"`
	DefaultTier string             `yaml:"default_tier"`
}

// Confidence holds the per-rule confidence constants (REQ-NOISE-004).
type Confidence struct {
	DispositionNoAction    float64 `yaml:"disposition_no_action"`
	LinkedChangeOrIncident float64 `yaml:"linked_change_or_incident"`
	AckedManualClose       float64 `yaml:"acked_manual_close"`
	AmbiguityDefault       float64 `yaml:"ambiguity_default"`
}

// ConfidenceBands maps numeric confidence onto contract bands.
type ConfidenceBands struct {
	HighFloor float64 `yaml:"high_floor"`
}

// LoadConfig reads and validates a scoring config. Unknown keys are
// rejected: a typo silently falling back to a default would break
// reproducibility guarantees.
func LoadConfig(path string) (Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("scoring config: %w", err)
	}
	defer f.Close() //nolint:errcheck // read-only close
	return DecodeConfig(f)
}

// DecodeConfig decodes and validates a scoring config from r, rejecting
// unknown keys.
func DecodeConfig(r io.Reader) (Config, error) {
	dec := yaml.NewDecoder(r)
	dec.KnownFields(true)
	var c Config
	if err := dec.Decode(&c); err != nil {
		return Config{}, fmt.Errorf("scoring config: %w", err)
	}
	if err := c.Validate(); err != nil {
		return Config{}, fmt.Errorf("scoring config: %w", err)
	}
	return c, nil
}

// Validate enforces internal consistency so the engine can trust every
// constant without re-checking.
func (c Config) Validate() error {
	if c.ScoringConfigVersion < 1 {
		return fmt.Errorf("scoring_config_version %d: must be >= 1", c.ScoringConfigVersion)
	}
	if c.Weights.Noise <= 0 || c.Weights.Coverage <= 0 || c.Weights.Threshold <= 0 {
		return fmt.Errorf("weights must all be positive: %+v", c.Weights)
	}
	if c.WindowDays < 1 {
		return fmt.Errorf("window_days %d: must be >= 1", c.WindowDays)
	}
	if c.ColdStart.MinAlertAgeDays < 0 || c.ColdStart.MinFiresToScore < 1 {
		return fmt.Errorf("cold_start invalid: %+v", c.ColdStart)
	}
	if len(c.Noise.TierNoiseBudgetPerWeek) == 0 {
		return fmt.Errorf("noise.tier_noise_budget_per_week must not be empty")
	}
	if len(c.Criticality.Multiplier) == 0 {
		return fmt.Errorf("criticality.multiplier must not be empty")
	}
	if _, ok := c.Criticality.Multiplier[c.Criticality.DefaultTier]; !ok {
		return fmt.Errorf("criticality.default_tier %q has no multiplier entry", c.Criticality.DefaultTier)
	}
	// Budgets and multipliers must cover the same tier set so normalization
	// and ranking can never disagree about what a tier is.
	if !sameKeys(c.Noise.TierNoiseBudgetPerWeek, c.Criticality.Multiplier) {
		return fmt.Errorf("tier sets differ between noise budgets %v and criticality multipliers %v",
			keys(c.Noise.TierNoiseBudgetPerWeek), keys(c.Criticality.Multiplier))
	}
	for tier, budget := range c.Noise.TierNoiseBudgetPerWeek {
		if budget <= 0 {
			return fmt.Errorf("noise budget for %s must be positive, got %v", tier, budget)
		}
	}
	for _, conf := range []float64{
		c.Confidence.DispositionNoAction, c.Confidence.LinkedChangeOrIncident,
		c.Confidence.AckedManualClose, c.Confidence.AmbiguityDefault,
	} {
		if conf <= 0 || conf > 1 {
			return fmt.Errorf("confidence values must be in (0, 1]: %+v", c.Confidence)
		}
	}
	if c.LowConfidenceCeiling <= 0 || c.LowConfidenceCeiling >= 1 {
		return fmt.Errorf("low_confidence_ceiling %v: must be in (0, 1)", c.LowConfidenceCeiling)
	}
	if c.ConfidenceBands.HighFloor <= c.LowConfidenceCeiling || c.ConfidenceBands.HighFloor > 1 {
		return fmt.Errorf("confidence_bands.high_floor %v must be in (low_confidence_ceiling %v, 1]",
			c.ConfidenceBands.HighFloor, c.LowConfidenceCeiling)
	}
	return nil
}

// BandOf maps a numeric rule confidence onto the contract band:
// >= high_floor -> high; <= low_confidence_ceiling -> low; else medium.
func (c Config) BandOf(confidence float64) Band {
	switch {
	case confidence >= c.ConfidenceBands.HighFloor:
		return BandHigh
	case confidence <= c.LowConfidenceCeiling:
		return BandLow
	default:
		return BandMedium
	}
}

// LowConfidence reports whether a determination at this confidence carries
// the low_confidence tag — the CLI -> skill handoff (ADR 0003).
func (c Config) LowConfidence(confidence float64) bool {
	return confidence <= c.LowConfidenceCeiling
}

func sameKeys(a, b map[string]float64) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if _, ok := b[k]; !ok {
			return false
		}
	}
	return true
}

func keys(m map[string]float64) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out) // deterministic error messages (ADR 0005)
	return out
}
