package output

import (
	"sort"
)

// Run-over-run comparison. Finding ids are deterministic content hashes
// of (type, subject, window) precisely so that diffs show real change,
// not churn — this is the consumer those ids were designed for.

// DiffResult compares two corpora ("old" → "new").
type DiffResult struct {
	// Services present in both corpora, with score movement. Sorted by
	// absolute priority delta descending, then ci_id.
	Changed []ServiceDiff `json:"changed"`
	// NewServices appear only in the new corpus; RemovedServices only in
	// the old. Sorted by ci_id.
	NewServices     []WorklistEntry `json:"new_services"`
	RemovedServices []WorklistEntry `json:"removed_services"`
}

// ServiceDiff is one service's movement between runs.
type ServiceDiff struct {
	CIID   string `json:"ci_id"`
	CIName string `json:"ci_name"`
	// Priority/Composite deltas are new − old; nil when either side has
	// a null score (dormant/insufficient runs are states, not zeros).
	PriorityDelta  *float64 `json:"priority_delta"`
	CompositeDelta *float64 `json:"composite_delta"`
	OldPriority    *float64 `json:"old_priority"`
	NewPriority    *float64 `json:"new_priority"`
	// RankMove is oldRank − newRank (positive = moved up the worklist);
	// zero when unranked in either run.
	RankMove int `json:"rank_move"`
	// Finding movement by content-hash id.
	NewFindings      []FindingRef `json:"new_findings"`
	ResolvedFindings []FindingRef `json:"resolved_findings"`
	Persisting       int          `json:"persisting"`
}

// FindingRef is the digest of a finding for diff output.
type FindingRef struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	Severity  string `json:"severity"`
	Rationale string `json:"rationale"`
}

// Diff aggregates both corpora (same dedup semantics as the worklist)
// and compares. Zero scoring logic: it reads what analyze wrote.
func Diff(oldDirs, newDirs []string) (DiffResult, error) {
	var res DiffResult
	oldWL, err := Aggregate(oldDirs)
	if err != nil {
		return res, err
	}
	newWL, err := Aggregate(newDirs)
	if err != nil {
		return res, err
	}

	oldKept, _, _, err := loadNewest(oldDirs)
	if err != nil {
		return res, err
	}
	newKept, _, _, err := loadNewest(newDirs)
	if err != nil {
		return res, err
	}
	oldDocs := map[string]Document{}
	for id, k := range oldKept {
		oldDocs[id] = k.doc
	}
	newDocs := map[string]Document{}
	for id, k := range newKept {
		newDocs[id] = k.doc
	}

	oldRank := rankIndex(oldWL)
	newRank := rankIndex(newWL)

	for ciID, newDoc := range newDocs {
		oldDoc, existed := oldDocs[ciID]
		if !existed {
			continue
		}
		sd := ServiceDiff{CIID: ciID, CIName: newDoc.Identity.CI.Name,
			OldPriority: oldDoc.Scores.PriorityScore, NewPriority: newDoc.Scores.PriorityScore}
		if oldDoc.Scores.PriorityScore != nil && newDoc.Scores.PriorityScore != nil {
			d := *newDoc.Scores.PriorityScore - *oldDoc.Scores.PriorityScore
			sd.PriorityDelta = &d
		}
		if oldDoc.Scores.Composite != nil && newDoc.Scores.Composite != nil {
			d := *newDoc.Scores.Composite - *oldDoc.Scores.Composite
			sd.CompositeDelta = &d
		}
		if or, ok1 := oldRank[ciID]; ok1 {
			if nr, ok2 := newRank[ciID]; ok2 {
				sd.RankMove = or - nr
			}
		}

		oldByID := map[string]Finding{}
		for _, f := range oldDoc.Findings {
			oldByID[f.ID] = f
		}
		newSeen := map[string]bool{}
		for _, f := range newDoc.Findings {
			newSeen[f.ID] = true
			if _, ok := oldByID[f.ID]; ok {
				sd.Persisting++
			} else {
				sd.NewFindings = append(sd.NewFindings, ref(f))
			}
		}
		for _, f := range oldDoc.Findings {
			if !newSeen[f.ID] {
				sd.ResolvedFindings = append(sd.ResolvedFindings, ref(f))
			}
		}
		sortRefs(sd.NewFindings)
		sortRefs(sd.ResolvedFindings)
		res.Changed = append(res.Changed, sd)
	}

	for ciID, doc := range newDocs {
		if _, existed := oldDocs[ciID]; !existed {
			res.NewServices = append(res.NewServices, entryFor(doc))
		}
	}
	for ciID, doc := range oldDocs {
		if _, exists := newDocs[ciID]; !exists {
			res.RemovedServices = append(res.RemovedServices, entryFor(doc))
		}
	}

	sort.Slice(res.Changed, func(i, j int) bool {
		ai, aj := absDelta(res.Changed[i]), absDelta(res.Changed[j])
		if ai != aj {
			return ai > aj
		}
		return res.Changed[i].CIID < res.Changed[j].CIID
	})
	sort.Slice(res.NewServices, func(i, j int) bool { return res.NewServices[i].CIID < res.NewServices[j].CIID })
	sort.Slice(res.RemovedServices, func(i, j int) bool { return res.RemovedServices[i].CIID < res.RemovedServices[j].CIID })
	return res, nil
}

func absDelta(sd ServiceDiff) float64 {
	if sd.PriorityDelta == nil {
		return 0
	}
	if *sd.PriorityDelta < 0 {
		return -*sd.PriorityDelta
	}
	return *sd.PriorityDelta
}

func ref(f Finding) FindingRef {
	return FindingRef{ID: f.ID, Type: f.Type, Severity: f.Severity, Rationale: f.Rationale}
}

func sortRefs(refs []FindingRef) {
	sort.Slice(refs, func(i, j int) bool { return refs[i].ID < refs[j].ID })
}

func rankIndex(wl Worklist) map[string]int {
	idx := make(map[string]int, len(wl.Ranked))
	for i, e := range wl.Ranked {
		idx[e.CIID] = i + 1
	}
	return idx
}

func entryFor(doc Document) WorklistEntry {
	return WorklistEntry{
		CIID:          doc.Identity.CI.ID,
		CIName:        doc.Identity.CI.Name,
		PriorityScore: doc.Scores.PriorityScore,
		Composite:     doc.Scores.Composite,
		Tier:          doc.Scores.CriticalityTier,
		Findings:      len(doc.Findings),
	}
}
