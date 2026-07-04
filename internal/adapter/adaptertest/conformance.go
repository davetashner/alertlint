// Package adaptertest provides the conformance suite every provider adapter
// must pass. Vendor adapter tests call Run with a scope/window that yields
// representative fixture data (docs/specs/provider-adapters.md, Testing &
// acceptance).
package adaptertest

import (
	"strings"
	"testing"

	"github.com/davetashner/alertlint/internal/adapter"
	"github.com/davetashner/alertlint/internal/model"
)

// Run exercises every data-class interface p satisfies against the given
// scope and window, enforcing the contract rules from
// docs/specs/provider-adapters.md §1–§4: stable identity, schema version
// discipline, deterministic iteration order, and well-formed envelopes.
func Run(t *testing.T, p adapter.Provider, scope adapter.Scope, window adapter.TimeWindow) {
	t.Helper()

	id, schema := p.ProviderID(), p.SchemaVersion()
	if id == "" {
		t.Fatal("ProviderID() must be non-empty")
	}
	if again := p.ProviderID(); again != id {
		t.Fatalf("ProviderID() unstable: %q then %q", id, again)
	}
	if again := p.SchemaVersion(); again != schema {
		t.Fatalf("SchemaVersion() unstable: %q then %q", schema, again)
	}
	if major(p.SchemaVersion()) != major(model.CanonicalSchemaVersion) {
		t.Fatalf("SchemaVersion() = %q, major must match canonical %q",
			p.SchemaVersion(), model.CanonicalSchemaVersion)
	}

	caps := adapter.CapabilitiesOf(p)
	if !caps.Config && !caps.History && !caps.Action && !caps.CI {
		t.Fatal("provider satisfies no data-class interface")
	}

	if cp, ok := p.(adapter.ConfigProvider); ok {
		t.Run("configs", func(t *testing.T) {
			ids := collectConfigs(t, cp, scope, window)
			again := collectConfigs(t, cp, scope, window)
			assertSameOrder(t, ids, again)
		})
	}
	if hp, ok := p.(adapter.HistoryProvider); ok {
		t.Run("events", func(t *testing.T) {
			ids := collectEvents(t, hp, scope, window)
			again := collectEvents(t, hp, scope, window)
			assertSameOrder(t, ids, again)
		})
	}
	if ap, ok := p.(adapter.ActionProvider); ok {
		t.Run("responses", func(t *testing.T) {
			ids := collectResponses(t, ap, scope, window)
			again := collectResponses(t, ap, scope, window)
			assertSameOrder(t, ids, again)
		})
	}
	if cp, ok := p.(adapter.CIProvider); ok {
		t.Run("cis", func(t *testing.T) {
			ids := collectCIs(t, cp, scope, window)
			again := collectCIs(t, cp, scope, window)
			assertSameOrder(t, ids, again)
		})
	}
}

func collectCIs(t *testing.T, cp adapter.CIProvider, scope adapter.Scope, window adapter.TimeWindow) []string {
	t.Helper()
	seen := map[string]bool{}
	var ids []string
	for ci, err := range cp.FetchCIs(scope, window) {
		if err != nil {
			t.Fatalf("FetchCIs failed: %v", err)
		}
		if ci.ID == "" || ci.Name == "" {
			t.Errorf("CI record must carry ci_id and name: %+v", ci)
		}
		if ci.Status == "" {
			t.Errorf("%s: CI status must be present", ci.ID)
		}
		if seen[ci.ID] {
			t.Errorf("%s: duplicate ci_id in one snapshot", ci.ID)
		}
		seen[ci.ID] = true
		ids = append(ids, ci.ID)
	}
	return ids
}

func major(v string) string {
	if i := strings.IndexByte(v, '.'); i >= 0 {
		return v[:i]
	}
	return v
}

func assertSameOrder(t *testing.T, first, second []string) {
	t.Helper()
	if len(first) != len(second) {
		t.Fatalf("iteration not deterministic: %d records then %d", len(first), len(second))
	}
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("iteration order not deterministic at index %d: %q vs %q", i, first[i], second[i])
		}
	}
}

func checkEnvelope(t *testing.T, e model.Envelope, p adapter.Provider) {
	t.Helper()
	if e.SchemaVersion != p.SchemaVersion() {
		t.Errorf("record schema_version %q != adapter SchemaVersion() %q", e.SchemaVersion, p.SchemaVersion())
	}
	if e.Source.Provider != p.ProviderID() {
		t.Errorf("record source.provider %q != ProviderID() %q", e.Source.Provider, p.ProviderID())
	}
	if e.Source.Tenant == "" {
		t.Error("source.tenant must echo the Scope tenant")
	}
	if e.SourceRef.Kind == "" || e.SourceRef.NativeID == "" {
		t.Errorf("source_ref must carry kind and native_id: %+v", e.SourceRef)
	}
	if e.IdentityHints.Tags == nil || e.IdentityHints.Names == nil || e.IdentityHints.ExternalRefs == nil {
		t.Error("identity_hints collections must be non-nil (empty, never null, in JSON)")
	}
}

func collectConfigs(t *testing.T, cp adapter.ConfigProvider, scope adapter.Scope, window adapter.TimeWindow) []string {
	t.Helper()
	var ids []string
	for rec, err := range cp.FetchConfigs(scope, window) {
		if err != nil {
			t.Fatalf("FetchConfigs failed: %v", err)
		}
		checkEnvelope(t, rec.Envelope, cp)
		if rec.ConditionRaw == "" {
			t.Errorf("%s: condition_raw is always present", rec.SourceRef.NativeID)
		}
		if !rec.Status.Valid() {
			t.Errorf("%s: invalid status %q", rec.SourceRef.NativeID, rec.Status)
		}
		if !rec.Severity.Normalized.Valid() {
			t.Errorf("%s: invalid normalized severity %q", rec.SourceRef.NativeID, rec.Severity.Normalized)
		}
		if rec.Comparator != nil && !model.ValidComparator(*rec.Comparator) {
			t.Errorf("%s: invalid comparator %q", rec.SourceRef.NativeID, *rec.Comparator)
		}
		for _, route := range rec.Routing {
			if !route.TargetKind.Valid() {
				t.Errorf("%s: invalid route target kind %q", rec.SourceRef.NativeID, route.TargetKind)
			}
		}
		ids = append(ids, rec.SourceRef.NativeID)
	}
	return ids
}

func collectEvents(t *testing.T, hp adapter.HistoryProvider, scope adapter.Scope, window adapter.TimeWindow) []string {
	t.Helper()
	var ids []string
	for rec, err := range hp.FetchEvents(scope, window) {
		if err != nil {
			t.Fatalf("FetchEvents failed: %v", err)
		}
		checkEnvelope(t, rec.Envelope, hp)
		if rec.FiredAt.Before(window.Start) || !rec.FiredAt.Before(window.End) {
			t.Errorf("%s: fired_at %v outside window [%v, %v)", rec.SourceRef.NativeID, rec.FiredAt, window.Start, window.End)
		}
		if rec.OccurrenceCount < 1 {
			t.Errorf("%s: occurrence_count %d < 1", rec.SourceRef.NativeID, rec.OccurrenceCount)
		}
		if !rec.Severity.Normalized.Valid() {
			t.Errorf("%s: invalid normalized severity %q", rec.SourceRef.NativeID, rec.Severity.Normalized)
		}
		ids = append(ids, rec.SourceRef.NativeID)
	}
	return ids
}

func collectResponses(t *testing.T, ap adapter.ActionProvider, scope adapter.Scope, window adapter.TimeWindow) []string {
	t.Helper()
	var ids []string
	for rec, err := range ap.FetchResponses(scope, window) {
		if err != nil {
			t.Fatalf("FetchResponses failed: %v", err)
		}
		checkEnvelope(t, rec.Envelope, ap)
		if !rec.Disposition.Valid() {
			t.Errorf("%s: disposition %q outside the closed taxonomy", rec.SourceRef.NativeID, rec.Disposition)
		}
		if rec.ReassignmentCount < 0 {
			t.Errorf("%s: reassignment_count %d < 0", rec.SourceRef.NativeID, rec.ReassignmentCount)
		}
		for _, lr := range rec.LinkedRecords {
			if !lr.Kind.Valid() {
				t.Errorf("%s: invalid linked record kind %q", rec.SourceRef.NativeID, lr.Kind)
			}
		}
		ids = append(ids, rec.SourceRef.NativeID)
	}
	return ids
}
