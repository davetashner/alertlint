package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The embedded schema must stay identical to the canonical repo copy.
func TestEmbeddedSchemaInSync(t *testing.T) {
	repo, err := os.ReadFile(filepath.Join("..", "..", "schemas", "output-contract-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(repo) != string(contractSchemaV1) {
		t.Fatal("cmd/alertlint/contract_schema.json diverged from schemas/output-contract-v1.json — copy it over")
	}
}

func TestValidateDemoCorpus(t *testing.T) {
	repoRoot := filepath.Join("..", "..")
	out := t.TempDir()
	var stdout, stderr strings.Builder
	code := run([]string{
		"analyze",
		"--replay", filepath.Join(repoRoot, "fixtures", "demo"),
		"--tenant", "demo",
		"--out", out,
		"--run-timestamp", "2026-07-04T18:00:00Z",
		"--scoring-config", filepath.Join(repoRoot, "configs", "scoring.yaml"),
		"--archetype-library", filepath.Join(repoRoot, "archetypes", "library.yaml"),
		"--identity-conventions", filepath.Join(repoRoot, "fixtures", "demo", "identity-conventions.yaml"),
	}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("analyze: %s", stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"validate", out}, &stdout, &stderr); code != 0 {
		t.Fatalf("valid corpus rejected:\n%s", stderr.String())
	}
	if strings.Count(stdout.String(), "ok ") != 3 {
		t.Errorf("expected 3 ok lines:\n%s", stdout.String())
	}

	// Mutate a finding type: must fail with a pointed path.
	docPath := filepath.Join(out, "payments-api.CI0002222.json")
	raw, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatal(err)
	}
	mutated := strings.Replace(string(raw), `"type": "noise"`, `"type": "vibes"`, 1)
	if mutated == string(raw) {
		t.Fatal("mutation did not apply")
	}
	if err := os.WriteFile(docPath, []byte(mutated), 0o644); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"validate", out}, &stdout, &stderr); code != 1 {
		t.Fatalf("mutated corpus must fail, got exit %d", code)
	}
	if !strings.Contains(stderr.String(), "vibes") && !strings.Contains(stderr.String(), "findings") {
		t.Errorf("error not pointed:\n%s", stderr.String())
	}
}
