package identity

import (
	"encoding/json"
	"testing"

	"github.com/davetashner/alertlint/internal/model"
)

func tier(n int) *int { return &n }

func inventory() *Inventory {
	return NewInventory([]CI{
		{ID: "CI001", Name: "payments-api", Aliases: []string{"svc-payments"}, CriticalityTier: tier(1), Status: "operational"},
		{ID: "CI002", Name: "checkout-api", CriticalityTier: tier(2), Status: "operational"},
		{ID: "CI003", Name: "billing", CriticalityTier: tier(3), Status: "retired"},
		{ID: "CI004", Name: "payments-worker", Aliases: []string{"payments"}, Status: "operational"},
		{ID: "CI005", Name: "dup-name", Status: "operational"},
		{ID: "CI006", Name: "dup-name", Status: "operational"},
	})
}

func rcfg() ResolverConfig { return ResolverConfig{CIIDTagKeys: []string{"ci_id"}} }

func artifact(source, key string, hints model.IdentityHints) Artifact {
	return Artifact{
		Ref:       ArtifactRef{Source: source, Kind: "monitor", Key: key},
		DataClass: ClassConfig,
		Hints:     hints,
	}
}

func tags(kv ...string) model.IdentityHints {
	h := model.IdentityHints{Tags: map[string]string{}, Names: []string{}, ExternalRefs: []model.ExternalRef{}}
	for i := 0; i < len(kv); i += 2 {
		h.Tags[kv[i]] = kv[i+1]
	}
	return h
}

func conventions(t *testing.T) *Conventions {
	t.Helper()
	c, err := LoadConventions("../../configs/identity-conventions.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestExactStrategy(t *testing.T) {
	inv := inventory()
	cases := []struct {
		name        string
		hints       model.IdentityHints
		wantMap     string // CI id, "" = no mapping
		wantFinding string // subtype, "" = none
	}{
		{"operational CI maps at 1.0", tags("ci_id", "CI001"), "CI001", ""},
		{"retired CI is stale, no mapping", tags("ci_id", "CI003"), "", FindingStaleCIReference},
		{"unknown CI is dangling, no fall-through", tags("ci_id", "CI999", "service", "payments-api"), "", FindingDanglingCIReference},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := Resolve([]Artifact{artifact("datadog", "m1", tc.hints)}, inv, nil, conventions(t), rcfg())
			checkOutcome(t, res, tc.wantMap, "exact", ConfidenceExact, tc.wantFinding)
			if len(res.Unresolved) != 0 {
				t.Error("explicit reference must terminate the chain, never fall to unresolved")
			}
		})
	}
}

func TestAmbiguousExactReference(t *testing.T) {
	// Two conflicting explicit refs via two configured tag keys.
	cfg := ResolverConfig{CIIDTagKeys: []string{"ci_id", "cmdb_ci"}}
	h := tags("ci_id", "CI001", "cmdb_ci", "CI002")
	res := Resolve([]Artifact{artifact("datadog", "m1", h)}, inventory(), nil, nil, cfg)
	if len(res.Mappings) != 0 || len(res.Findings) != 1 || res.Findings[0].Subtype != FindingAmbiguousCIReference {
		t.Fatalf("want single ambiguous_ci_reference, got %+v", res)
	}
	if len(res.Findings[0].Candidates) != 2 {
		t.Errorf("candidates = %v, want both refs", res.Findings[0].Candidates)
	}
}

func TestConfirmedStrategy(t *testing.T) {
	inv := inventory()
	art := artifact("datadog", "monitor/4812007", tags("service", "payments_api_v2"))
	confirmed := []ConfirmedMapping{{Artifact: art.Ref, CIID: "CI001"}}

	// The worked example's second run: the legacy-tagged monitor joins via
	// its confirmed mapping at 0.95 — no fuzzy involved.
	res := Resolve([]Artifact{art}, inv, confirmed, conventions(t), rcfg())
	checkOutcome(t, res, "CI001", "confirmed", ConfidenceConfirmed, "")

	// Exact beats confirmed when both exist.
	art2 := artifact("datadog", "monitor/4812007", tags("ci_id", "CI002", "service", "payments_api_v2"))
	res = Resolve([]Artifact{art2}, inv, confirmed, conventions(t), rcfg())
	checkOutcome(t, res, "CI002", "exact", ConfidenceExact, "")

	// Confirmed mapping to a retired CI is stale, not a join.
	confirmedStale := []ConfirmedMapping{{Artifact: art.Ref, CIID: "CI003"}}
	res = Resolve([]Artifact{art}, inv, confirmedStale, conventions(t), rcfg())
	checkOutcome(t, res, "", "", 0, FindingStaleConfirmedMapping)
}

func TestConventionStrategy(t *testing.T) {
	inv := inventory()

	// dd-service-tag: lowercase + strip -prod => payments-api (unique).
	res := Resolve([]Artifact{artifact("datadog", "m1", tags("service", "Payments-API-PROD"))},
		inv, nil, conventions(t), rcfg())
	checkOutcome(t, res, "CI001", "convention", 0.8, "")
	if res.Mappings[0].Evidence.RuleID != "dd-service-tag" {
		t.Errorf("evidence rule_id = %q", res.Mappings[0].Evidence.RuleID)
	}
	if res.Mappings[0].Evidence.MatchedValue != "payments-api" {
		t.Errorf("evidence matched_value = %q", res.Mappings[0].Evidence.MatchedValue)
	}

	// Source filter: a datadog rule never fires for a pagerduty artifact.
	pd := Artifact{
		Ref:       ArtifactRef{Source: "pagerduty", Kind: "service", Key: "P1"},
		DataClass: ClassAction,
		Hints:     model.IdentityHints{Tags: map[string]string{}, Names: []string{"Checkout   API"}, ExternalRefs: []model.ExternalRef{}},
	}
	res = Resolve([]Artifact{pd}, inv, nil, conventions(t), rcfg())
	checkOutcome(t, res, "CI002", "convention", 0.8, "")
	if res.Mappings[0].Evidence.RuleID != "pd-service-name" {
		t.Errorf("pd artifact resolved by %q, want pd-service-name", res.Mappings[0].Evidence.RuleID)
	}

	// Multi-CI match: deterministic refusal with finding.
	dup := Resolve([]Artifact{artifact("datadog", "m2", tags("service", "dup-name"))},
		inv, nil, conventions(t), rcfg())
	if len(dup.Mappings) != 0 || len(dup.Findings) != 1 || dup.Findings[0].Subtype != FindingAmbiguousConvention {
		t.Fatalf("want ambiguous_convention_match, got %+v", dup)
	}
	if len(dup.Findings[0].Candidates) != 2 {
		t.Errorf("candidates = %v", dup.Findings[0].Candidates)
	}

	// Alias lookup via `also: aliases`.
	res = Resolve([]Artifact{artifact("datadog", "m3", tags("service", "svc-payments"))},
		inv, nil, conventions(t), rcfg())
	checkOutcome(t, res, "CI001", "convention", 0.8, "")
}

func TestUnresolvedGoesToFuzzyQueue(t *testing.T) {
	res := Resolve([]Artifact{artifact("datadog", "m1", tags("service", "no-such-thing"))},
		inventory(), nil, conventions(t), rcfg())
	if len(res.Mappings) != 0 || len(res.Findings) != 0 || len(res.Unresolved) != 1 {
		t.Fatalf("want pure unresolved, got %+v", res)
	}
}

func TestCoverage(t *testing.T) {
	mappings := []Mapping{
		{CIID: "CI001", DataClass: ClassConfig, Method: "exact", Confidence: 1.0},
		{CIID: "CI001", DataClass: ClassConfig, Method: "convention", Confidence: 0.8},
		{CIID: "CI001", DataClass: ClassHistory, Method: "confirmed", Confidence: 0.95},
		{CIID: "CI999", DataClass: ClassConfig, Method: "exact", Confidence: 1.0}, // other service
	}
	cov := CoverageFor("CI001", mappings, map[DataClass]int{ClassConfig: 2})

	if cov.Resolved[ClassConfig] != (MethodCounts{Exact: 1, Convention: 1}) {
		t.Errorf("config counts = %+v", cov.Resolved[ClassConfig])
	}
	if got := cov.PerClass[ClassConfig]; got != 0.5 {
		t.Errorf("config coverage = %v, want 0.5 (2 resolved / 4)", got)
	}
	if got := cov.PerClass[ClassHistory]; got != 1.0 {
		t.Errorf("history coverage = %v, want 1.0", got)
	}
	if got := cov.PerClass[ClassAction]; got != 1.0 {
		t.Errorf("empty action class coverage = %v, want 1.0", got)
	}
	if cov.MinConfidence != 0.8 {
		t.Errorf("min_confidence = %v, want 0.8 — weakest joined method", cov.MinConfidence)
	}
	if !cov.Partial {
		t.Error("partial must be true when any class < 1.0")
	}
	if got := cov.Overall; got != 3.0/5.0 {
		t.Errorf("overall = %v, want 0.6 (3 resolved / 5 attributable)", got)
	}
}

func TestResolveDeterministic(t *testing.T) {
	inv := inventory()
	conv := conventions(t)
	arts := []Artifact{
		artifact("datadog", "m1", tags("service", "Payments-API-PROD")),
		artifact("datadog", "m2", tags("ci_id", "CI002")),
		artifact("datadog", "m3", tags("service", "unknown-thing")),
	}
	first, _ := json.Marshal(Resolve(arts, inv, nil, conv, rcfg()))
	for range 50 {
		again, _ := json.Marshal(Resolve(arts, inv, nil, conv, rcfg()))
		if string(first) != string(again) {
			t.Fatal("Resolve not byte-identical across runs")
		}
	}
}

func checkOutcome(t *testing.T, res Result, wantCI, wantMethod string, wantConf float64, wantFinding string) {
	t.Helper()
	if wantCI == "" {
		if len(res.Mappings) != 0 {
			t.Fatalf("unexpected mapping: %+v", res.Mappings)
		}
	} else {
		if len(res.Mappings) != 1 {
			t.Fatalf("mappings = %+v, want one to %s", res.Mappings, wantCI)
		}
		m := res.Mappings[0]
		if m.CIID != wantCI || m.Method != wantMethod || m.Confidence != wantConf {
			t.Fatalf("mapping = %+v, want %s/%s@%v", m, wantCI, wantMethod, wantConf)
		}
	}
	if wantFinding == "" {
		if len(res.Findings) != 0 {
			t.Fatalf("unexpected findings: %+v", res.Findings)
		}
	} else {
		if len(res.Findings) != 1 || res.Findings[0].Subtype != wantFinding {
			t.Fatalf("findings = %+v, want %s", res.Findings, wantFinding)
		}
	}
}
