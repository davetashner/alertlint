package datadog

import (
	"fmt"
	"iter"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"time"

	"github.com/davetashner/alertlint/internal/adapter"
	"github.com/davetashner/alertlint/internal/model"
)

// HistoryProvider implementation (REQ-SRC-008): alert events from the v1
// events API become firing episodes, so monitors that never page — the
// chat-spam tier — still contribute firing history and auto-resolve
// signals (REQ-NOISE-001).
//
// The window is pulled in ≤15-day chunks (the events API caps results per
// query; chunking bounds silent loss). Triggered events open an episode
// per monitor, recovery events close it: Datadog recoveries are condition
// recoveries, so closed episodes carry auto_resolved: true. Manual
// UI-resolves are indistinguishable in the event stream and rare; the
// assumption is documented here and in provider-adapters.md.

const eventChunkDays = 15

type ddEvent struct {
	ID           int64    `json:"id"`
	Title        string   `json:"title"`
	AlertType    string   `json:"alert_type"` // error | warning | success | info
	DateHappened int64    `json:"date_happened"`
	MonitorID    *int64   `json:"monitor_id"`
	Tags         []string `json:"tags"`
	URL          string   `json:"url"`
}

type eventsPage struct {
	Events []ddEvent `json:"events"`
}

// FetchEvents implements adapter.HistoryProvider.
func (a *Adapter) FetchEvents(scope adapter.Scope, window adapter.TimeWindow) iter.Seq2[model.AlertEvent, error] {
	return func(yield func(model.AlertEvent, error) bool) {
		events, err := a.pullEvents(scope, window)
		if err != nil {
			var zero model.AlertEvent
			yield(zero, err)
			return
		}
		for _, ep := range pairEpisodes(events, scope) {
			if !yield(ep, nil) {
				return
			}
		}
	}
}

func (a *Adapter) pullEvents(scope adapter.Scope, window adapter.TimeWindow) ([]ddEvent, error) {
	var out []ddEvent
	for chunkStart := window.Start; chunkStart.Before(window.End); {
		chunkEnd := chunkStart.AddDate(0, 0, eventChunkDays)
		if chunkEnd.After(window.End) {
			chunkEnd = window.End
		}
		q := url.Values{
			"start":   {strconv.FormatInt(chunkStart.Unix(), 10)},
			"end":     {strconv.FormatInt(chunkEnd.Unix(), 10)},
			"sources": {"alert"},
		}
		if scope.Selector != "" {
			q.Set("tags", scope.Selector)
		}
		var page eventsPage
		if err := a.get("/api/v1/events", q, &page); err != nil {
			return nil, err
		}
		out = append(out, page.Events...)
		chunkStart = chunkEnd
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].DateHappened != out[j].DateHappened {
			return out[i].DateHappened < out[j].DateHappened
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

var titleMonitorName = regexp.MustCompile(`^\[(?:Triggered|Warn|Recovered)(?:[^\]]*)?\]\s*(.+)$`)

// pairEpisodes folds a time-ordered alert-event stream into firing
// episodes per monitor: error/warning opens (re-triggers while open fold
// into occurrence_count), success closes with auto_resolved: true. An
// episode still open at window end has no resolved_at.
func pairEpisodes(events []ddEvent, scope adapter.Scope) []model.AlertEvent {
	openByMonitor := map[int64]*model.AlertEvent{}
	var episodes []*model.AlertEvent

	for _, ev := range events {
		if ev.MonitorID == nil {
			continue // non-monitor alert events cannot join configs
		}
		mid := *ev.MonitorID
		switch ev.AlertType {
		case "error", "warning":
			if ep := openByMonitor[mid]; ep != nil {
				ep.OccurrenceCount++
				continue
			}
			ep := newEpisode(ev, mid, scope)
			openByMonitor[mid] = ep
			episodes = append(episodes, ep)
		case "success":
			ep := openByMonitor[mid]
			if ep == nil {
				continue // recovery without an observed trigger: outside window
			}
			resolved := time.Unix(ev.DateHappened, 0).UTC()
			auto := true // condition recovery — the monitor-side signal (REQ-NOISE-001)
			ep.ResolvedAt = &resolved
			ep.AutoResolved = &auto
			delete(openByMonitor, mid)
		}
	}
	out := make([]model.AlertEvent, len(episodes))
	for i, ep := range episodes {
		out[i] = *ep
	}
	return out
}

func newEpisode(ev ddEvent, monitorID int64, scope adapter.Scope) *model.AlertEvent {
	fired := time.Unix(ev.DateHappened, 0).UTC()
	native := strconv.FormatInt(monitorID, 10)
	name := ev.Title
	if m := titleMonitorName.FindStringSubmatch(ev.Title); m != nil {
		name = m[1]
	}
	severity := model.Severity{Native: ev.AlertType, Normalized: model.SeverityHigh}
	if ev.AlertType == "warning" {
		severity.Normalized = model.SeverityMedium
	}
	ep := &model.AlertEvent{
		Envelope: model.Envelope{
			SchemaVersion: model.CanonicalSchemaVersion,
			Source:        model.Source{Provider: providerID, Tenant: scope.Tenant},
			SourceRef:     model.SourceRef{Kind: "alert_event", NativeID: fmt.Sprintf("%d", ev.ID)},
			IdentityHints: model.IdentityHints{
				Tags:         map[string]string{},
				Names:        []string{},
				ExternalRefs: []model.ExternalRef{},
			},
		},
		AlertRef:        model.AlertRef{Provider: strp(providerID), NativeID: &native, Name: &name},
		FiredAt:         fired,
		OccurrenceCount: 1,
		Severity:        severity,
	}
	if ev.URL != "" {
		u := ev.URL
		ep.SourceRef.URL = &u
	}
	for _, t := range ev.Tags {
		k, v, _ := cutTag(t)
		ep.IdentityHints.Tags[k] = v
		if k == "service" && v != "" {
			ep.IdentityHints.Names = append(ep.IdentityHints.Names, v)
		}
	}
	return ep
}

func cutTag(t string) (string, string, bool) {
	for i := 0; i < len(t); i++ {
		if t[i] == ':' {
			return t[:i], t[i+1:], true
		}
	}
	return t, "", false
}

func strp(s string) *string { return &s }
