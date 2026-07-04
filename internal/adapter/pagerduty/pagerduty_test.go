package pagerduty

import (
	"fmt"
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

// fixtureTransport serves recorded pages keyed by path and offset — the
// same replay shape the snapshot cache's raw pages provide.
type fixtureTransport struct {
	t     *testing.T
	pages map[string]string // "<path>@<offset>" -> testdata file
	fail  map[string]int    // "<path>@<offset>" -> HTTP status to return
}

func (f *fixtureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	offset := req.URL.Query().Get("offset")
	key := req.URL.Path + "@" + offset
	if status, ok := f.fail[key]; ok {
		return &http.Response{
			StatusCode: status,
			Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"rate limited"}}`)),
		}, nil
	}
	file, ok := f.pages[key]
	if !ok {
		f.t.Fatalf("unexpected request %s", key)
	}
	buf, err := os.ReadFile(filepath.Join("testdata", file))
	if err != nil {
		return nil, err
	}
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(buf)))}, nil
}

func fixtureAdapter(t *testing.T) *Adapter {
	return &Adapter{
		Token: "test-token",
		Transport: &fixtureTransport{t: t, pages: map[string]string{
			"/incidents@0":   "incidents_page1.json",
			"/log_entries@0": "log_entries_page1.json",
		}},
	}
}

func window() adapter.TimeWindow {
	end := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	return adapter.TimeWindow{Start: end.AddDate(0, 0, -90), End: end}
}

func scope() adapter.Scope { return adapter.Scope{Tenant: "acct-primary"} }

func TestConformance(t *testing.T) {
	adaptertest.Run(t, fixtureAdapter(t), scope(), window())
}

func TestEventMapping(t *testing.T) {
	var events []model.AlertEvent
	for ev, err := range fixtureAdapter(t).FetchEvents(scope(), window()) {
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, ev)
	}
	if len(events) != 2 {
		t.Fatalf("events = %d, want 2", len(events))
	}

	// Q1...: integration-resolved fast — the ambiguity-default shape.
	e := events[0]
	if e.SourceRef.NativeID != "Q1ABC2DEF3GHI4" || e.SourceRef.Kind != "incident" {
		t.Errorf("source_ref = %+v", e.SourceRef)
	}
	if e.AutoResolved == nil || !*e.AutoResolved {
		t.Error("service_reference resolver must mean auto_resolved=true")
	}
	if e.ResolvedAt == nil || e.ResolvedAt.Sub(e.FiredAt) != 7*time.Minute+31*time.Second {
		t.Errorf("resolve delta wrong: %v", e.ResolvedAt)
	}
	if e.Severity.Normalized != model.SeverityHigh || e.Severity.Native != "high" {
		t.Errorf("severity = %+v", e.Severity)
	}
	if len(e.IdentityHints.Names) != 1 || e.IdentityHints.Names[0] != "Checkout API" {
		t.Errorf("names = %v", e.IdentityHints.Names)
	}
	if len(e.IdentityHints.ExternalRefs) != 1 || e.IdentityHints.ExternalRefs[0].NativeID != "PXYZ123" {
		t.Errorf("external_refs = %v", e.IdentityHints.ExternalRefs)
	}

	// Q5...: user-resolved, grouped alerts.
	e = events[1]
	if e.AutoResolved == nil || *e.AutoResolved {
		t.Error("user_reference resolver must mean auto_resolved=false")
	}
	if e.OccurrenceCount != 3 {
		t.Errorf("occurrence_count = %d, want 3 (alert_counts.all)", e.OccurrenceCount)
	}
}

func TestResponseMapping(t *testing.T) {
	var recs []model.ResponseRecord
	for r, err := range fixtureAdapter(t).FetchResponses(scope(), window()) {
		if err != nil {
			t.Fatal(err)
		}
		recs = append(recs, r)
	}
	if len(recs) != 2 {
		t.Fatalf("responses = %d, want 2", len(recs))
	}

	// Q1...: never acked, auto-resolved; no close code exists in PD.
	r := recs[0]
	if r.AckedAt != nil {
		t.Error("never-acked incident must have nil acked_at")
	}
	if r.Disposition != model.DispositionUnknown || r.DispositionNative != nil {
		t.Errorf("PD disposition must be unknown/nil, got %s/%v", r.Disposition, r.DispositionNative)
	}
	if r.ClosedAt == nil {
		t.Error("resolved incident must carry closed_at")
	}
	if r.EventRef.NativeID == nil || *r.EventRef.NativeID != "Q1ABC2DEF3GHI4" {
		t.Errorf("event_ref = %+v", r.EventRef)
	}

	// Q5...: acked at 11:06:30, reassigned + escalated = 2, user resolver.
	r = recs[1]
	if r.AckedAt == nil || r.AckedAt.Format(time.RFC3339) != "2026-05-20T11:06:30Z" {
		t.Errorf("acked_at = %v", r.AckedAt)
	}
	if r.ReassignmentCount != 2 {
		t.Errorf("reassignment_count = %d, want 2 (reassign + escalate)", r.ReassignmentCount)
	}
	if r.ActorRef == nil || *r.ActorRef != "user_reference:PUSR77" {
		t.Errorf("actor_ref = %v", r.ActorRef)
	}
}

func TestPaginationFollowsMore(t *testing.T) {
	// Page 1 says more:true; page 2 closes it out.
	page1 := `{"incidents":[{"id":"A1","title":"t","status":"triggered","urgency":"low",
		"created_at":"2026-05-01T00:00:00Z","last_status_change_at":"2026-05-01T00:00:00Z",
		"service":{"id":"S","summary":"svc"},"alert_counts":{"all":1},
		"last_status_change_by":{"type":"user_reference"}}],"more":true}`
	page2 := `{"incidents":[{"id":"A2","title":"t2","status":"triggered","urgency":"low",
		"created_at":"2026-05-02T00:00:00Z","last_status_change_at":"2026-05-02T00:00:00Z",
		"service":{"id":"S","summary":"svc"},"alert_counts":{"all":1},
		"last_status_change_by":{"type":"user_reference"}}],"more":false}`
	dir := t.TempDir()
	for name, content := range map[string]string{"p1.json": page1, "p2.json": page2} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	a := &Adapter{Token: "t", Transport: &tempTransport{dir: dir, pages: map[string]string{
		"/incidents@0": "p1.json", "/incidents@100": "p2.json",
	}}}
	var ids []string
	for ev, err := range a.FetchEvents(scope(), window()) {
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, ev.SourceRef.NativeID)
	}
	if len(ids) != 2 || ids[0] != "A1" || ids[1] != "A2" {
		t.Errorf("paginated ids = %v", ids)
	}
}

type tempTransport struct {
	dir   string
	pages map[string]string
}

func (f *tempTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	key := req.URL.Path + "@" + req.URL.Query().Get("offset")
	file, ok := f.pages[key]
	if !ok {
		return nil, fmt.Errorf("unexpected request %s", key)
	}
	buf, err := os.ReadFile(filepath.Join(f.dir, file))
	if err != nil {
		return nil, err
	}
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(buf)))}, nil
}

func TestErrorsAreNotEmptyResults(t *testing.T) {
	a := &Adapter{Token: "t", Transport: &fixtureTransport{
		t:     t,
		pages: map[string]string{},
		fail:  map[string]int{"/incidents@0": http.StatusTooManyRequests},
	}}
	var got error
	for _, err := range a.FetchEvents(scope(), window()) {
		if err != nil {
			got = err
			break
		}
	}
	if got == nil || !strings.Contains(got.Error(), "HTTP 429") {
		t.Fatalf("rate-limited pull must surface an error, got %v", got)
	}
}

type capturingRecorder struct{ pages int }

func (c *capturingRecorder) RecordPage([]byte) error { c.pages++; return nil }

func TestRecorderReceivesEveryPage(t *testing.T) {
	rec := &capturingRecorder{}
	a := fixtureAdapter(t)
	a.Recorder = rec
	for _, err := range a.FetchResponses(scope(), window()) {
		if err != nil {
			t.Fatal(err)
		}
	}
	if rec.pages != 2 { // one incidents page + one log_entries page
		t.Errorf("recorded pages = %d, want 2", rec.pages)
	}
}
