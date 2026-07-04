package model

// CanonicalSchemaVersion is the single schema version shared by all three
// canonical models, versioned alongside the output contract (ADR 0004).
// Minor bumps are additive optional fields only; anything that removes,
// renames, retypes a field, or changes an enum (including the disposition
// taxonomy) is a major bump.
const CanonicalSchemaVersion = "1.0"

// Source identifies the adapter and tenant a record was pulled from.
type Source struct {
	Provider string `json:"provider"`
	Tenant   string `json:"tenant"`
}

// SourceRef is the raw-artifact reference required for evidence trails:
// every finding downstream can point back to the exact vendor object.
type SourceRef struct {
	Kind     string  `json:"kind"`
	NativeID string  `json:"native_id"`
	URL      *string `json:"url,omitempty"`
}

// ExternalRef is a cross-system reference embedded in a vendor artifact,
// letting the resolver chain identities across systems.
type ExternalRef struct {
	System   string `json:"system"`
	NativeID string `json:"native_id"`
}

// IdentityHints carries native identity material verbatim and unresolved.
// Adapters know nothing about the CMDB: they must not filter, normalize, or
// interpret hints beyond structural extraction (docs/specs/provider-adapters.md §3).
type IdentityHints struct {
	Tags         map[string]string `json:"tags"`
	Names        []string          `json:"names"`
	ExternalRefs []ExternalRef     `json:"external_refs"`
}

// Envelope is carried by every canonical record regardless of model
// (docs/specs/provider-adapters.md §2).
type Envelope struct {
	SchemaVersion string        `json:"schema_version"`
	Source        Source        `json:"source"`
	SourceRef     SourceRef     `json:"source_ref"`
	IdentityHints IdentityHints `json:"identity_hints"`
}

// NormalizedSeverity is the vendor-independent severity scale.
type NormalizedSeverity string

const (
	SeverityCritical NormalizedSeverity = "critical"
	SeverityHigh     NormalizedSeverity = "high"
	SeverityMedium   NormalizedSeverity = "medium"
	SeverityLow      NormalizedSeverity = "low"
	SeverityInfo     NormalizedSeverity = "info"
	SeverityUnknown  NormalizedSeverity = "unknown"
)

// Valid reports whether s is a member of the normalized severity scale.
func (s NormalizedSeverity) Valid() bool {
	switch s {
	case SeverityCritical, SeverityHigh, SeverityMedium, SeverityLow, SeverityInfo, SeverityUnknown:
		return true
	}
	return false
}

// Severity pairs the vendor's raw severity with its normalized value.
// Native may be empty when the vendor supplies none.
type Severity struct {
	Native     string             `json:"native"`
	Normalized NormalizedSeverity `json:"normalized"`
}
