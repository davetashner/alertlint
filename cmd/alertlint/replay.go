package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/davetashner/alertlint/internal/adapter"
	"github.com/davetashner/alertlint/internal/adapter/fake"
)

// loadReplayRegistry builds a registry from a fixture corpus directory:
//
//	<dir>/<provider>/configs.jsonl    -> ConfigProvider records
//	<dir>/<provider>/events.jsonl     -> HistoryProvider records
//	<dir>/<provider>/responses.jsonl  -> ActionProvider records
//	<dir>/<provider>/cis.jsonl        -> CIProvider records
//	<dir>/<provider>/maintenance.jsonl -> MaintenanceProvider records
//
// This is the offline path (docs/specs/provider-adapters.md §6 replay):
// no credentials, no network, byte-identical documents given identical
// fixtures and --run-timestamp.
func loadReplayRegistry(dir string) (*adapter.Registry, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("replay corpus: %w", err)
	}
	registry := adapter.NewRegistry()
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		return nil, fmt.Errorf("replay corpus %s: no provider directories", dir)
	}
	for _, name := range names {
		p := &fake.Provider{ID: name}
		base, err := canonicalDir(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		if err := loadJSONL(filepath.Join(base, "configs.jsonl"), &p.Configs); err != nil {
			return nil, err
		}
		if err := loadJSONL(filepath.Join(base, "events.jsonl"), &p.Events); err != nil {
			return nil, err
		}
		if err := loadJSONL(filepath.Join(base, "responses.jsonl"), &p.Responses); err != nil {
			return nil, err
		}
		if err := loadJSONL(filepath.Join(base, "cis.jsonl"), &p.CIs); err != nil {
			return nil, err
		}
		if err := loadJSONL(filepath.Join(base, "maintenance.jsonl"), &p.Maintenance); err != nil {
			return nil, err
		}
		if err := registry.Register(p); err != nil {
			return nil, err
		}
	}
	return registry, nil
}

// canonicalDir resolves a provider directory to where its JSONL files
// live: either flat fixtures (<dir>/*.jsonl) or a snapshot-cache layout
// (<dir>/<key-hash>/canonical/*.jsonl) — a cache directory written by
// `analyze --cache-dir` is directly replayable. With several snapshots,
// the newest complete one wins; failed snapshots are never replayed.
func canonicalDir(dir string) (string, error) {
	flat, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil {
		return "", err
	}
	if len(flat) > 0 {
		return dir, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	best := ""
	var bestFetched string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		buf, err := os.ReadFile(filepath.Join(dir, e.Name(), "manifest.json"))
		if err != nil {
			continue // unsealed or foreign directory: not replayable
		}
		var m struct {
			FetchedAt string `json:"fetched_at"`
			Status    string `json:"status"`
		}
		if json.Unmarshal(buf, &m) != nil || m.Status != "complete" {
			continue
		}
		if best == "" || m.FetchedAt > bestFetched || (m.FetchedAt == bestFetched && e.Name() > best) {
			best, bestFetched = e.Name(), m.FetchedAt
		}
	}
	if best == "" {
		return "", fmt.Errorf("replay corpus: %s has neither *.jsonl fixtures nor a complete snapshot", dir)
	}
	return filepath.Join(dir, best, "canonical"), nil
}

// loadJSONL decodes one canonical record per line into out (a pointer to
// a slice). A missing file means the provider does not serve that data
// class — but the fake satisfies all four interfaces, so absent files
// simply yield empty streams.
func loadJSONL[T any](path string, out *[]T) error {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close() //nolint:errcheck // read-only close
	dec := json.NewDecoder(f)
	for dec.More() {
		var rec T
		if err := dec.Decode(&rec); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		*out = append(*out, rec)
	}
	return nil
}

// Type assertions documenting what replay providers serve.
var (
	_ adapter.ConfigProvider  = (*fake.Provider)(nil)
	_ adapter.HistoryProvider = (*fake.Provider)(nil)
	_ adapter.ActionProvider  = (*fake.Provider)(nil)
	_ adapter.CIProvider      = (*fake.Provider)(nil)
)
