// Package servicenow implements HistoryProvider and ActionProvider over
// the ServiceNow Table API (docs/specs/provider-adapters.md §7).
//
// ServiceNow is where close codes live, so this adapter owns the
// close_code → disposition mapping table. Codes are instance-customizable,
// so the built-in defaults merge with a per-org override config; an
// unmapped code translates to "unknown" with the raw code preserved —
// adapters translate, they never judge (ADR 0003).
//
// ServiceNow CI reference fields are demoted to identity hints (tag
// "cmdb_ci" + display name), never resolved here: identity resolution is
// core logic (ADR 0004).
package servicenow

import (
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/davetashner/alertlint/internal/adapter"
	"github.com/davetashner/alertlint/internal/model"
)

const (
	providerID = "servicenow"
	pageLimit  = 200
	timeLayout = "2006-01-02 15:04:05" // ServiceNow's UTC display format
)

// DefaultDispositions maps common OOTB close codes into the closed
// taxonomy. Instance-specific codes arrive via Adapter.DispositionOverrides.
var DefaultDispositions = map[string]model.Disposition{
	"Closed/Resolved - No Action Taken": model.DispositionNoAction,
	"No action taken":                   model.DispositionNoAction,
	"Solved (Permanently)":              model.DispositionActionTaken,
	"Solved (Work Around)":              model.DispositionActionTaken,
	"Solved Remotely (Permanently)":     model.DispositionActionTaken,
	"Solution provided":                 model.DispositionActionTaken,
	"Resolved by change":                model.DispositionActionTaken,
	"Duplicate":                         model.DispositionDuplicate,
	"Known error":                       model.DispositionKnownIssue,
	"Closed automatically":              model.DispositionAutoClosed,
}

// PageRecorder receives every verbatim response page, in pull order.
type PageRecorder interface {
	RecordPage(body []byte) error
}

// Adapter pulls incident records. Inject Transport to replay recorded
// fixture pages; the same mapping code path serves live and offline runs.
type Adapter struct {
	BaseURL   string // e.g. https://example.service-now.com
	User      string
	Password  string
	Transport http.RoundTripper
	Recorder  PageRecorder
	// DispositionOverrides merges over DefaultDispositions — the per-org
	// close-code table for customized instances. Values must be members
	// of the closed taxonomy; validated at pull time.
	DispositionOverrides map[string]model.Disposition
}

// ProviderID implements adapter.Provider.
func (a *Adapter) ProviderID() string { return providerID }

// SchemaVersion implements adapter.Provider.
func (a *Adapter) SchemaVersion() string { return model.CanonicalSchemaVersion }

// snowField is the {display_value, value} pair every field becomes under
// sysparm_display_value=all.
type snowField struct {
	DisplayValue string `json:"display_value"`
	Value        string `json:"value"`
}

type snowIncident struct {
	SysID             snowField `json:"sys_id"`
	Number            snowField `json:"number"`
	ShortDescription  snowField `json:"short_description"`
	OpenedAt          snowField `json:"opened_at"`
	ResolvedAt        snowField `json:"resolved_at"`
	ClosedAt          snowField `json:"closed_at"`
	WorkStart         snowField `json:"work_start"`
	CloseCode         snowField `json:"close_code"`
	Severity          snowField `json:"severity"`
	ReassignmentCount snowField `json:"reassignment_count"`
	AssignedTo        snowField `json:"assigned_to"`
	CmdbCI            snowField `json:"cmdb_ci"`
	CorrelationID     snowField `json:"correlation_id"`
}

type tablePage struct {
	Result []snowIncident `json:"result"`
}

// FetchEvents implements adapter.HistoryProvider: one AlertEvent per
// incident opened in the window. auto_resolved is absent — whether the
// monitoring system self-resolved is not visible from ServiceNow, and
// absent means unknown, never a guess.
func (a *Adapter) FetchEvents(scope adapter.Scope, window adapter.TimeWindow) iter.Seq2[model.AlertEvent, error] {
	return func(yield func(model.AlertEvent, error) bool) {
		incidents, err := a.pullIncidents(scope, window)
		if err != nil {
			var zero model.AlertEvent
			yield(zero, err)
			return
		}
		for _, inc := range incidents {
			ev, err := a.toEvent(inc, scope)
			if err != nil {
				var zero model.AlertEvent
				yield(zero, err)
				return
			}
			if !yield(ev, nil) {
				return
			}
		}
	}
}

// FetchResponses implements adapter.ActionProvider.
func (a *Adapter) FetchResponses(scope adapter.Scope, window adapter.TimeWindow) iter.Seq2[model.ResponseRecord, error] {
	return func(yield func(model.ResponseRecord, error) bool) {
		table, err := a.dispositionTable()
		if err != nil {
			var zero model.ResponseRecord
			yield(zero, err)
			return
		}
		incidents, err := a.pullIncidents(scope, window)
		if err != nil {
			var zero model.ResponseRecord
			yield(zero, err)
			return
		}
		for _, inc := range incidents {
			rec, err := a.toResponse(inc, scope, table)
			if err != nil {
				var zero model.ResponseRecord
				yield(zero, err)
				return
			}
			if !yield(rec, nil) {
				return
			}
		}
	}
}

func (a *Adapter) toEvent(inc snowIncident, scope adapter.Scope) (model.AlertEvent, error) {
	opened, err := snowTime(inc.OpenedAt.Value)
	if err != nil || opened == nil {
		return model.AlertEvent{}, fmt.Errorf("servicenow %s: bad opened_at %q", inc.Number.Value, inc.OpenedAt.Value)
	}
	ev := model.AlertEvent{
		Envelope:        a.envelope(inc, scope),
		FiredAt:         *opened,
		OccurrenceCount: 1,
		Severity:        severityFrom(inc.Severity),
	}
	if inc.ShortDescription.Value != "" {
		name := inc.ShortDescription.Value
		ev.AlertRef.Name = &name
	}
	resolved, err := snowTime(inc.ResolvedAt.Value)
	if err != nil {
		return model.AlertEvent{}, fmt.Errorf("servicenow %s: bad resolved_at %q", inc.Number.Value, inc.ResolvedAt.Value)
	}
	ev.ResolvedAt = resolved
	return ev, nil
}

func (a *Adapter) toResponse(inc snowIncident, scope adapter.Scope, table map[string]model.Disposition) (model.ResponseRecord, error) {
	rec := model.ResponseRecord{
		Envelope:      a.envelope(inc, scope),
		LinkedRecords: []model.LinkedRecord{},
	}
	sysID := inc.SysID.Value
	rec.EventRef = model.EventRef{Provider: strp(providerID), NativeID: &sysID}

	// work_start is the closest standard analogue to "acknowledged":
	// when a human began working the record. Absent = never worked.
	ack, err := snowTime(inc.WorkStart.Value)
	if err != nil {
		return rec, fmt.Errorf("servicenow %s: bad work_start %q", inc.Number.Value, inc.WorkStart.Value)
	}
	rec.AckedAt = ack

	closed, err := snowTime(firstNonEmpty(inc.ClosedAt.Value, inc.ResolvedAt.Value))
	if err != nil {
		return rec, fmt.Errorf("servicenow %s: bad closed_at %q", inc.Number.Value, inc.ClosedAt.Value)
	}
	rec.ClosedAt = closed

	if code := inc.CloseCode.Value; code != "" {
		native := code
		rec.DispositionNative = &native
		if d, ok := table[code]; ok {
			rec.Disposition = d
		} else {
			rec.Disposition = model.DispositionUnknown
		}
	} else {
		rec.Disposition = model.DispositionUnknown
	}

	if inc.ReassignmentCount.Value != "" {
		n, err := strconv.ParseInt(inc.ReassignmentCount.Value, 10, 64)
		if err != nil {
			return rec, fmt.Errorf("servicenow %s: bad reassignment_count %q", inc.Number.Value, inc.ReassignmentCount.Value)
		}
		rec.ReassignmentCount = n
	}
	if inc.AssignedTo.Value != "" {
		actor := "sys_user:" + inc.AssignedTo.Value
		rec.ActorRef = &actor
	}
	return rec, nil
}

func (a *Adapter) envelope(inc snowIncident, scope adapter.Scope) model.Envelope {
	env := model.Envelope{
		SchemaVersion: model.CanonicalSchemaVersion,
		Source:        model.Source{Provider: providerID, Tenant: scope.Tenant},
		SourceRef:     model.SourceRef{Kind: "incident", NativeID: inc.Number.Value},
		IdentityHints: model.IdentityHints{
			Tags:         map[string]string{},
			Names:        []string{},
			ExternalRefs: []model.ExternalRef{},
		},
	}
	if a.BaseURL != "" && inc.SysID.Value != "" {
		u := a.BaseURL + "/incident.do?sys_id=" + inc.SysID.Value
		env.SourceRef.URL = &u
	}
	// CI reference demoted to hints: sys_id as a tag (orgs point the
	// resolver's CIIDTagKeys at it), display name as a name candidate.
	if inc.CmdbCI.Value != "" {
		env.IdentityHints.Tags["cmdb_ci"] = inc.CmdbCI.Value
	}
	if inc.CmdbCI.DisplayValue != "" {
		env.IdentityHints.Names = append(env.IdentityHints.Names, inc.CmdbCI.DisplayValue)
	}
	if inc.CorrelationID.Value != "" {
		env.IdentityHints.ExternalRefs = append(env.IdentityHints.ExternalRefs,
			model.ExternalRef{System: "correlation_id", NativeID: inc.CorrelationID.Value})
	}
	return env
}

func (a *Adapter) dispositionTable() (map[string]model.Disposition, error) {
	table := make(map[string]model.Disposition, len(DefaultDispositions)+len(a.DispositionOverrides))
	for code, d := range DefaultDispositions {
		table[code] = d
	}
	for code, d := range a.DispositionOverrides {
		if !d.Valid() {
			return nil, fmt.Errorf("servicenow disposition override %q -> %q: not in the closed taxonomy", code, d)
		}
		table[code] = d
	}
	return table, nil
}

func severityFrom(f snowField) model.Severity {
	s := model.Severity{Native: f.Value}
	switch f.Value {
	case "1":
		s.Normalized = model.SeverityCritical
	case "2":
		s.Normalized = model.SeverityHigh
	case "3":
		s.Normalized = model.SeverityMedium
	case "4":
		s.Normalized = model.SeverityLow
	case "5":
		s.Normalized = model.SeverityInfo
	default:
		s.Normalized = model.SeverityUnknown
	}
	return s
}

func (a *Adapter) pullIncidents(scope adapter.Scope, window adapter.TimeWindow) ([]snowIncident, error) {
	var out []snowIncident
	for offset := 0; ; offset += pageLimit {
		query := fmt.Sprintf("opened_at>=%s^opened_at<%s^ORDERBYopened_at",
			window.Start.UTC().Format(timeLayout), window.End.UTC().Format(timeLayout))
		if scope.Selector != "" {
			query += "^" + scope.Selector // opaque provider-native narrowing
		}
		q := url.Values{
			"sysparm_query":         {query},
			"sysparm_limit":         {strconv.Itoa(pageLimit)},
			"sysparm_offset":        {strconv.Itoa(offset)},
			"sysparm_display_value": {"all"},
		}
		var page tablePage
		if err := a.get("/api/now/table/incident", q, &page); err != nil {
			return nil, err
		}
		out = append(out, page.Result...)
		if len(page.Result) < pageLimit {
			break
		}
	}
	return out, nil
}

func (a *Adapter) get(path string, q url.Values, into any) error {
	req, err := http.NewRequest(http.MethodGet, a.BaseURL+path+"?"+q.Encode(), nil)
	if err != nil {
		return fmt.Errorf("servicenow %s: %w", path, err)
	}
	req.SetBasicAuth(a.User, a.Password)
	req.Header.Set("Accept", "application/json")

	transport := a.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	resp, err := adapter.DoWithRetry(transport, req)
	if err != nil {
		return fmt.Errorf("servicenow %s: %w", path, err)
	}
	defer resp.Body.Close() //nolint:errcheck // read-only close
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("servicenow %s: read body: %w", path, err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("servicenow %s: HTTP %d: %.200s", path, resp.StatusCode, body)
	}
	if a.Recorder != nil {
		if err := a.Recorder.RecordPage(body); err != nil {
			return fmt.Errorf("servicenow %s: record page: %w", path, err)
		}
	}
	if err := json.Unmarshal(body, into); err != nil {
		return fmt.Errorf("servicenow %s: decode: %w", path, err)
	}
	return nil
}

// snowTime parses ServiceNow's "YYYY-MM-DD HH:MM:SS" UTC format; empty is
// nil, never a zero time.
func snowTime(v string) (*time.Time, error) {
	if v == "" {
		return nil, nil
	}
	t, err := time.ParseInLocation(timeLayout, v, time.UTC)
	if err != nil {
		return nil, err
	}
	u := t.UTC()
	return &u, nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func strp(s string) *string { return &s }
