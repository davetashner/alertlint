package identity

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// SupportedConventionsVersion is the rules-file schema this CLI understands.
const SupportedConventionsVersion = 1

// DefaultRuleConfidence applies when a rule omits confidence
// (docs/specs/identity-resolution.md, strategy 3).
const DefaultRuleConfidence = 0.8

// HintKind is what part of an artifact's identity hints a rule matches.
type HintKind string

const (
	HintTag             HintKind = "tag"
	HintServiceName     HintKind = "service_name"
	HintIntegrationLink HintKind = "integration_link"
)

// Conventions is the parsed identity-conventions.yaml: deterministic
// org-specific resolution rules, versioned config with test fixtures
// (ADR 0002: adding a convention is config, not code).
type Conventions struct {
	Version int    `yaml:"version"`
	Rules   []Rule `yaml:"rules"`

	contentHash string // sha256 of the file bytes, for run metadata
}

// Rule is one convention: extract a hint, transform it, equal-match it
// against a CI inventory field. First rule (file order) matching exactly
// one CI wins; multi-match is a finding, never a mapping.
type Rule struct {
	ID          string      `yaml:"id"`
	Description string      `yaml:"description"`
	Source      string      `yaml:"source"` // adapter id, or "*" for any
	Match       Match       `yaml:"match"`
	Transform   []Transform `yaml:"transform"`
	Lookup      Lookup      `yaml:"lookup"`
	Confidence  float64     `yaml:"confidence"`
	// Tests are per-rule transform fixtures, CI-gated: input hint value ->
	// expected transformed value.
	Tests []RuleTest `yaml:"tests"`
}

// Match selects which hint a rule reads.
type Match struct {
	Hint HintKind `yaml:"hint"`
	Key  string   `yaml:"key"` // for tags: the tag key
}

// Lookup names the CI inventory field(s) the transformed value must
// equal-match.
type Lookup struct {
	Field string `yaml:"field"`
	Also  string `yaml:"also"`
}

// RuleTest is one transform fixture.
type RuleTest struct {
	Input  string `yaml:"input"`
	Expect string `yaml:"expect"`
}

// Transform is one step of the fixed vocabulary. Exactly one field is set.
// No regex-with-capture-groups in v1 — the rule file stays auditable.
type Transform struct {
	Lowercase          bool
	CollapseWhitespace bool
	StripPrefix        []string
	StripSuffix        []string
	Replace            *ReplacePair
}

// ReplacePair is the argument of a replace step.
type ReplacePair struct {
	Old string `yaml:"old"`
	New string `yaml:"new"`
}

// UnmarshalYAML accepts the spec's mixed form: bare strings ("lowercase",
// "collapse_whitespace") and single-key maps (strip_suffix: [...],
// strip_prefix: [...], replace: {old, new}).
func (t *Transform) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind == yaml.ScalarNode {
		var name string
		if err := node.Decode(&name); err != nil {
			return err
		}
		switch name {
		case "lowercase":
			t.Lowercase = true
		case "collapse_whitespace":
			t.CollapseWhitespace = true
		default:
			return fmt.Errorf("unknown transform %q", name)
		}
		return nil
	}
	var m map[string]yaml.Node
	if err := node.Decode(&m); err != nil {
		return err
	}
	if len(m) != 1 {
		return fmt.Errorf("transform step must have exactly one key, got %d", len(m))
	}
	for key, val := range m {
		switch key {
		case "strip_prefix":
			return val.Decode(&t.StripPrefix)
		case "strip_suffix":
			return val.Decode(&t.StripSuffix)
		case "replace":
			t.Replace = &ReplacePair{}
			return val.Decode(t.Replace)
		default:
			return fmt.Errorf("unknown transform %q", key)
		}
	}
	return nil
}

// Apply runs one transform step.
func (t Transform) Apply(s string) string {
	switch {
	case t.Lowercase:
		return strings.ToLower(s)
	case t.CollapseWhitespace:
		return strings.Join(strings.Fields(s), " ")
	case len(t.StripPrefix) > 0:
		for _, p := range t.StripPrefix { // first matching affix wins; one strip per step
			if rest, ok := strings.CutPrefix(s, p); ok {
				return rest
			}
		}
	case len(t.StripSuffix) > 0:
		for _, p := range t.StripSuffix {
			if rest, ok := strings.CutSuffix(s, p); ok {
				return rest
			}
		}
	case t.Replace != nil:
		return strings.ReplaceAll(s, t.Replace.Old, t.Replace.New)
	}
	return s
}

// TransformValue applies a rule's transform chain in order.
func (r Rule) TransformValue(s string) string {
	for _, t := range r.Transform {
		s = t.Apply(s)
	}
	return s
}

// ContentHash is the sha256 of the loaded file, recorded in run metadata
// (REQ-OUT-002 reproducibility).
func (c *Conventions) ContentHash() string { return c.contentHash }

// LoadConventions reads and validates a rules file.
func LoadConventions(path string) (*Conventions, error) {
	buf, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("identity conventions: %w", err)
	}
	var c Conventions
	if err := yaml.Unmarshal(buf, &c); err != nil {
		return nil, fmt.Errorf("identity conventions: %w", err)
	}
	if c.Version != SupportedConventionsVersion {
		return nil, fmt.Errorf("identity conventions version %d: this CLI supports %d",
			c.Version, SupportedConventionsVersion)
	}
	seen := map[string]bool{}
	for i := range c.Rules {
		r := &c.Rules[i]
		if r.ID == "" {
			return nil, fmt.Errorf("identity conventions: rule %d has no id", i)
		}
		if seen[r.ID] {
			return nil, fmt.Errorf("identity conventions: duplicate rule id %q", r.ID)
		}
		seen[r.ID] = true
		switch r.Match.Hint {
		case HintTag:
			if r.Match.Key == "" {
				return nil, fmt.Errorf("identity conventions: rule %s: hint tag requires match.key", r.ID)
			}
		case HintServiceName, HintIntegrationLink:
			// no key needed
		default:
			return nil, fmt.Errorf("identity conventions: rule %s: unknown hint %q", r.ID, r.Match.Hint)
		}
		if r.Source == "" {
			return nil, fmt.Errorf("identity conventions: rule %s: source required (use \"*\" for any)", r.ID)
		}
		if r.Lookup.Field == "" {
			return nil, fmt.Errorf("identity conventions: rule %s: lookup.field required", r.ID)
		}
		if r.Confidence == 0 {
			r.Confidence = DefaultRuleConfidence
		}
		if r.Confidence <= 0 || r.Confidence > 1 {
			return nil, fmt.Errorf("identity conventions: rule %s: confidence %v out of (0, 1]", r.ID, r.Confidence)
		}
	}
	sum := sha256.Sum256(buf)
	c.contentHash = hex.EncodeToString(sum[:])
	return &c, nil
}

// RunRuleTests executes every rule's inline fixtures and returns one error
// per failure — the CI gate that keeps org rule edits honest.
func (c *Conventions) RunRuleTests() []error {
	var failures []error
	for _, r := range c.Rules {
		for _, tc := range r.Tests {
			if got := r.TransformValue(tc.Input); got != tc.Expect {
				failures = append(failures,
					fmt.Errorf("rule %s: TransformValue(%q) = %q, want %q", r.ID, tc.Input, got, tc.Expect))
			}
		}
	}
	return failures
}
