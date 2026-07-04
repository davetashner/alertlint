// Package datadog implements ConfigProvider over the Datadog monitors API
// (docs/specs/provider-adapters.md §7).
//
// Threshold/comparator/duration extraction is best-effort over the monitor
// query (spec: a missing extraction is legal and means "not parseable",
// never a guessed value). Tags pass through verbatim as identity hints;
// @pagerduty-* mentions in the message become routing entries and
// pagerduty external refs.
package datadog

import (
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/davetashner/alertlint/internal/adapter"
	"github.com/davetashner/alertlint/internal/model"
)

const (
	providerID = "datadog"
	pageSize   = 100
)

// PageRecorder receives every verbatim response page, in pull order.
type PageRecorder interface {
	RecordPage(body []byte) error
}

// Adapter pulls monitor definitions. Inject Transport to replay recorded
// fixture pages.
type Adapter struct {
	BaseURL   string // default https://api.datadoghq.com
	APIKey    string
	AppKey    string
	Transport http.RoundTripper
	Recorder  PageRecorder
}

// ProviderID implements adapter.Provider.
func (a *Adapter) ProviderID() string { return providerID }

// SchemaVersion implements adapter.Provider.
func (a *Adapter) SchemaVersion() string { return model.CanonicalSchemaVersion }

type ddMonitor struct {
	ID           int64    `json:"id"`
	Name         string   `json:"name"`
	Query        string   `json:"query"`
	Message      string   `json:"message"`
	Type         string   `json:"type"`
	Priority     *int     `json:"priority"`
	Tags         []string `json:"tags"`
	OverallState string   `json:"overall_state"`
	Created      string   `json:"created"`
	Modified     string   `json:"modified"`
	Options      struct {
		Silenced   map[string]any `json:"silenced"`
		Thresholds struct {
			Critical *float64 `json:"critical"`
		} `json:"thresholds"`
	} `json:"options"`
}

// FetchConfigs implements adapter.ConfigProvider: one AlertConfig per
// monitor. Config is a current snapshot at pull time; the window exists
// for cache-key symmetry.
func (a *Adapter) FetchConfigs(scope adapter.Scope, _ adapter.TimeWindow) iter.Seq2[model.AlertConfig, error] {
	return func(yield func(model.AlertConfig, error) bool) {
		monitors, err := a.pullMonitors(scope)
		if err != nil {
			var zero model.AlertConfig
			yield(zero, err)
			return
		}
		for _, m := range monitors {
			if !yield(a.toConfig(m, scope), nil) {
				return
			}
		}
	}
}

var (
	comparatorRe = regexp.MustCompile(`(>=|<=|==|!=|>|<)\s*(-?\d+(?:\.\d+)?)\s*$`)
	windowRe     = regexp.MustCompile(`last_(\d+)([smhd])`)
	pdMentionRe  = regexp.MustCompile(`@pagerduty-([A-Za-z0-9_-]+)`)
	emailRe      = regexp.MustCompile(`@([A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,})`)
)

func (a *Adapter) toConfig(m ddMonitor, scope adapter.Scope) model.AlertConfig {
	native := strconv.FormatInt(m.ID, 10)
	monURL := "https://app.datadoghq.com/monitors/" + native
	cfg := model.AlertConfig{
		Envelope: model.Envelope{
			SchemaVersion: model.CanonicalSchemaVersion,
			Source:        model.Source{Provider: providerID, Tenant: scope.Tenant},
			SourceRef:     model.SourceRef{Kind: "monitor", NativeID: native, URL: &monURL},
			IdentityHints: model.IdentityHints{
				Tags:         map[string]string{},
				Names:        []string{},
				ExternalRefs: []model.ExternalRef{},
			},
		},
		Name:         m.Name,
		ConditionRaw: m.Query,
		Severity:     severityFromPriority(m.Priority),
		Routing:      []model.Route{},
		Status:       model.StatusEnabled,
	}

	// Tags verbatim: "k:v" split on first colon; bare tags keep empty value.
	for _, t := range m.Tags {
		k, v, _ := strings.Cut(t, ":")
		cfg.IdentityHints.Tags[k] = v
		if k == "service" && v != "" {
			cfg.IdentityHints.Names = append(cfg.IdentityHints.Names, v)
		}
	}

	// Routing from message mentions; PD services double as external refs.
	for _, match := range pdMentionRe.FindAllStringSubmatch(m.Message, -1) {
		cfg.Routing = append(cfg.Routing, model.Route{TargetKind: model.RoutePagerDutyService, Target: match[1]})
		cfg.IdentityHints.ExternalRefs = append(cfg.IdentityHints.ExternalRefs,
			model.ExternalRef{System: "pagerduty_service_name", NativeID: match[1]})
	}
	for _, match := range emailRe.FindAllStringSubmatch(m.Message, -1) {
		cfg.Routing = append(cfg.Routing, model.Route{TargetKind: model.RouteEmail, Target: match[1]})
	}

	// Best-effort extraction: comparator+threshold from the query tail;
	// options.thresholds.critical wins for the numeric value when present.
	if sub := comparatorRe.FindStringSubmatch(strings.TrimSpace(m.Query)); sub != nil {
		comp := sub[1]
		cfg.Comparator = &comp
		if v, err := strconv.ParseFloat(sub[2], 64); err == nil {
			cfg.Threshold = &v
		}
	}
	if m.Options.Thresholds.Critical != nil {
		cfg.Threshold = m.Options.Thresholds.Critical
	}
	if sub := windowRe.FindStringSubmatch(m.Query); sub != nil {
		n, _ := strconv.ParseInt(sub[1], 10, 64)
		switch sub[2] {
		case "s":
		case "m":
			n *= 60
		case "h":
			n *= 3600
		case "d":
			n *= 86400
		}
		cfg.DurationS = &n
	}

	// Silenced scopes ("*" or any scope) => silenced; Datadog "muted" is
	// carried in options.silenced.
	if len(m.Options.Silenced) > 0 {
		cfg.Status = model.StatusSilenced
	}

	if ts := parseDDTime(m.Created); ts != nil {
		cfg.CreatedAt = ts
	}
	if ts := parseDDTime(m.Modified); ts != nil {
		cfg.UpdatedAt = ts
	}
	return cfg
}

func severityFromPriority(p *int) model.Severity {
	if p == nil {
		return model.Severity{Native: "", Normalized: model.SeverityUnknown}
	}
	s := model.Severity{Native: "P" + strconv.Itoa(*p)}
	switch *p {
	case 1:
		s.Normalized = model.SeverityCritical
	case 2:
		s.Normalized = model.SeverityHigh
	case 3:
		s.Normalized = model.SeverityMedium
	case 4:
		s.Normalized = model.SeverityLow
	case 5:
		s.Normalized = model.SeverityInfo
	default:
		s.Normalized = model.SeverityUnknown
	}
	return s
}

func parseDDTime(v string) *time.Time {
	if v == "" {
		return nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05.999999-07:00"} {
		if t, err := time.Parse(layout, v); err == nil {
			u := t.UTC()
			return &u
		}
	}
	return nil
}

func (a *Adapter) pullMonitors(scope adapter.Scope) ([]ddMonitor, error) {
	var out []ddMonitor
	for page := 0; ; page++ {
		q := url.Values{
			"page":      {strconv.Itoa(page)},
			"page_size": {strconv.Itoa(pageSize)},
		}
		if scope.Selector != "" {
			q.Set("monitor_tags", scope.Selector)
		}
		var monitors []ddMonitor
		if err := a.get("/api/v1/monitor", q, &monitors); err != nil {
			return nil, err
		}
		out = append(out, monitors...)
		if len(monitors) < pageSize {
			break
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (a *Adapter) get(path string, q url.Values, into any) error {
	base := a.BaseURL
	if base == "" {
		base = "https://api.datadoghq.com"
	}
	req, err := http.NewRequest(http.MethodGet, base+path+"?"+q.Encode(), nil)
	if err != nil {
		return fmt.Errorf("datadog %s: %w", path, err)
	}
	req.Header.Set("DD-API-KEY", a.APIKey)
	req.Header.Set("DD-APPLICATION-KEY", a.AppKey)

	transport := a.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	resp, err := transport.RoundTrip(req)
	if err != nil {
		return fmt.Errorf("datadog %s: %w", path, err)
	}
	defer resp.Body.Close() //nolint:errcheck // read-only close
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("datadog %s: read body: %w", path, err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("datadog %s: HTTP %d: %.200s", path, resp.StatusCode, body)
	}
	if a.Recorder != nil {
		if err := a.Recorder.RecordPage(body); err != nil {
			return fmt.Errorf("datadog %s: record page: %w", path, err)
		}
	}
	if err := json.Unmarshal(body, into); err != nil {
		return fmt.Errorf("datadog %s: decode: %w", path, err)
	}
	return nil
}
