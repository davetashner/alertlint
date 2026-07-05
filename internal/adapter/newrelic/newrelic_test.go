package newrelic

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
			Body: io.NopCloser(strings.NewReader(`{"error":{"title":"quota"}}`))}, nil
	}
	if req.Header.Get("Api-Key") == "" {
		f.t.Error("Api-Key header not set")
	}
	q := req.URL.Query()
	var file string
	switch {
	case req.URL.Path == "/v2/alerts_policies.json" && q.Get("page") == "1":
		file = "policies_p1.json"
	case req.URL.Path == "/v2/alerts_policies.json":
		return emptyJSON(`{"policies":[]}`), nil
	case req.URL.Path == "/v2/alerts_nrql_conditions.json" && q.Get("page") == "1":
		file = "conditions_" + q.Get("policy_id") + "_p1.json"
	case req.URL.Path == "/v2/alerts_nrql_conditions.json":
		return emptyJSON(`{"nrql_conditions":[]}`), nil
	default:
		f.t.Fatalf("unexpected request %s?%s", req.URL.Path, req.URL.RawQuery)
	}
	buf, err := os.ReadFile(filepath.Join("testdata", file))
	if err != nil {
		return nil, err
	}
	return emptyJSON(string(buf)), nil
}

func emptyJSON(s string) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(s))}
}

func fixtureAdapter(t *testing.T) *Adapter {
	return &Adapter{APIKey: "k", Transport: &fixtureTransport{t: t}}
}

func window() adapter.TimeWindow {
	end := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	return adapter.TimeWindow{Start: end.AddDate(0, 0, -90), End: end}
}

func TestConformance(t *testing.T) {
	adaptertest.Run(t, fixtureAdapter(t), adapter.Scope{Tenant: "acct"}, window())
}

func TestConfigMapping(t *testing.T) {
	var cfgs []model.AlertConfig
	for c, err := range fixtureAdapter(t).FetchConfigs(adapter.Scope{Tenant: "acct"}, window()) {
		if err != nil {
			t.Fatal(err)
		}
		cfgs = append(cfgs, c)
	}
	if len(cfgs) != 2 {
		t.Fatalf("configs = %d, want 2", len(cfgs))
	}

	// Condition 998811: critical term wins over warning.
	c := cfgs[0]
	if c.SourceRef.NativeID != "998811" || c.SourceRef.Kind != "nrql_condition" {
		t.Errorf("source_ref = %+v", c.SourceRef)
	}
	if !strings.Contains(c.ConditionRaw, "SELECT percentile") {
		t.Errorf("condition_raw = %q — NRQL must be verbatim", c.ConditionRaw)
	}
	if c.Threshold == nil || *c.Threshold != 1.5 {
		t.Errorf("threshold = %v, want critical term's 1.5", c.Threshold)
	}
	if c.Comparator == nil || *c.Comparator != ">=" {
		t.Errorf("comparator = %v, want >= (above)", c.Comparator)
	}
	if c.DurationS == nil || *c.DurationS != 600 {
		t.Errorf("duration_s = %v, want 600 (10 minutes)", c.DurationS)
	}
	if c.Severity.Normalized != model.SeverityCritical {
		t.Errorf("severity = %+v", c.Severity)
	}
	// Identity hints: policy + condition names, verbatim, no tags in v2.
	if len(c.IdentityHints.Names) != 2 || c.IdentityHints.Names[0] != "Payments API" {
		t.Errorf("names = %v", c.IdentityHints.Names)
	}

	// Condition 555001: disabled, no terms => no extraction, no guess.
	c = cfgs[1]
	if c.Status != model.StatusDisabled {
		t.Errorf("status = %s, want disabled", c.Status)
	}
	if c.Threshold != nil || c.Comparator != nil || c.DurationS != nil {
		t.Errorf("termless condition must have no extraction: %v/%v/%v", c.Threshold, c.Comparator, c.DurationS)
	}
	if c.Severity.Normalized != model.SeverityUnknown {
		t.Errorf("severity = %+v, want unknown", c.Severity)
	}
}

func TestErrorsAreNotEmptyResults(t *testing.T) {
	a := &Adapter{APIKey: "k", Transport: &fixtureTransport{t: t, fail: http.StatusPaymentRequired}}
	var got error
	for _, err := range a.FetchConfigs(adapter.Scope{Tenant: "acct"}, window()) {
		if err != nil {
			got = err
			break
		}
	}
	if got == nil || !strings.Contains(got.Error(), "HTTP 402") {
		t.Fatalf("failed pull must surface an error, got %v", got)
	}
}
