package identity

// MethodCounts tallies resolved artifacts by method for one data class.
type MethodCounts struct {
	Exact      int `json:"exact"`
	Confirmed  int `json:"confirmed"`
	Convention int `json:"convention"`
}

func (m MethodCounts) total() int { return m.Exact + m.Confirmed + m.Convention }

// Coverage is the per-service mapping coverage block
// (docs/specs/identity-resolution.md, "Per-service mapping coverage").
type Coverage struct {
	Resolved  map[DataClass]MethodCounts `json:"resolved"`
	Suggested map[DataClass]int          `json:"suggested"`
	// PerClass coverage = resolved / (resolved + suggested), per class.
	// A class with no artifacts at all reports 1.0 — nothing was missed.
	PerClass map[DataClass]float64 `json:"coverage"`
	Overall  float64               `json:"overall"`
	// MinConfidence is the weakest mapping confidence feeding this
	// service's joins: a service joined entirely by convention shows 0.8,
	// never 1.0.
	MinConfidence float64 `json:"min_confidence"`
	// Partial is true when any per-class coverage < 1.0; the scoring
	// stage must surface it as partial_mapping (ADR 0002).
	Partial bool `json:"partial"`
}

// CoverageFor computes the coverage block for one CI from the run's
// mappings plus the fuzzy-suggestion counts pointing at that CI (the
// visible "probably yours but unjoined" set; zero until the fuzzy stage
// feeds it).
func CoverageFor(ciID string, mappings []Mapping, suggested map[DataClass]int) Coverage {
	cov := Coverage{
		Resolved:  map[DataClass]MethodCounts{},
		Suggested: map[DataClass]int{},
		PerClass:  map[DataClass]float64{},
	}
	classes := []DataClass{ClassConfig, ClassHistory, ClassAction}
	for _, c := range classes {
		cov.Resolved[c] = MethodCounts{}
		cov.Suggested[c] = suggested[c]
	}

	minConf := 0.0
	first := true
	for _, m := range mappings {
		if m.CIID != ciID {
			continue
		}
		mc := cov.Resolved[m.DataClass]
		switch m.Method {
		case "exact":
			mc.Exact++
		case "confirmed":
			mc.Confirmed++
		case "convention":
			mc.Convention++
		}
		cov.Resolved[m.DataClass] = mc
		if first || m.Confidence < minConf {
			minConf = m.Confidence
			first = false
		}
	}
	cov.MinConfidence = minConf

	var resolvedTotal, denomTotal int
	for _, c := range classes {
		resolved := cov.Resolved[c].total()
		denom := resolved + cov.Suggested[c]
		if denom == 0 {
			cov.PerClass[c] = 1.0
			continue
		}
		cov.PerClass[c] = float64(resolved) / float64(denom)
		resolvedTotal += resolved
		denomTotal += denom
		if cov.PerClass[c] < 1.0 {
			cov.Partial = true
		}
	}
	if denomTotal == 0 {
		cov.Overall = 1.0
	} else {
		cov.Overall = float64(resolvedTotal) / float64(denomTotal)
	}
	return cov
}
