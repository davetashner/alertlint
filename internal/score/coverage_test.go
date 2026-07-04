package score

import (
	"strings"
	"testing"

	"github.com/davetashner/alertlint/internal/archetype"
)

func covCfg(t *testing.T) Config {
	t.Helper()
	c, err := DecodeConfig(strings.NewReader(validYAML))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func archResult(applies bool, satisfied ...bool) archetype.Result {
	r := archetype.Result{Applies: applies}
	for i, s := range satisfied {
		r.Signals = append(r.Signals, archetype.SignalResult{SignalID: string(rune('a' + i)), Satisfied: s})
	}
	return r
}

func TestCoverageScore(t *testing.T) {
	// Two applicable archetypes: 3 of 5 signals present => 60.
	score, ok := CoverageScore([]archetype.Result{
		archResult(true, true, true, false),
		archResult(true, true, false),
	})
	if !ok || score != 60 {
		t.Errorf("score = %v/%v, want 60/true", score, ok)
	}

	// Non-applying archetype contributes nothing.
	score, ok = CoverageScore([]archetype.Result{
		archResult(true, true, true),
		archResult(false, false, false), // signals ignored: does not apply
	})
	if !ok || score != 100 {
		t.Errorf("score = %v/%v, want 100/true", score, ok)
	}

	// No archetype applies: not_applicable, weight redistributed upstream.
	if _, ok := CoverageScore([]archetype.Result{archResult(false)}); ok {
		t.Error("zero applicable signals must report not_applicable")
	}
	if _, ok := CoverageScore(nil); ok {
		t.Error("empty results must report not_applicable")
	}

	// Suppressed archetype (negative override) contributes nothing.
	suppressed := archetype.Result{Applies: false, Suppressed: true,
		Signals: []archetype.SignalResult{{SignalID: "x", Satisfied: false}}}
	if _, ok := CoverageScore([]archetype.Result{suppressed}); ok {
		t.Error("suppressed archetype must not create applicable signals")
	}
}

func TestCoverageFindingConfidenceCap(t *testing.T) {
	c := covCfg(t) // ceiling 0.50
	// Full mapping: confidence passes through.
	if got := CoverageFindingConfidence(0.9, false, c); got != 0.9 {
		t.Errorf("uncapped = %v, want 0.9", got)
	}
	// Partial mapping: capped to the ceiling => forced into low band.
	got := CoverageFindingConfidence(0.9, true, c)
	if got != 0.50 {
		t.Errorf("capped = %v, want 0.50", got)
	}
	if !c.LowConfidence(got) || c.BandOf(got) != BandLow {
		t.Error("capped confidence must land in the low-confidence triage band")
	}
	// Already below the ceiling: unchanged.
	if got := CoverageFindingConfidence(0.35, true, c); got != 0.35 {
		t.Errorf("below-ceiling = %v, want 0.35", got)
	}
}

func TestResolveTier(t *testing.T) {
	c := covCfg(t) // tiers 1-4, default tier3
	one := 1
	nine := 9

	key, source, finding := ResolveTier(&one, c)
	if key != "tier1" || source != CriticalityFromCMDB || finding {
		t.Errorf("known tier: %s/%s/%v", key, source, finding)
	}

	key, source, finding = ResolveTier(nil, c)
	if key != "tier3" || source != CriticalityFromDefault || !finding {
		t.Errorf("missing tier must default with finding (REQ-CRIT-003): %s/%s/%v", key, source, finding)
	}

	key, source, finding = ResolveTier(&nine, c)
	if key != "tier3" || source != CriticalityFromDefault || !finding {
		t.Errorf("out-of-scale tier must default with finding: %s/%s/%v", key, source, finding)
	}
}
