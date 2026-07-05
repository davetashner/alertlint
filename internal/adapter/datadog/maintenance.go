package datadog

import (
	"iter"
	"net/url"
	"sort"
	"strconv"
	"time"

	"github.com/davetashner/alertlint/internal/adapter"
	"github.com/davetashner/alertlint/internal/model"
)

// MaintenanceProvider implementation (REQ-NOISE-005): downtimes become
// canonical maintenance windows. Cancelled and disabled downtimes are
// skipped; windows that do not overlap the analysis window are skipped.
// Whether a fire falls inside a window is core logic — this adapter only
// translates the declarations.

type ddDowntime struct {
	ID        int64    `json:"id"`
	Start     int64    `json:"start"`
	End       *int64   `json:"end"`
	MonitorID *int64   `json:"monitor_id"`
	Scope     []string `json:"scope"`
	Message   string   `json:"message"`
	Disabled  bool     `json:"disabled"`
	Canceled  *int64   `json:"canceled"`
}

// FetchMaintenance implements adapter.MaintenanceProvider.
func (a *Adapter) FetchMaintenance(scope adapter.Scope, window adapter.TimeWindow) iter.Seq2[model.MaintenanceWindow, error] {
	return func(yield func(model.MaintenanceWindow, error) bool) {
		q := url.Values{"current_only": {"false"}}
		var downtimes []ddDowntime
		if err := a.get("/api/v1/downtime", q, &downtimes); err != nil {
			var zero model.MaintenanceWindow
			yield(zero, err)
			return
		}
		sort.Slice(downtimes, func(i, j int) bool { return downtimes[i].ID < downtimes[j].ID })
		for _, dt := range downtimes {
			if dt.Disabled || dt.Canceled != nil {
				continue
			}
			starts := time.Unix(dt.Start, 0).UTC()
			var ends *time.Time
			if dt.End != nil {
				e := time.Unix(*dt.End, 0).UTC()
				ends = &e
			}
			// Skip windows entirely outside the analysis window.
			if ends != nil && ends.Before(window.Start) {
				continue
			}
			if starts.After(window.End) {
				continue
			}
			mw := model.MaintenanceWindow{
				Envelope: model.Envelope{
					SchemaVersion: model.CanonicalSchemaVersion,
					Source:        model.Source{Provider: providerID, Tenant: scope.Tenant},
					SourceRef:     model.SourceRef{Kind: "downtime", NativeID: strconv.FormatInt(dt.ID, 10)},
					IdentityHints: model.IdentityHints{
						Tags:         map[string]string{},
						Names:        []string{},
						ExternalRefs: []model.ExternalRef{},
					},
				},
				StartsAt:    starts,
				EndsAt:      ends,
				MonitorRefs: []model.MonitorRef{},
			}
			if dt.MonitorID != nil {
				mw.MonitorRefs = append(mw.MonitorRefs,
					model.MonitorRef{Provider: providerID, NativeID: strconv.FormatInt(*dt.MonitorID, 10)})
			}
			if dt.Message != "" {
				msg := dt.Message
				mw.Reason = &msg
			}
			if !yield(mw, nil) {
				return
			}
		}
	}
}
