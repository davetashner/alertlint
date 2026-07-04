package score

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/davetashner/alertlint/internal/adapter"
	"github.com/davetashner/alertlint/internal/archetype"
	"github.com/davetashner/alertlint/internal/model"
)

var updateGolden = flag.Bool("update", false, "rewrite golden files from current engine output")

// goldenInput is one service's full scoring input, loaded from
// testdata/golden/<case>.input.json. The snapshot-cache raw/canonical
// convention applies here in miniature: input is source of truth, the
// .golden.json is regenerable (-update) and reviewed like code.
type goldenInput struct {
	Tier   *int          `json:"criticality_tier"`
	Alerts []goldenAlert `json:"alerts"`
	// ArchetypeSignals summarizes archetype evaluation output (satisfied
	// flags per applicable archetype), decoupling these fixtures from the
	// library file's evolution.
	ArchetypeSignals []goldenArchetype `json:"archetype_signals"`
}

type goldenArchetype struct {
	Applies   bool   `json:"applies"`
	Satisfied []bool `json:"satisfied"`
}

type goldenAlert struct {
	ID                   string       `json:"id"`
	CreatedDaysBeforeEnd *int         `json:"created_days_before_end"` // nil = unknown age
	Fires                []goldenFire `json:"fires"`
}

type goldenFire struct {
	DayOffset       int     `json:"day_offset"` // fired_at = window.start + offset days
	HourOffset      int     `json:"hour_offset"`
	AckedAfterMin   *int    `json:"acked_after_min"`
	ClosedAfterMin  *int    `json:"closed_after_min"`
	AutoResolved    *bool   `json:"auto_resolved"`
	ResolveAfterMin *int    `json:"resolve_after_min"`
	Disposition     *string `json:"disposition"`
	Reassignments   int64   `json:"reassignments"`
	LinkedKind      *string `json:"linked_kind"`
}

// goldenOutput is the engine's complete verdict for the service.
type goldenOutput struct {
	ServiceState  AnalysisState            `json:"service_state"`
	TierKey       string                   `json:"tier_key"`
	TierSource    CriticalitySource        `json:"tier_source"`
	AlertStates   map[string]AnalysisState `json:"alert_states"`
	AlertNoise    []AlertNoise             `json:"alert_noise"`
	Threshold     []ThresholdFinding       `json:"threshold_findings"`
	Noise         SubScore                 `json:"noise"`
	Coverage      SubScore                 `json:"coverage"`
	ThresholdSub  SubScore                 `json:"threshold"`
	Composite     CompositeResult          `json:"composite"`
	PriorityScore *float64                 `json:"priority_score"`
}

// scoreService composes the full deterministic engine for one service —
// the same composition the analyze command will perform.
func scoreService(in goldenInput, window adapter.TimeWindow, cfg Config) goldenOutput {
	out := goldenOutput{AlertStates: map[string]AnalysisState{}}

	tierKey, tierSource, _ := ResolveTier(in.Tier, cfg)
	out.TierKey, out.TierSource = tierKey, tierSource

	var states []AnalysisState
	var scoreableAlerts int
	for _, ga := range in.Alerts {
		fires, classifications := materialize(ga, window, cfg)
		classified := 0
		for _, c := range classifications {
			if c.Class == ClassNoise || c.Class == ClassActionable || c.Class == ClassUnclear {
				classified++
			}
		}
		alertCfg := model.AlertConfig{}
		if ga.CreatedDaysBeforeEnd != nil {
			created := window.End.AddDate(0, 0, -*ga.CreatedDaysBeforeEnd)
			alertCfg.CreatedAt = &created
		}
		state := AlertState(alertCfg, classified, len(fires), window, cfg)
		out.AlertStates[ga.ID] = state
		states = append(states, state)
		if state != StateScoreable {
			continue
		}
		scoreableAlerts++
		out.AlertNoise = append(out.AlertNoise, NoiseForAlert(ga.ID, classifications, cfg))
		out.Threshold = append(out.Threshold,
			ThresholdForAlert(AlertThresholdInput{AlertID: ga.ID, Fires: fires, Classifications: classifications}, window, cfg)...)
	}
	out.ServiceState = ServiceState(states)
	if out.ServiceState != StateScoreable {
		return out
	}

	noiseVal := NoiseScore(out.AlertNoise, tierKey, window, cfg)
	out.Noise = SubScore{Value: noiseVal, Available: scoreableAlerts > 0}

	var archResults []archetype.Result
	for _, ga := range in.ArchetypeSignals {
		r := archetype.Result{Applies: ga.Applies}
		for i, s := range ga.Satisfied {
			r.Signals = append(r.Signals, archetype.SignalResult{SignalID: string(rune('a' + i)), Satisfied: s})
		}
		archResults = append(archResults, r)
	}
	covVal, covOK := CoverageScore(archResults)
	out.Coverage = SubScore{Value: covVal, Available: covOK}

	thVal, thOK := ThresholdScore(out.Threshold, scoreableAlerts)
	out.ThresholdSub = SubScore{Value: thVal, Available: thOK}

	out.Composite = Composite(CompositeInput{Noise: out.Noise, Coverage: out.Coverage, Threshold: out.ThresholdSub}, cfg)
	if out.Composite.Available {
		p := Priority(out.Composite.Composite, tierKey, cfg)
		out.PriorityScore = &p
	}
	return out
}

func materialize(ga goldenAlert, window adapter.TimeWindow, cfg Config) ([]Fire, []FireClassification) {
	var fires []Fire
	for _, gf := range ga.Fires {
		fired := window.Start.AddDate(0, 0, gf.DayOffset).Add(time.Duration(gf.HourOffset) * time.Hour)
		f := Fire{Event: model.AlertEvent{FiredAt: fired, OccurrenceCount: 1}}
		if gf.AutoResolved != nil {
			f.Event.AutoResolved = gf.AutoResolved
			if gf.ResolveAfterMin != nil {
				r := fired.Add(time.Duration(*gf.ResolveAfterMin) * time.Minute)
				f.Event.ResolvedAt = &r
			}
		}
		if gf.AckedAfterMin != nil || gf.ClosedAfterMin != nil || gf.Disposition != nil || gf.Reassignments > 0 || gf.LinkedKind != nil {
			resp := model.ResponseRecord{Disposition: model.DispositionUnknown, ReassignmentCount: gf.Reassignments, LinkedRecords: []model.LinkedRecord{}}
			if gf.AckedAfterMin != nil {
				a := fired.Add(time.Duration(*gf.AckedAfterMin) * time.Minute)
				resp.AckedAt = &a
			}
			if gf.ClosedAfterMin != nil {
				c := fired.Add(time.Duration(*gf.ClosedAfterMin) * time.Minute)
				resp.ClosedAt = &c
			}
			if gf.Disposition != nil {
				resp.Disposition = model.Disposition(*gf.Disposition)
			}
			if gf.LinkedKind != nil {
				resp.LinkedRecords = append(resp.LinkedRecords, model.LinkedRecord{Kind: model.LinkedRecordKind(*gf.LinkedKind), NativeID: "LNK1"})
			}
			f.Response = &resp
		}
		fires = append(fires, f)
	}
	classifications := make([]FireClassification, len(fires))
	for i, f := range fires {
		classifications[i] = Classify(f, cfg)
	}
	return fires, classifications
}

func TestGoldenFixtures(t *testing.T) {
	cfg, err := LoadConfigForTest()
	if err != nil {
		t.Fatal(err)
	}
	end := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	window := adapter.TimeWindow{Start: end.AddDate(0, 0, -90), End: end}

	inputs, err := filepath.Glob(filepath.Join("testdata", "golden", "*.input.json"))
	if err != nil || len(inputs) == 0 {
		t.Fatalf("no golden inputs found: %v", err)
	}
	for _, inputPath := range inputs {
		name := strings.TrimSuffix(filepath.Base(inputPath), ".input.json")
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(inputPath)
			if err != nil {
				t.Fatal(err)
			}
			var in goldenInput
			if err := json.Unmarshal(raw, &in); err != nil {
				t.Fatalf("parse %s: %v", inputPath, err)
			}
			got, err := json.MarshalIndent(scoreService(in, window, cfg), "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			got = append(got, '\n')

			goldenPath := filepath.Join("testdata", "golden", name+".golden.json")
			if *updateGolden {
				if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(goldenPath)
			if err != nil {
				t.Fatalf("missing golden file (run with -update): %v", err)
			}
			if string(want) != string(got) {
				t.Errorf("golden mismatch for %s — engine output changed.\nIf intentional, bump scoring_config_version and re-run with -update.\ngot:\n%s", name, got)
			}
		})
	}
}

// LoadConfigForTest loads the committed default scoring config.
func LoadConfigForTest() (Config, error) {
	return LoadConfig(filepath.Join("..", "..", "configs", "scoring.yaml"))
}

// Byte-identical repetition: the full engine, 25 runs, one fixture.
func TestEngineByteIdentical(t *testing.T) {
	cfg, err := LoadConfigForTest()
	if err != nil {
		t.Fatal(err)
	}
	end := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	window := adapter.TimeWindow{Start: end.AddDate(0, 0, -90), End: end}
	raw, err := os.ReadFile(filepath.Join("testdata", "golden", "kitchen-sink.input.json"))
	if err != nil {
		t.Fatal(err)
	}
	var in goldenInput
	if err := json.Unmarshal(raw, &in); err != nil {
		t.Fatal(err)
	}
	first, _ := json.Marshal(scoreService(in, window, cfg))
	for range 25 {
		again, _ := json.Marshal(scoreService(in, window, cfg))
		if string(first) != string(again) {
			t.Fatal("engine output not byte-identical across runs (map-order canary)")
		}
	}
}
