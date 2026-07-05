package servicenow

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/davetashner/alertlint/internal/adapter"
	"github.com/davetashner/alertlint/internal/adapter/adaptertest"
	"github.com/davetashner/alertlint/internal/model"
)

type fixtureTransport struct {
	t     *testing.T
	pages map[string]string // "<offset>" -> testdata file
	fail  int               // when non-zero, return this HTTP status
}

func (f *fixtureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if f.fail != 0 {
		return &http.Response{StatusCode: f.fail,
			Body: io.NopCloser(strings.NewReader(`{"error":{"message":"maintenance"}}`))}, nil
	}
	if user, pass, ok := req.BasicAuth(); !ok || user != "svc-alertlint" || pass != "pw" {
		f.t.Error("basic auth not set on request")
	}
	file, ok := f.pages[req.URL.Query().Get("sysparm_offset")]
	if !ok {
		f.t.Fatalf("unexpected offset %q", req.URL.Query().Get("sysparm_offset"))
	}
	buf, err := os.ReadFile(filepath.Join("testdata", file))
	if err != nil {
		return nil, err
	}
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(buf)))}, nil
}

func fixtureAdapter(t *testing.T) *Adapter {
	return &Adapter{
		BaseURL: "https://example.service-now.com", User: "svc-alertlint", Password: "pw",
		Transport: &fixtureTransport{t: t, pages: map[string]string{"0": "incidents_page1.json"}},
	}
}

func window() adapter.TimeWindow {
	end := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	return adapter.TimeWindow{Start: end.AddDate(0, 0, -90), End: end}
}

func scope() adapter.Scope { return adapter.Scope{Tenant: "instance-prod"} }

func TestConformance(t *testing.T) {
	adaptertest.Run(t, fixtureAdapter(t), scope(), window())
}

func collect(t *testing.T, a *Adapter) []model.ResponseRecord {
	t.Helper()
	var recs []model.ResponseRecord
	for r, err := range a.FetchResponses(scope(), window()) {
		if err != nil {
			t.Fatal(err)
		}
		recs = append(recs, r)
	}
	return recs
}

func TestDispositionMapping(t *testing.T) {
	recs := collect(t, fixtureAdapter(t))
	if len(recs) != 3 {
		t.Fatalf("responses = %d, want 3", len(recs))
	}

	// INC0451923: the spec's canonical no-action example.
	r := recs[0]
	if r.Disposition != model.DispositionNoAction {
		t.Errorf("disposition = %s, want no_action", r.Disposition)
	}
	if r.DispositionNative == nil || *r.DispositionNative != "Closed/Resolved - No Action Taken" {
		t.Errorf("native code = %v — must be preserved verbatim", r.DispositionNative)
	}
	if r.AckedAt != nil {
		t.Error("empty work_start must mean nil acked_at")
	}
	if r.ReassignmentCount != 2 {
		t.Errorf("reassignment_count = %d, want 2", r.ReassignmentCount)
	}

	// INC0452001: solved permanently, worked at 11:15.
	r = recs[1]
	if r.Disposition != model.DispositionActionTaken {
		t.Errorf("disposition = %s, want action_taken", r.Disposition)
	}
	if r.AckedAt == nil || !r.AckedAt.Equal(time.Date(2026, 5, 20, 11, 15, 0, 0, time.UTC)) {
		t.Errorf("acked_at = %v, want work_start", r.AckedAt)
	}
	if r.ClosedAt == nil || !r.ClosedAt.Equal(time.Date(2026, 5, 20, 15, 30, 0, 0, time.UTC)) {
		t.Errorf("closed_at = %v, want resolved_at fallback when closed_at empty", r.ClosedAt)
	}

	// INC0452500: instance-custom code, unmapped => unknown + preserved.
	r = recs[2]
	if r.Disposition != model.DispositionUnknown {
		t.Errorf("unmapped code disposition = %s, want unknown", r.Disposition)
	}
	if r.DispositionNative == nil || *r.DispositionNative != "Fixed Per Runbook 7" {
		t.Errorf("native = %v", r.DispositionNative)
	}
}

func TestDispositionOverrides(t *testing.T) {
	a := fixtureAdapter(t)
	a.DispositionOverrides = map[string]model.Disposition{
		"Fixed Per Runbook 7": model.DispositionActionTaken,
	}
	recs := collect(t, a)
	if recs[2].Disposition != model.DispositionActionTaken {
		t.Errorf("override not applied: %s", recs[2].Disposition)
	}

	a.DispositionOverrides = map[string]model.Disposition{"X": "self_healed"}
	var got error
	for _, err := range a.FetchResponses(scope(), window()) {
		if err != nil {
			got = err
			break
		}
	}
	if got == nil || !strings.Contains(got.Error(), "closed taxonomy") {
		t.Errorf("invalid override value must fail: %v", got)
	}
}

func TestEventMappingAndHints(t *testing.T) {
	var events []model.AlertEvent
	for ev, err := range fixtureAdapter(t).FetchEvents(scope(), window()) {
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, ev)
	}
	e := events[0]
	if e.FiredAt != time.Date(2026, 5, 14, 3, 22, 30, 0, time.UTC) {
		t.Errorf("fired_at = %v", e.FiredAt)
	}
	if e.AutoResolved != nil {
		t.Error("auto_resolved must be absent — ServiceNow cannot see monitor-side auto-resolve")
	}
	if e.Severity.Normalized != model.SeverityHigh || e.Severity.Native != "2" {
		t.Errorf("severity = %+v", e.Severity)
	}
	// CI demoted to hints: sys_id as tag, display name as name candidate.
	if e.IdentityHints.Tags["cmdb_ci"] != "CI0007777" {
		t.Errorf("cmdb_ci tag = %q", e.IdentityHints.Tags["cmdb_ci"])
	}
	if len(e.IdentityHints.Names) != 1 || e.IdentityHints.Names[0] != "checkout-api" {
		t.Errorf("names = %v", e.IdentityHints.Names)
	}
	if len(e.IdentityHints.ExternalRefs) != 1 || e.IdentityHints.ExternalRefs[0].System != "correlation_id" {
		t.Errorf("external_refs = %v", e.IdentityHints.ExternalRefs)
	}
	// Open incident: no resolved_at.
	if events[2].ResolvedAt != nil {
		t.Error("open incident must have nil resolved_at")
	}
}

func TestErrorsAreNotEmptyResults(t *testing.T) {
	a := fixtureAdapter(t)
	a.Transport = &fixtureTransport{t: t, fail: http.StatusBadRequest} // non-retryable: surfaces immediately
	var got error
	for _, err := range a.FetchEvents(scope(), window()) {
		if err != nil {
			got = err
			break
		}
	}
	if got == nil || !strings.Contains(got.Error(), "HTTP 400") {
		t.Fatalf("failed pull must surface an error, got %v", got)
	}
}
