package model

import "time"

// ConfigStatus is the lifecycle state of an alert definition. A
// silenced/disabled monitor with zero fires must be distinguishable from a
// healthy dormant one (REQ-HIST-002).
type ConfigStatus string

const (
	StatusEnabled  ConfigStatus = "enabled"
	StatusDisabled ConfigStatus = "disabled"
	StatusSilenced ConfigStatus = "silenced"
)

// Valid reports whether s is a member of the config status enum.
func (s ConfigStatus) Valid() bool {
	switch s {
	case StatusEnabled, StatusDisabled, StatusSilenced:
		return true
	}
	return false
}

// RouteTargetKind classifies where an alert notification is delivered.
type RouteTargetKind string

const (
	RoutePagerDutyService RouteTargetKind = "pagerduty_service"
	RouteEmail            RouteTargetKind = "email"
	RouteWebhook          RouteTargetKind = "webhook"
	RouteChat             RouteTargetKind = "chat"
	RouteOther            RouteTargetKind = "other"
)

// Valid reports whether k is a member of the route target kind enum.
func (k RouteTargetKind) Valid() bool {
	switch k {
	case RoutePagerDutyService, RouteEmail, RouteWebhook, RouteChat, RouteOther:
		return true
	}
	return false
}

// Route is one notification destination of an alert definition.
type Route struct {
	TargetKind RouteTargetKind `json:"target_kind"`
	Target     string          `json:"target"`
}

// Comparator values permitted on AlertConfig. A nil comparator means the
// condition was not parseable — never a guessed value.
var validComparators = map[string]bool{">": true, ">=": true, "<": true, "<=": true, "==": true, "!=": true}

// ValidComparator reports whether c is a permitted comparator literal.
func ValidComparator(c string) bool { return validComparators[c] }

// AlertConfig is one alert definition (monitor, alarm, alert condition,
// saved search with alert action) in canonical form
// (docs/specs/provider-adapters.md §4.1).
//
// ConditionRaw is always present so no information is lost; Threshold,
// Comparator, and DurationS are best-effort extractions — absent means
// "not parseable", never a default.
type AlertConfig struct {
	Envelope
	Name          string       `json:"name"`
	Description   *string      `json:"description,omitempty"`
	ConditionRaw  string       `json:"condition_raw"`
	Threshold     *float64     `json:"threshold"`
	Comparator    *string      `json:"comparator"`
	DurationS     *int64       `json:"duration_s"`
	Severity      Severity     `json:"severity"`
	Routing       []Route      `json:"routing"`
	Status        ConfigStatus `json:"status"`
	SilencedUntil *time.Time   `json:"silenced_until"`
	CreatedAt     *time.Time   `json:"created_at"`
	UpdatedAt     *time.Time   `json:"updated_at"`
}
