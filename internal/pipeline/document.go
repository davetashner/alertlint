package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"

	"github.com/davetashner/alertlint/internal/archetype"
	"github.com/davetashner/alertlint/internal/identity"
	"github.com/davetashner/alertlint/internal/model"
	"github.com/davetashner/alertlint/internal/output"
	"github.com/davetashner/alertlint/internal/score"
)

// serviceJoin groups one CI's canonical records for scoring.
type serviceJoin struct {
	configs map[identity.ArtifactRef]model.AlertConfig
	fires   map[string][]score.Fire // alert id (config native id or UnjoinedAlertID) -> fires
}

func buildDocument(opts Options, ci identity.CI, mappings []identity.Mapping, pull pulled, resolved identity.Result, suggested map[identity.DataClass]int) output.Document {
	join := joinService(mappings, pull)
	tierKey, tierSource, tierFinding := score.ResolveTier(ci.CriticalityTier, opts.Config)

	// Per-alert classification, states, and scoring — the same
	// composition proven by the golden suite.
	type alertVerdict struct {
		id              string
		state           score.AnalysisState
		classifications []score.FireClassification
		fires           []score.Fire
	}
	alertIDs := make([]string, 0, len(join.configs)+1)
	cfgByID := map[string]model.AlertConfig{}
	for ref, cfg := range join.configs {
		alertIDs = append(alertIDs, ref.Key)
		cfgByID[ref.Key] = cfg
	}
	if len(join.fires[UnjoinedAlertID]) > 0 {
		alertIDs = append(alertIDs, UnjoinedAlertID)
	}
	sort.Strings(alertIDs)

	var verdicts []alertVerdict
	var states []score.AnalysisState
	scoreable := 0
	var alertNoise []score.AlertNoise
	var thresholdFindings []score.ThresholdFinding
	inputs := output.ScoreInputs{}

	for _, id := range alertIDs {
		fires := join.fires[id]
		if opts.Config.ExcludeMaintenance() {
			var active []score.Fire
			for _, f := range fires {
				if fireInMaintenance(f, pull.maintenance) {
					inputs.FiresInMaintenance++
					continue
				}
				active = append(active, f)
			}
			fires = active
		}
		classifications := make([]score.FireClassification, len(fires))
		classified := 0
		for i, f := range fires {
			classifications[i] = score.Classify(f, opts.Config)
			if classifications[i].Class != score.ClassUnclassified {
				classified++
			}
		}
		alertCfg, hasCfg := cfgByID[id]
		var state score.AnalysisState
		if hasCfg {
			state = score.AlertState(alertCfg, classified, len(fires), opts.Window, opts.Config)
		} else {
			// Pseudo-alert for unjoined fires: real burden, no config to
			// gate on; scoreable when it has enough classified evidence.
			state = score.AlertState(model.AlertConfig{}, classified, len(fires), opts.Window, opts.Config)
		}
		verdicts = append(verdicts, alertVerdict{id: id, state: state, classifications: classifications, fires: fires})
		states = append(states, state)
		switch state {
		case score.StateScoreable:
			inputs.AlertsScored++
		case score.StateDormantHealthy:
			inputs.AlertsDormant++
		case score.StateInsufficientData:
			inputs.AlertsInsufficientData++
		}
		if state != score.StateScoreable {
			continue
		}
		scoreable++
		for _, f := range fires {
			if opts.Config.OffHours.IsOffHours(f.Event.FiredAt) {
				inputs.FiresOffHours++
			}
		}
		alertNoise = append(alertNoise, score.NoiseForAlert(id, classifications, opts.Config))
		thresholdFindings = append(thresholdFindings,
			score.ThresholdForAlert(score.AlertThresholdInput{AlertID: id, Fires: fires, Classifications: classifications}, opts.Window, opts.Config)...)
	}

	// Archetype evaluation over this service's telemetry inventory.
	archInventory := archetypeInventory(join)
	archResults := archetype.Evaluate(opts.Library, archInventory, overridesFor(opts.Overrides, ci.ID))

	// Coverage from mapping.
	coverage := identity.CoverageFor(ci.ID, resolved.Mappings, suggested)

	doc := output.Document{
		ContractVersion: output.ContractVersion,
		Identity:        buildIdentity(ci, tierKey, tierSource, mappings, coverage),
		Findings:        []output.Finding{},
	}
	window := output.Window{Start: opts.Window.Start, End: opts.Window.End, Days: int(opts.Window.End.Sub(opts.Window.Start).Hours() / 24)}

	// Scores block.
	serviceState := score.ServiceState(states)
	doc.Scores.CriticalityTier = tierNumber(tierKey)
	doc.Scores.Inputs = inputs
	if serviceState == score.StateScoreable {
		noiseVal := score.NoiseScore(alertNoise, tierKey, opts.Window, opts.Config)
		noiseSub := score.SubScore{Value: noiseVal, Available: scoreable > 0}
		covVal, covOK := score.CoverageScore(archResults)
		covSub := score.SubScore{Value: covVal, Available: covOK}
		thVal, thOK := score.ThresholdScore(thresholdFindings, scoreable)
		thSub := score.SubScore{Value: thVal, Available: thOK}
		composite := score.Composite(score.CompositeInput{Noise: noiseSub, Coverage: covSub, Threshold: thSub}, opts.Config)

		doc.Scores.Noise = availPtr(noiseSub)
		doc.Scores.Coverage = availPtr(covSub)
		doc.Scores.Threshold = availPtr(thSub)
		if composite.Available {
			c := composite.Composite
			doc.Scores.Composite = &c
			p := score.Priority(c, tierKey, opts.Config)
			doc.Scores.PriorityScore = &p
		}
	}

	// Findings assembly.
	fa := findingAssembler{window: window, cfg: opts.Config}
	for _, v := range verdicts {
		if v.state != score.StateScoreable {
			continue
		}
		for _, an := range alertNoise {
			if an.AlertID == v.id && an.Finding != nil {
				doc.Findings = append(doc.Findings, fa.noise(*an.Finding, v.fires, v.classifications, cfgByID[v.id]))
			}
		}
	}
	for _, tf := range thresholdFindings {
		doc.Findings = append(doc.Findings, fa.threshold(tf, cfgByID[tf.AlertID]))
	}
	for _, ar := range archResults {
		if ar.Suppressed {
			doc.Findings = append(doc.Findings, fa.suppressedArchetype(ar))
			continue
		}
		if !ar.Applies {
			continue
		}
		for _, sig := range ar.Signals {
			if !sig.Satisfied {
				doc.Findings = append(doc.Findings, fa.coverage(ar, sig, coverage.Partial))
			}
		}
	}
	if tierFinding {
		doc.Findings = append(doc.Findings, fa.missingCriticality(ci))
	}
	sortFindings(doc.Findings)

	doc.Metadata = buildMetadata(opts, window, pull)
	return doc
}

func buildIdentity(ci identity.CI, tierKey string, tierSource score.CriticalitySource, mappings []identity.Mapping, coverage identity.Coverage) output.Identity {
	id := output.Identity{
		CI: &output.CIBlock{
			ID:                ci.ID,
			Name:              ci.Name,
			CriticalityTier:   tierNumber(tierKey),
			CriticalitySource: string(tierSource),
		},
		Artifacts: []output.Artifact{},
	}
	sorted := append([]identity.Mapping(nil), mappings...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Artifact.Source != sorted[j].Artifact.Source {
			return sorted[i].Artifact.Source < sorted[j].Artifact.Source
		}
		return sorted[i].Artifact.Key < sorted[j].Artifact.Key
	})
	bySource := map[string]int{}
	for _, m := range sorted {
		bySource[m.Artifact.Source]++
		art := output.Artifact{
			Source:   m.Artifact.Source,
			Kind:     kindLabel(m.DataClass),
			NativeID: m.Artifact.Kind + ":" + m.Artifact.Key,
			Resolution: output.Resolution{
				Method:     m.Method,
				Confidence: confidenceBandFor(m.Method),
			},
		}
		if m.Evidence.RuleID != "" {
			rule := m.Evidence.RuleID
			art.Resolution.Rule = &rule
		}
		id.Artifacts = append(id.Artifacts, art)
	}
	note := "full"
	if coverage.Partial {
		note = "partial"
	}
	id.Mapping = output.Mapping{
		ResolvedCount:  len(sorted),
		CandidateCount: coverage.Suggested[identity.ClassConfig] + coverage.Suggested[identity.ClassHistory] + coverage.Suggested[identity.ClassAction],
		CoverageNote:   note,
		BySource:       bySource,
	}
	return id
}

func buildUnresolvedDocument(opts Options, resolved identity.Result, suggestions []identity.Suggestion) output.Document {
	doc := output.Document{
		ContractVersion: output.ContractVersion,
		Identity: output.Identity{
			CI:        nil,
			Artifacts: []output.Artifact{},
			Mapping:   output.Mapping{CoverageNote: "partial", BySource: map[string]int{}},
		},
		Findings: []output.Finding{},
	}
	window := output.Window{Start: opts.Window.Start, End: opts.Window.End, Days: int(opts.Window.End.Sub(opts.Window.Start).Hours() / 24)}
	fa := findingAssembler{window: window, cfg: opts.Config}
	for _, sg := range suggestions {
		doc.Identity.Mapping.BySource[sg.Artifact.Ref.Source]++
		doc.Findings = append(doc.Findings, fa.unresolvedArtifact(sg.Artifact, sg.Candidates))
	}
	for _, f := range resolved.Findings {
		doc.Findings = append(doc.Findings, fa.identityFinding(f))
	}
	sortFindings(doc.Findings)
	doc.Metadata = buildMetadata(opts, window, pulled{})
	return doc
}

func buildMetadata(opts Options, window output.Window, pull pulled) output.Metadata {
	sources := pull.sources
	if sources == nil {
		sources = []output.SourceMeta{}
	}
	return output.Metadata{
		Run:    opts.RunMeta,
		Window: window,
		Config: output.ConfigBlock{
			Weights: output.WeightsBlock{
				Noise:     opts.Config.Weights.Noise,
				Coverage:  opts.Config.Weights.Coverage,
				Threshold: opts.Config.Weights.Threshold,
			},
			PriorityFormulaVersion: "1",
			ConfigHash:             configHash(opts.Config),
		},
		ArchetypeLibraryVersion:  opts.Library.LibraryVersion,
		ConventionRulesetVersion: conventionVersion(opts.Convention),
		Sources:                  sources,
	}
}

// --- small helpers ---

// fireInMaintenance reports whether the fire started inside any declared
// maintenance window that covers its monitor (scope-wide windows cover
// everything). REQ-NOISE-005: suppressed fires stay visible via
// scores.inputs.fires_in_maintenance.
func fireInMaintenance(f score.Fire, windows []model.MaintenanceWindow) bool {
	t := f.Event.FiredAt
	for _, w := range windows {
		if t.Before(w.StartsAt) {
			continue
		}
		if w.EndsAt != nil && !t.Before(*w.EndsAt) {
			continue
		}
		if len(w.MonitorRefs) == 0 {
			return true // scope-wide
		}
		if f.Event.AlertRef.Provider == nil || f.Event.AlertRef.NativeID == nil {
			continue
		}
		for _, ref := range w.MonitorRefs {
			if ref.Provider == *f.Event.AlertRef.Provider && ref.NativeID == *f.Event.AlertRef.NativeID {
				return true
			}
		}
	}
	return false
}

func joinService(mappings []identity.Mapping, pull pulled) serviceJoin {
	join := serviceJoin{configs: map[identity.ArtifactRef]model.AlertConfig{}, fires: map[string][]score.Fire{}}

	// Configs first, so events can join to them.
	for _, m := range mappings {
		if m.DataClass == identity.ClassConfig {
			if cfg, ok := pull.configs[m.Artifact]; ok {
				join.configs[m.Artifact] = cfg
			}
		}
	}
	// Index responses by (source, native id) for the event join.
	respByRef := map[string]model.ResponseRecord{}
	for ref, rec := range pull.responses {
		respByRef[ref.Source+"\x00"+ref.Key] = rec
		if rec.EventRef.NativeID != nil {
			prov := ref.Source
			if rec.EventRef.Provider != nil {
				prov = *rec.EventRef.Provider
			}
			respByRef[prov+"\x00"+*rec.EventRef.NativeID] = rec
		}
	}
	// Events attributed to this service join to a config by alert_ref,
	// else land on the unjoined pseudo-alert.
	for _, m := range mappings {
		if m.DataClass != identity.ClassHistory {
			continue
		}
		ev, ok := pull.events[m.Artifact]
		if !ok {
			continue
		}
		fire := score.Fire{Event: ev}
		if rec, ok := respByRef[m.Artifact.Source+"\x00"+m.Artifact.Key]; ok {
			r := rec
			fire.Response = &r
		}
		alertID := UnjoinedAlertID
		if ev.AlertRef.NativeID != nil {
			for ref := range join.configs {
				if ref.Key == *ev.AlertRef.NativeID {
					alertID = ref.Key
					break
				}
			}
		}
		if alertID == UnjoinedAlertID && ev.AlertRef.Name != nil {
			for ref, cfg := range join.configs {
				if cfg.Name == *ev.AlertRef.Name {
					alertID = ref.Key
					break
				}
			}
		}
		join.fires[alertID] = append(join.fires[alertID], fire)
	}
	// Deterministic fire order within each alert.
	for id := range join.fires {
		fires := join.fires[id]
		sort.Slice(fires, func(i, j int) bool {
			if !fires[i].Event.FiredAt.Equal(fires[j].Event.FiredAt) {
				return fires[i].Event.FiredAt.Before(fires[j].Event.FiredAt)
			}
			return fires[i].Event.SourceRef.NativeID < fires[j].Event.SourceRef.NativeID
		})
		join.fires[id] = fires
	}
	return join
}

func archetypeInventory(join serviceJoin) []archetype.Artifact {
	refs := make([]identity.ArtifactRef, 0, len(join.configs))
	for ref := range join.configs {
		refs = append(refs, ref)
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].Key < refs[j].Key })
	out := make([]archetype.Artifact, 0, len(refs))
	for _, ref := range refs {
		cfg := join.configs[ref]
		out = append(out, archetype.Artifact{
			ID:         ref.Key,
			MetricRefs: []string{cfg.ConditionRaw, cfg.Name},
			Tags:       cfg.IdentityHints.Tags,
		})
	}
	return out
}

func overridesFor(all []archetype.Override, ciID string) []archetype.Override {
	var out []archetype.Override
	for _, o := range all {
		if o.CI == "" || o.CI == ciID {
			out = append(out, o)
		}
	}
	return out
}

func availPtr(s score.SubScore) *float64 {
	if !s.Available {
		return nil
	}
	v := s.Value
	return &v
}

func tierNumber(tierKey string) int {
	if len(tierKey) > 4 {
		if n := int(tierKey[4] - '0'); n >= 0 && n <= 9 {
			return n
		}
	}
	return 0
}

func kindLabel(c identity.DataClass) string {
	switch c {
	case identity.ClassConfig:
		return "alert_config"
	case identity.ClassHistory:
		return "alert_event"
	default:
		return "response_record"
	}
}

func confidenceBandFor(method string) string {
	if method == "convention" {
		return "medium"
	}
	return "high"
}

func conventionVersion(c *identity.Conventions) string {
	if c == nil {
		return "none"
	}
	return c.ContentHash()[:12]
}

func configHash(cfg score.Config) string {
	buf, _ := json.Marshal(cfg)
	sum := sha256Hex(buf)
	return "sha256:" + sum[:12]
}

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
