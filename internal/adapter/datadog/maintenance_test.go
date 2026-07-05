package datadog

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/davetashner/alertlint/internal/adapter"
	"github.com/davetashner/alertlint/internal/model"
)

type downtimeTransport struct{ t *testing.T }

func (f *downtimeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Path != "/api/v1/downtime" {
		f.t.Fatalf("unexpected path %s", req.URL.Path)
	}
	body := `[
	  {"id": 1, "start": 1746400000, "end": 1746403600, "monitor_id": 70001,
	   "scope": ["*"], "message": "deploy window", "disabled": false, "canceled": null},
	  {"id": 2, "start": 1746500000, "end": null, "monitor_id": null,
	   "scope": ["env:prod"], "message": "", "disabled": false, "canceled": null},
	  {"id": 3, "start": 1746600000, "end": 1746603600, "monitor_id": 70002,
	   "scope": ["*"], "message": "cancelled", "disabled": false, "canceled": 1746601000},
	  {"id": 4, "start": 946684800, "end": 946688400, "monitor_id": 70003,
	   "scope": ["*"], "message": "ancient", "disabled": false, "canceled": null}
	]`
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}, nil
}

func TestMaintenanceTranslation(t *testing.T) {
	a := &Adapter{APIKey: "k", AppKey: "a", Transport: &downtimeTransport{t: t}}
	w := adapter.TimeWindow{
		Start: time.Unix(1746000000, 0).UTC(),
		End:   time.Unix(1747000000, 0).UTC(),
	}
	var windows []model.MaintenanceWindow
	for mw, err := range a.FetchMaintenance(adapter.Scope{Tenant: "acct"}, w) {
		if err != nil {
			t.Fatal(err)
		}
		windows = append(windows, mw)
	}
	// Cancelled (3) and outside-window (4) are skipped.
	if len(windows) != 2 {
		t.Fatalf("windows = %d, want 2", len(windows))
	}
	mw := windows[0]
	if len(mw.MonitorRefs) != 1 || mw.MonitorRefs[0].NativeID != "70001" {
		t.Errorf("monitor refs = %+v", mw.MonitorRefs)
	}
	if mw.Reason == nil || *mw.Reason != "deploy window" {
		t.Errorf("reason = %v", mw.Reason)
	}
	if mw.EndsAt == nil || mw.EndsAt.Sub(mw.StartsAt) != time.Hour {
		t.Errorf("interval = %v", mw.EndsAt)
	}
	// Scope-wide open-ended window.
	mw = windows[1]
	if len(mw.MonitorRefs) != 0 || mw.EndsAt != nil {
		t.Errorf("scope-wide window = %+v", mw)
	}
}
