package score

import (
	"strings"
	"testing"
)

// TestCommittedDefaultsMatchSpec loads the repo's default config file and
// pins every value to the spec's proposed starting points
// (docs/specs/scoring-engine.md). Changing a constant here must be a
// deliberate act paired with a scoring_config_version bump.
func TestCommittedDefaultsMatchSpec(t *testing.T) {
	c, err := LoadConfig("../../configs/scoring.yaml")
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if c.ScoringConfigVersion != 3 {
		t.Errorf("version = %d, want 3", c.ScoringConfigVersion)
	}
	if c.OffHours.Timezone != "UTC" || c.OffHours.StartHour != 20 || !c.OffHours.WeekendIsOffHours {
		t.Errorf("offhours = %+v", c.OffHours)
	}
	if !c.ExcludeMaintenance() {
		t.Error("default maintenance policy must exclude (REQ-NOISE-005)")
	}
	if c.Weights != (Weights{Noise: 45, Coverage: 30, Threshold: 25}) {
		t.Errorf("weights = %+v, want 45/30/25 (REQ-SCORE-004)", c.Weights)
	}
	if c.WindowDays != 90 {
		t.Errorf("window_days = %d, want 90 (REQ-HIST-001)", c.WindowDays)
	}
	if c.ColdStart != (ColdStart{MinAlertAgeDays: 14, MinFiresToScore: 3}) {
		t.Errorf("cold_start = %+v", c.ColdStart)
	}
	wantBudget := map[string]float64{"tier1": 2.0, "tier2": 3.0, "tier3": 5.0, "tier4": 8.0}
	for tier, want := range wantBudget {
		if got := c.Noise.TierNoiseBudgetPerWeek[tier]; got != want {
			t.Errorf("noise budget %s = %v, want %v", tier, got, want)
		}
	}
	wantMult := map[string]float64{"tier1": 2.0, "tier2": 1.5, "tier3": 1.0, "tier4": 0.7}
	for tier, want := range wantMult {
		if got := c.Criticality.Multiplier[tier]; got != want {
			t.Errorf("multiplier %s = %v, want %v", tier, got, want)
		}
	}
	if c.Criticality.DefaultTier != "tier3" {
		t.Errorf("default_tier = %q, want tier3 (REQ-CRIT-003)", c.Criticality.DefaultTier)
	}
	if c.Confidence.AmbiguityDefault != 0.35 {
		t.Errorf("ambiguity_default = %v, want 0.35 (REQ-NOISE-003)", c.Confidence.AmbiguityDefault)
	}
	if c.LowConfidenceCeiling != 0.50 || c.ConfidenceBands.HighFloor != 0.85 {
		t.Errorf("bands = ceiling %v / high floor %v, want 0.50 / 0.85",
			c.LowConfidenceCeiling, c.ConfidenceBands.HighFloor)
	}
	if c.Threshold.ChattyFiresPerWeek != 10 || c.Threshold.ChattyNoActionRatio != 0.70 {
		t.Errorf("threshold chatty params = %+v", c.Threshold)
	}
}

func TestUnknownKeysRejected(t *testing.T) {
	_, err := DecodeConfig(strings.NewReader(validYAML + "\nnot_a_real_key: 7\n"))
	if err == nil {
		t.Fatal("unknown top-level key must be rejected")
	}
	_, err = DecodeConfig(strings.NewReader(strings.Replace(validYAML,
		"  min_fires_to_score: 3", "  min_fires_to_score: 3\n  typo_key: 1", 1)))
	if err == nil {
		t.Fatal("unknown nested key must be rejected")
	}
}

func TestValidationFailures(t *testing.T) {
	cases := []struct {
		name string
		old  string
		new  string
	}{
		{"zero weight", "weights: { noise: 45", "weights: { noise: 0"},
		{"bad version", "scoring_config_version: 1", "scoring_config_version: 0"},
		{"default tier missing", "default_tier: tier3", "default_tier: tier9"},
		{"tier sets differ", "tier_noise_budget_per_week: { tier1: 2.0, tier2: 3.0, tier3: 5.0, tier4: 8.0 }",
			"tier_noise_budget_per_week: { tier1: 2.0 }"},
		{"band inversion", "high_floor: 0.85", "high_floor: 0.40"},
		{"confidence out of range", "ambiguity_default: 0.35", "ambiguity_default: 1.5"},
		{"negative budget", "tier4: 8.0 }", "tier4: -1 }"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mutated := strings.Replace(validYAML, tc.old, tc.new, 1)
			if mutated == validYAML {
				t.Fatalf("mutation %q did not apply", tc.name)
			}
			if _, err := DecodeConfig(strings.NewReader(mutated)); err == nil {
				t.Errorf("%s: expected validation error", tc.name)
			}
		})
	}
}

func TestBandMapping(t *testing.T) {
	c, err := DecodeConfig(strings.NewReader(validYAML))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		conf float64
		want Band
		low  bool
	}{
		{0.90, BandHigh, false},
		{0.85, BandHigh, false}, // floor inclusive
		{0.84, BandMedium, false},
		{0.51, BandMedium, false},
		{0.50, BandLow, true}, // ceiling inclusive
		{0.35, BandLow, true},
	}
	for _, tc := range cases {
		if got := c.BandOf(tc.conf); got != tc.want {
			t.Errorf("BandOf(%v) = %q, want %q", tc.conf, got, tc.want)
		}
		if got := c.LowConfidence(tc.conf); got != tc.low {
			t.Errorf("LowConfidence(%v) = %v, want %v", tc.conf, got, tc.low)
		}
	}
}

const validYAML = `
scoring_config_version: 1
weights: { noise: 45, coverage: 30, threshold: 25 }
window_days: 90
cold_start:
  min_alert_age_days: 14
  min_fires_to_score: 3
noise:
  fast_auto_resolve_minutes: 10
  never_acked_grace_minutes: 30
  high_reassignment_count: 3
  tier_noise_budget_per_week: { tier1: 2.0, tier2: 3.0, tier3: 5.0, tier4: 8.0 }
threshold:
  chatty_fires_per_week: 10
  chatty_no_action_ratio: 0.70
  flap_p50_auto_resolve_minutes: 5
  flap_fires_per_week: 3
  burst_fires_per_day: 6
  burst_min_days: 3
criticality:
  multiplier: { tier1: 2.0, tier2: 1.5, tier3: 1.0, tier4: 0.7 }
  default_tier: tier3
confidence:
  disposition_no_action: 0.90
  linked_change_or_incident: 0.90
  acked_manual_close: 0.70
  ambiguity_default: 0.35
low_confidence_ceiling: 0.50
confidence_bands:
  high_floor: 0.85
`
