package output

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// The aggregation layer is deliberately dumb (ADR 0001): it reads corpora
// of per-service documents, dedups, and sorts. It contains zero scoring
// logic — the priority score computed at analyze time is the one and only
// ranking key, identical whether the corpus is one team's or the fleet's
// (REQ-EXEC-003).

// WorklistEntry is one ranked (or unranked) service.
type WorklistEntry struct {
	CIID          string   `json:"ci_id"`
	CIName        string   `json:"ci_name"`
	PriorityScore *float64 `json:"priority_score"`
	Composite     *float64 `json:"composite"`
	Tier          int      `json:"criticality_tier"`
	Findings      int      `json:"findings"`
	RunTimestamp  string   `json:"run_timestamp"`
	SourceFile    string   `json:"source_file"`
}

// Worklist is the aggregation result.
type Worklist struct {
	// Ranked is sorted by priority_score descending; ties break on
	// composite ascending (worse quality first), then ci_id — fully
	// deterministic (ADR 0005).
	Ranked []WorklistEntry `json:"ranked"`
	// NotRanked lists services whose priority is null (all-dormant /
	// insufficient data) — surfaced separately, never sorted as zero
	// (REQ-HIST-004).
	NotRanked []WorklistEntry `json:"not_ranked"`
	// UnresolvedArtifacts counts findings carried by _unresolved.json
	// documents across the corpus — nothing drops silently (REQ-ID-003).
	UnresolvedArtifacts int `json:"unresolved_artifacts"`
	// Deduped counts documents discarded because a newer run of the same
	// CI was present.
	Deduped int `json:"deduped"`
}

// Aggregate reads every *.json document under the given directories,
// dedups on identity.ci.id (newest metadata.run.timestamp wins whole —
// documents are never field-merged), and ranks. Mixed contract majors are
// an error, not a silent skip.
type keptDoc struct {
	doc  Document
	file string
}

// loadNewest reads every document under dirs with newest-run-wins dedup
// per CI (documents are never field-merged) and mixed-major rejection —
// the shared loading semantics of worklist and diff.
func loadNewest(dirs []string) (map[string]keptDoc, int, int, error) {
	newest := map[string]keptDoc{}
	major := ""
	unresolved, deduped := 0, 0
	for _, dir := range dirs {
		paths, err := filepath.Glob(filepath.Join(dir, "*.json"))
		if err != nil {
			return nil, 0, 0, err
		}
		sort.Strings(paths)
		for _, path := range paths {
			raw, err := os.ReadFile(path)
			if err != nil {
				return nil, 0, 0, err
			}
			var doc Document
			if err := json.Unmarshal(raw, &doc); err != nil {
				return nil, 0, 0, fmt.Errorf("%s: %w", path, err)
			}
			m, _, _ := strings.Cut(doc.ContractVersion, ".")
			if major == "" {
				major = m
			} else if m != major {
				return nil, 0, 0, fmt.Errorf("%s: contract major %s mixed with %s — re-run analyze with a matching tool version", path, m, major)
			}

			if doc.Identity.CI == nil {
				unresolved += len(doc.Findings)
				continue
			}
			id := doc.Identity.CI.ID
			prev, seen := newest[id]
			if seen {
				deduped++
				// Newest run wins whole; ties keep the first (sorted
				// path order keeps this deterministic).
				if !doc.Metadata.Run.Timestamp.After(prev.doc.Metadata.Run.Timestamp) {
					continue
				}
			}
			newest[id] = keptDoc{doc: doc, file: path}
		}
	}
	return newest, unresolved, deduped, nil
}

func Aggregate(dirs []string) (Worklist, error) {
	var wl Worklist
	newest, unresolved, deduped, err := loadNewest(dirs)
	if err != nil {
		return wl, err
	}
	wl.UnresolvedArtifacts = unresolved
	wl.Deduped = deduped

	for _, k := range newest {
		entry := WorklistEntry{
			CIID:          k.doc.Identity.CI.ID,
			CIName:        k.doc.Identity.CI.Name,
			PriorityScore: k.doc.Scores.PriorityScore,
			Composite:     k.doc.Scores.Composite,
			Tier:          k.doc.Scores.CriticalityTier,
			Findings:      len(k.doc.Findings),
			RunTimestamp:  k.doc.Metadata.Run.Timestamp.Format("2006-01-02T15:04:05Z07:00"),
			SourceFile:    k.file,
		}
		if entry.PriorityScore == nil {
			wl.NotRanked = append(wl.NotRanked, entry)
		} else {
			wl.Ranked = append(wl.Ranked, entry)
		}
	}

	sort.Slice(wl.Ranked, func(i, j int) bool {
		a, b := wl.Ranked[i], wl.Ranked[j]
		if *a.PriorityScore != *b.PriorityScore {
			return *a.PriorityScore > *b.PriorityScore
		}
		if a.Composite != nil && b.Composite != nil && *a.Composite != *b.Composite {
			return *a.Composite < *b.Composite
		}
		return a.CIID < b.CIID
	})
	sort.Slice(wl.NotRanked, func(i, j int) bool { return wl.NotRanked[i].CIID < wl.NotRanked[j].CIID })
	return wl, nil
}
