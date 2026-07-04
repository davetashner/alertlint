package archetype

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Override is one entry of the archetype-overrides file
// (docs/specs/archetype-library.md §4): judgment as input data, so the CLI
// stays deterministic and recomputes (ADR 0003).
type Override struct {
	CI         string `yaml:"ci"`
	Archetype  string `yaml:"archetype"`
	Applies    bool   `yaml:"applies"`
	Source     string `yaml:"source"` // "enriched" (path C) | "confirmed" (path D)
	Provenance string `yaml:"provenance"`
	AssertedBy string `yaml:"asserted_by"`
}

func (o Override) sourceEnum() Source {
	if o.Source == "confirmed" {
		return SourceConfirmed
	}
	return SourceEnriched
}

type overrideFile struct {
	Overrides []Override `yaml:"overrides"`
}

// LoadOverrides reads an overrides file and returns the entries for one
// service CI. Precedence within the file: confirmed (path D) beats
// enriched (path C) for the same archetype; duplicate same-source entries
// are an error — silent last-wins would hide authoring mistakes.
func LoadOverrides(path, ci string) ([]Override, error) {
	buf, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("archetype overrides: %w", err)
	}
	var f overrideFile
	if err := yaml.Unmarshal(buf, &f); err != nil {
		return nil, fmt.Errorf("archetype overrides: %w", err)
	}
	var out []Override
	seen := map[string]string{} // archetype -> source already kept
	for _, o := range f.Overrides {
		if o.CI != ci {
			continue
		}
		if o.Source != "enriched" && o.Source != "confirmed" {
			return nil, fmt.Errorf("archetype overrides: %s/%s: source %q not enriched|confirmed",
				o.CI, o.Archetype, o.Source)
		}
		if prev, dup := seen[o.Archetype]; dup {
			if prev == o.Source {
				return nil, fmt.Errorf("archetype overrides: duplicate %s entry for %s/%s",
					o.Source, o.CI, o.Archetype)
			}
			if prev == "confirmed" {
				continue // confirmed already kept; enriched loses
			}
			// replace the kept enriched entry with this confirmed one
			for i := range out {
				if out[i].Archetype == o.Archetype {
					out[i] = o
				}
			}
			seen[o.Archetype] = o.Source
			continue
		}
		seen[o.Archetype] = o.Source
		out = append(out, o)
	}
	return out, nil
}

// indexOverrides maps archetype id -> winning override.
func indexOverrides(overrides []Override) map[string]Override {
	idx := make(map[string]Override, len(overrides))
	for _, o := range overrides {
		if prev, ok := idx[o.Archetype]; ok && prev.sourceEnum() == SourceConfirmed {
			continue
		}
		idx[o.Archetype] = o
	}
	return idx
}
