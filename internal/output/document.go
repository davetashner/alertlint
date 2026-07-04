// Package output implements the per-service JSON contract
// (docs/specs/output-contract.md): the stable interface the skill
// consumes and the atomic unit of the worklist corpus.
//
// Marshaling is struct-based throughout (never maps, except string-keyed
// maps that encoding/json sorts deterministically) so documents are
// byte-identical across runs on the same input (ADR 0005, REQ-SCORE-007).
package output

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"time"
)

// ContractVersion is the semver of the document contract, versioned
// independently of the tool (see spec "Versioning & evolution").
const ContractVersion = "1.0.0"

// UnresolvedDocumentName is the reserved filename for the corpus-level
// unattributed-artifacts document (identity.ci == null).
const UnresolvedDocumentName = "_unresolved.json"

// Document is one per-service output document.
type Document struct {
	ContractVersion string   `json:"contract_version"`
	Identity        Identity `json:"identity"`
	Scores          Scores   `json:"scores"`
	// Findings may be empty ([]), never absent.
	Findings []Finding `json:"findings"`
	Metadata Metadata  `json:"metadata"`
}

// Identity is the CI, its resolved artifacts, and mapping coverage.
type Identity struct {
	// CI is null only in the reserved unresolved document.
	CI        *CIBlock   `json:"ci"`
	Artifacts []Artifact `json:"artifacts"`
	Mapping   Mapping    `json:"mapping"`
}

// CIBlock is the canonical service identity (REQ-ID-001).
type CIBlock struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	CriticalityTier int    `json:"criticality_tier"`
	// CriticalitySource "default" means the middle-tier fallback applied
	// (REQ-CRIT-003) and a type:identity finding MUST accompany it.
	CriticalitySource string `json:"criticality_source"`
}

// Artifact is one join record: a source artifact resolved to this CI.
type Artifact struct {
	Source     string     `json:"source"`
	Kind       string     `json:"kind"` // alert_config | alert_event | response_record
	NativeID   string     `json:"native_id"`
	NativeName *string    `json:"native_name"`
	Resolution Resolution `json:"resolution"`
	// AnalysisState is meaningful only for kind=="alert_config"
	// (scored | dormant | insufficient_data); null otherwise.
	AnalysisState *string `json:"analysis_state"`
}

// Resolution records how an artifact joined (fuzzy NEVER joins — ADR 0002).
type Resolution struct {
	Method     string  `json:"method"`     // exact | confirmed | convention
	Confidence string  `json:"confidence"` // high | medium
	Rule       *string `json:"rule"`       // convention-rule id, else null
}

// Mapping is the per-service coverage block (REQ-ID-002/003).
type Mapping struct {
	ResolvedCount  int            `json:"resolved_count"`
	CandidateCount int            `json:"candidate_count"`
	CoverageNote   string         `json:"coverage_note"` // full | partial
	BySource       map[string]int `json:"by_source"`     // sorted by encoding/json
}

// Scores: quality sub-scores (higher better) and the attention-ranked
// priority score. Nullable members are null when every alert lacked input
// (REQ-HIST-004 — states, never conflated with healthy).
type Scores struct {
	Noise           *float64    `json:"noise"`
	Coverage        *float64    `json:"coverage"`
	Threshold       *float64    `json:"threshold"`
	Composite       *float64    `json:"composite"`
	CriticalityTier int         `json:"criticality_tier"`
	PriorityScore   *float64    `json:"priority_score"`
	Inputs          ScoreInputs `json:"inputs"`
}

// ScoreInputs makes the cold-start states explicit in every document.
type ScoreInputs struct {
	AlertsScored           int `json:"alerts_scored"`
	AlertsDormant          int `json:"alerts_dormant"`
	AlertsInsufficientData int `json:"alerts_insufficient_data"`
}

// Finding is one self-contained, greppable record of the frozen taxonomy.
type Finding struct {
	ID         string  `json:"id"`
	Type       string  `json:"type"`       // noise | coverage | threshold | identity
	Severity   string  `json:"severity"`   // critical | high | medium | low
	Confidence string  `json:"confidence"` // high | medium | low — the ADR 0003 handoff
	Subject    Subject `json:"subject"`
	Rationale  string  `json:"rationale"`
	// Evidence is type-specific with a required minimum key set per type
	// (spec table); open to additive keys, so it stays raw JSON here and
	// typed evidence structs marshal into it.
	Evidence       json.RawMessage `json:"evidence"`
	ProposedChange *ProposedChange `json:"proposed_change"`
}

// Subject is what a finding is about.
type Subject struct {
	Source   *string `json:"source"`
	NativeID *string `json:"native_id"`
	Signal   *string `json:"signal"`
}

// ProposedChange is the level-B block and level-C seam (REQ-REC-002/003).
type ProposedChange struct {
	Kind      string          `json:"kind"`
	Target    Target          `json:"target"`
	Current   json.RawMessage `json:"current"`  // null for add_alert / mapping_add
	Proposed  json.RawMessage `json:"proposed"` // concrete value, never prose
	Rationale string          `json:"rationale"`
	// GeneratedBy is "cli" | "skill" — provenance (ADR 0003).
	GeneratedBy string `json:"generated_by"`
	// Diff is reserved for level-C; v1 producers emit null.
	Diff *string `json:"diff"`
}

// Target is the vendor-addressable object of a proposed change.
type Target struct {
	Source   string  `json:"source"`
	NativeID string  `json:"native_id"`
	Path     *string `json:"path,omitempty"`
}

// Metadata carries everything needed to reproduce the document
// (REQ-SCORE-007).
type Metadata struct {
	Run                      Run          `json:"run"`
	Window                   Window       `json:"window"`
	Config                   ConfigBlock  `json:"config"`
	ArchetypeLibraryVersion  string       `json:"archetype_library_version"`
	ConventionRulesetVersion string       `json:"convention_ruleset_version"`
	Sources                  []SourceMeta `json:"sources"`
}

// Run identifies one CLI invocation; shared across its documents.
type Run struct {
	Timestamp    time.Time `json:"timestamp"`
	ToolVersion  string    `json:"tool_version"`
	InvocationID string    `json:"invocation_id"`
}

// Window is the analysis window (REQ-HIST-001).
type Window struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
	Days  int       `json:"days"`
}

// ConfigBlock pins the scoring configuration that produced the scores.
type ConfigBlock struct {
	Weights                WeightsBlock `json:"weights"`
	PriorityFormulaVersion string       `json:"priority_formula_version"`
	ConfigHash             string       `json:"config_hash"`
}

// WeightsBlock mirrors the configured sub-score weights (REQ-SCORE-004).
type WeightsBlock struct {
	Noise     float64 `json:"noise"`
	Coverage  float64 `json:"coverage"`
	Threshold float64 `json:"threshold"`
}

// SourceMeta records one contributing adapter and its snapshot.
type SourceMeta struct {
	Source                 string `json:"source"`
	AdapterVersion         string `json:"adapter_version"`
	CanonicalSchemaVersion string `json:"canonical_schema_version"`
	SnapshotKey            string `json:"snapshot_key"`
}

// FindingID computes the deterministic content-hash id of a finding:
// stable across reruns on the same input, so run-over-run diffs show real
// change, not churn. Hash input is (type, subject, window) per the spec.
func FindingID(findingType string, subject Subject, window Window) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s\x00%s\x00%s",
		findingType, deref(subject.Source), deref(subject.NativeID), deref(subject.Signal),
		window.Start.UTC().Format(time.RFC3339), window.End.UTC().Format(time.RFC3339))
	return "ald-" + hex.EncodeToString(h.Sum(nil))[:8]
}

var unsafeFilenameChars = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

// Filename returns the document filename for a CI: "<name>.<id>.json"
// with every byte outside [a-zA-Z0-9._-] collapsed to a single "-".
// This defines the spec's open filename-sanitization rule: purely
// mechanical, collision-safe via the CI id component, and stable.
func Filename(ciName, ciID string) string {
	name := unsafeFilenameChars.ReplaceAllString(ciName, "-")
	id := unsafeFilenameChars.ReplaceAllString(ciID, "-")
	return name + "." + id + ".json"
}

// Marshal renders a document with a trailing newline, two-space indent —
// the canonical on-disk form (diffable, byte-identical across runs).
func Marshal(doc Document) ([]byte, error) {
	buf, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(buf, '\n'), nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
