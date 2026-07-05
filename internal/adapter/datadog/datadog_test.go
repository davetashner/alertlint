package datadog

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
	pages map[string]string // page number -> testdata file; missing = empty page
	fail  int
}

func (f *fixtureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if f.fail != 0 {
		return &http.Response{StatusCode: f.fail,
			Body: io.NopCloser(strings.NewReader(`{"errors":["Forbidden"]}`))}, nil
	}
	if req.Header.Get("DD-API-KEY") == "" || req.Header.Get("DD-APPLICATION-KEY") == "" {
		f.t.Error("API keys not set on request")
	}
	if req.URL.Path == "/api/v1/events" {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"events":[]}`))}, nil
	}
	file, ok := f.pages[req.URL.Query().Get("page")]
	if !ok {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`[]`))}, nil
	}
	buf, err := os.ReadFile(filepath.Join("testdata", file))
	if err != nil {
		return nil, err
	}
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(buf)))}, nil
}

func fixtureAdapter(t *testing.T) *Adapter {
	return &Adapter{
		APIKey: "k", AppKey: "a",
		Transport: &fixtureTransport{t: t, pages: map[string]string{"0": "monitors_page1.json"}},
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

func collect(t *testing.T) []model.AlertConfig {
	t.Helper()
	var out []model.AlertConfig
	for c, err := range fixtureAdapter(t).FetchConfigs(scope(), window()) {
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, c)
	}
	return out
}

func TestConfigMapping(t *testing.T) {
	cfgs := collect(t)
	if len(cfgs) != 3 {
		t.Fatalf("configs = %d, want 3", len(cfgs))
	}

	// Monitor 84312077: full extraction.
	c := cfgs[0]
	if c.SourceRef.NativeID != "84312077" || c.SourceRef.Kind != "monitor" {
		t.Errorf("source_ref = %+v", c.SourceRef)
	}
	if c.ConditionRaw == "" || !strings.Contains(c.ConditionRaw, "p95:trace.http") {
		t.Errorf("condition_raw = %q — must be verbatim", c.ConditionRaw)
	}
	if c.Threshold == nil || *c.Threshold != 2.5 {
		t.Errorf("threshold = %v, want 2.5", c.Threshold)
	}
	if c.Comparator == nil || *c.Comparator != ">" {
		t.Errorf("comparator = %v, want >", c.Comparator)
	}
	if c.DurationS == nil || *c.DurationS != 600 {
		t.Errorf("duration_s = %v, want 600 (last_10m)", c.DurationS)
	}
	if c.Severity.Native != "P2" || c.Severity.Normalized != model.SeverityHigh {
		t.Errorf("severity = %+v", c.Severity)
	}
	if c.IdentityHints.Tags["service"] != "checkout-api" || c.IdentityHints.Tags["managed-by-terraform"] != "" {
		t.Errorf("tags = %v — verbatim k:v split, bare tags keep empty value", c.IdentityHints.Tags)
	}
	if len(c.Routing) != 2 || c.Routing[0].TargetKind != model.RoutePagerDutyService || c.Routing[0].Target != "Checkout-API" {
		t.Errorf("routing = %+v", c.Routing)
	}
	if c.Routing[1].TargetKind != model.RouteEmail || c.Routing[1].Target != "oncall@example.com" {
		t.Errorf("email route = %+v", c.Routing[1])
	}
	if c.Status != model.StatusEnabled {
		t.Errorf("status = %s", c.Status)
	}
	if c.CreatedAt == nil || c.CreatedAt.Year() != 2025 {
		t.Errorf("created_at = %v", c.CreatedAt)
	}

	// Monitor 84312440: silenced, >= comparator, last_1h.
	c = cfgs[1]
	if c.Status != model.StatusSilenced {
		t.Errorf("silenced monitor status = %s", c.Status)
	}
	if c.Comparator == nil || *c.Comparator != ">=" || c.DurationS == nil || *c.DurationS != 3600 {
		t.Errorf("extraction = %v/%v", c.Comparator, c.DurationS)
	}

	// Composite monitor: not parseable — absent, never guessed.
	c = cfgs[2]
	if c.Threshold != nil || c.Comparator != nil || c.DurationS != nil {
		t.Errorf("composite monitor must have no extraction: %v/%v/%v", c.Threshold, c.Comparator, c.DurationS)
	}
	if c.ConditionRaw != "12345 && 67890" {
		t.Errorf("condition_raw = %q — verbatim even when unparseable", c.ConditionRaw)
	}
	if c.Severity.Normalized != model.SeverityLow {
		t.Errorf("P4 severity = %+v", c.Severity)
	}
}

func TestErrorsAreNotEmptyResults(t *testing.T) {
	a := fixtureAdapter(t)
	a.Transport = &fixtureTransport{t: t, fail: http.StatusForbidden}
	var got error
	for _, err := range a.FetchConfigs(scope(), window()) {
		if err != nil {
			got = err
			break
		}
	}
	if got == nil || !strings.Contains(got.Error(), "HTTP 403") {
		t.Fatalf("forbidden pull must surface an error, got %v", got)
	}
}
