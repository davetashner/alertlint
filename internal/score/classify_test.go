package score

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/davetashner/alertlint/internal/model"
)

var fired = time.Date(2026, 6, 1, 3, 0, 0, 0, time.UTC)

func cfg(t *testing.T) Config {
	t.Helper()
	c, err := DecodeConfig(strings.NewReader(validYAML))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func ts(offset time.Duration) *time.Time {
	v := fired.Add(offset)
	return &v
}

func boolp(b bool) *bool    { return &b }
func strp(s string) *string { return &s }

// fire builds a Fire with sensible defaults, mutated by opts.
func fire(mods ...func(*Fire)) Fire {
	f := Fire{
		Event: model.AlertEvent{FiredAt: fired, OccurrenceCount: 1},
	}
	for _, m := range mods {
		m(&f)
	}
	return f
}

func withResponse(r model.ResponseRecord) func(*Fire) {
	return func(f *Fire) { f.Response = &r }
}

func TestDecisionTablePerRule(t *testing.T) {
	c := cfg(t)
	cases := []struct {
		name      string
		fire      Fire
		wantRule  string
		wantClass Class
		wantConf  float64
		wantLow   bool
	}{
		{
			name: "N-D1 no-action close code",
			fire: fire(withResponse(model.ResponseRecord{
				Disposition:       model.DispositionNoAction,
				DispositionNative: strp("Closed/Resolved - No Action Taken"),
				ClosedAt:          ts(40 * time.Minute),
			})),
			wantRule: "N-D1", wantClass: ClassNoise, wantConf: 0.90,
		},
		{
			name: "N-D2 linked change record",
			fire: fire(withResponse(model.ResponseRecord{
				Disposition:   model.DispositionUnknown,
				LinkedRecords: []model.LinkedRecord{{Kind: model.LinkedChange, NativeID: "CHG001"}},
			})),
			wantRule: "N-D2", wantClass: ClassActionable, wantConf: 0.90,
		},
		{
			name: "N-D3 acked manual close with substantive code",
			fire: fire(withResponse(model.ResponseRecord{
				Disposition: model.DispositionActionTaken,
				AckedAt:     ts(5 * time.Minute),
				ClosedAt:    ts(50 * time.Minute),
			})),
			wantRule: "N-D3", wantClass: ClassActionable, wantConf: 0.70,
		},
		{
			name: "N-D4 acked after auto-resolve",
			fire: fire(func(f *Fire) {
				f.Event.AutoResolved = boolp(true)
				f.Event.ResolvedAt = ts(4 * time.Minute)
			}, withResponse(model.ResponseRecord{
				Disposition: model.DispositionUnknown,
				AckedAt:     ts(9 * time.Minute), // after resolve, within grace
			})),
			wantRule: "N-D4", wantClass: ClassNoise, wantConf: 0.60,
		},
		{
			name: "N-T1 ambiguity default: never acked, fast auto-resolve, no code",
			fire: fire(func(f *Fire) {
				f.Event.AutoResolved = boolp(true)
				f.Event.ResolvedAt = ts(6 * time.Minute)
			}),
			wantRule: "N-T1", wantClass: ClassNoise, wantConf: 0.35, wantLow: true,
		},
		{
			name: "N-T2 never acked, no auto-resolve, closed without code",
			fire: fire(withResponse(model.ResponseRecord{
				Disposition: model.DispositionUnknown,
				ClosedAt:    ts(3 * time.Hour),
			})),
			wantRule: "N-T2", wantClass: ClassNoise, wantConf: 0.45, wantLow: true,
		},
		{
			name: "N-T3 acked but heavily reassigned",
			fire: fire(withResponse(model.ResponseRecord{
				Disposition:       model.DispositionUnknown,
				AckedAt:           ts(10 * time.Minute),
				ReassignmentCount: 3,
			})),
			wantRule: "N-T3", wantClass: ClassUnclear, wantConf: 0.40, wantLow: true,
		},
		{
			name:     "N-T4 no response record at all, no auto-resolve",
			fire:     fire(),
			wantRule: "N-T4", wantClass: ClassUnclassified, wantConf: 0.20, wantLow: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Classify(tc.fire, c)
			if got.RuleID != tc.wantRule || got.Class != tc.wantClass {
				t.Errorf("= %s/%s, want %s/%s", got.RuleID, got.Class, tc.wantRule, tc.wantClass)
			}
			if got.Confidence != tc.wantConf {
				t.Errorf("confidence = %v, want %v", got.Confidence, tc.wantConf)
			}
			if got.LowConfidence != tc.wantLow {
				t.Errorf("low_confidence = %v, want %v", got.LowConfidence, tc.wantLow)
			}
		})
	}
}

// First-match-wins: a fire that satisfies several rules must classify by
// the earliest.
func TestDecisionTableOrder(t *testing.T) {
	c := cfg(t)

	// no_action + linked change: N-D1 beats N-D2.
	f := fire(withResponse(model.ResponseRecord{
		Disposition:   model.DispositionNoAction,
		LinkedRecords: []model.LinkedRecord{{Kind: model.LinkedChange, NativeID: "CHG9"}},
	}))
	if got := Classify(f, c); got.RuleID != "N-D1" {
		t.Errorf("N-D1 must win over N-D2, got %s", got.RuleID)
	}

	// linked incident + substantive ack/close: N-D2 beats N-D3.
	f = fire(withResponse(model.ResponseRecord{
		Disposition:   model.DispositionActionTaken,
		AckedAt:       ts(2 * time.Minute),
		ClosedAt:      ts(30 * time.Minute),
		LinkedRecords: []model.LinkedRecord{{Kind: model.LinkedIncident, NativeID: "INC7"}},
	}))
	if got := Classify(f, c); got.RuleID != "N-D2" {
		t.Errorf("N-D2 must win over N-D3, got %s", got.RuleID)
	}

	// problem-linked record is NOT an N-D2 trigger (change|incident only).
	f = fire(withResponse(model.ResponseRecord{
		Disposition:   model.DispositionKnownIssue,
		AckedAt:       ts(2 * time.Minute),
		ClosedAt:      ts(30 * time.Minute),
		LinkedRecords: []model.LinkedRecord{{Kind: model.LinkedProblem, NativeID: "PRB1"}},
	}))
	if got := Classify(f, c); got.RuleID != "N-D3" {
		t.Errorf("problem link must not trigger N-D2; want N-D3, got %s", got.RuleID)
	}
}

func TestSlowAutoResolveIsNotAmbiguityDefault(t *testing.T) {
	c := cfg(t)
	// Auto-resolved but slower than fast_auto_resolve_minutes: N-T1 must
	// not fire; with no close and no ack it falls through to N-T4.
	f := fire(func(f *Fire) {
		f.Event.AutoResolved = boolp(true)
		f.Event.ResolvedAt = ts(45 * time.Minute)
	})
	if got := Classify(f, c); got.RuleID != "N-T4" {
		t.Errorf("slow auto-resolve: want N-T4, got %s", got.RuleID)
	}
}

func TestLateAckCountsAsNeverAcked(t *testing.T) {
	c := cfg(t)
	// Acked long after the grace period, auto-resolved fast, no code:
	// still the N-T1 ambiguity default... but N-D4 fires first because the
	// ack came after auto-resolve — the human-arrived-late signal.
	f := fire(func(f *Fire) {
		f.Event.AutoResolved = boolp(true)
		f.Event.ResolvedAt = ts(5 * time.Minute)
	}, withResponse(model.ResponseRecord{
		Disposition: model.DispositionUnknown,
		AckedAt:     ts(2 * time.Hour),
	}))
	if got := Classify(f, c); got.RuleID != "N-D4" {
		t.Errorf("late ack after auto-resolve: want N-D4, got %s", got.RuleID)
	}
}

func TestEvidencePopulated(t *testing.T) {
	c := cfg(t)
	f := fire(func(f *Fire) {
		f.Event.AutoResolved = boolp(true)
		f.Event.ResolvedAt = ts(4 * time.Minute)
	}, withResponse(model.ResponseRecord{
		Disposition:       model.DispositionNoAction,
		DispositionNative: strp("Closed - No Action"),
		ClosedAt:          ts(10 * time.Minute),
		ReassignmentCount: 1,
	}))
	got := Classify(f, c)
	ev := got.Evidence
	if ev.Disposition == nil || *ev.Disposition != model.DispositionNoAction {
		t.Error("evidence missing disposition")
	}
	if ev.DispositionNative == nil {
		t.Error("evidence missing native code")
	}
	if ev.AutoResolveSeconds == nil || *ev.AutoResolveSeconds != 240 {
		t.Errorf("auto_resolve_seconds = %v, want 240", ev.AutoResolveSeconds)
	}
	if ev.CloseDelaySeconds == nil || *ev.CloseDelaySeconds != 600 {
		t.Errorf("close_delay_seconds = %v, want 600", ev.CloseDelaySeconds)
	}
	if ev.AckDelaySeconds != nil {
		t.Error("ack_delay_seconds must be absent for never-acked fire")
	}
}

func TestClassifyIsDeterministic(t *testing.T) {
	c := cfg(t)
	f := fire(withResponse(model.ResponseRecord{
		Disposition: model.DispositionUnknown,
		ClosedAt:    ts(time.Hour),
	}))
	first := Classify(f, c)
	for range 100 {
		if got := Classify(f, c); !reflect.DeepEqual(got, first) {
			t.Fatal("classification not deterministic")
		}
	}
}
