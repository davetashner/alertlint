package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The MVP dry run, guarded in CI: replay the committed demo corpus and
// assert the run shape plus byte-identical repeatability
// (docs/dryrun/mvp-dry-run.md).
func TestReplayDemoCorpus(t *testing.T) {
	repoRoot := filepath.Join("..", "..")
	runOnce := func(outDir string) string {
		t.Helper()
		var stdout, stderr strings.Builder
		code := run([]string{
			"analyze",
			"--replay", filepath.Join(repoRoot, "fixtures", "demo"),
			"--tenant", "demo",
			"--out", outDir,
			"--run-timestamp", "2026-07-04T18:00:00Z",
			"--scoring-config", filepath.Join(repoRoot, "configs", "scoring.yaml"),
			"--archetype-library", filepath.Join(repoRoot, "archetypes", "library.yaml"),
			"--identity-conventions", filepath.Join(repoRoot, "fixtures", "demo", "identity-conventions.yaml"),
		}, &stdout, &stderr)
		if code != 0 {
			t.Fatalf("analyze exit %d: %s", code, stderr.String())
		}
		return stdout.String()
	}

	dirA := t.TempDir()
	summary := runOnce(dirA)
	if !strings.Contains(summary, "analyzed 2 service(s), 1 unresolved artifact(s)") {
		t.Fatalf("unexpected run summary: %s", summary)
	}
	for _, name := range []string{"checkout-api.CI0001111.json", "payments-api.CI0002222.json", "_unresolved.json"} {
		if _, err := os.Stat(filepath.Join(dirA, name)); err != nil {
			t.Errorf("missing document %s: %v", name, err)
		}
	}

	// The skill's triage queue exists: checkout-api carries a
	// low-confidence noise finding (REQ-NOISE-003 seam).
	raw, err := os.ReadFile(filepath.Join(dirA, "checkout-api.CI0001111.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"confidence": "low"`) {
		t.Error("demo corpus must produce a low-confidence finding — the skill scenario depends on it")
	}

	// Byte-identical repeatability with a pinned --run-timestamp.
	dirB := t.TempDir()
	runOnce(dirB)
	for _, name := range []string{"checkout-api.CI0001111.json", "payments-api.CI0002222.json", "_unresolved.json"} {
		a, _ := os.ReadFile(filepath.Join(dirA, name))
		b, _ := os.ReadFile(filepath.Join(dirB, name))
		if string(a) != string(b) {
			t.Errorf("%s not byte-identical across replay runs", name)
		}
	}

	// Worklist ranks payments-api (worse quality) above checkout-api
	// despite the lower tier — the criticality-weighted design intent.
	var wlOut, wlErr strings.Builder
	if code := run([]string{"worklist", dirA}, &wlOut, &wlErr); code != 0 {
		t.Fatalf("worklist exit %d: %s", code, wlErr.String())
	}
	lines := strings.Split(strings.TrimSpace(wlOut.String()), "\n")
	if len(lines) < 3 || !strings.Contains(lines[1], "payments-api") || !strings.Contains(lines[2], "checkout-api") {
		t.Errorf("worklist order wrong:\n%s", wlOut.String())
	}
}
