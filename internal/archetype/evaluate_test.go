package archetype

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func lib(t *testing.T) *Library {
	t.Helper()
	l, err := LoadLibrary(filepath.Join("..", "..", "archetypes", "library.yaml"))
	if err != nil {
		t.Fatalf("LoadLibrary(committed asset): %v", err)
	}
	return l
}

func httpArtifact(id string) Artifact {
	return Artifact{
		ID:          id,
		MonitorType: "apm_latency",
		MetricRefs:  []string{"trace.http.request.duration"},
	}
}

func TestLoadCommittedLibrary(t *testing.T) {
	l := lib(t)
	if l.LibraryVersion != "2.0.0" || len(l.Archetypes) != 3 {
		t.Fatalf("library = v%s with %d archetypes, want 2.0.0 / 3", l.LibraryVersion, len(l.Archetypes))
	}
	// Every metric_pattern in the shipped library compiled under RE2 —
	// LoadLibrary would have failed otherwise. This is the authoritative
	// compile check the Python CI validator approximates.
}

func TestLoadRejectsBadLibraries(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	if _, err := LoadLibrary(write("v2.yaml", "schema_version: 2\nlibrary_version: \"9.0.0\"\nanchors: [liveness]\narchetypes: []\n")); err == nil {
		t.Error("unsupported schema_version must fail")
	}
	badAnchor := `schema_version: 1
library_version: "1.0.0"
anchors: [liveness]
archetypes:
  - id: x
    description: d
    applies_when: { any: [ { kind: signal_class, equals: http_server } ] }
    required_signals:
      - id: s
        anchor: not-there
        rationale: r
        absence_severity: high
        satisfied_by: { any: [ { kind: signal_class, equals: http_server } ] }
`
	if _, err := LoadLibrary(write("anchor.yaml", badAnchor)); err == nil {
		t.Error("unknown anchor must fail")
	}
	badRe := `schema_version: 1
library_version: "1.0.0"
anchors: [liveness]
archetypes:
  - id: x
    description: d
    applies_when: { any: [ { kind: metric_pattern, pattern: "([" } ] }
    required_signals:
      - id: s
        anchor: liveness
        rationale: r
        absence_severity: high
        satisfied_by: { any: [ { kind: metric_pattern, pattern: "ok" } ] }
`
	if _, err := LoadLibrary(write("re.yaml", badRe)); err == nil {
		t.Error("non-compiling pattern must fail")
	}
}

func TestPredicateKinds(t *testing.T) {
	l := lib(t)
	cases := []struct {
		name    string
		art     Artifact
		applies bool
		conf    float64
	}{
		{"signal_class strong match", Artifact{ID: "a", SignalClasses: []string{"http_server"}}, true, ConfidenceStrong},
		{"monitor_type strong match", Artifact{ID: "a", MonitorType: "synthetics_http"}, true, ConfidenceStrong},
		{"tag opt-in", Artifact{ID: "a", Tags: map[string]string{"alertlint:archetype": "rest-api"}}, true, ConfidenceStrong},
		{"strong metric pattern", Artifact{ID: "a", MetricRefs: []string{"http.request.latency"}}, true, ConfidenceStrong},
		{"weak-only metric pattern (elb infra hint)", Artifact{ID: "a", MetricRefs: []string{"aws.elb.healthy_hosts"}}, true, ConfidenceWeak},
		{"no match", Artifact{ID: "a", MetricRefs: []string{"disk.free"}}, false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := Evaluate(l, []Artifact{tc.art}, nil)
			rest := findResult(t, res, "rest-api")
			if rest.Applies != tc.applies {
				t.Fatalf("applies = %v, want %v", rest.Applies, tc.applies)
			}
			if tc.applies && rest.Confidence != tc.conf {
				t.Errorf("confidence = %v, want %v", rest.Confidence, tc.conf)
			}
			if tc.applies && rest.Source != SourceInferred {
				t.Errorf("source = %s, want inferred", rest.Source)
			}
		})
	}
}

// Real vendor query strings must drive applicability even though the
// metric term is followed by {tags} or "> N", not a [._] boundary
// (alertlint-o7p regression).
func TestVendorQueryBoundaries(t *testing.T) {
	l := lib(t)
	cases := []struct {
		name    string
		ref     string
		applies bool
	}{
		{"datadog tag-suffixed http metric", "avg(last_10m):p95:trace.http.request.duration{service:checkout-api,env:prod} > 2.5", true},
		{"colon-prefixed request rate", "sum:request.rate{*} > 100", true},
		{"whitespace after term", "http.request.count > 50", true},
		{"cpu metric still not rest-api", "avg(last_5m):avg:system.cpu.utilization.pct{service:x} > 85", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := Evaluate(l, []Artifact{{ID: "m", MetricRefs: []string{tc.ref}}}, nil)
			if got := findResult(t, res, "rest-api").Applies; got != tc.applies {
				t.Errorf("applies = %v, want %v for %q", got, tc.applies, tc.ref)
			}
		})
	}
}

func TestSignalSatisfaction(t *testing.T) {
	l := lib(t)
	inventory := []Artifact{
		httpArtifact("m-latency"), // satisfies latency
		{ID: "m-errors", MetricRefs: []string{"http.request.error_rate"}},
		// nothing satisfies saturation
	}
	res := Evaluate(l, inventory, nil)
	rest := findResult(t, res, "rest-api")
	if !rest.Applies {
		t.Fatal("rest-api must apply")
	}
	bySignal := map[string]SignalResult{}
	for _, s := range rest.Signals {
		bySignal[s.SignalID] = s
	}
	if !bySignal["latency"].Satisfied || !bySignal["error-rate"].Satisfied {
		t.Errorf("latency/error-rate should be satisfied: %+v", rest.Signals)
	}
	if bySignal["saturation"].Satisfied {
		t.Error("saturation must be missing — that is the coverage finding")
	}
	if got := bySignal["latency"].SatisfiedBy; len(got) != 1 || got[0] != "m-latency" {
		t.Errorf("latency satisfied_by = %v", got)
	}
	if bySignal["saturation"].AbsenceSeverity != "medium" {
		t.Errorf("saturation absence_severity = %q, want medium (from library)", bySignal["saturation"].AbsenceSeverity)
	}
}

func TestOverridePrecedence(t *testing.T) {
	l := lib(t)
	// No telemetry at all: business-transactions cannot be inferred.
	empty := []Artifact{}

	// Positive enriched override makes it apply at 0.95.
	res := Evaluate(l, empty, []Override{{
		CI: "SVC1", Archetype: "business-transactions", Applies: true,
		Source: "enriched", Provenance: "OpenAPI declares /payments",
	}})
	biz := findResult(t, res, "business-transactions")
	if !biz.Applies || biz.Source != SourceEnriched || biz.Confidence != ConfidenceEnriched {
		t.Errorf("enriched override: %+v", biz)
	}
	if biz.Provenance == "" {
		t.Error("override provenance must be recorded")
	}

	// Confirmed beats enriched.
	res = Evaluate(l, empty, []Override{
		{Archetype: "business-transactions", Applies: true, Source: "enriched"},
		{Archetype: "business-transactions", Applies: true, Source: "confirmed"},
	})
	biz = findResult(t, res, "business-transactions")
	if biz.Source != SourceConfirmed || biz.Confidence != ConfidenceConfirmed {
		t.Errorf("confirmed must beat enriched: %+v", biz)
	}

	// Negative override suppresses even with matching telemetry — and is
	// recorded, never silent.
	res = Evaluate(l, []Artifact{httpArtifact("m-1")}, []Override{{
		Archetype: "rest-api", Applies: false, Source: "confirmed",
	}})
	rest := findResult(t, res, "rest-api")
	if rest.Applies || !rest.Suppressed {
		t.Errorf("negative override must suppress and record: %+v", rest)
	}
	if len(rest.Signals) != 0 {
		t.Error("suppressed archetype must emit no signal results")
	}
}

// Determinism guarantee from the spec: same inventory + library +
// overrides => byte-identical results.
func TestEvaluateByteIdentical(t *testing.T) {
	l := lib(t)
	inventory := []Artifact{
		httpArtifact("m-1"),
		{ID: "m-2", MetricRefs: []string{"payments.settlement.lag", "order.count"}},
		{ID: "m-3", MonitorType: "tcp_check", Tags: map[string]string{"env": "prod"}},
	}
	first, err := json.Marshal(Evaluate(l, inventory, nil))
	if err != nil {
		t.Fatal(err)
	}
	for range 50 {
		again, err := json.Marshal(Evaluate(l, inventory, nil))
		if err != nil {
			t.Fatal(err)
		}
		if string(first) != string(again) {
			t.Fatal("Evaluate output not byte-identical across runs")
		}
	}
}

func findResult(t *testing.T, results []Result, id string) Result {
	t.Helper()
	for _, r := range results {
		if r.ArchetypeID == id {
			return r
		}
	}
	t.Fatalf("no result for %s", id)
	return Result{}
}
