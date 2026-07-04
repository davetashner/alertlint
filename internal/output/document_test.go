package output

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func exampleDoc(t *testing.T) (Document, []byte) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "example.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc Document
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal spec example: %v", err)
	}
	return doc, raw
}

// The spec's full example document must survive a round trip with no
// field dropped, renamed, or retyped in either direction.
func TestSpecExampleRoundTrip(t *testing.T) {
	doc, raw := exampleDoc(t)
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	var want, got map[string]any
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(want, got) {
		diffKeys(t, "", want, got)
		t.Error("round-trip mismatch against the spec example (see logged diffs)")
	}
}

// diffKeys logs the first-level paths that differ, for actionable failures.
func diffKeys(t *testing.T, prefix string, want, got map[string]any) {
	t.Helper()
	for k, wv := range want {
		gv, ok := got[k]
		if !ok {
			t.Logf("missing key %s%s", prefix, k)
			continue
		}
		if !reflect.DeepEqual(wv, gv) {
			if wm, ok1 := wv.(map[string]any); ok1 {
				if gm, ok2 := gv.(map[string]any); ok2 {
					diffKeys(t, prefix+k+".", wm, gm)
					continue
				}
			}
			t.Logf("value differs at %s%s:\n  want %v\n  got  %v", prefix, k, wv, gv)
		}
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			t.Logf("extra key %s%s", prefix, k)
		}
	}
}

// The spec's jq acceptance checks 1–7, mirrored as Go assertions against
// the parsed example (the jq-in-CI wiring is alertlint-qec).
func TestAcceptanceChecksOnExample(t *testing.T) {
	doc, _ := exampleDoc(t)

	// 1. Contract version present and major-1.
	if !strings.HasPrefix(doc.ContractVersion, "1.") {
		t.Errorf("contract_version = %q", doc.ContractVersion)
	}

	// 2. Only the frozen taxonomy appears.
	valid := map[string]bool{"noise": true, "coverage": true, "threshold": true, "identity": true}
	for _, f := range doc.Findings {
		if !valid[f.Type] {
			t.Errorf("finding %s: type %q outside frozen taxonomy", f.ID, f.Type)
		}
	}

	// 3. Every finding carries rationale, evidence, severity, confidence.
	for _, f := range doc.Findings {
		if f.Rationale == "" || len(f.Evidence) == 0 || f.Severity == "" || f.Confidence == "" {
			t.Errorf("finding %s incomplete", f.ID)
		}
	}

	// 4. The triage queue is expressible: at least one low-confidence
	// finding exists in the example (the designed seam).
	lowCount := 0
	for _, f := range doc.Findings {
		if f.Confidence == "low" {
			lowCount++
		}
	}
	if lowCount == 0 {
		t.Error("example must exercise the low-confidence triage queue")
	}

	// 5. Level-B proposals are concrete, never prose-only.
	for _, f := range doc.Findings {
		if pc := f.ProposedChange; pc != nil {
			if pc.Kind == "" || pc.Target.Source == "" || len(pc.Proposed) == 0 || string(pc.Proposed) == "null" || pc.GeneratedBy == "" {
				t.Errorf("finding %s: proposed_change not concrete: %+v", f.ID, pc)
			}
		}
	}

	// 6. Reproducibility metadata complete.
	m := doc.Metadata
	if m.Window.Days == 0 || m.Run.ToolVersion == "" || m.ArchetypeLibraryVersion == "" ||
		m.Config.ConfigHash == "" || m.Config.Weights == (WeightsBlock{}) {
		t.Errorf("metadata incomplete: %+v", m)
	}

	// 7. Fuzzy never joins: artifact methods restricted.
	methods := map[string]bool{"exact": true, "confirmed": true, "convention": true}
	for _, a := range doc.Identity.Artifacts {
		if !methods[a.Resolution.Method] {
			t.Errorf("artifact %s joined by %q — fuzzy must never join (ADR 0002)", a.NativeID, a.Resolution.Method)
		}
	}
}

func TestFindingIDStableAndDistinct(t *testing.T) {
	w := Window{
		Start: time.Date(2026, 4, 5, 0, 0, 0, 0, time.UTC),
		End:   time.Date(2026, 7, 4, 0, 0, 0, 0, time.UTC),
		Days:  90,
	}
	src, id := "datadog", "monitor:4821337"
	subject := Subject{Source: &src, NativeID: &id}

	a := FindingID("noise", subject, w)
	if a != FindingID("noise", subject, w) {
		t.Error("finding id not stable")
	}
	if !strings.HasPrefix(a, "ald-") || len(a) != 12 {
		t.Errorf("id format = %q, want ald-XXXXXXXX", a)
	}
	if a == FindingID("threshold", subject, w) {
		t.Error("type must differentiate ids")
	}
	w2 := w
	w2.End = w.End.Add(24 * time.Hour)
	if a == FindingID("noise", subject, w2) {
		t.Error("window must differentiate ids")
	}
}

func TestFilenameSanitization(t *testing.T) {
	cases := []struct{ name, id, want string }{
		{"payments-api", "CI0012345", "payments-api.CI0012345.json"},
		{"payments api (EU) / v2", "CI 99", "payments-api-EU-v2.CI-99.json"},
		{"härte/страх", "CI1", "h-rte-.CI1.json"},
		{"..", "x", "...x.json"}, // dots are allowed; id keeps it unique
	}
	for _, tc := range cases {
		if got := Filename(tc.name, tc.id); got != tc.want {
			t.Errorf("Filename(%q, %q) = %q, want %q", tc.name, tc.id, got, tc.want)
		}
	}
}

func TestMarshalByteIdentical(t *testing.T) {
	doc, _ := exampleDoc(t)
	first, err := Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(first), "}\n") {
		t.Error("canonical form must end with a newline")
	}
	for range 25 {
		again, err := Marshal(doc)
		if err != nil {
			t.Fatal(err)
		}
		if string(first) != string(again) {
			t.Fatal("Marshal not byte-identical across calls")
		}
	}
}

func TestUnresolvedDocumentShape(t *testing.T) {
	doc := Document{
		ContractVersion: ContractVersion,
		Identity: Identity{
			CI:        nil, // the reserved unresolved document
			Artifacts: []Artifact{},
			Mapping:   Mapping{CoverageNote: "partial", BySource: map[string]int{}},
		},
		Findings: []Finding{},
	}
	buf, err := Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(buf, &m); err != nil {
		t.Fatal(err)
	}
	identity := m["identity"].(map[string]any)
	if identity["ci"] != nil {
		t.Error("unresolved document must carry ci: null explicitly")
	}
	if m["findings"] == nil {
		t.Error("findings must be [], never null")
	}
}
