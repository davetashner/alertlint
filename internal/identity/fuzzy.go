package identity

import (
	"sort"
	"strings"
)

// FuzzyConfig is the identity.fuzzy.* config surface
// (docs/specs/identity-resolution.md, strategy 4).
type FuzzyConfig struct {
	// MinScore is the similarity floor for emitting a candidate.
	MinScore float64
	// MaxCandidates caps the candidate list per artifact.
	MaxCandidates int
	// StopTokens are stripped during normalization (org-configurable).
	StopTokens []string
}

// DefaultFuzzyConfig returns the proposed starting parameters (spec open
// question notes they need tuning against a labeled extract).
func DefaultFuzzyConfig() FuzzyConfig {
	return FuzzyConfig{
		MinScore:      0.75,
		MaxCandidates: 3,
		StopTokens:    []string{"svc", "service", "prod", "production", "v1", "v2", "v3"},
	}
}

// Candidate is one fuzzy suggestion: a finding payload member, never a
// mapping. There is deliberately no code path from a Candidate into the
// mapping table — the only route is the confirmed-mappings file after
// explicit confirmation (ADR 0002, enforced structurally).
type Candidate struct {
	CIID  string  `json:"ci_id"`
	CI    string  `json:"ci_name"`
	Score float64 `json:"match_score"`
}

// Suggestion pairs an unresolved artifact with its candidate CIs.
type Suggestion struct {
	Artifact   Artifact
	Candidates []Candidate
}

// Suggest runs strategy 4 over the resolver's unresolved queue: normalize
// artifact hints and CI names/aliases, score by similarity, and emit the
// top candidates per artifact. Deterministic: candidates sort by score
// descending then ci_id; ties in normalization never depend on map order.
func Suggest(unresolved []Artifact, inv *Inventory, cfg FuzzyConfig) []Suggestion {
	type ciKey struct {
		id   string
		name string
		norm string
	}
	var keys []ciKey
	for _, ci := range inv.cis {
		if ci.Status != "operational" {
			continue
		}
		keys = append(keys, ciKey{ci.ID, ci.Name, normalize(ci.Name, cfg.StopTokens)})
		for _, alias := range ci.Aliases {
			keys = append(keys, ciKey{ci.ID, ci.Name, normalize(alias, cfg.StopTokens)})
		}
	}

	out := make([]Suggestion, 0, len(unresolved))
	for _, art := range unresolved {
		best := map[string]Candidate{} // ci id -> best-scoring candidate
		for _, hint := range hintStrings(art) {
			normHint := normalize(hint, cfg.StopTokens)
			if normHint == "" {
				continue
			}
			for _, k := range keys {
				if k.norm == "" {
					continue
				}
				score := similarity(normHint, k.norm)
				if score < cfg.MinScore {
					continue
				}
				if prev, ok := best[k.id]; !ok || score > prev.Score {
					best[k.id] = Candidate{CIID: k.id, CI: k.name, Score: score}
				}
			}
		}
		candidates := make([]Candidate, 0, len(best))
		for _, c := range best {
			candidates = append(candidates, c)
		}
		sort.Slice(candidates, func(i, j int) bool {
			if candidates[i].Score != candidates[j].Score {
				return candidates[i].Score > candidates[j].Score
			}
			return candidates[i].CIID < candidates[j].CIID
		})
		if cfg.MaxCandidates > 0 && len(candidates) > cfg.MaxCandidates {
			candidates = candidates[:cfg.MaxCandidates]
		}
		out = append(out, Suggestion{Artifact: art, Candidates: candidates})
	}
	return out
}

// hintStrings flattens an artifact's identity hints into scoreable strings.
func hintStrings(art Artifact) []string {
	var out []string
	out = append(out, art.Hints.Names...)
	tagKeys := make([]string, 0, len(art.Hints.Tags))
	for k := range art.Hints.Tags {
		tagKeys = append(tagKeys, k)
	}
	sort.Strings(tagKeys) // deterministic order (ADR 0005)
	for _, k := range tagKeys {
		if v := art.Hints.Tags[k]; v != "" {
			out = append(out, v)
		}
	}
	return out
}

// normalize lowercases, strips separators, and removes stop tokens.
func normalize(s string, stopTokens []string) string {
	s = strings.ToLower(s)
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return r == '-' || r == '_' || r == ' ' || r == '.' || r == '/' || r == ':'
	})
	var kept []string
	for _, f := range fields {
		stopped := false
		for _, tok := range stopTokens {
			if f == tok {
				stopped = true
				break
			}
		}
		if !stopped {
			kept = append(kept, f)
		}
	}
	return strings.Join(kept, "")
}

// similarity is 1 − levenshtein/maxlen: simple, deterministic, and
// dependency-free. The metric choice is a spec open question; swapping it
// is a config-surface change, not a contract change, because scores only
// ever appear inside finding candidates.
func similarity(a, b string) float64 {
	if a == b {
		return 1
	}
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	dist := levenshtein(a, b)
	max := len(a)
	if len(b) > max {
		max = len(b)
	}
	return 1 - float64(dist)/float64(max)
}

func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min3(cur[j-1]+1, prev[j]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}

func min3(a, b, c int) int {
	if b < a {
		a = b
	}
	if c < a {
		a = c
	}
	return a
}
