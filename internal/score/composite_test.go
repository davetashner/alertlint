package score

import (
	"math"
	"strings"
	"testing"
)

func compCfg(t *testing.T) Config {
	t.Helper()
	c, err := DecodeConfig(strings.NewReader(validYAML))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func avail(v float64) SubScore { return SubScore{Value: v, Available: true} }

func TestCompositeAllAvailable(t *testing.T) {
	c := compCfg(t)
	res := Composite(CompositeInput{Noise: avail(80), Coverage: avail(60), Threshold: avail(40)}, c)
	want := (80*45 + 60*30 + 40*25) / 100.0 // 64
	if !res.Available || res.PartialScore {
		t.Fatalf("res = %+v, want available, not partial", res)
	}
	if math.Abs(res.Composite-want) > 1e-9 {
		t.Errorf("composite = %v, want %v", res.Composite, want)
	}
	if res.Effective != res.Configured {
		t.Errorf("all-available effective weights must equal configured: %+v", res.Effective)
	}
}

func TestCompositeRedistribution(t *testing.T) {
	c := compCfg(t)
	// Coverage not applicable: its 30 redistributes over noise 45 + threshold 25.
	res := Composite(CompositeInput{Noise: avail(80), Coverage: SubScore{}, Threshold: avail(40)}, c)
	want := (80*45 + 40*25) / 70.0
	if !res.Available || !res.PartialScore {
		t.Fatalf("res = %+v, want available AND partial", res)
	}
	if math.Abs(res.Composite-want) > 1e-9 {
		t.Errorf("composite = %v, want %v", res.Composite, want)
	}
	// Effective weights scale to the configured total (100).
	if math.Abs(res.Effective.Noise-45*100.0/70.0) > 1e-9 ||
		res.Effective.Coverage != 0 ||
		math.Abs(res.Effective.Threshold-25*100.0/70.0) > 1e-9 {
		t.Errorf("effective weights = %+v", res.Effective)
	}
	if math.Abs((res.Effective.Noise+res.Effective.Threshold)-100) > 1e-9 {
		t.Error("effective weights must sum to the configured total")
	}
}

func TestCompositeNothingAvailable(t *testing.T) {
	c := compCfg(t)
	res := Composite(CompositeInput{}, c)
	if res.Available {
		t.Fatal("no available sub-score must yield state, not score (REQ-HIST-004)")
	}
	if !res.PartialScore {
		t.Error("nothing-available is maximally partial")
	}
}

func TestPriorityFormula(t *testing.T) {
	c := compCfg(t)
	if got := Priority(64, "tier1", c); got != (100-64)*2.0 {
		t.Errorf("priority = %v, want 72", got)
	}
	if got := Priority(64, "tier4", c); got != (100-64)*0.7 {
		t.Errorf("tier4 priority = %v", got)
	}
	// The design intent (ADR 0001 / REQ-SCORE-005): a mediocre tier-1
	// service outranks a terrible tier-4 one.
	mediocreTier1 := Priority(60, "tier1", c) // 40 × 2.0 = 80
	terribleTier4 := Priority(20, "tier4", c) // 80 × 0.7 = 56
	if mediocreTier1 <= terribleTier4 {
		t.Errorf("mediocre tier1 (%v) must outrank terrible tier4 (%v)", mediocreTier1, terribleTier4)
	}
	// Perfect service: priority 0 regardless of tier.
	if Priority(100, "tier1", c) != 0 {
		t.Error("perfect composite must yield zero priority")
	}
}

// Monotonicity property (spec testing section): improving any sub-score
// never raises priority.
func TestPriorityMonotonicity(t *testing.T) {
	c := compCfg(t)
	base := CompositeInput{Noise: avail(50), Coverage: avail(50), Threshold: avail(50)}
	basePriority := Priority(Composite(base, c).Composite, "tier2", c)
	for _, bump := range []func(*CompositeInput){
		func(in *CompositeInput) { in.Noise.Value += 10 },
		func(in *CompositeInput) { in.Coverage.Value += 10 },
		func(in *CompositeInput) { in.Threshold.Value += 10 },
	} {
		in := base
		bump(&in)
		if p := Priority(Composite(in, c).Composite, "tier2", c); p > basePriority {
			t.Errorf("improving a sub-score raised priority: %v > %v", p, basePriority)
		}
	}
}
