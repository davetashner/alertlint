// Package splunk implements ConfigProvider over the Splunk REST API's
// saved-search endpoint (docs/specs/provider-adapters.md §7): one
// AlertConfig per saved search that is an alert (has an alert_type or
// alert actions).
//
// SPL parse depth is deliberately shallow (spec open question): the SPL
// stays verbatim in condition_raw; threshold/comparator come from the
// saved search's alert_threshold/alert_comparator fields when Splunk
// carries them, never from parsing SPL.
package splunk

import (
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/davetashner/alertlint/internal/adapter"
	"github.com/davetashner/alertlint/internal/model"
)

const (
	providerID = "splunk"
	pageCount  = 100
)

// PageRecorder receives every verbatim response page, in pull order.
type PageRecorder interface {
	RecordPage(body []byte) error
}

// Adapter pulls saved searches. Inject Transport to replay fixtures.
type Adapter struct {
	BaseURL   string // e.g. https://splunk.example.com:8089
	Token     string // bearer token
	Transport http.RoundTripper
	Recorder  PageRecorder
}

// ProviderID implements adapter.Provider.
func (a *Adapter) ProviderID() string { return providerID }

// SchemaVersion implements adapter.Provider.
func (a *Adapter) SchemaVersion() string { return model.CanonicalSchemaVersion }

type spSearch struct {
	Name    string `json:"name"`
	Content struct {
		Search          string `json:"search"`
		Disabled        bool   `json:"disabled"`
		AlertType       string `json:"alert_type"`       // "number of events" | ... | "always"
		AlertComparator string `json:"alert_comparator"` // "greater than" | "less than" | "equal to" | ...
		AlertThreshold  string `json:"alert_threshold"`
		AlertSeverity   int    `json:"alert.severity"` // 1..6 per Splunk docs
		Actions         string `json:"actions"`        // comma-separated action names
		CronSchedule    string `json:"cron_schedule"`
	} `json:"content"`
	ACL struct {
		App string `json:"app"`
	} `json:"acl"`
}

type spPage struct {
	Entry []spSearch `json:"entry"`
}

// FetchConfigs implements adapter.ConfigProvider: saved searches that are
// alerts, in Splunk's stable name order, offset-paginated.
func (a *Adapter) FetchConfigs(scope adapter.Scope, _ adapter.TimeWindow) iter.Seq2[model.AlertConfig, error] {
	return func(yield func(model.AlertConfig, error) bool) {
		searches, err := a.pullSearches(scope)
		if err != nil {
			var zero model.AlertConfig
			yield(zero, err)
			return
		}
		for _, sp := range searches {
			if !isAlert(sp) {
				continue // scheduled reports are not alerts
			}
			if !yield(a.toConfig(sp, scope), nil) {
				return
			}
		}
	}
}

// isAlert distinguishes alerting saved searches from plain scheduled
// reports: an alert either has a condition type or fires actions.
func isAlert(sp spSearch) bool {
	return (sp.Content.AlertType != "" && sp.Content.AlertType != "always") || sp.Content.Actions != ""
}

func (a *Adapter) toConfig(sp spSearch, scope adapter.Scope) model.AlertConfig {
	cfg := model.AlertConfig{
		Envelope: model.Envelope{
			SchemaVersion: model.CanonicalSchemaVersion,
			Source:        model.Source{Provider: providerID, Tenant: scope.Tenant},
			SourceRef:     model.SourceRef{Kind: "saved_search", NativeID: sp.Name},
			IdentityHints: model.IdentityHints{
				Tags:         map[string]string{},
				Names:        []string{sp.Name},
				ExternalRefs: []model.ExternalRef{},
			},
		},
		Name:         sp.Name,
		ConditionRaw: sp.Content.Search,
		Severity:     severityFrom(sp.Content.AlertSeverity),
		Routing:      routesFrom(sp.Content.Actions),
		Status:       model.StatusEnabled,
	}
	if sp.Content.Disabled {
		cfg.Status = model.StatusDisabled
	}
	if sp.ACL.App != "" {
		cfg.IdentityHints.Tags["splunk_app"] = sp.ACL.App
	}
	// Threshold/comparator from Splunk's own alert fields — SPL is never
	// parsed (shallow v1 per the spec's open question).
	if v, err := strconv.ParseFloat(sp.Content.AlertThreshold, 64); err == nil {
		cfg.Threshold = &v
		if comp := comparatorFrom(sp.Content.AlertComparator); comp != "" {
			c := comp
			cfg.Comparator = &c
		}
	}
	return cfg
}

func severityFrom(sev int) model.Severity {
	s := model.Severity{Native: strconv.Itoa(sev)}
	switch sev {
	case 6:
		s.Normalized = model.SeverityCritical
	case 5:
		s.Normalized = model.SeverityCritical
	case 4:
		s.Normalized = model.SeverityHigh
	case 3:
		s.Normalized = model.SeverityMedium
	case 2:
		s.Normalized = model.SeverityLow
	case 1:
		s.Normalized = model.SeverityInfo
	default:
		s.Native = ""
		s.Normalized = model.SeverityUnknown
	}
	return s
}

func comparatorFrom(comp string) string {
	switch comp {
	case "greater than":
		return ">"
	case "less than":
		return "<"
	case "equal to":
		return "=="
	case "not equal to":
		return "!="
	}
	return "" // "rises by", "drops by" etc. have no scalar comparator
}

func routesFrom(actions string) []model.Route {
	routes := []model.Route{}
	for _, action := range strings.Split(actions, ",") {
		action = strings.TrimSpace(action)
		if action == "" {
			continue
		}
		kind := model.RouteOther
		switch action {
		case "email":
			kind = model.RouteEmail
		case "pagerduty":
			kind = model.RoutePagerDutyService
		case "webhook":
			kind = model.RouteWebhook
		case "slack":
			kind = model.RouteChat
		}
		routes = append(routes, model.Route{TargetKind: kind, Target: action})
	}
	return routes
}

func (a *Adapter) pullSearches(scope adapter.Scope) ([]spSearch, error) {
	var out []spSearch
	for offset := 0; ; offset += pageCount {
		q := url.Values{
			"output_mode": {"json"},
			"count":       {strconv.Itoa(pageCount)},
			"offset":      {strconv.Itoa(offset)},
		}
		if scope.Selector != "" {
			q.Set("search", scope.Selector) // opaque provider-native narrowing
		}
		var page spPage
		if err := a.get("/services/saved/searches", q, &page); err != nil {
			return nil, err
		}
		out = append(out, page.Entry...)
		if len(page.Entry) < pageCount {
			break
		}
	}
	return out, nil
}

func (a *Adapter) get(path string, q url.Values, into any) error {
	req, err := http.NewRequest(http.MethodGet, a.BaseURL+path+"?"+q.Encode(), nil)
	if err != nil {
		return fmt.Errorf("splunk %s: %w", path, err)
	}
	req.Header.Set("Authorization", "Bearer "+a.Token)

	transport := a.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	resp, err := transport.RoundTrip(req)
	if err != nil {
		return fmt.Errorf("splunk %s: %w", path, err)
	}
	defer resp.Body.Close() //nolint:errcheck // read-only close
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("splunk %s: read body: %w", path, err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("splunk %s: HTTP %d: %.200s", path, resp.StatusCode, body)
	}
	if a.Recorder != nil {
		if err := a.Recorder.RecordPage(body); err != nil {
			return fmt.Errorf("splunk %s: record page: %w", path, err)
		}
	}
	if err := json.Unmarshal(body, into); err != nil {
		return fmt.Errorf("splunk %s: decode: %w", path, err)
	}
	return nil
}
