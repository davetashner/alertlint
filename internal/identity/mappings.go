package identity

import (
	"fmt"
	"os"
	"sort"

	"gopkg.in/yaml.v3"
)

// SupportedMappingsVersion is the confirmed-mappings file schema this CLI
// understands (docs/specs/identity-resolution.md, strategy 2).
const SupportedMappingsVersion = 1

// MappingsFile is identity-mappings.yaml: pinned artifact→CI facts — the
// ratchet that makes resolution coverage climb run over run. Entries are
// never auto-deleted; removal is a human/agent edit under version control.
type MappingsFile struct {
	Version  int            `yaml:"version"`
	Mappings []MappingEntry `yaml:"mappings"`
}

// MappingEntry is one confirmed mapping. Matching is on the artifact's
// stable (source, kind, key) triple — never on names, so a renamed
// monitor with a stable key keeps its mapping.
type MappingEntry struct {
	Artifact    ArtifactRef    `yaml:"artifact"`
	CIID        string         `yaml:"ci_id"`
	ConfirmedBy string         `yaml:"confirmed_by"`
	ConfirmedAt string         `yaml:"confirmed_at"` // date, YYYY-MM-DD
	Origin      *MappingOrigin `yaml:"origin,omitempty"`
	Note        string         `yaml:"note,omitempty"`
}

// MappingOrigin records what suggested the mapping before confirmation.
type MappingOrigin struct {
	Method string  `yaml:"method"` // usually "fuzzy"
	Score  float64 `yaml:"score,omitempty"`
	Hint   string  `yaml:"hint,omitempty"`
}

// LoadMappings reads and validates a confirmed-mappings file, returning
// the resolver's strategy-2 input. A missing file is an empty ratchet,
// not an error — first runs have nothing confirmed yet.
func LoadMappings(path string) (*MappingsFile, []ConfirmedMapping, error) {
	buf, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &MappingsFile{Version: SupportedMappingsVersion}, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("identity mappings: %w", err)
	}
	var f MappingsFile
	if err := yaml.Unmarshal(buf, &f); err != nil {
		return nil, nil, fmt.Errorf("identity mappings: %w", err)
	}
	if f.Version != SupportedMappingsVersion {
		return nil, nil, fmt.Errorf("identity mappings version %d: this CLI supports %d", f.Version, SupportedMappingsVersion)
	}
	seen := map[ArtifactRef]string{}
	confirmed := make([]ConfirmedMapping, 0, len(f.Mappings))
	for i, m := range f.Mappings {
		if m.Artifact.Source == "" || m.Artifact.Kind == "" || m.Artifact.Key == "" || m.CIID == "" {
			return nil, nil, fmt.Errorf("identity mappings: entry %d incomplete (artifact triple and ci_id required)", i)
		}
		if m.ConfirmedBy == "" {
			return nil, nil, fmt.Errorf("identity mappings: entry %d: confirmed_by required — anonymous confirmations are not auditable", i)
		}
		if prev, dup := seen[m.Artifact]; dup {
			return nil, nil, fmt.Errorf("identity mappings: %v mapped to both %s and %s — resolve the conflict by hand", m.Artifact, prev, m.CIID)
		}
		seen[m.Artifact] = m.CIID
		confirmed = append(confirmed, ConfirmedMapping{Artifact: m.Artifact, CIID: m.CIID})
	}
	return &f, confirmed, nil
}

// Confirm appends a new entry (or no-ops when the identical mapping
// already exists) and writes the file back with stable ordering.
// A conflicting confirmation for an already-mapped artifact is an error:
// changing a pin is a deliberate hand edit, not a drive-by overwrite.
func Confirm(path string, entry MappingEntry) error {
	f, _, err := LoadMappings(path)
	if err != nil {
		return err
	}
	for _, m := range f.Mappings {
		if m.Artifact == entry.Artifact {
			if m.CIID == entry.CIID {
				return nil // already confirmed: idempotent
			}
			return fmt.Errorf("identity confirm: %v already mapped to %s; edit %s by hand to change it",
				entry.Artifact, m.CIID, path)
		}
	}
	f.Mappings = append(f.Mappings, entry)
	sort.Slice(f.Mappings, func(i, j int) bool {
		a, b := f.Mappings[i].Artifact, f.Mappings[j].Artifact
		if a.Source != b.Source {
			return a.Source < b.Source
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.Key < b.Key
	})
	buf, err := yaml.Marshal(f)
	if err != nil {
		return err
	}
	header := []byte("# alertlint confirmed identity mappings (docs/specs/identity-resolution.md,\n" +
		"# strategy 2). Structured writes via `alertlint identity confirm`; hand\n" +
		"# edits are legal. Entries are never auto-deleted.\n")
	return os.WriteFile(path, append(header, buf...), 0o644)
}
