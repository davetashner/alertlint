package model

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// roundTrip unmarshals the spec example into v, re-marshals it, and asserts
// the result is semantically identical to the original document — proving no
// field is dropped, renamed, or retyped in either direction.
func roundTrip(t *testing.T, file string, v any) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", file))
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		t.Fatalf("unmarshal %s into %T: %v", file, v, err)
	}
	out, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %T: %v", v, err)
	}
	var want, got map[string]any
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatalf("normalize original: %v", err)
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("normalize round-tripped: %v", err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Errorf("round-trip mismatch for %s:\noriginal:     %v\nround-tripped: %v", file, want, got)
	}
}

func TestAlertConfigRoundTrip(t *testing.T) {
	var c AlertConfig
	roundTrip(t, "alertconfig.json", &c)

	if c.SchemaVersion != CanonicalSchemaVersion {
		t.Errorf("schema_version = %q, want %q", c.SchemaVersion, CanonicalSchemaVersion)
	}
	if c.Source.Provider != "datadog" || c.SourceRef.NativeID != "84312077" {
		t.Errorf("envelope not populated: %+v", c.Envelope)
	}
	if c.IdentityHints.Tags["service"] != "checkout-api" {
		t.Errorf("identity hints not populated: %+v", c.IdentityHints)
	}
	if c.Threshold == nil || *c.Threshold != 2.5 {
		t.Errorf("threshold = %v, want 2.5", c.Threshold)
	}
	if c.Comparator == nil || !ValidComparator(*c.Comparator) {
		t.Errorf("comparator = %v, want valid comparator", c.Comparator)
	}
	if !c.Status.Valid() || c.Status != StatusEnabled {
		t.Errorf("status = %q, want enabled", c.Status)
	}
	if !c.Severity.Normalized.Valid() {
		t.Errorf("normalized severity %q not valid", c.Severity.Normalized)
	}
	for _, r := range c.Routing {
		if !r.TargetKind.Valid() {
			t.Errorf("route target kind %q not valid", r.TargetKind)
		}
	}
}

func TestAlertEventRoundTrip(t *testing.T) {
	var e AlertEvent
	roundTrip(t, "alertevent.json", &e)

	if e.Source.Provider != "pagerduty" {
		t.Errorf("source provider = %q, want pagerduty", e.Source.Provider)
	}
	if e.AlertRef.NativeID == nil || *e.AlertRef.NativeID != "84312077" {
		t.Errorf("alert_ref.native_id = %v, want 84312077", e.AlertRef.NativeID)
	}
	if e.FiredAt.IsZero() {
		t.Error("fired_at is zero")
	}
	if e.AutoResolved == nil || !*e.AutoResolved {
		t.Errorf("auto_resolved = %v, want true", e.AutoResolved)
	}
	if e.OccurrenceCount < 1 {
		t.Errorf("occurrence_count = %d, want >= 1", e.OccurrenceCount)
	}
}

func TestResponseRecordRoundTrip(t *testing.T) {
	var r ResponseRecord
	roundTrip(t, "responserecord.json", &r)

	if r.Source.Provider != "servicenow" {
		t.Errorf("source provider = %q, want servicenow", r.Source.Provider)
	}
	if r.AckedAt != nil {
		t.Errorf("acked_at = %v, want nil (never acknowledged)", r.AckedAt)
	}
	if r.Disposition != DispositionNoAction || !r.Disposition.Valid() {
		t.Errorf("disposition = %q, want no_action", r.Disposition)
	}
	if r.DispositionNative == nil {
		t.Error("disposition_native missing — raw code must be preserved")
	}
	if r.ReassignmentCount != 2 {
		t.Errorf("reassignment_count = %d, want 2", r.ReassignmentCount)
	}
}

func TestDispositionTaxonomyIsClosed(t *testing.T) {
	valid := []Disposition{
		DispositionNoAction, DispositionActionTaken, DispositionEscalated,
		DispositionDuplicate, DispositionKnownIssue, DispositionAutoClosed,
		DispositionUnknown,
	}
	if len(valid) != 7 {
		t.Fatalf("taxonomy has %d values, spec fixes exactly 7", len(valid))
	}
	seen := map[Disposition]bool{}
	for _, d := range valid {
		if !d.Valid() {
			t.Errorf("%q should be valid", d)
		}
		if seen[d] {
			t.Errorf("%q duplicated", d)
		}
		seen[d] = true
	}
	for _, d := range []Disposition{"", "noise", "self_healed", "NO_ACTION"} {
		if d.Valid() {
			t.Errorf("%q should be invalid — the taxonomy is closed", d)
		}
	}
}

func TestEnumValidators(t *testing.T) {
	if NormalizedSeverity("sev1").Valid() || !SeverityUnknown.Valid() {
		t.Error("severity validator wrong")
	}
	if ConfigStatus("muted").Valid() || !StatusSilenced.Valid() {
		t.Error("status validator wrong")
	}
	if LinkedRecordKind("ticket").Valid() || !LinkedChange.Valid() {
		t.Error("linked record kind validator wrong")
	}
	if ValidComparator("=>") || !ValidComparator("!=") {
		t.Error("comparator validator wrong")
	}
}
