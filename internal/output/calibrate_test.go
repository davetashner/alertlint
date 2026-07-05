package output

import (
	"testing"
	"time"
)

func TestCalibrate(t *testing.T) {
	dir := t.TempDir()
	t1 := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)

	// tier1 budget 2.0: noise 50 -> burden .5 -> rate 1.0; noise 80 -> 0.4.
	d1 := doc("CI1", "a", f(70), f(60), t1)
	d1.Scores.Noise = f(50)
	d1.Scores.CriticalityTier = 1
	d1.Findings = []Finding{{ID: "ald-aaaaaaaa", Type: "noise", Severity: "high", Confidence: "low",
		Rationale: "r", Evidence: []byte(`{"fire_count": 40, "off_hours_ratio": 0.6, "median_time_to_resolve_s": 240}`)}}
	writeDoc(t, dir, "a.CI1.json", d1)

	d2 := doc("CI2", "b", f(30), f(80), t1)
	d2.Scores.Noise = f(80)
	d2.Scores.CriticalityTier = 1
	writeDoc(t, dir, "b.CI2.json", d2)

	// Unranked service: counted, not calibrated.
	writeDoc(t, dir, "c.CI3.json", doc("CI3", "c", nil, nil, t1))

	report, err := Calibrate([]string{dir}, map[string]float64{"tier1": 2.0})
	if err != nil {
		t.Fatal(err)
	}
	if report.Services != 3 || report.Ranked != 2 {
		t.Fatalf("services/ranked = %d/%d", report.Services, report.Ranked)
	}
	tc := report.PerTier["tier1"]
	if tc == nil || tc.Services != 2 {
		t.Fatalf("tier1 = %+v", tc)
	}
	// Observed rates {~0.4, 1.0}: p50 nearest-rank ~= 0.4 (floating
	// point: (1-80/100)*2 = 0.3999...), p75 = 1.0.
	if tc.ObservedRatePcts.P50 < 0.399 || tc.ObservedRatePcts.P50 > 0.401 || tc.SuggestedBudget != 1.0 {
		t.Errorf("rates = %+v suggested %v", tc.ObservedRatePcts, tc.SuggestedBudget)
	}
	if report.NoiseEvidence.Findings != 1 || report.NoiseEvidence.FireCountPcts.Max != 40 {
		t.Errorf("evidence stats = %+v", report.NoiseEvidence)
	}
	if report.NoiseEvidence.OffHoursRatioPcts.P50 != 0.6 {
		t.Errorf("off-hours p50 = %v", report.NoiseEvidence.OffHoursRatioPcts.P50)
	}
}

func TestPercentilesNearestRank(t *testing.T) {
	p := percentiles([]float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10})
	if p.P50 != 5 || p.P75 != 8 || p.P90 != 9 || p.Max != 10 {
		t.Errorf("percentiles = %+v", p)
	}
	if (percentiles(nil) != Percentiles{}) {
		t.Error("empty input must yield zero percentiles")
	}
}
