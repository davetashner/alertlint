package output

import (
	"testing"
	"time"
)

func docWithFindings(ciID, ciName string, priority, composite *float64, runAt time.Time, findings ...Finding) Document {
	d := doc(ciID, ciName, priority, composite, runAt)
	d.Findings = findings
	return d
}

func finding(id, ftype, severity string) Finding {
	return Finding{ID: id, Type: ftype, Severity: severity, Confidence: "high",
		Rationale: "r-" + id, Evidence: []byte(`{}`)}
}

func TestDiff(t *testing.T) {
	oldDir, newDir := t.TempDir(), t.TempDir()
	t1 := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	t2 := t1.AddDate(0, 0, 7)

	// CI1: priority worsens, one finding resolved, one new, one persists.
	writeDoc(t, oldDir, "a.CI1.json", docWithFindings("CI1", "alpha", f(50), f(75), t1,
		finding("ald-11111111", "noise", "high"),
		finding("ald-22222222", "threshold", "medium")))
	writeDoc(t, newDir, "a.CI1.json", docWithFindings("CI1", "alpha", f(70), f(65), t2,
		finding("ald-22222222", "threshold", "medium"),
		finding("ald-33333333", "coverage", "high")))

	// CI2: improves and drops below CI1 in rank.
	writeDoc(t, oldDir, "b.CI2.json", docWithFindings("CI2", "beta", f(80), f(60), t1))
	writeDoc(t, newDir, "b.CI2.json", docWithFindings("CI2", "beta", f(40), f(80), t2))

	// CI3 removed; CI4 new.
	writeDoc(t, oldDir, "c.CI3.json", docWithFindings("CI3", "gone", f(20), f(90), t1))
	writeDoc(t, newDir, "d.CI4.json", docWithFindings("CI4", "fresh", f(30), f(85), t2))

	res, err := Diff([]string{oldDir}, []string{newDir})
	if err != nil {
		t.Fatal(err)
	}

	if len(res.Changed) != 2 {
		t.Fatalf("changed = %d, want 2", len(res.Changed))
	}
	// Sorted by |priority delta| desc: CI2 (−40) before CI1 (+20).
	if res.Changed[0].CIID != "CI2" || res.Changed[1].CIID != "CI1" {
		t.Fatalf("order = %s, %s", res.Changed[0].CIID, res.Changed[1].CIID)
	}
	ci1 := res.Changed[1]
	if *ci1.PriorityDelta != 20 || *ci1.CompositeDelta != -10 {
		t.Errorf("CI1 deltas = %v / %v", *ci1.PriorityDelta, *ci1.CompositeDelta)
	}
	// CI1 was rank 2 of [CI2(80), CI1(50), CI3(20)], now rank 1 of
	// [CI1(70), CI2(40), CI4(30)]: moved up 1.
	if ci1.RankMove != 1 {
		t.Errorf("CI1 rank move = %d, want +1", ci1.RankMove)
	}
	if len(ci1.NewFindings) != 1 || ci1.NewFindings[0].ID != "ald-33333333" {
		t.Errorf("CI1 new findings = %+v", ci1.NewFindings)
	}
	if len(ci1.ResolvedFindings) != 1 || ci1.ResolvedFindings[0].ID != "ald-11111111" {
		t.Errorf("CI1 resolved = %+v", ci1.ResolvedFindings)
	}
	if ci1.Persisting != 1 {
		t.Errorf("CI1 persisting = %d, want 1", ci1.Persisting)
	}

	if len(res.NewServices) != 1 || res.NewServices[0].CIID != "CI4" {
		t.Errorf("new services = %+v", res.NewServices)
	}
	if len(res.RemovedServices) != 1 || res.RemovedServices[0].CIID != "CI3" {
		t.Errorf("removed services = %+v", res.RemovedServices)
	}
}

func TestDiffNullScoresAreStatesNotZeros(t *testing.T) {
	oldDir, newDir := t.TempDir(), t.TempDir()
	t1 := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	writeDoc(t, oldDir, "a.CI1.json", docWithFindings("CI1", "alpha", f(50), f(75), t1))
	writeDoc(t, newDir, "a.CI1.json", docWithFindings("CI1", "alpha", nil, nil, t1.AddDate(0, 0, 7)))

	res, err := Diff([]string{oldDir}, []string{newDir})
	if err != nil {
		t.Fatal(err)
	}
	sd := res.Changed[0]
	if sd.PriorityDelta != nil || sd.CompositeDelta != nil {
		t.Errorf("deltas across a null score must be nil, got %v/%v", sd.PriorityDelta, sd.CompositeDelta)
	}
	if sd.OldPriority == nil || sd.NewPriority != nil {
		t.Errorf("raw priorities must be preserved: %v -> %v", sd.OldPriority, sd.NewPriority)
	}
}
