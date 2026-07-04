package score

import (
	"strings"
	"testing"
	"time"

	"github.com/davetashner/alertlint/internal/adapter"
	"github.com/davetashner/alertlint/internal/model"
)

func thCfg(t *testing.T) Config {
	t.Helper()
	c, err := DecodeConfig(strings.NewReader(validYAML))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// thWindow is a 4-week window so fires-per-week math stays readable.
func thWindow() adapter.TimeWindow {
	end := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	return adapter.TimeWindow{Start: end.AddDate(0, 0, -28), End: end}
}

// firesAt builds n fires spread across distinct days (no bursts), each
// auto-resolved after the given minutes (0 = not auto-resolved).
func firesAt(n int, autoResolveMin float64) []Fire {
	start := thWindow().Start
	fires := make([]Fire, 0, n)
	for i := range n {
		f := Fire{Event: model.AlertEvent{
			FiredAt:         start.Add(time.Duration(i) * 25 * time.Hour), // distinct days
			OccurrenceCount: 1,
		}}
		if autoResolveMin > 0 {
			yes := true
			resolved := f.Event.FiredAt.Add(time.Duration(autoResolveMin * float64(time.Minute)))
			f.Event.AutoResolved = &yes
			f.Event.ResolvedAt = &resolved
		}
		fires = append(fires, f)
	}
	return fires
}

func classes(class Class, n int) []FireClassification {
	out := make([]FireClassification, n)
	for i := range out {
		out[i] = FireClassification{Class: class, Confidence: 0.5}
	}
	return out
}

func ruleIDs(fs []ThresholdFinding) []string {
	ids := make([]string, len(fs))
	for i, f := range fs {
		ids[i] = f.RuleID
	}
	return ids
}

func TestTH1ChattyNoAction(t *testing.T) {
	c := thCfg(t)
	// 44 fires / 4 weeks = 11/week ≥ 10; all noise => ratio 1.0 ≥ 0.7.
	in := AlertThresholdInput{AlertID: "a", Fires: firesAt(44, 0), Classifications: classes(ClassNoise, 44)}
	got := ThresholdForAlert(in, thWindow(), c)
	if !containsRule(got, "TH-1") {
		t.Fatalf("want TH-1, got %v", ruleIDs(got))
	}
	// TH-4 also fires (all classified are noise) — rules are independent.
	if !containsRule(got, "TH-4") {
		t.Errorf("want TH-4 alongside TH-1, got %v", ruleIDs(got))
	}
	f := got[0]
	if f.Severity != "high" || f.Evidence.FiresPerWeek != 11 || f.Evidence.NoActionRatio != 1.0 {
		t.Errorf("TH-1 finding = %+v", f)
	}

	// Chatty but acted on: no TH-1.
	in.Classifications = classes(ClassActionable, 44)
	if got := ThresholdForAlert(in, thWindow(), c); containsRule(got, "TH-1") {
		t.Error("actionable chatty alert must not match TH-1")
	}
}

func TestTH2Flappy(t *testing.T) {
	c := thCfg(t)
	// 12 fires / 4wk = 3/week ≥ 3; p50 auto-resolve 4min ≤ 5.
	in := AlertThresholdInput{AlertID: "a", Fires: firesAt(12, 4), Classifications: classes(ClassActionable, 12)}
	got := ThresholdForAlert(in, thWindow(), c)
	if !containsRule(got, "TH-2") {
		t.Fatalf("want TH-2, got %v", ruleIDs(got))
	}
	// Slow auto-resolve: no flap.
	in.Fires = firesAt(12, 30)
	if got := ThresholdForAlert(in, thWindow(), c); containsRule(got, "TH-2") {
		t.Error("30-minute p50 must not match TH-2")
	}
	// Never auto-resolves at all: no flap (p50 undefined).
	in.Fires = firesAt(12, 0)
	if got := ThresholdForAlert(in, thWindow(), c); containsRule(got, "TH-2") {
		t.Error("no auto-resolves must not match TH-2")
	}
}

func TestTH3Bursty(t *testing.T) {
	c := thCfg(t)
	// 3 days with 7 fires each (≥6/day on ≥3 days), plus quiet days.
	var fires []Fire
	base := thWindow().Start
	for day := range 3 {
		for i := range 7 {
			fires = append(fires, Fire{Event: model.AlertEvent{
				FiredAt: base.AddDate(0, 0, day*5).Add(time.Duration(i) * time.Hour),
			}})
		}
	}
	in := AlertThresholdInput{AlertID: "a", Fires: fires, Classifications: classes(ClassActionable, len(fires))}
	got := ThresholdForAlert(in, thWindow(), c)
	if !containsRule(got, "TH-3") {
		t.Fatalf("want TH-3, got %v", ruleIDs(got))
	}
	f := findRule(got, "TH-3")
	if f.Evidence.BurstDays != 3 || f.Evidence.MaxFiresInOneDay != 7 {
		t.Errorf("burst evidence = %+v", f.Evidence)
	}
	// Only 2 burst days: below burst_min_days.
	in.Fires = fires[:14]
	if got := ThresholdForAlert(in, thWindow(), c); containsRule(got, "TH-3") {
		t.Error("2 burst days must not match TH-3")
	}
}

func TestTH4NeverActioned(t *testing.T) {
	c := thCfg(t)
	// 3 classified fires, all noise, low volume (no TH-1: 3/4 per week < 10).
	in := AlertThresholdInput{AlertID: "a", Fires: firesAt(3, 0), Classifications: classes(ClassNoise, 3)}
	got := ThresholdForAlert(in, thWindow(), c)
	if !containsRule(got, "TH-4") || containsRule(got, "TH-1") {
		t.Fatalf("want exactly TH-4, got %v", ruleIDs(got))
	}
	// One actionable fire breaks it.
	in.Classifications = append(classes(ClassNoise, 2), classes(ClassActionable, 1)...)
	if got := ThresholdForAlert(in, thWindow(), c); containsRule(got, "TH-4") {
		t.Error("one actioned fire must clear TH-4")
	}
	// Too few classified fires: unclear/unclassified don't count.
	in.Classifications = append(classes(ClassNoise, 2), classes(ClassUnclear, 5)...)
	if got := ThresholdForAlert(in, thWindow(), c); containsRule(got, "TH-4") {
		t.Error("2 classified fires is below min_fires_to_score")
	}
}

func TestThresholdScore(t *testing.T) {
	high := ThresholdFinding{AlertID: "a", RuleID: "TH-1", Severity: "high"}
	med := ThresholdFinding{AlertID: "b", RuleID: "TH-2", Severity: "medium"}

	// 1.0 + 0.5 flagged over 5 scoreable => burden 0.3 => score 70.
	score, ok := ThresholdScore([]ThresholdFinding{high, med}, 5)
	if !ok || score != 70 {
		t.Errorf("score = %v/%v, want 70/true", score, ok)
	}

	// Same alert matching high and medium counts once at 1.0.
	both := []ThresholdFinding{high, {AlertID: "a", RuleID: "TH-2", Severity: "medium"}}
	score, _ = ThresholdScore(both, 4)
	if score != 75 {
		t.Errorf("double-flagged alert score = %v, want 75 (1.0/4)", score)
	}

	// No scoreable alerts: sub-score unavailable, never a fake 100.
	if _, ok := ThresholdScore(nil, 0); ok {
		t.Error("zero scoreable alerts must report unavailable")
	}

	// Clean service: 100.
	if score, ok := ThresholdScore(nil, 8); !ok || score != 100 {
		t.Errorf("clean service = %v/%v, want 100/true", score, ok)
	}
}

func containsRule(fs []ThresholdFinding, rule string) bool {
	for _, f := range fs {
		if f.RuleID == rule {
			return true
		}
	}
	return false
}

func findRule(fs []ThresholdFinding, rule string) ThresholdFinding {
	for _, f := range fs {
		if f.RuleID == rule {
			return f
		}
	}
	return ThresholdFinding{}
}
