package model

import "time"

// MonitorRef points a maintenance window at a covered monitor.
type MonitorRef struct {
	Provider string `json:"provider"`
	NativeID string `json:"native_id"`
}

// MaintenanceWindow is one declared maintenance interval in canonical
// form (docs/specs/provider-adapters.md §4.4; REQ-NOISE-005). Whether a
// fire falls inside a window is core logic — adapters only translate the
// declaration.
type MaintenanceWindow struct {
	Envelope
	StartsAt time.Time `json:"starts_at"`
	// EndsAt absent = still open at pull time.
	EndsAt *time.Time `json:"ends_at"`
	// MonitorRefs lists covered monitors; empty means scope-wide.
	MonitorRefs []MonitorRef `json:"monitor_refs"`
	// Reason is the operator-supplied text, verbatim.
	Reason *string `json:"reason"`
}
