// Package fake provides an in-memory provider for tests: it serves
// whatever canonical records it is constructed with, in insertion order,
// and satisfies all three data-class interfaces.
package fake

import (
	"iter"

	"github.com/davetashner/alertlint/internal/adapter"
	"github.com/davetashner/alertlint/internal/identity"
	"github.com/davetashner/alertlint/internal/model"
)

// Provider is a configurable in-memory adapter. The zero value serves no
// records; populate the slices (and Err to simulate a failed pull).
type Provider struct {
	ID          string
	Schema      string
	Configs     []model.AlertConfig
	Events      []model.AlertEvent
	Responses   []model.ResponseRecord
	CIs         []identity.CI
	Maintenance []model.MaintenanceWindow
	// Err, when non-nil, is yielded after the records to simulate a pull
	// that fails partway: callers must abort that source's contribution.
	Err error
}

// ProviderID implements adapter.Provider.
func (p *Provider) ProviderID() string { return p.ID }

// SchemaVersion implements adapter.Provider.
func (p *Provider) SchemaVersion() string {
	if p.Schema == "" {
		return model.CanonicalSchemaVersion
	}
	return p.Schema
}

// FetchConfigs implements adapter.ConfigProvider.
func (p *Provider) FetchConfigs(adapter.Scope, adapter.TimeWindow) iter.Seq2[model.AlertConfig, error] {
	return yieldAll(p.Configs, p.Err)
}

// FetchEvents implements adapter.HistoryProvider.
func (p *Provider) FetchEvents(adapter.Scope, adapter.TimeWindow) iter.Seq2[model.AlertEvent, error] {
	return yieldAll(p.Events, p.Err)
}

// FetchResponses implements adapter.ActionProvider.
func (p *Provider) FetchResponses(adapter.Scope, adapter.TimeWindow) iter.Seq2[model.ResponseRecord, error] {
	return yieldAll(p.Responses, p.Err)
}

// FetchCIs implements adapter.CIProvider.
func (p *Provider) FetchCIs(adapter.Scope, adapter.TimeWindow) iter.Seq2[identity.CI, error] {
	return yieldAll(p.CIs, p.Err)
}

// FetchMaintenance implements adapter.MaintenanceProvider.
func (p *Provider) FetchMaintenance(adapter.Scope, adapter.TimeWindow) iter.Seq2[model.MaintenanceWindow, error] {
	return yieldAll(p.Maintenance, p.Err)
}

func yieldAll[T any](records []T, failWith error) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		for _, rec := range records {
			if !yield(rec, nil) {
				return
			}
		}
		if failWith != nil {
			var zero T
			yield(zero, failWith)
		}
	}
}
