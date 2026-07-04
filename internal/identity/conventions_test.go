package identity

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func committed(t *testing.T) *Conventions {
	t.Helper()
	c, err := LoadConventions(filepath.Join("..", "..", "configs", "identity-conventions.yaml"))
	if err != nil {
		t.Fatalf("LoadConventions(committed): %v", err)
	}
	return c
}

// The CI gate: every inline fixture of the committed rules file must pass.
func TestCommittedRuleFixtures(t *testing.T) {
	c := committed(t)
	for _, err := range c.RunRuleTests() {
		t.Error(err)
	}
	if c.ContentHash() == "" || len(c.ContentHash()) != 64 {
		t.Errorf("content hash = %q, want sha256 hex", c.ContentHash())
	}
}

func TestCommittedFileShape(t *testing.T) {
	c := committed(t)
	if len(c.Rules) < 2 {
		t.Fatalf("expected the two starter rules, got %d", len(c.Rules))
	}
	dd := c.Rules[0]
	if dd.ID != "dd-service-tag" || dd.Match.Hint != HintTag || dd.Match.Key != "service" {
		t.Errorf("dd rule = %+v", dd)
	}
	if dd.Confidence != 0.8 || dd.Lookup.Also != "aliases" {
		t.Errorf("dd rule confidence/lookup = %v/%q", dd.Confidence, dd.Lookup.Also)
	}
	pd := c.Rules[1]
	if pd.Confidence != DefaultRuleConfidence {
		t.Errorf("omitted confidence must default to %v, got %v", DefaultRuleConfidence, pd.Confidence)
	}
}

func TestTransformVocabulary(t *testing.T) {
	cases := []struct {
		name string
		tr   Transform
		in   string
		want string
	}{
		{"lowercase", Transform{Lowercase: true}, "Payments-API", "payments-api"},
		{"collapse_whitespace", Transform{CollapseWhitespace: true}, "  a \t b  c ", "a b c"},
		{"strip_prefix first match wins", Transform{StripPrefix: []string{"svc-", "service-"}}, "svc-payments", "payments"},
		{"strip_suffix", Transform{StripSuffix: []string{"-prod", "-production"}}, "billing-production", "billing"},
		{"strip_suffix no match is no-op", Transform{StripSuffix: []string{"-prod"}}, "billing", "billing"},
		{"replace", Transform{Replace: &ReplacePair{Old: "_", New: "-"}}, "a_b_c", "a-b-c"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.tr.Apply(tc.in); got != tc.want {
				t.Errorf("Apply(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestTransformChainOrder(t *testing.T) {
	r := Rule{Transform: []Transform{
		{Lowercase: true},
		{StripSuffix: []string{"-prod"}},
		{Replace: &ReplacePair{Old: "_", New: "-"}},
	}}
	// Order matters: suffix strip happens after lowercasing, so "-PROD"
	// is caught; replace runs last.
	if got := r.TransformValue("PAY_API-PROD"); got != "pay-api" {
		t.Errorf("TransformValue = %q, want pay-api", got)
	}
}

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "conv.yaml")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

const minimalRule = `
version: 1
rules:
  - id: r1
    description: d
    source: "*"
    match: { hint: service_name }
    lookup: { field: name }
`

func TestLoaderValidation(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(string) string
		wantErr string
	}{
		{"wrong version", func(s string) string { return strings.Replace(s, "version: 1", "version: 9", 1) }, "supports 1"},
		{"unknown hint", func(s string) string { return strings.Replace(s, "hint: service_name", "hint: hostname", 1) }, "unknown hint"},
		{"tag without key", func(s string) string { return strings.Replace(s, "hint: service_name", "hint: tag", 1) }, "requires match.key"},
		{"missing lookup field", func(s string) string {
			return strings.Replace(s, "lookup: { field: name }", "lookup: { also: aliases }", 1)
		}, "lookup.field required"},
		{"missing source", func(s string) string { return strings.Replace(s, "source: \"*\"", "source: \"\"", 1) }, "source required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadConventions(writeTemp(t, tc.mutate(minimalRule)))
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("err = %v, want contains %q", err, tc.wantErr)
			}
		})
	}

	dup := minimalRule + `
  - id: r1
    description: d2
    source: "*"
    match: { hint: service_name }
    lookup: { field: name }
`
	if _, err := LoadConventions(writeTemp(t, dup)); err == nil || !strings.Contains(err.Error(), "duplicate rule id") {
		t.Errorf("duplicate id: err = %v", err)
	}

	unknownTransform := strings.Replace(minimalRule, "match: { hint: service_name }",
		"match: { hint: service_name }\n    transform: [uppercase]", 1)
	if _, err := LoadConventions(writeTemp(t, unknownTransform)); err == nil || !strings.Contains(err.Error(), "unknown transform") {
		t.Errorf("unknown transform: err = %v", err)
	}

	badConf := strings.Replace(minimalRule, "lookup: { field: name }",
		"lookup: { field: name }\n    confidence: 1.5", 1)
	if _, err := LoadConventions(writeTemp(t, badConf)); err == nil || !strings.Contains(err.Error(), "out of (0, 1]") {
		t.Errorf("bad confidence: err = %v", err)
	}
}

func TestFailingFixtureIsReported(t *testing.T) {
	broken := strings.Replace(minimalRule, "lookup: { field: name }",
		"lookup: { field: name }\n    transform: [lowercase]\n    tests:\n      - { input: \"ABC\", expect: \"ABC\" }", 1)
	c, err := LoadConventions(writeTemp(t, broken))
	if err != nil {
		t.Fatal(err)
	}
	failures := c.RunRuleTests()
	if len(failures) != 1 || !strings.Contains(failures[0].Error(), `"abc"`) {
		t.Errorf("failures = %v, want one mismatch mentioning got value", failures)
	}
}

func TestContentHashChangesWithContent(t *testing.T) {
	a, err := LoadConventions(writeTemp(t, minimalRule))
	if err != nil {
		t.Fatal(err)
	}
	b, err := LoadConventions(writeTemp(t, minimalRule+"\n# comment\n"))
	if err != nil {
		t.Fatal(err)
	}
	if a.ContentHash() == b.ContentHash() {
		t.Error("content hash must change when file bytes change")
	}
}
