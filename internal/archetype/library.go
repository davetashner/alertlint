package archetype

import (
	"fmt"
	"os"
	"regexp"

	"gopkg.in/yaml.v3"
)

// Confidence constants fixed in the CLI per library schema_version
// (docs/specs/archetype-library.md §3 step 4, §4).
const (
	ConfidenceStrong    = 0.9  // strongest matched applies_when predicate: strong
	ConfidenceWeak      = 0.5  // strongest matched applies_when predicate: weak
	ConfidenceEnriched  = 0.95 // positive path-C override
	ConfidenceConfirmed = 1.0  // positive path-D override
)

// SupportedSchemaVersion is the library file format this CLI understands.
const SupportedSchemaVersion = 1

// Library is the parsed archetype -> required-signal library
// (archetypes/library.yaml).
type Library struct {
	SchemaVersion  int         `yaml:"schema_version"`
	LibraryVersion string      `yaml:"library_version"`
	Anchors        []string    `yaml:"anchors"`
	Archetypes     []Archetype `yaml:"archetypes"`
}

// Archetype is one entry: applicability rules plus required signals.
type Archetype struct {
	ID              string           `yaml:"id"`
	Description     string           `yaml:"description"`
	AppliesWhen     Combinator       `yaml:"applies_when"`
	RequiredSignals []RequiredSignal `yaml:"required_signals"`
}

// RequiredSignal is one opinion: a signal the archetype must alert on.
type RequiredSignal struct {
	ID              string     `yaml:"id"`
	Anchor          string     `yaml:"anchor"`
	Rationale       string     `yaml:"rationale"`
	AbsenceSeverity string     `yaml:"absence_severity"`
	SatisfiedBy     Combinator `yaml:"satisfied_by"`
}

// Combinator combines predicates and nested combinators with any/all/none
// semantics. Empty lists are absent, not vacuous truths: a combinator with
// only `any` requires at least one match in `any`.
type Combinator struct {
	Any  []Node `yaml:"any"`
	All  []Node `yaml:"all"`
	None []Node `yaml:"none"`
}

// Node is either a leaf predicate (Kind != "") or a nested combinator.
type Node struct {
	// Leaf predicate fields.
	Kind     string   `yaml:"kind"`
	Equals   string   `yaml:"equals"`
	Pattern  string   `yaml:"pattern"`
	In       []string `yaml:"in"`
	Strength string   `yaml:"strength"`

	// Nested combinator fields.
	Any  []Node `yaml:"any"`
	All  []Node `yaml:"all"`
	None []Node `yaml:"none"`

	re *regexp.Regexp // compiled at load; RE2 by construction (Go regexp)
}

// LoadLibrary reads, structurally checks, and compiles a library file.
// Pattern compilation here is the authoritative RE2 check (Go's regexp is
// RE2): a library that loads is a library whose every pattern is linear-time.
func LoadLibrary(path string) (*Library, error) {
	buf, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("archetype library: %w", err)
	}
	var lib Library
	if err := yaml.Unmarshal(buf, &lib); err != nil {
		return nil, fmt.Errorf("archetype library: %w", err)
	}
	if lib.SchemaVersion != SupportedSchemaVersion {
		return nil, fmt.Errorf("archetype library schema_version %d: this CLI supports %d",
			lib.SchemaVersion, SupportedSchemaVersion)
	}
	anchors := map[string]bool{}
	for _, a := range lib.Anchors {
		anchors[a] = true
	}
	for i := range lib.Archetypes {
		arch := &lib.Archetypes[i]
		if err := compileCombinator(&arch.AppliesWhen); err != nil {
			return nil, fmt.Errorf("archetype %s applies_when: %w", arch.ID, err)
		}
		for j := range arch.RequiredSignals {
			sig := &arch.RequiredSignals[j]
			if !anchors[sig.Anchor] {
				return nil, fmt.Errorf("archetype %s signal %s: anchor %q not in anchors enum",
					arch.ID, sig.ID, sig.Anchor)
			}
			if err := compileCombinator(&sig.SatisfiedBy); err != nil {
				return nil, fmt.Errorf("archetype %s signal %s satisfied_by: %w", arch.ID, sig.ID, err)
			}
		}
	}
	return &lib, nil
}

func compileCombinator(c *Combinator) error {
	for _, list := range [][]Node{c.Any, c.All, c.None} {
		for i := range list {
			if err := compileNode(&list[i]); err != nil {
				return err
			}
		}
	}
	return nil
}

func compileNode(n *Node) error {
	if n.Kind == "metric_pattern" {
		re, err := regexp.Compile(n.Pattern)
		if err != nil {
			return fmt.Errorf("pattern %q: %w", n.Pattern, err)
		}
		n.re = re
	}
	nested := Combinator{Any: n.Any, All: n.All, None: n.None}
	if err := compileCombinator(&nested); err != nil {
		return err
	}
	n.Any, n.All, n.None = nested.Any, nested.All, nested.None
	return nil
}
