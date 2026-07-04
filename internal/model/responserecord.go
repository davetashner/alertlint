package model

import "time"

// Disposition is the closed taxonomy every ActionProvider maps vendor close
// codes into (docs/specs/provider-adapters.md §4.3). Adapters must never
// invent values: an unmappable code maps to DispositionUnknown with the raw
// code preserved in DispositionNative. Adding a value is a major schema
// change because every ActionProvider mapping table must be revisited.
type Disposition string

const (
	// DispositionNoAction: closed with an explicit no-action-taken code —
	// the primary REQ-NOISE-001 signal (e.g. ServiceNow "Closed – No Action").
	DispositionNoAction Disposition = "no_action"
	// DispositionActionTaken: a human intervened — fix, restart, config
	// change, remediation.
	DispositionActionTaken Disposition = "action_taken"
	// DispositionEscalated: handed to an incident / major-incident /
	// problem process.
	DispositionEscalated Disposition = "escalated"
	// DispositionDuplicate: closed as duplicate of another record.
	DispositionDuplicate Disposition = "duplicate"
	// DispositionKnownIssue: closed against an existing problem /
	// known-error record.
	DispositionKnownIssue Disposition = "known_issue"
	// DispositionAutoClosed: closed by automation or timeout with no human
	// involvement.
	DispositionAutoClosed Disposition = "auto_closed"
	// DispositionUnknown: vendor code maps to nothing above, or no close
	// code at all. What unknown means for noise is scoring-engine logic
	// (ADR 0003), not adapter logic.
	DispositionUnknown Disposition = "unknown"
)

// Valid reports whether d is a member of the closed disposition taxonomy.
func (d Disposition) Valid() bool {
	switch d {
	case DispositionNoAction, DispositionActionTaken, DispositionEscalated,
		DispositionDuplicate, DispositionKnownIssue, DispositionAutoClosed,
		DispositionUnknown:
		return true
	}
	return false
}

// EventRef points a response trail at the AlertEvent it responds to.
type EventRef struct {
	Provider *string `json:"provider"`
	NativeID *string `json:"native_id"`
}

// LinkedRecordKind classifies records linked to a response trail.
type LinkedRecordKind string

const (
	LinkedChange   LinkedRecordKind = "change"
	LinkedIncident LinkedRecordKind = "incident"
	LinkedProblem  LinkedRecordKind = "problem"
	LinkedOther    LinkedRecordKind = "other"
)

// Valid reports whether k is a member of the linked record kind enum.
func (k LinkedRecordKind) Valid() bool {
	switch k {
	case LinkedChange, LinkedIncident, LinkedProblem, LinkedOther:
		return true
	}
	return false
}

// LinkedRecord is a change/incident/problem record attached to a response;
// its presence is a strong actionability signal (REQ-NOISE-002).
type LinkedRecord struct {
	Kind     LinkedRecordKind `json:"kind"`
	NativeID string           `json:"native_id"`
	URL      *string          `json:"url,omitempty"`
}

// ResponseRecord is one human-response trail attached to an alert event, in
// canonical form (docs/specs/provider-adapters.md §4.3). Time-to-ack,
// time-to-close, and never-acked are derived by the core from the
// timestamps here; adapters emit timestamps, never computed durations.
type ResponseRecord struct {
	Envelope
	EventRef EventRef `json:"event_ref"`
	// AckedAt absent = never acknowledged.
	AckedAt  *time.Time `json:"acked_at"`
	ClosedAt *time.Time `json:"closed_at"`
	// Disposition is the canonical close classification.
	Disposition Disposition `json:"disposition"`
	// DispositionNative preserves the vendor's raw close code verbatim for
	// evidence and mapping audits.
	DispositionNative *string `json:"disposition_native"`
	// ReassignmentCount is 0 if never reassigned.
	ReassignmentCount int64 `json:"reassignment_count"`
	// ActorRef is an opaque assignee/resolver identifier (PII handling is
	// an open question in the spec).
	ActorRef      *string        `json:"actor_ref"`
	LinkedRecords []LinkedRecord `json:"linked_records"`
}
