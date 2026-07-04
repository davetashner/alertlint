package adapter

import (
	"iter"
	"time"

	"github.com/davetashner/alertlint/internal/model"
)

// Scope narrows a pull to one tenant and an optional provider-native filter
// (docs/specs/provider-adapters.md §1). Both values are passed through
// opaquely — their meaning is vendor-specific.
type Scope struct {
	Tenant   string
	Selector string // optional; empty = no narrowing
}

// TimeWindow bounds a pull: Start inclusive, End exclusive, both UTC.
// The default analysis window is the last 90 days (REQ-HIST-001).
type TimeWindow struct {
	Start time.Time
	End   time.Time
}

// DefaultWindowDays is the default analysis window length (REQ-HIST-001).
const DefaultWindowDays = 90

// DefaultWindow returns the standard 90-day window ending at now.
func DefaultWindow(now time.Time) TimeWindow {
	now = now.UTC()
	return TimeWindow{Start: now.AddDate(0, 0, -DefaultWindowDays), End: now}
}

// Provider is the base every adapter satisfies regardless of data class.
type Provider interface {
	// ProviderID is the stable adapter id, e.g. "datadog".
	ProviderID() string
	// SchemaVersion is the canonical schema version the adapter emits. The
	// core refuses records whose major version it does not support.
	SchemaVersion() string
}

// Streams yield records incrementally with a terminal error check:
// pagination, rate limiting, and retries are entirely the adapter's problem
// and invisible to the core. Iteration order must be deterministic for a
// given source state (ADR 0005: iterators, not channels).
//
// Errors are not empty results (spec §1): when the yield function receives a
// non-nil error the pull has failed, the sequence ends, and the caller must
// abort that source's contribution for the run — partial pages are never
// silently a complete result.

// ConfigProvider pulls alert definitions as a current snapshot at pull time
// (the window parameter exists for cache-key symmetry).
type ConfigProvider interface {
	Provider
	FetchConfigs(scope Scope, window TimeWindow) iter.Seq2[model.AlertConfig, error]
}

// HistoryProvider pulls firing episodes whose fired_at falls in the window.
type HistoryProvider interface {
	Provider
	FetchEvents(scope Scope, window TimeWindow) iter.Seq2[model.AlertEvent, error]
}

// ActionProvider pulls human-response trails for events in the window.
type ActionProvider interface {
	Provider
	FetchResponses(scope Scope, window TimeWindow) iter.Seq2[model.ResponseRecord, error]
}
