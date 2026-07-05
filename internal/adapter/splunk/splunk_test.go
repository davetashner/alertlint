package splunk

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
	t    *testing.T
	fail int
}

func (f *fixtureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if f.fail != 0 {
		return &http.Response{StatusCode: f.fail,
			Body: io.NopCloser(strings.NewReader(`{"messages":[{"type":"ERROR","text":"unauthorized"}]}`))}, nil
	}
	if !strings.HasPrefix(req.Header.Get("Authorization"), "Bearer ") {
		f.t.Error("bearer token not set")
	}
	if req.URL.Query().Get("offset") != "0" {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"entry":[]}`))}, nil
	}
	buf, err := os.ReadFile(filepath.Join("testdata", "searches_p1.json"))
	if err != nil {
		return nil, err
	}
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(string(buf)))}, nil
}

func fixtureAdapter(t *testing.T) *Adapter {
	return &Adapter{BaseURL: "https://splunk.example.com:8089", Token: "tok",
		Transport: &fixtureTransport{t: t}}
}

func window() adapter.TimeWindow {
	end := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	return adapter.TimeWindow{Start: end.AddDate(0, 0, -90), End: end}
}

func TestConformance(t *testing.T) {
	adaptertest.Run(t, fixtureAdapter(t), adapter.Scope{Tenant: "prod-stack"}, window())
}

func TestConfigMapping(t *testing.T) {
	var cfgs []model.AlertConfig
	for c, err := range fixtureAdapter(t).FetchConfigs(adapter.Scope{Tenant: "prod-stack"}, window()) {
		if err != nil {
			t.Fatal(err)
		}
		cfgs = append(cfgs, c)
	}
	// The "always" scheduled report is filtered out: 2 alerts remain.
	if len(cfgs) != 2 {
		t.Fatalf("configs = %d, want 2 (reports are not alerts)", len(cfgs))
	}

	c := cfgs[0]
	if c.SourceRef.Kind != "saved_search" || c.SourceRef.NativeID != "checkout-api error spike" {
		t.Errorf("source_ref = %+v", c.SourceRef)
	}
	if !strings.Contains(c.ConditionRaw, "index=prod service=checkout-api") {
		t.Errorf("condition_raw = %q — SPL must be verbatim", c.ConditionRaw)
	}
	if c.Threshold == nil || *c.Threshold != 50 || c.Comparator == nil || *c.Comparator != ">" {
		t.Errorf("extraction = %v %v", c.Threshold, c.Comparator)
	}
	if c.Severity.Native != "4" || c.Severity.Normalized != model.SeverityHigh {
		t.Errorf("severity = %+v", c.Severity)
	}
	if len(c.Routing) != 2 || c.Routing[0].TargetKind != model.RouteEmail || c.Routing[1].TargetKind != model.RoutePagerDutyService {
		t.Errorf("routing = %+v", c.Routing)
	}
	if c.IdentityHints.Tags["splunk_app"] != "checkout" {
		t.Errorf("app hint = %v", c.IdentityHints.Tags)
	}

	// "rises by" + non-numeric threshold: no extraction, never a guess.
	c = cfgs[1]
	if c.Status != model.StatusDisabled {
		t.Errorf("disabled search status = %s", c.Status)
	}
	if c.Threshold != nil || c.Comparator != nil {
		t.Errorf("non-numeric threshold must yield no extraction: %v %v", c.Threshold, c.Comparator)
	}
	if c.Severity.Normalized != model.SeverityCritical {
		t.Errorf("severity 5 = %+v, want critical", c.Severity)
	}
	if len(c.Routing) != 1 || c.Routing[0].TargetKind != model.RouteChat {
		t.Errorf("slack routing = %+v", c.Routing)
	}
}

func TestErrorsAreNotEmptyResults(t *testing.T) {
	a := fixtureAdapter(t)
	a.Transport = &fixtureTransport{t: t, fail: http.StatusUnauthorized}
	var got error
	for _, err := range a.FetchConfigs(adapter.Scope{Tenant: "prod-stack"}, window()) {
		if err != nil {
			got = err
			break
		}
	}
	if got == nil || !strings.Contains(got.Error(), "HTTP 401") {
		t.Fatalf("unauthorized pull must surface an error, got %v", got)
	}
}
