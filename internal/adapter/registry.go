package adapter

import (
	"fmt"
	"sort"
	"sync"
)

// Capabilities describes which data classes a registered provider serves,
// discovered by type assertion (ADR 0005). Per-service source coverage is
// derivable from which providers contributed records (ADR 0004).
type Capabilities struct {
	Config  bool
	History bool
	Action  bool
	CI      bool
}

// Registry holds registered providers. Registration is compile-time wiring:
// each vendor package registers its adapter from an init hook or explicit
// setup call — there is no dynamic plugin loading (ADR 0005).
type Registry struct {
	mu        sync.RWMutex
	providers map[string]Provider
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{providers: map[string]Provider{}}
}

// Register adds p under its ProviderID. A provider must satisfy at least one
// data-class interface; duplicate ids are a programming error.
func (r *Registry) Register(p Provider) error {
	caps := CapabilitiesOf(p)
	if !caps.Config && !caps.History && !caps.Action && !caps.CI {
		return fmt.Errorf("adapter %q implements no data-class interface", p.ProviderID())
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.providers[p.ProviderID()]; dup {
		return fmt.Errorf("adapter %q already registered", p.ProviderID())
	}
	r.providers[p.ProviderID()] = p
	return nil
}

// Get returns the provider registered under id, or false.
func (r *Registry) Get(id string) (Provider, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.providers[id]
	return p, ok
}

// IDs returns all registered provider ids in sorted order — never map
// order, so downstream output stays deterministic (ADR 0005).
func (r *Registry) IDs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ids := make([]string, 0, len(r.providers))
	for id := range r.providers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// ConfigProviders returns registered adapters that serve alert configs,
// ordered by provider id.
func (r *Registry) ConfigProviders() []ConfigProvider {
	var out []ConfigProvider
	for _, id := range r.IDs() {
		p, _ := r.Get(id)
		if cp, ok := p.(ConfigProvider); ok {
			out = append(out, cp)
		}
	}
	return out
}

// HistoryProviders returns registered adapters that serve firing history,
// ordered by provider id.
func (r *Registry) HistoryProviders() []HistoryProvider {
	var out []HistoryProvider
	for _, id := range r.IDs() {
		p, _ := r.Get(id)
		if hp, ok := p.(HistoryProvider); ok {
			out = append(out, hp)
		}
	}
	return out
}

// ActionProviders returns registered adapters that serve response trails,
// ordered by provider id.
func (r *Registry) ActionProviders() []ActionProvider {
	var out []ActionProvider
	for _, id := range r.IDs() {
		p, _ := r.Get(id)
		if ap, ok := p.(ActionProvider); ok {
			out = append(out, ap)
		}
	}
	return out
}

// CIProviders returns registered adapters that serve the CMDB CI
// inventory, ordered by provider id.
func (r *Registry) CIProviders() []CIProvider {
	var out []CIProvider
	for _, id := range r.IDs() {
		p, _ := r.Get(id)
		if cp, ok := p.(CIProvider); ok {
			out = append(out, cp)
		}
	}
	return out
}

// CapabilitiesOf reports which data-class interfaces p satisfies.
func CapabilitiesOf(p Provider) Capabilities {
	_, config := p.(ConfigProvider)
	_, history := p.(HistoryProvider)
	_, action := p.(ActionProvider)
	_, ci := p.(CIProvider)
	return Capabilities{Config: config, History: history, Action: action, CI: ci}
}
