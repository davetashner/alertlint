package identity

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mappingsPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "identity-mappings.yaml")
}

func entry(source, kind, key, ci string) MappingEntry {
	return MappingEntry{
		Artifact:    ArtifactRef{Source: source, Kind: kind, Key: key},
		CIID:        ci,
		ConfirmedBy: "test",
		ConfirmedAt: "2026-07-04",
		Origin:      &MappingOrigin{Method: "fuzzy", Score: 0.91, Hint: "service:payments_api_v2"},
	}
}

func TestMissingFileIsEmptyRatchet(t *testing.T) {
	f, confirmed, err := LoadMappings(mappingsPath(t))
	if err != nil || len(confirmed) != 0 || f.Version != SupportedMappingsVersion {
		t.Fatalf("missing file: f=%+v confirmed=%v err=%v", f, confirmed, err)
	}
}

func TestConfirmRoundTrip(t *testing.T) {
	path := mappingsPath(t)
	if err := Confirm(path, entry("datadog", "monitor", "monitor/4812007", "CI001")); err != nil {
		t.Fatal(err)
	}
	if err := Confirm(path, entry("pagerduty", "service", "P1", "CI002")); err != nil {
		t.Fatal(err)
	}

	f, confirmed, err := LoadMappings(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(confirmed) != 2 {
		t.Fatalf("confirmed = %d, want 2", len(confirmed))
	}
	// Stable ordering: datadog before pagerduty.
	if f.Mappings[0].Artifact.Source != "datadog" || f.Mappings[1].Artifact.Source != "pagerduty" {
		t.Errorf("ordering = %+v", f.Mappings)
	}
	if f.Mappings[0].Origin == nil || f.Mappings[0].Origin.Score != 0.91 {
		t.Errorf("origin lost: %+v", f.Mappings[0].Origin)
	}
	// The file carries its explanatory header for hand editors.
	raw, _ := os.ReadFile(path)
	if !strings.HasPrefix(string(raw), "# alertlint confirmed identity mappings") {
		t.Error("file header missing")
	}
}

func TestConfirmIdempotentAndConflictSafe(t *testing.T) {
	path := mappingsPath(t)
	e := entry("datadog", "monitor", "m1", "CI001")
	if err := Confirm(path, e); err != nil {
		t.Fatal(err)
	}
	// Identical confirmation: no-op.
	if err := Confirm(path, e); err != nil {
		t.Fatalf("idempotent confirm errored: %v", err)
	}
	f, _, _ := LoadMappings(path)
	if len(f.Mappings) != 1 {
		t.Fatalf("duplicate appended: %d entries", len(f.Mappings))
	}
	// Conflicting CI: refused, hand edit required.
	conflict := e
	conflict.CIID = "CI999"
	if err := Confirm(path, conflict); err == nil || !strings.Contains(err.Error(), "already mapped") {
		t.Errorf("conflicting confirm must be refused: %v", err)
	}
}

func TestLoadMappingsValidation(t *testing.T) {
	dir := t.TempDir()
	write := func(content string) string {
		p := filepath.Join(dir, "m.yaml")
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	cases := []struct {
		name, content, wantErr string
	}{
		{"wrong version", "version: 9\nmappings: []\n", "supports 1"},
		{"incomplete entry", `version: 1
mappings:
  - artifact: { source: datadog, kind: monitor }
    ci_id: CI1
    confirmed_by: x
`, "incomplete"},
		{"anonymous", `version: 1
mappings:
  - artifact: { source: datadog, kind: monitor, key: m1 }
    ci_id: CI1
`, "confirmed_by required"},
		{"conflict", `version: 1
mappings:
  - artifact: { source: datadog, kind: monitor, key: m1 }
    ci_id: CI1
    confirmed_by: a
  - artifact: { source: datadog, kind: monitor, key: m1 }
    ci_id: CI2
    confirmed_by: b
`, "resolve the conflict"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := LoadMappings(write(tc.content))
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("err = %v, want contains %q", err, tc.wantErr)
			}
		})
	}
}
