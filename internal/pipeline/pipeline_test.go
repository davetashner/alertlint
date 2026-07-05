package pipeline

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/davetashner/alertlint/internal/adapter"
	"github.com/davetashner/alertlint/internal/adapter/fake"
	"github.com/davetashner/alertlint/internal/archetype"
	"github.com/davetashner/alertlint/internal/identity"
	"github.com/davetashner/alertlint/internal/model"
	"github.com/davetashner/alertlint/internal/output"
	"github.com/davetashner/alertlint/internal/score"
)

func window() adapter.TimeWindow {
	end := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	return adapter.TimeWindow{Start: end.AddDate(0, 0, -90), End: end}
}

func hints(tags map[string]string, names ...string) model.IdentityHints {
	if tags == nil {
		tags = map[string]string{}
	}
	return model.IdentityHints{Tags: tags, Names: names, ExternalRefs: []model.ExternalRef{}}
}

func envelope(provider, kind, id string, h model.IdentityHints) model.Envelope {
	return model.Envelope{
		SchemaVersion: model.CanonicalSchemaVersion,
		Source:        model.Source{Provider: provider, Tenant: "acct"},
		SourceRef:     model.SourceRef{Kind: kind, NativeID: id},
		IdentityHints: h,
	}
}

// fixtureRegistry builds the MVP scenario: a Datadog-style config source,
// a PagerDuty-style history+action source, and a ServiceNow-style CI
// inventory, all resolving to one CI via the service tag convention.
func fixtureRegistry(t *testing.T, w adapter.TimeWindow) *adapter.Registry {
	t.Helper()
	tier := 1
	_ = tier

	serviceTags := map[string]string{"service": "checkout-api", "env": "prod"}
	monitorName := "[prod] checkout-api p95 latency high"

	configs := &fake.Provider{
		ID: "datadog",
		Configs: []model.AlertConfig{
			{
				Envelope:     envelope("datadog", "monitor", "84312077", hints(serviceTags, "checkout-api")),
				Name:         monitorName,
				ConditionRaw: "avg(last_10m):p95:trace.http.request.duration.p95{service:checkout-api} > 2.5",
				Severity:     model.Severity{Native: "P2", Normalized: model.SeverityHigh},
				Routing:      []model.Route{},
				Status:       model.StatusEnabled,
				CreatedAt:    timePtr(w.End.AddDate(0, 0, -300)),
			},
			{
				Envelope:     envelope("datadog", "monitor", "84312440", hints(serviceTags)),
				Name:         "checkout-api dormant disk monitor",
				ConditionRaw: "avg(last_1h):avg:system.disk.in_use{service:checkout-api} >= 0.9",
				Severity:     model.Severity{Native: "P3", Normalized: model.SeverityMedium},
				Routing:      []model.Route{},
				Status:       model.StatusEnabled,
				CreatedAt:    timePtr(w.End.AddDate(0, 0, -300)),
			},
		},
	}

	// 14 never-acked fast-auto-resolved fires against the latency monitor
	// — the ambiguity-default pile — plus response records with no codes.
	events := make([]model.AlertEvent, 0, 14)
	responses := make([]model.ResponseRecord, 0, 14)
	for i := range 14 {
		id := "INC" + string(rune('A'+i))
		fired := w.Start.AddDate(0, 0, 20+i/5).Add(time.Duration(i%5*3) * time.Hour)
		resolved := fired.Add(4 * time.Minute)
		auto := true
		events = append(events, model.AlertEvent{
			Envelope:        envelope("pagerduty", "incident", id, hints(nil, "Checkout API")),
			AlertRef:        model.AlertRef{Name: &monitorName},
			FiredAt:         fired,
			ResolvedAt:      &resolved,
			AutoResolved:    &auto,
			OccurrenceCount: 1,
			Severity:        model.Severity{Native: "high", Normalized: model.SeverityHigh},
		})
		responses = append(responses, model.ResponseRecord{
			Envelope:      envelope("pagerduty", "incident", id, hints(nil, "Checkout API")),
			EventRef:      model.EventRef{Provider: strPtr("pagerduty"), NativeID: strPtr(id)},
			Disposition:   model.DispositionUnknown,
			LinkedRecords: []model.LinkedRecord{},
		})
	}
	history := &fake.Provider{ID: "pagerduty", Events: events, Responses: responses}

	one := 1
	cmdb := &fake.Provider{ID: "servicenow", CIs: []identity.CI{
		{ID: "CI0012345", Name: "checkout-api", CriticalityTier: &one, Status: "operational"},
		{ID: "CI0099999", Name: "unrelated-svc", Status: "operational"},
	}}

	// An artifact nothing can resolve: lands in _unresolved.json.
	orphan := &fake.Provider{ID: "newrelic", Configs: []model.AlertConfig{{
		Envelope:     envelope("newrelic", "policy", "998811", hints(nil, "pmts-api-golden", "Checkout API")),
		Name:         "pmts-api-golden",
		ConditionRaw: "SELECT ...",
		Severity:     model.Severity{Native: "", Normalized: model.SeverityUnknown},
		Routing:      []model.Route{},
		Status:       model.StatusEnabled,
	}}}

	r := adapter.NewRegistry()
	for _, p := range []*fake.Provider{configs, history, cmdb, orphan} {
		if err := r.Register(p); err != nil {
			t.Fatal(err)
		}
	}
	return r
}

func testOptions(t *testing.T, outDir string) Options {
	t.Helper()
	cfg, err := score.LoadConfig(filepath.Join("..", "..", "configs", "scoring.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	lib, err := archetype.LoadLibrary(filepath.Join("..", "..", "archetypes", "library.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	conv, err := identity.LoadConventions(filepath.Join("..", "..", "configs", "identity-conventions.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	w := window()
	return Options{
		Registry:   fixtureRegistry(t, w),
		Scope:      adapter.Scope{Tenant: "acct"},
		Window:     w,
		Config:     cfg,
		Library:    lib,
		Convention: conv,
		Resolver:   identity.ResolverConfig{CIIDTagKeys: []string{"ci_id"}},
		Fuzzy:      identity.DefaultFuzzyConfig(),
		OutDir:     outDir,
		RunMeta: output.Run{
			Timestamp:    time.Date(2026, 7, 4, 17, 0, 0, 0, time.UTC),
			ToolVersion:  "0.0.1-test",
			InvocationID: "test-run-1",
		},
	}
}

func TestEndToEnd(t *testing.T) {
	dir := t.TempDir()
	res, err := Run(testOptions(t, dir))
	if err != nil {
		t.Fatal(err)
	}
	if res.Services != 1 {
		t.Fatalf("services = %d, want 1 (checkout-api)", res.Services)
	}
	if res.Unresolved != 1 {
		t.Errorf("unresolved = %d, want 1 (the newrelic orphan)", res.Unresolved)
	}

	docPath := filepath.Join(dir, "checkout-api.CI0012345.json")
	raw, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatalf("expected document at sanitized filename: %v", err)
	}
	var doc output.Document
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}

	// Identity: resolved via the dd-service-tag convention.
	if doc.Identity.CI == nil || doc.Identity.CI.ID != "CI0012345" || doc.Identity.CI.CriticalitySource != "cmdb" {
		t.Fatalf("identity = %+v", doc.Identity.CI)
	}
	if len(doc.Identity.Artifacts) == 0 || doc.Identity.Artifacts[0].Resolution.Method != "convention" {
		t.Errorf("artifacts = %+v", doc.Identity.Artifacts)
	}

	// Scores: noisy latency alert + dormant disk monitor.
	if doc.Scores.PriorityScore == nil || doc.Scores.Composite == nil {
		t.Fatalf("scores missing: %+v", doc.Scores)
	}
	if doc.Scores.Inputs.AlertsScored != 1 || doc.Scores.Inputs.AlertsDormant != 1 {
		t.Errorf("inputs = %+v, want 1 scored + 1 dormant", doc.Scores.Inputs)
	}
	if *doc.Scores.Noise >= 100 {
		t.Errorf("noise = %v — 14 ambiguity-default fires must cost something", *doc.Scores.Noise)
	}

	// Findings: the low-confidence noise finding (skill triage queue),
	// TH-4 threshold, coverage gaps from the rest-api archetype.
	var lowNoise, th4, coverage int
	for _, f := range doc.Findings {
		if f.Type == "noise" && f.Confidence == "low" {
			lowNoise++
		}
		if f.Type == "threshold" && strings.Contains(string(f.Evidence), `"TH-4"`) {
			th4++
		}
		if f.Type == "coverage" {
			coverage++
		}
	}
	if lowNoise != 1 {
		t.Errorf("low-confidence noise findings = %d, want 1 (REQ-NOISE-003 seam)", lowNoise)
	}
	if th4 != 1 {
		t.Errorf("TH-4 findings = %d, want 1", th4)
	}
	if coverage == 0 {
		t.Error("rest-api archetype gaps must emit coverage findings")
	}

	// The unresolved document carries the orphan (REQ-ID-003).
	var unres output.Document
	rawU, err := os.ReadFile(filepath.Join(dir, output.UnresolvedDocumentName))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(rawU, &unres); err != nil {
		t.Fatal(err)
	}
	if unres.Identity.CI != nil {
		t.Error("unresolved document must carry ci: null")
	}
	if len(unres.Findings) == 0 || unres.Identity.Mapping.BySource["newrelic"] != 1 {
		t.Errorf("unresolved doc = %+v", unres.Identity.Mapping)
	}
	// Fuzzy candidates ride inside the finding, never as mappings.
	var candFinding string
	for _, f := range unres.Findings {
		if strings.Contains(string(f.Evidence), `"method": "fuzzy"`) {
			candFinding = string(f.Evidence)
		}
	}
	if candFinding == "" {
		t.Error("orphan with a close name must carry fuzzy candidates in evidence")
	} else if !strings.Contains(candFinding, "ambiguous_candidates") {
		t.Errorf("candidate finding reason wrong: %s", candFinding)
	}
}

// Overrides are input data: a positive enriched override makes an
// archetype apply with zero telemetry match; a negative confirmed
// override suppresses — and the suppression is recorded, never silent
// (archetype-library.md §4).
func TestOverridesFlipApplicability(t *testing.T) {
	dir := t.TempDir()
	opts := testOptions(t, dir)
	opts.Overrides = []archetype.Override{
		{CI: "CI0012345", Archetype: "business-transactions", Applies: true,
			Source: "enriched", Provenance: "OpenAPI declares /payments"},
		{CI: "CI0012345", Archetype: "rest-api", Applies: false,
			Source: "confirmed", Provenance: "owner says batch-only"},
	}
	if _, err := Run(opts); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "checkout-api.CI0012345.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc output.Document
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	var bizGaps, restGaps, suppressed int
	for _, f := range doc.Findings {
		if f.Type != "coverage" {
			continue
		}
		ev := string(f.Evidence)
		switch {
		case strings.Contains(ev, `"(suppressed)"`):
			suppressed++
			if !strings.Contains(ev, "rest-api") || !strings.Contains(ev, "confirmed") {
				t.Errorf("suppression record wrong: %s", ev)
			}
		case strings.Contains(ev, "business-transactions"):
			bizGaps++
			if !strings.Contains(ev, `"archetype_source": "enriched"`) {
				t.Errorf("enriched finding must carry archetype_source enriched: %s", ev)
			}
		case strings.Contains(ev, "rest-api"):
			restGaps++
		}
	}
	if bizGaps == 0 {
		t.Error("enriched business-transactions must produce coverage gaps with zero telemetry match")
	}
	if restGaps != 0 {
		t.Error("suppressed rest-api must emit no gap findings")
	}
	if suppressed != 1 {
		t.Errorf("suppression records = %d, want exactly 1", suppressed)
	}
}

// Offline replay guarantee: two runs over identical inputs produce
// byte-identical documents (REQ-SCORE-007).
func TestRunByteIdentical(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	if _, err := Run(testOptions(t, dirA)); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(testOptions(t, dirB)); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"checkout-api.CI0012345.json", output.UnresolvedDocumentName} {
		a, err := os.ReadFile(filepath.Join(dirA, name))
		if err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(filepath.Join(dirB, name))
		if err != nil {
			t.Fatal(err)
		}
		if string(a) != string(b) {
			t.Errorf("%s not byte-identical across runs", name)
		}
	}
}

func timePtr(t time.Time) *time.Time { return &t }
func strPtr(s string) *string        { return &s }
