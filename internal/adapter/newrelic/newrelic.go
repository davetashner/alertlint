// Package newrelic implements ConfigProvider over the New Relic REST v2
// alerts API (docs/specs/provider-adapters.md §7): alert policies plus
// their NRQL conditions, one AlertConfig per condition.
//
// v2 conditions carry no entity tags (that requires NerdGraph, a later
// enhancement), so identity hints are name candidates only: the policy
// name and the condition name, verbatim.
package newrelic

import (
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/url"
	"sort"
	"strconv"

	"github.com/davetashner/alertlint/internal/adapter"
	"github.com/davetashner/alertlint/internal/model"
)

const providerID = "newrelic"

// PageRecorder receives every verbatim response page, in pull order.
type PageRecorder interface {
	RecordPage(body []byte) error
}

// Adapter pulls alert policies and NRQL conditions. Inject Transport to
// replay recorded fixture pages.
type Adapter struct {
	BaseURL   string // default https://api.newrelic.com
	APIKey    string
	Transport http.RoundTripper
	Recorder  PageRecorder
}

// ProviderID implements adapter.Provider.
func (a *Adapter) ProviderID() string { return providerID }

// SchemaVersion implements adapter.Provider.
func (a *Adapter) SchemaVersion() string { return model.CanonicalSchemaVersion }

type nrPolicy struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type nrPoliciesPage struct {
	Policies []nrPolicy `json:"policies"`
}

type nrTerm struct {
	Threshold    string `json:"threshold"`
	Duration     string `json:"duration"` // minutes
	Operator     string `json:"operator"` // above | below | equal
	Priority     string `json:"priority"` // critical | warning
	TimeFunction string `json:"time_function"`
}

type nrCondition struct {
	ID      int64    `json:"id"`
	Name    string   `json:"name"`
	Enabled bool     `json:"enabled"`
	Terms   []nrTerm `json:"terms"`
	Nrql    struct {
		Query string `json:"query"`
	} `json:"nrql"`
}

type nrConditionsPage struct {
	NrqlConditions []nrCondition `json:"nrql_conditions"`
}

// FetchConfigs implements adapter.ConfigProvider: policies are pulled
// first, then each policy's NRQL conditions, in policy-id order.
func (a *Adapter) FetchConfigs(scope adapter.Scope, _ adapter.TimeWindow) iter.Seq2[model.AlertConfig, error] {
	return func(yield func(model.AlertConfig, error) bool) {
		policies, err := a.pullPolicies()
		if err != nil {
			var zero model.AlertConfig
			yield(zero, err)
			return
		}
		for _, policy := range policies {
			conditions, err := a.pullConditions(policy.ID)
			if err != nil {
				var zero model.AlertConfig
				yield(zero, err)
				return
			}
			for _, cond := range conditions {
				if !yield(a.toConfig(policy, cond, scope), nil) {
					return
				}
			}
		}
	}
}

func (a *Adapter) toConfig(policy nrPolicy, cond nrCondition, scope adapter.Scope) model.AlertConfig {
	native := strconv.FormatInt(cond.ID, 10)
	cfg := model.AlertConfig{
		Envelope: model.Envelope{
			SchemaVersion: model.CanonicalSchemaVersion,
			Source:        model.Source{Provider: providerID, Tenant: scope.Tenant},
			SourceRef:     model.SourceRef{Kind: "nrql_condition", NativeID: native},
			IdentityHints: model.IdentityHints{
				Tags:         map[string]string{},
				Names:        []string{policy.Name, cond.Name},
				ExternalRefs: []model.ExternalRef{},
			},
		},
		Name:         cond.Name,
		ConditionRaw: cond.Nrql.Query,
		Routing:      []model.Route{},
		Status:       model.StatusEnabled,
		Severity:     model.Severity{Native: "", Normalized: model.SeverityUnknown},
	}
	if !cond.Enabled {
		cfg.Status = model.StatusDisabled
	}

	// Best-effort extraction from the critical term (falling back to the
	// first term): absent means not parseable, never a guess.
	term := pickTerm(cond.Terms)
	if term != nil {
		cfg.Severity = severityFromPriority(term.Priority)
		if v, err := strconv.ParseFloat(term.Threshold, 64); err == nil {
			cfg.Threshold = &v
		}
		if comp := comparatorFromOperator(term.Operator); comp != "" {
			c := comp
			cfg.Comparator = &c
		}
		if mins, err := strconv.ParseInt(term.Duration, 10, 64); err == nil {
			secs := mins * 60
			cfg.DurationS = &secs
		}
	}
	return cfg
}

func pickTerm(terms []nrTerm) *nrTerm {
	for i := range terms {
		if terms[i].Priority == "critical" {
			return &terms[i]
		}
	}
	if len(terms) > 0 {
		return &terms[0]
	}
	return nil
}

func severityFromPriority(priority string) model.Severity {
	s := model.Severity{Native: priority}
	switch priority {
	case "critical":
		s.Normalized = model.SeverityCritical
	case "warning":
		s.Normalized = model.SeverityMedium
	default:
		s.Normalized = model.SeverityUnknown
	}
	return s
}

// comparatorFromOperator maps NR operators onto the canonical comparator
// enum. NR thresholds trigger at the boundary, so above/below map to the
// inclusive forms.
func comparatorFromOperator(op string) string {
	switch op {
	case "above":
		return ">="
	case "below":
		return "<="
	case "equal":
		return "=="
	}
	return ""
}

func (a *Adapter) pullPolicies() ([]nrPolicy, error) {
	var out []nrPolicy
	for page := 1; ; page++ {
		var pg nrPoliciesPage
		q := url.Values{"page": {strconv.Itoa(page)}}
		if err := a.get("/v2/alerts_policies.json", q, &pg); err != nil {
			return nil, err
		}
		out = append(out, pg.Policies...)
		if len(pg.Policies) == 0 {
			break
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (a *Adapter) pullConditions(policyID int64) ([]nrCondition, error) {
	var out []nrCondition
	for page := 1; ; page++ {
		var pg nrConditionsPage
		q := url.Values{
			"policy_id": {strconv.FormatInt(policyID, 10)},
			"page":      {strconv.Itoa(page)},
		}
		if err := a.get("/v2/alerts_nrql_conditions.json", q, &pg); err != nil {
			return nil, err
		}
		out = append(out, pg.NrqlConditions...)
		if len(pg.NrqlConditions) == 0 {
			break
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (a *Adapter) get(path string, q url.Values, into any) error {
	base := a.BaseURL
	if base == "" {
		base = "https://api.newrelic.com"
	}
	req, err := http.NewRequest(http.MethodGet, base+path+"?"+q.Encode(), nil)
	if err != nil {
		return fmt.Errorf("newrelic %s: %w", path, err)
	}
	req.Header.Set("Api-Key", a.APIKey)

	transport := a.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	resp, err := adapter.DoWithRetry(transport, req)
	if err != nil {
		return fmt.Errorf("newrelic %s: %w", path, err)
	}
	defer resp.Body.Close() //nolint:errcheck // read-only close
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("newrelic %s: read body: %w", path, err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("newrelic %s: HTTP %d: %.200s", path, resp.StatusCode, body)
	}
	if a.Recorder != nil {
		if err := a.Recorder.RecordPage(body); err != nil {
			return fmt.Errorf("newrelic %s: record page: %w", path, err)
		}
	}
	if err := json.Unmarshal(body, into); err != nil {
		return fmt.Errorf("newrelic %s: decode: %w", path, err)
	}
	return nil
}
