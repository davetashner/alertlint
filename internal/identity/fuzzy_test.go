package identity

import (
	"encoding/json"
	"testing"

	"github.com/davetashner/alertlint/internal/model"
)

func fuzzyInventory() *Inventory {
	return NewInventory([]CI{
		{ID: "CI1", Name: "payments-api", Status: "operational"},
		{ID: "CI2", Name: "checkout-api", Aliases: []string{"checkout"}, Status: "operational"},
		{ID: "CI3", Name: "payments-worker", Status: "operational"},
		{ID: "CI4", Name: "payments-api-old", Status: "retired"}, // never suggested
	})
}

func unresolvedArtifact(names []string, tags map[string]string) Artifact {
	if tags == nil {
		tags = map[string]string{}
	}
	return Artifact{
		Ref:       ArtifactRef{Source: "newrelic", Kind: "policy", Key: "998811"},
		DataClass: ClassConfig,
		Hints:     model.IdentityHints{Tags: tags, Names: names, ExternalRefs: []model.ExternalRef{}},
	}
}

func TestSuggestFindsCloseNames(t *testing.T) {
	cfg := DefaultFuzzyConfig()
	suggestions := Suggest([]Artifact{
		unresolvedArtifact([]string{"payment-api"}, nil), // 1 edit from payments-api: non-exact match
	}, fuzzyInventory(), cfg)
	if len(suggestions) != 1 {
		t.Fatalf("suggestions = %d", len(suggestions))
	}
	cands := suggestions[0].Candidates
	if len(cands) == 0 || cands[0].CIID != "CI1" {
		t.Fatalf("top candidate = %+v, want payments-api", cands)
	}
	if cands[0].Score < cfg.MinScore || cands[0].Score > 1 {
		t.Errorf("score = %v out of range", cands[0].Score)
	}
	for _, c := range cands {
		if c.CIID == "CI4" {
			t.Error("retired CIs must never be suggested")
		}
	}
}

func TestSuggestNormalization(t *testing.T) {
	cfg := DefaultFuzzyConfig()
	// Separators, case, and stop tokens vanish: "Checkout   API (prod)"
	// via tag value should match checkout-api exactly after
	// normalization (checkoutapi == checkoutapi).
	suggestions := Suggest([]Artifact{
		unresolvedArtifact(nil, map[string]string{"app": "Checkout_API-prod"}),
	}, fuzzyInventory(), cfg)
	cands := suggestions[0].Candidates
	if len(cands) == 0 || cands[0].CIID != "CI2" || cands[0].Score != 1 {
		t.Fatalf("candidates = %+v, want checkout-api at 1.0", cands)
	}
}

func TestSuggestRespectsFloorAndCap(t *testing.T) {
	cfg := DefaultFuzzyConfig()
	// Unrelated name: nothing clears the floor.
	suggestions := Suggest([]Artifact{
		unresolvedArtifact([]string{"inventory-batch-runner"}, nil),
	}, fuzzyInventory(), cfg)
	if len(suggestions[0].Candidates) != 0 {
		t.Errorf("unrelated artifact must have no candidates: %+v", suggestions[0].Candidates)
	}

	// Cap: lower the floor so all payments CIs match, cap at 1.
	cfg.MinScore = 0.3
	cfg.MaxCandidates = 1
	suggestions = Suggest([]Artifact{
		unresolvedArtifact([]string{"payments"}, nil),
	}, fuzzyInventory(), cfg)
	if len(suggestions[0].Candidates) != 1 {
		t.Errorf("cap not applied: %+v", suggestions[0].Candidates)
	}
}

// The structural invariant from ADR 0002: the fuzzy stage's output type
// carries no mapping fields — a Candidate cannot enter the join table.
func TestCandidateIsNotAMapping(t *testing.T) {
	buf, _ := json.Marshal(Candidate{CIID: "CI1", CI: "payments-api", Score: 0.9})
	var asMap map[string]any
	_ = json.Unmarshal(buf, &asMap)
	for _, forbidden := range []string{"method", "confidence", "data_class"} {
		if _, ok := asMap[forbidden]; ok {
			t.Errorf("Candidate must not carry mapping field %q", forbidden)
		}
	}
}

func TestSuggestDeterministic(t *testing.T) {
	cfg := DefaultFuzzyConfig()
	cfg.MinScore = 0.3
	art := unresolvedArtifact([]string{"payments"}, map[string]string{"team": "payments", "app": "payments-x"})
	first, _ := json.Marshal(Suggest([]Artifact{art}, fuzzyInventory(), cfg))
	for range 50 {
		again, _ := json.Marshal(Suggest([]Artifact{art}, fuzzyInventory(), cfg))
		if string(first) != string(again) {
			t.Fatal("Suggest not deterministic")
		}
	}
}
