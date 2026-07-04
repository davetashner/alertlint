package score

import (
	"time"

	"github.com/davetashner/alertlint/internal/adapter"
	"github.com/davetashner/alertlint/internal/model"
)

// AnalysisState gates an alert (or a whole service) before any scoring
// (docs/specs/scoring-engine.md, "Cold-start states"; REQ-HIST-002..004).
// These are distinct output values, never conflated with a healthy — or
// any — score.
type AnalysisState string

const (
	// StateScoreable: participates in all applicable sub-scores.
	StateScoreable AnalysisState = "scoreable"
	// StateDormantHealthy: config exists, mature, zero fires in window.
	// Not penalized, excluded from Noise/Threshold, surfaced explicitly so
	// a dead or silenced monitor cannot hide (REQ-HIST-002). Still counts
	// toward Coverage (its signal is present).
	StateDormantHealthy AnalysisState = "dormant_healthy"
	// StateInsufficientData: too new or too few classified fires to score
	// on honest evidence (REQ-HIST-003).
	StateInsufficientData AnalysisState = "insufficient_data"
)

// AlertState gates one alert. classifiedFires is the number of fires the
// decision table classified into a class other than unclassified;
// totalFires is all fires joined to the alert in the window.
//
// A nil CreatedAt (vendor does not expose creation time) is treated as
// mature: an alert of unknown age with real firing history should be
// scored, not hidden, and one with zero fires reads as dormant rather
// than insufficient — the less alarming, still-explicit state.
func AlertState(alert model.AlertConfig, classifiedFires, totalFires int, window adapter.TimeWindow, cfg Config) AnalysisState {
	minAge := time.Duration(cfg.ColdStart.MinAlertAgeDays) * 24 * time.Hour
	young := alert.CreatedAt != nil && alert.CreatedAt.After(window.End.Add(-minAge))

	if young {
		return StateInsufficientData // REQ-HIST-003: never scored on thin evidence
	}
	if totalFires == 0 {
		return StateDormantHealthy
	}
	if classifiedFires < cfg.ColdStart.MinFiresToScore {
		return StateInsufficientData
	}
	return StateScoreable
}

// ServiceState rolls alert states up to the service level. Any scoreable
// alert makes the service scoreable; all-dormant is dormant; otherwise
// (all insufficient, or a dormant/insufficient mix with nothing scoreable)
// the service is insufficient_data and no composite/priority is computed —
// the output carries the state instead of a score (REQ-HIST-004).
func ServiceState(states []AnalysisState) AnalysisState {
	if len(states) == 0 {
		return StateInsufficientData
	}
	allDormant := true
	for _, s := range states {
		switch s {
		case StateScoreable:
			return StateScoreable
		case StateDormantHealthy:
			// still possibly all-dormant
		default:
			allDormant = false
		}
	}
	if allDormant {
		return StateDormantHealthy
	}
	return StateInsufficientData
}
