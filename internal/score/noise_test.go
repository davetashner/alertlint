package score

import (
	"math"
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/davetashner/alertlint/internal/adapter"
)

func noiseCfg(t *testing.T) Config {
	t.Helper()
	c, err := DecodeConfig(strings.NewReader(validYAML))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func cls(class Class, conf float64) FireClassification {
	return FireClassification{Class: class, Confidence: conf}
}

func window90() adapter.TimeWindow {
	end := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	return adapter.TimeWindow{Start: end.AddDate(0, 0, -90), End: end}
}

func approx(t *testing.T, got, want float64, msg string) {
	t.Helper()
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("%s = %v, want %v", msg, got, want)
	}
}

func TestNoiseForAlertRatio(t *testing.T) {
	c := noiseCfg(t)
	// 2 firm noise (0.90), 1 actionable (0.70): ratio = 1.8 / 2.5 = 0.72.
	a := NoiseForAlert("m-1", []FireClassification{
		cls(ClassNoise, 0.90), cls(ClassNoise, 0.90), cls(ClassActionable, 0.70),
		cls(ClassUnclear, 0.40), cls(ClassUnclassified, 0.20), // excluded from ratio
	}, c)
	if !a.HasRatio {
		t.Fatal("expected a ratio")
	}
	approx(t, a.Ratio, 1.8/2.5, "ratio")
	approx(t, a.WeightedNoiseFires, 1.8, "weighted noise fires")
	if a.Counts != (ClassCounts{Noise: 2, Actionable: 1, Unclear: 1, Unclassified: 1}) {
		t.Errorf("counts = %+v", a.Counts)
	}
	if a.Finding == nil {
		t.Fatal("ratio 0.72 over 3 participating fires must emit a finding")
	}
	// Confidence-weighted mean of noise classifications: Σc²/Σc = 1.62/1.8 = 0.90.
	approx(t, a.Finding.Confidence, 0.90, "finding confidence")
	if a.Finding.Band != BandHigh || a.Finding.LowConfidence {
		t.Errorf("finding band = %s low=%v, want high/false", a.Finding.Band, a.Finding.LowConfidence)
	}
}

func TestNoiseForAlertNoFindingCases(t *testing.T) {
	c := noiseCfg(t)

	// Below ratio floor: mostly actionable.
	a := NoiseForAlert("m-2", []FireClassification{
		cls(ClassActionable, 0.90), cls(ClassActionable, 0.90), cls(ClassNoise, 0.35),
	}, c)
	if a.Finding != nil {
		t.Errorf("ratio %v below floor must not emit finding", a.Ratio)
	}

	// Too few participating fires (min_fires_to_score = 3).
	a = NoiseForAlert("m-3", []FireClassification{
		cls(ClassNoise, 0.90), cls(ClassNoise, 0.90),
	}, c)
	if a.Finding != nil {
		t.Error("2 participating fires must not emit finding")
	}

	// Only unclear/unclassified: no ratio at all.
	a = NoiseForAlert("m-4", []FireClassification{
		cls(ClassUnclear, 0.40), cls(ClassUnclassified, 0.20),
	}, c)
	if a.HasRatio || a.Finding != nil {
		t.Error("no noise/actionable evidence must mean no ratio and no finding")
	}

	// Low-confidence pile still emits when ratio and count clear the bar,
	// but the finding itself is low-confidence — the skill's triage queue.
	a = NoiseForAlert("m-5", []FireClassification{
		cls(ClassNoise, 0.35), cls(ClassNoise, 0.35), cls(ClassNoise, 0.35),
	}, c)
	if a.Finding == nil {
		t.Fatal("expected finding")
	}
	approx(t, a.Finding.Confidence, 0.35, "low-conf finding confidence")
	if !a.Finding.LowConfidence || a.Finding.Band != BandLow {
		t.Error("all-ambiguous finding must be tagged low confidence")
	}
}

func TestNoiseScoreTierNormalization(t *testing.T) {
	c := noiseCfg(t)
	w := window90() // 90/7 weeks
	weeks := WindowWeeks(w)

	// Service with 2 alerts: weighted noise fires 1.8 + 7.2 = 9.0.
	alerts := []AlertNoise{
		{AlertID: "a", WeightedNoiseFires: 1.8},
		{AlertID: "b", WeightedNoiseFires: 7.2},
	}
	perWeek := 9.0 / weeks // = 0.7 noisy fires/week

	// tier1 budget 2.0/week: burden 0.35 => score 65.
	approx(t, NoiseScore(alerts, "tier1", w, c), 100*(1-perWeek/2.0), "tier1 score")
	// tier4 budget 8.0/week: same burden divided by looser budget => higher score.
	if NoiseScore(alerts, "tier4", w, c) <= NoiseScore(alerts, "tier1", w, c) {
		t.Error("looser tier-4 budget must never score below tier-1 for identical burden (REQ-SCORE-006)")
	}

	// Burden saturates at 1: score floors at 0, never negative.
	flood := []AlertNoise{{AlertID: "x", WeightedNoiseFires: 10000}}
	if got := NoiseScore(flood, "tier1", w, c); got != 0 {
		t.Errorf("flooded service score = %v, want 0", got)
	}

	// Zero noise: perfect score.
	if got := NoiseScore(nil, "tier1", w, c); got != 100 {
		t.Errorf("quiet service score = %v, want 100", got)
	}
}

// Permutation invariance (spec property test): alert order and fire order
// never change the outcome. Corpus independence is structural — NoiseScore
// takes only this service's alerts — but permutation is worth pinning.
func TestNoisePermutationInvariance(t *testing.T) {
	c := noiseCfg(t)
	w := window90()
	fires := []FireClassification{
		cls(ClassNoise, 0.90), cls(ClassActionable, 0.70), cls(ClassNoise, 0.35),
		cls(ClassUnclear, 0.40), cls(ClassNoise, 0.45), cls(ClassActionable, 0.90),
	}
	base := NoiseForAlert("m", fires, c)

	rng := rand.New(rand.NewSource(1)) // fixed seed: test itself stays deterministic
	for range 25 {
		shuffled := append([]FireClassification(nil), fires...)
		rng.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
		got := NoiseForAlert("m", shuffled, c)
		approx(t, got.Ratio, base.Ratio, "shuffled ratio")
		approx(t, got.WeightedNoiseFires, base.WeightedNoiseFires, "shuffled weighted fires")
	}

	alerts := []AlertNoise{{WeightedNoiseFires: 1.1}, {WeightedNoiseFires: 2.2}, {WeightedNoiseFires: 3.3}}
	baseScore := NoiseScore(alerts, "tier2", w, c)
	reversed := []AlertNoise{alerts[2], alerts[1], alerts[0]}
	approx(t, NoiseScore(reversed, "tier2", w, c), baseScore, "alert-order score")
}
