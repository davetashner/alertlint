// Package pagerduty implements HistoryProvider and ActionProvider over the
// PagerDuty REST API v2 (docs/specs/provider-adapters.md §7).
//
// PagerDuty has no close-code taxonomy, so every ResponseRecord carries
// disposition "unknown" with a nil native code — the timing fallbacks
// (REQ-NOISE-002) and the auto-resolve flag do the classification work
// downstream. Adapters translate; they do not judge (ADR 0003).
package pagerduty

import (
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"time"

	"github.com/davetashner/alertlint/internal/adapter"
	"github.com/davetashner/alertlint/internal/model"
)

const (
	providerID = "pagerduty"
	pageLimit  = 100
)

// PageRecorder receives every verbatim response page, in pull order — the
// snapshot cache's Writer.WriteRawPage satisfies it.
type PageRecorder interface {
	RecordPage(body []byte) error
}

// Adapter pulls incidents and their response trails. Inject Transport to
// replay recorded fixture pages (the snapshot cache's raw pages) — the
// same mapping code path serves live pulls and offline regeneration.
type Adapter struct {
	BaseURL   string // default https://api.pagerduty.com
	Token     string
	Transport http.RoundTripper // default http.DefaultTransport
	Recorder  PageRecorder      // optional
}

// ProviderID implements adapter.Provider.
func (a *Adapter) ProviderID() string { return providerID }

// SchemaVersion implements adapter.Provider.
func (a *Adapter) SchemaVersion() string { return model.CanonicalSchemaVersion }

// ---- PagerDuty API payload shapes (subset) ----

type pdIncident struct {
	ID                 string    `json:"id"`
	Title              string    `json:"title"`
	Status             string    `json:"status"` // triggered | acknowledged | resolved
	Urgency            string    `json:"urgency"`
	CreatedAt          time.Time `json:"created_at"`
	LastStatusChangeAt time.Time `json:"last_status_change_at"`
	HTMLURL            string    `json:"html_url"`
	Service            struct {
		ID      string `json:"id"`
		Summary string `json:"summary"`
	} `json:"service"`
	AlertCounts struct {
		All int64 `json:"all"`
	} `json:"alert_counts"`
	LastStatusChangeBy struct {
		Type string `json:"type"` // user_reference | service_reference | ...
	} `json:"last_status_change_by"`
}

type pdLogEntry struct {
	Type      string    `json:"type"` // acknowledge_log_entry | resolve_log_entry | reassign_log_entry | escalate_log_entry
	CreatedAt time.Time `json:"created_at"`
	Agent     struct {
		ID   string `json:"id"`
		Type string `json:"type"`
	} `json:"agent"`
	Incident struct {
		ID string `json:"id"`
	} `json:"incident"`
}

type incidentsPage struct {
	Incidents []pdIncident `json:"incidents"`
	More      bool         `json:"more"`
}

type logEntriesPage struct {
	LogEntries []pdLogEntry `json:"log_entries"`
	More       bool         `json:"more"`
}

// FetchEvents implements adapter.HistoryProvider: one AlertEvent per
// incident whose created_at falls in the window.
func (a *Adapter) FetchEvents(scope adapter.Scope, window adapter.TimeWindow) iter.Seq2[model.AlertEvent, error] {
	return func(yield func(model.AlertEvent, error) bool) {
		incidents, err := a.pullIncidents(scope, window)
		if err != nil {
			var zero model.AlertEvent
			yield(zero, err)
			return
		}
		for _, inc := range incidents {
			if !yield(a.toEvent(inc, scope), nil) {
				return
			}
		}
	}
}

// FetchResponses implements adapter.ActionProvider: one ResponseRecord per
// incident, with ack/close/reassignment facts joined from log entries.
func (a *Adapter) FetchResponses(scope adapter.Scope, window adapter.TimeWindow) iter.Seq2[model.ResponseRecord, error] {
	return func(yield func(model.ResponseRecord, error) bool) {
		incidents, err := a.pullIncidents(scope, window)
		if err != nil {
			var zero model.ResponseRecord
			yield(zero, err)
			return
		}
		entries, err := a.pullLogEntries(scope, window)
		if err != nil {
			var zero model.ResponseRecord
			yield(zero, err)
			return
		}
		byIncident := map[string][]pdLogEntry{}
		for _, e := range entries {
			byIncident[e.Incident.ID] = append(byIncident[e.Incident.ID], e)
		}
		for _, inc := range incidents {
			if !yield(a.toResponse(inc, byIncident[inc.ID], scope), nil) {
				return
			}
		}
	}
}

func (a *Adapter) toEvent(inc pdIncident, scope adapter.Scope) model.AlertEvent {
	ev := model.AlertEvent{
		Envelope:        a.envelope(inc, scope),
		AlertRef:        model.AlertRef{Name: &inc.Title},
		FiredAt:         inc.CreatedAt.UTC(),
		OccurrenceCount: max(inc.AlertCounts.All, 1),
		Severity:        severityFromUrgency(inc.Urgency),
	}
	if inc.Status == "resolved" {
		resolved := inc.LastStatusChangeAt.UTC()
		ev.ResolvedAt = &resolved
		auto := inc.LastStatusChangeBy.Type == "service_reference"
		ev.AutoResolved = &auto
	}
	return ev
}

func (a *Adapter) toResponse(inc pdIncident, entries []pdLogEntry, scope adapter.Scope) model.ResponseRecord {
	rec := model.ResponseRecord{
		Envelope: a.envelope(inc, scope),
		EventRef: model.EventRef{Provider: strp(providerID), NativeID: &inc.ID},
		// PagerDuty has no close codes: unknown, native nil — timing
		// signals classify downstream (REQ-NOISE-002).
		Disposition:   model.DispositionUnknown,
		LinkedRecords: []model.LinkedRecord{},
	}
	var reassignments int64
	for _, e := range entries {
		switch e.Type {
		case "acknowledge_log_entry":
			if rec.AckedAt == nil || e.CreatedAt.Before(*rec.AckedAt) {
				t := e.CreatedAt.UTC()
				rec.AckedAt = &t
			}
		case "resolve_log_entry":
			t := e.CreatedAt.UTC()
			rec.ClosedAt = &t
			if e.Agent.ID != "" {
				actor := e.Agent.Type + ":" + e.Agent.ID
				rec.ActorRef = &actor
			}
		case "reassign_log_entry", "escalate_log_entry":
			reassignments++
		}
	}
	if rec.ClosedAt == nil && inc.Status == "resolved" {
		t := inc.LastStatusChangeAt.UTC()
		rec.ClosedAt = &t
	}
	rec.ReassignmentCount = reassignments
	return rec
}

func (a *Adapter) envelope(inc pdIncident, scope adapter.Scope) model.Envelope {
	u := inc.HTMLURL
	env := model.Envelope{
		SchemaVersion: model.CanonicalSchemaVersion,
		Source:        model.Source{Provider: providerID, Tenant: scope.Tenant},
		SourceRef:     model.SourceRef{Kind: "incident", NativeID: inc.ID},
		IdentityHints: model.IdentityHints{
			Tags:         map[string]string{},
			Names:        []string{},
			ExternalRefs: []model.ExternalRef{},
		},
	}
	if u != "" {
		env.SourceRef.URL = &u
	}
	if inc.Service.Summary != "" {
		env.IdentityHints.Names = append(env.IdentityHints.Names, inc.Service.Summary)
	}
	if inc.Service.ID != "" {
		env.IdentityHints.ExternalRefs = append(env.IdentityHints.ExternalRefs,
			model.ExternalRef{System: "pagerduty_service", NativeID: inc.Service.ID})
	}
	return env
}

func severityFromUrgency(urgency string) model.Severity {
	s := model.Severity{Native: urgency}
	switch urgency {
	case "high":
		s.Normalized = model.SeverityHigh
	case "low":
		s.Normalized = model.SeverityLow
	default:
		s.Normalized = model.SeverityUnknown
	}
	return s
}

// ---- paging ----

func (a *Adapter) pullIncidents(scope adapter.Scope, window adapter.TimeWindow) ([]pdIncident, error) {
	var out []pdIncident
	for offset := 0; ; offset += pageLimit {
		q := url.Values{
			"since":     {window.Start.UTC().Format(time.RFC3339)},
			"until":     {window.End.UTC().Format(time.RFC3339)},
			"limit":     {strconv.Itoa(pageLimit)},
			"offset":    {strconv.Itoa(offset)},
			"sort_by":   {"created_at:asc"},
			"time_zone": {"UTC"},
		}
		if scope.Selector != "" {
			q.Set("service_ids[]", scope.Selector)
		}
		var page incidentsPage
		if err := a.get("/incidents", q, &page); err != nil {
			return nil, err
		}
		out = append(out, page.Incidents...)
		if !page.More {
			break
		}
	}
	// Defensive: deterministic order even if the API misbehaves.
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func (a *Adapter) pullLogEntries(scope adapter.Scope, window adapter.TimeWindow) ([]pdLogEntry, error) {
	var out []pdLogEntry
	for offset := 0; ; offset += pageLimit {
		q := url.Values{
			"since":     {window.Start.UTC().Format(time.RFC3339)},
			"until":     {window.End.UTC().Format(time.RFC3339)},
			"limit":     {strconv.Itoa(pageLimit)},
			"offset":    {strconv.Itoa(offset)},
			"time_zone": {"UTC"},
		}
		var page logEntriesPage
		if err := a.get("/log_entries", q, &page); err != nil {
			return nil, err
		}
		out = append(out, page.LogEntries...)
		if !page.More {
			break
		}
	}
	return out, nil
}

func (a *Adapter) get(path string, q url.Values, into any) error {
	base := a.BaseURL
	if base == "" {
		base = "https://api.pagerduty.com"
	}
	req, err := http.NewRequest(http.MethodGet, base+path+"?"+q.Encode(), nil)
	if err != nil {
		return fmt.Errorf("pagerduty %s: %w", path, err)
	}
	req.Header.Set("Authorization", "Token token="+a.Token)
	req.Header.Set("Content-Type", "application/json")

	transport := a.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	resp, err := adapter.DoWithRetry(transport, req)
	if err != nil {
		return fmt.Errorf("pagerduty %s: %w", path, err)
	}
	defer resp.Body.Close() //nolint:errcheck // read-only close
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("pagerduty %s: read body: %w", path, err)
	}
	if resp.StatusCode != http.StatusOK {
		// Errors are not empty results (provider-adapters.md §1).
		return fmt.Errorf("pagerduty %s: HTTP %d: %.200s", path, resp.StatusCode, body)
	}
	if a.Recorder != nil {
		if err := a.Recorder.RecordPage(body); err != nil {
			return fmt.Errorf("pagerduty %s: record page: %w", path, err)
		}
	}
	if err := json.Unmarshal(body, into); err != nil {
		return fmt.Errorf("pagerduty %s: decode: %w", path, err)
	}
	return nil
}

func strp(s string) *string { return &s }
