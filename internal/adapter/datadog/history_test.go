package datadog

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/davetashner/alertlint/internal/adapter"
	"github.com/davetashner/alertlint/internal/model"
)

// eventsTransport serves the alert-event fixture on the first chunk and
// empty pages after, recording chunk boundaries.
type eventsTransport struct {
	t      *testing.T
	events string
	starts []int64
}

func (f *eventsTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Path != "/api/v1/events" {
		f.t.Fatalf("unexpected path %s", req.URL.Path)
	}
	start, _ := strconv.ParseInt(req.URL.Query().Get("start"), 10, 64)
	f.starts = append(f.starts, start)
	body := `{"events":[]}`
	if len(f.starts) == 1 {
		body = f.events
	}
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
}

func historyWindow() adapter.TimeWindow {
	end := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	return adapter.TimeWindow{Start: end.AddDate(0, 0, -90), End: end}
}

func eventsFixture(w adapter.TimeWindow) string {
	base := w.Start.Unix()
	ev := func(id int64, alertType string, offset int64, monitor string) string {
		mid := ""
		if monitor != "" {
			mid = `"monitor_id": ` + monitor + `,`
		}
		return fmt.Sprintf(`{"id": %d, "title": "[Triggered] checkout-api cpu high", "alert_type": %q,
			"date_happened": %d, %s "tags": ["service:checkout-api", "env:prod"],
			"url": "https://app.datadoghq.com/event/%d"}`, id, alertType, base+offset, mid, id)
	}
	return `{"events":[` + strings.Join([]string{
		ev(1, "error", 100, "70001"),   // opens episode 1
		ev(2, "error", 200, "70001"),   // re-trigger while open: folds in
		ev(3, "success", 400, "70001"), // closes: 300s episode, auto-resolved
		ev(4, "warning", 900, "70001"), // opens episode 2, stays open
		ev(5, "success", 950, "99999"), // recovery with no observed trigger: dropped
		ev(6, "error", 990, ""),        // no monitor id: dropped
	}, ",") + `]}`
}

func TestHistoryEpisodePairing(t *testing.T) {
	w := historyWindow()
	a := &Adapter{APIKey: "k", AppKey: "a", Transport: &eventsTransport{t: t, events: eventsFixture(w)}}
	var eps []model.AlertEvent
	for ep, err := range a.FetchEvents(adapter.Scope{Tenant: "acct"}, w) {
		if err != nil {
			t.Fatal(err)
		}
		eps = append(eps, ep)
	}
	if len(eps) != 2 {
		t.Fatalf("episodes = %d, want 2", len(eps))
	}

	ep := eps[0]
	if ep.OccurrenceCount != 2 {
		t.Errorf("occurrence_count = %d, want 2 (re-trigger folded)", ep.OccurrenceCount)
	}
	if ep.AutoResolved == nil || !*ep.AutoResolved {
		t.Error("condition recovery must set auto_resolved true (REQ-NOISE-001)")
	}
	if ep.ResolvedAt == nil || ep.ResolvedAt.Sub(ep.FiredAt) != 300*time.Second {
		t.Errorf("episode duration wrong: %v", ep.ResolvedAt)
	}
	if ep.AlertRef.NativeID == nil || *ep.AlertRef.NativeID != "70001" || *ep.AlertRef.Provider != "datadog" {
		t.Errorf("alert_ref = %+v — must join to the monitor config", ep.AlertRef)
	}
	if ep.AlertRef.Name == nil || *ep.AlertRef.Name != "checkout-api cpu high" {
		t.Errorf("monitor name = %v, want title with [Triggered] stripped", ep.AlertRef.Name)
	}
	if ep.IdentityHints.Tags["service"] != "checkout-api" {
		t.Errorf("tags = %v", ep.IdentityHints.Tags)
	}

	// Second episode: warning severity, still open.
	ep = eps[1]
	if ep.ResolvedAt != nil || ep.AutoResolved != nil {
		t.Error("open episode must have nil resolved_at and unknown auto_resolved")
	}
	if ep.Severity.Normalized != model.SeverityMedium {
		t.Errorf("warning severity = %+v", ep.Severity)
	}
}

func TestHistoryWindowChunking(t *testing.T) {
	w := historyWindow() // 90 days => 6 chunks of <=15 days
	tr := &eventsTransport{t: t, events: `{"events":[]}`}
	a := &Adapter{APIKey: "k", AppKey: "a", Transport: tr}
	for _, err := range a.FetchEvents(adapter.Scope{Tenant: "acct"}, w) {
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(tr.starts) != 6 {
		t.Fatalf("chunks = %d, want 6", len(tr.starts))
	}
	if tr.starts[0] != w.Start.Unix() {
		t.Errorf("first chunk start = %d, want window start", tr.starts[0])
	}
}
