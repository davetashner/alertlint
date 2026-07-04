package model

import "time"

// AlertRef is a best-effort reference from a firing episode back to the
// originating alert definition. When history comes from PagerDuty or
// ServiceNow the originating monitor is a different vendor and only whatever
// reference survived the integration is available; joining AlertEvent to
// AlertConfig is core logic, not adapter logic
// (docs/specs/provider-adapters.md §4.2).
type AlertRef struct {
	Provider *string `json:"provider"`
	NativeID *string `json:"native_id"`
	Name     *string `json:"name"`
}

// AlertEvent is one firing episode (trigger → resolve) inside the analysis
// window, in canonical form (docs/specs/provider-adapters.md §4.2).
type AlertEvent struct {
	Envelope
	AlertRef AlertRef `json:"alert_ref"`
	// FiredAt is when the episode triggered (UTC RFC 3339).
	FiredAt time.Time `json:"fired_at"`
	// ResolvedAt is absent when the episode is still open at pull time.
	ResolvedAt *time.Time `json:"resolved_at"`
	// AutoResolved true = resolved by the monitoring system, not a human;
	// absent = unknown. Primary noise signal (REQ-NOISE-001).
	AutoResolved *bool `json:"auto_resolved"`
	// OccurrenceCount is >= 1; grouped/deduped firings folded into one
	// episode by the source.
	OccurrenceCount int64    `json:"occurrence_count"`
	Severity        Severity `json:"severity"`
}
