package adapter_test

import (
	"errors"
	"testing"
	"time"

	"github.com/davetashner/alertlint/internal/adapter"
	"github.com/davetashner/alertlint/internal/adapter/adaptertest"
	"github.com/davetashner/alertlint/internal/adapter/fake"
	"github.com/davetashner/alertlint/internal/identity"
	"github.com/davetashner/alertlint/internal/model"
)

func envelope(provider, kind, nativeID string) model.Envelope {
	return model.Envelope{
		SchemaVersion: model.CanonicalSchemaVersion,
		Source:        model.Source{Provider: provider, Tenant: "tenant-1"},
		SourceRef:     model.SourceRef{Kind: kind, NativeID: nativeID},
		IdentityHints: model.IdentityHints{
			Tags:         map[string]string{"service": "checkout-api"},
			Names:        []string{"checkout-api"},
			ExternalRefs: []model.ExternalRef{},
		},
	}
}

func testWindow() adapter.TimeWindow {
	end := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	return adapter.TimeWindow{Start: end.AddDate(0, 0, -90), End: end}
}

func fullFake() *fake.Provider {
	w := testWindow()
	return &fake.Provider{
		ID: "fakevendor",
		Configs: []model.AlertConfig{{
			Envelope:     envelope("fakevendor", "monitor", "m-1"),
			Name:         "p95 latency high",
			ConditionRaw: "p95 > 2.5",
			Severity:     model.Severity{Native: "P2", Normalized: model.SeverityHigh},
			Routing:      []model.Route{{TargetKind: model.RoutePagerDutyService, Target: "PXYZ"}},
			Status:       model.StatusEnabled,
		}},
		Events: []model.AlertEvent{{
			Envelope:        envelope("fakevendor", "incident", "i-1"),
			FiredAt:         w.Start.Add(24 * time.Hour),
			OccurrenceCount: 1,
			Severity:        model.Severity{Native: "high", Normalized: model.SeverityHigh},
		}},
		Responses: []model.ResponseRecord{{
			Envelope:      envelope("fakevendor", "incident", "r-1"),
			Disposition:   model.DispositionNoAction,
			LinkedRecords: []model.LinkedRecord{},
		}},
		CIs: []identity.CI{{ID: "CI001", Name: "checkout-api", Status: "operational"}},
		Maintenance: []model.MaintenanceWindow{{
			Envelope:    envelope("fakevendor", "downtime", "dt-1"),
			StartsAt:    w.Start.Add(48 * time.Hour),
			MonitorRefs: []model.MonitorRef{{Provider: "fakevendor", NativeID: "m-1"}},
		}},
	}
}

func TestFakeProviderConformance(t *testing.T) {
	adaptertest.Run(t, fullFake(), adapter.Scope{Tenant: "tenant-1"}, testWindow())
}

func TestRegistryCapabilityDiscovery(t *testing.T) {
	r := adapter.NewRegistry()
	full := fullFake()
	if err := r.Register(full); err != nil {
		t.Fatalf("register: %v", err)
	}

	caps := adapter.CapabilitiesOf(full)
	if !caps.Config || !caps.History || !caps.Action || !caps.CI || !caps.Maintenance {
		t.Errorf("full fake should satisfy all five interfaces: %+v", caps)
	}
	if got := len(r.ConfigProviders()); got != 1 {
		t.Errorf("ConfigProviders = %d, want 1", got)
	}
	if got := len(r.HistoryProviders()); got != 1 {
		t.Errorf("HistoryProviders = %d, want 1", got)
	}
	if got := len(r.ActionProviders()); got != 1 {
		t.Errorf("ActionProviders = %d, want 1", got)
	}
	if got := len(r.CIProviders()); got != 1 {
		t.Errorf("CIProviders = %d, want 1", got)
	}
	if got := len(r.MaintenanceProviders()); got != 1 {
		t.Errorf("MaintenanceProviders = %d, want 1", got)
	}
}

func TestRegistryRejectsDuplicatesAndEmptyCapability(t *testing.T) {
	r := adapter.NewRegistry()
	if err := r.Register(fullFake()); err != nil {
		t.Fatalf("first register: %v", err)
	}
	if err := r.Register(fullFake()); err == nil {
		t.Error("duplicate id must be rejected")
	}
}

func TestRegistryIDsSorted(t *testing.T) {
	r := adapter.NewRegistry()
	for _, id := range []string{"zeta", "alpha", "midl"} {
		p := fullFake()
		p.ID = id
		// re-stamp envelopes so source.provider matches
		for i := range p.Configs {
			p.Configs[i].Source.Provider = id
		}
		if err := r.Register(p); err != nil {
			t.Fatalf("register %s: %v", id, err)
		}
	}
	ids := r.IDs()
	want := []string{"alpha", "midl", "zeta"}
	for i, id := range want {
		if ids[i] != id {
			t.Fatalf("IDs() = %v, want %v (sorted, never map order)", ids, want)
		}
	}
}

func TestErrorsAreNotEmptyResults(t *testing.T) {
	p := fullFake()
	p.Err = errors.New("rate limited on page 3")
	var got error
	var records int
	for _, err := range p.FetchConfigs(adapter.Scope{Tenant: "tenant-1"}, testWindow()) {
		if err != nil {
			got = err
			break
		}
		records++
	}
	if got == nil {
		t.Fatal("failed pull must surface an error, not end as a short result")
	}
	if records != len(p.Configs) {
		t.Errorf("records before error = %d, want %d", records, len(p.Configs))
	}
}

func TestDefaultWindow(t *testing.T) {
	now := time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC)
	w := adapter.DefaultWindow(now)
	if w.End != now || w.Start != now.AddDate(0, 0, -90) {
		t.Errorf("DefaultWindow = [%v, %v), want 90 days ending at now", w.Start, w.End)
	}
}
