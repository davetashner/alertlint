package output

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeDoc(t *testing.T, dir, name string, doc Document) {
	t.Helper()
	buf, err := Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), buf, 0o644); err != nil {
		t.Fatal(err)
	}
}

func doc(ciID, ciName string, priority *float64, composite *float64, runAt time.Time) Document {
	return Document{
		ContractVersion: ContractVersion,
		Identity: Identity{
			CI:        &CIBlock{ID: ciID, Name: ciName, CriticalityTier: 2, CriticalitySource: "cmdb"},
			Artifacts: []Artifact{},
			Mapping:   Mapping{CoverageNote: "full", BySource: map[string]int{}},
		},
		Scores:   Scores{PriorityScore: priority, Composite: composite, CriticalityTier: 2},
		Findings: []Finding{},
		Metadata: Metadata{Run: Run{Timestamp: runAt, ToolVersion: "t", InvocationID: "i"},
			Sources: []SourceMeta{}},
	}
}

func f(v float64) *float64 { return &v }

func TestAggregateRankAndDedup(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	t1 := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	t2 := t1.Add(2 * time.Hour)

	// Caller A's corpus: two services + an older run of shared-svc.
	writeDoc(t, dirA, "alpha.CI1.json", doc("CI1", "alpha", f(80), f(60), t1))
	writeDoc(t, dirA, "shared.CI2.json", doc("CI2", "shared", f(30), f(85), t1))
	// Caller B's corpus: overlapping newer run of CI2 + a dormant service.
	writeDoc(t, dirB, "shared.CI2.json", doc("CI2", "shared", f(55), f(70), t2))
	writeDoc(t, dirB, "dormant.CI3.json", doc("CI3", "dormant-svc", nil, nil, t2))

	wl, err := Aggregate([]string{dirA, dirB})
	if err != nil {
		t.Fatal(err)
	}
	if wl.Deduped != 1 {
		t.Errorf("deduped = %d, want 1", wl.Deduped)
	}
	if len(wl.Ranked) != 2 || wl.Ranked[0].CIID != "CI1" || wl.Ranked[1].CIID != "CI2" {
		t.Fatalf("ranked = %+v", wl.Ranked)
	}
	// Newest run won whole: CI2 shows priority 55, not 30.
	if *wl.Ranked[1].PriorityScore != 55 {
		t.Errorf("CI2 priority = %v, want the newer run's 55", *wl.Ranked[1].PriorityScore)
	}
	// Null priority is listed, never ranked as zero.
	if len(wl.NotRanked) != 1 || wl.NotRanked[0].CIID != "CI3" {
		t.Errorf("not_ranked = %+v", wl.NotRanked)
	}
}

// Ranking a merged corpus equals ranking the union directly — the
// spec's merge test (REQ-EXEC-003: the formula is corpus-independent).
func TestMergeEqualsUnion(t *testing.T) {
	t1 := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC)
	split1, split2, union := t.TempDir(), t.TempDir(), t.TempDir()
	docs := map[string]Document{
		"a.CI1.json": doc("CI1", "a", f(90), f(40), t1),
		"b.CI2.json": doc("CI2", "b", f(70), f(55), t1),
		"c.CI3.json": doc("CI3", "c", f(70), f(50), t1), // tie on priority
		"d.CI4.json": doc("CI4", "d", f(10), f(95), t1),
	}
	i := 0
	for name, d := range docs {
		writeDoc(t, union, name, d)
		if i%2 == 0 {
			writeDoc(t, split1, name, d)
		} else {
			writeDoc(t, split2, name, d)
		}
		i++
	}
	merged, err := Aggregate([]string{split1, split2})
	if err != nil {
		t.Fatal(err)
	}
	direct, err := Aggregate([]string{union})
	if err != nil {
		t.Fatal(err)
	}
	if len(merged.Ranked) != len(direct.Ranked) {
		t.Fatalf("merged %d vs direct %d", len(merged.Ranked), len(direct.Ranked))
	}
	for i := range merged.Ranked {
		if merged.Ranked[i].CIID != direct.Ranked[i].CIID {
			t.Errorf("rank %d: merged %s vs direct %s", i, merged.Ranked[i].CIID, direct.Ranked[i].CIID)
		}
	}
	// The priority tie broke on composite ascending (worse quality first).
	if direct.Ranked[1].CIID != "CI3" || direct.Ranked[2].CIID != "CI2" {
		t.Errorf("tie-break wrong: %+v", direct.Ranked[1:3])
	}
}

func TestAggregateRejectsMixedMajors(t *testing.T) {
	dir := t.TempDir()
	d1 := doc("CI1", "a", f(50), f(50), time.Now().UTC())
	writeDoc(t, dir, "a.json", d1)
	d2 := doc("CI2", "b", f(60), f(40), time.Now().UTC())
	d2.ContractVersion = "2.0.0"
	writeDoc(t, dir, "b.json", d2)

	if _, err := Aggregate([]string{dir}); err == nil {
		t.Fatal("mixed contract majors must error, never silently skip")
	}
}

func TestAggregateCountsUnresolved(t *testing.T) {
	dir := t.TempDir()
	un := Document{
		ContractVersion: ContractVersion,
		Identity:        Identity{CI: nil, Artifacts: []Artifact{}, Mapping: Mapping{BySource: map[string]int{}}},
		Findings: []Finding{
			{ID: "x", Type: "identity", Severity: "low", Confidence: "high", Rationale: "r", Evidence: []byte(`{}`)},
			{ID: "y", Type: "identity", Severity: "low", Confidence: "high", Rationale: "r", Evidence: []byte(`{}`)},
		},
		Metadata: Metadata{Sources: []SourceMeta{}},
	}
	writeDoc(t, dir, UnresolvedDocumentName, un)
	wl, err := Aggregate([]string{dir})
	if err != nil {
		t.Fatal(err)
	}
	if wl.UnresolvedArtifacts != 2 {
		t.Errorf("unresolved = %d, want 2", wl.UnresolvedArtifacts)
	}
}
