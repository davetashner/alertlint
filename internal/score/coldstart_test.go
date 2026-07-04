package score

import (
	"strings"
	"testing"
	"time"

	"github.com/davetashner/alertlint/internal/adapter"
	"github.com/davetashner/alertlint/internal/model"
)

func coldCfg(t *testing.T) Config {
	t.Helper()
	c, err := DecodeConfig(strings.NewReader(validYAML))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func coldWindow() adapter.TimeWindow {
	end := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	return adapter.TimeWindow{Start: end.AddDate(0, 0, -90), End: end}
}

func alertCreated(daysBeforeWindowEnd int) model.AlertConfig {
	created := coldWindow().End.AddDate(0, 0, -daysBeforeWindowEnd)
	return model.AlertConfig{CreatedAt: &created}
}

func TestAlertState(t *testing.T) {
	c := coldCfg(t) // min_alert_age_days: 14, min_fires_to_score: 3
	w := coldWindow()
	cases := []struct {
		name              string
		alert             model.AlertConfig
		classified, total int
		want              AnalysisState
	}{
		{"young alert always insufficient (REQ-HIST-003)", alertCreated(5), 10, 12, StateInsufficientData},
		{"young alert with zero fires still insufficient, not dormant", alertCreated(5), 0, 0, StateInsufficientData},
		{"age exactly at boundary is mature", alertCreated(14), 5, 5, StateScoreable},
		{"mature, zero fires => dormant (REQ-HIST-002)", alertCreated(200), 0, 0, StateDormantHealthy},
		{"mature, fires but too few classified => insufficient", alertCreated(200), 2, 8, StateInsufficientData},
		{"mature, exactly min classified fires => scoreable", alertCreated(200), 3, 3, StateScoreable},
		{"unknown creation time with fires => scoreable", model.AlertConfig{}, 4, 4, StateScoreable},
		{"unknown creation time, zero fires => dormant", model.AlertConfig{}, 0, 0, StateDormantHealthy},
		{"silenced monitor with zero fires is still dormant-state (status carried separately)",
			func() model.AlertConfig { a := alertCreated(200); a.Status = model.StatusSilenced; return a }(),
			0, 0, StateDormantHealthy},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := AlertState(tc.alert, tc.classified, tc.total, w, c); got != tc.want {
				t.Errorf("AlertState = %q, want %q", got, tc.want)
			}
		})
	}
}

// State exclusivity property (spec testing section): over a sweep of
// inputs, every alert lands in exactly one state and the state set is
// closed.
func TestAlertStateExclusivityProperty(t *testing.T) {
	c := coldCfg(t)
	w := coldWindow()
	valid := map[AnalysisState]bool{StateScoreable: true, StateDormantHealthy: true, StateInsufficientData: true}
	for age := 0; age <= 30; age += 2 {
		for classified := 0; classified <= 6; classified++ {
			for extra := 0; extra <= 2; extra++ {
				got := AlertState(alertCreated(age), classified, classified+extra, w, c)
				if !valid[got] {
					t.Fatalf("age %d / classified %d: state %q outside closed set", age, classified, got)
				}
			}
		}
	}
}

func TestServiceState(t *testing.T) {
	cases := []struct {
		name   string
		states []AnalysisState
		want   AnalysisState
	}{
		{"any scoreable wins", []AnalysisState{StateDormantHealthy, StateInsufficientData, StateScoreable}, StateScoreable},
		{"all dormant => dormant", []AnalysisState{StateDormantHealthy, StateDormantHealthy}, StateDormantHealthy},
		{"all insufficient => insufficient", []AnalysisState{StateInsufficientData}, StateInsufficientData},
		{"dormant + insufficient mix, nothing scoreable => insufficient", []AnalysisState{StateDormantHealthy, StateInsufficientData}, StateInsufficientData},
		{"no alerts at all => insufficient", nil, StateInsufficientData},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ServiceState(tc.states); got != tc.want {
				t.Errorf("ServiceState = %q, want %q", got, tc.want)
			}
		})
	}
}
