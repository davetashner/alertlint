package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/davetashner/alertlint/internal/adapter"
)

// Key identifies one snapshot: (source, scope, window) per
// docs/specs/provider-adapters.md §6, where source = (provider, tenant).
type Key struct {
	Provider string    `json:"provider"`
	Tenant   string    `json:"tenant"`
	Selector string    `json:"selector"`
	Start    time.Time `json:"start"`
	End      time.Time `json:"end"`
}

// NewKey builds a Key from adapter inputs.
func NewKey(provider string, scope adapter.Scope, window adapter.TimeWindow) Key {
	return Key{
		Provider: provider,
		Tenant:   scope.Tenant,
		Selector: scope.Selector,
		Start:    window.Start.UTC(),
		End:      window.End.UTC(),
	}
}

// Hash is the stable directory-name hash of the full key tuple.
func (k Key) Hash() string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s",
		k.Provider, k.Tenant, k.Selector,
		k.Start.UTC().Format(time.RFC3339), k.End.UTC().Format(time.RFC3339))))
	return hex.EncodeToString(h[:])[:16]
}

// Completeness status of a snapshot. Failed pulls are never presented as
// usable snapshots.
const (
	StatusComplete = "complete"
	StatusFailed   = "failed"
)

// Manifest records the full key tuple in the clear plus provenance, so a
// snapshot is self-describing and diffable.
type Manifest struct {
	Key            Key       `json:"key"`
	FetchedAt      time.Time `json:"fetched_at"`
	AdapterVersion string    `json:"adapter_version"`
	SchemaVersion  string    `json:"schema_version"`
	RawPages       int       `json:"raw_pages"`
	RecordCount    int       `json:"record_count"`
	Status         string    `json:"status"`
}

// Store is a snapshot cache rooted at one directory. Raw is the source of
// truth; canonical is derived and regenerable.
type Store struct {
	root string
}

// NewStore opens (creating if needed) a cache rooted at dir.
func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("cache root: %w", err)
	}
	return &Store{root: dir}, nil
}

// Dir returns the snapshot directory for k: <root>/<provider>/<key-hash>.
func (s *Store) Dir(k Key) string {
	return filepath.Join(s.root, k.Provider, k.Hash())
}

// ErrMissing is returned by reads in offline/replay mode when no usable
// snapshot exists for a key — replay never falls back to a live pull.
var ErrMissing = errors.New("cache: no usable snapshot for key")

// Writer accumulates one snapshot: raw pages first, then canonical records,
// then a manifest seal. Abandoning a Writer without Seal leaves the
// snapshot without a manifest, which readers treat as missing.
type Writer struct {
	dir      string
	rawPages int
	records  int
	canon    *os.File
	enc      *json.Encoder
}

// NewWriter starts a fresh snapshot for k, truncating any existing one.
func (s *Store) NewWriter(k Key) (*Writer, error) {
	dir := s.Dir(k)
	if err := os.RemoveAll(dir); err != nil {
		return nil, fmt.Errorf("reset snapshot: %w", err)
	}
	for _, sub := range []string{"raw", "canonical"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			return nil, fmt.Errorf("snapshot layout: %w", err)
		}
	}
	f, err := os.Create(filepath.Join(dir, "canonical", "records.jsonl"))
	if err != nil {
		return nil, fmt.Errorf("canonical file: %w", err)
	}
	return &Writer{dir: dir, canon: f, enc: json.NewEncoder(f)}, nil
}

// WriteRawPage stores one verbatim vendor response page, in pull order.
func (w *Writer) WriteRawPage(body []byte) error {
	w.rawPages++
	name := filepath.Join(w.dir, "raw", fmt.Sprintf("page-%04d.json", w.rawPages))
	if err := os.WriteFile(name, body, 0o644); err != nil {
		return fmt.Errorf("raw page %d: %w", w.rawPages, err)
	}
	return nil
}

// WriteRecord appends one canonical record as a JSONL line.
func (w *Writer) WriteRecord(rec any) error {
	if err := w.enc.Encode(rec); err != nil {
		return fmt.Errorf("canonical record %d: %w", w.records+1, err)
	}
	w.records++
	return nil
}

// Seal finalizes the snapshot with the given completeness status and
// provenance, writing the manifest last so partially written snapshots are
// never readable.
func (w *Writer) Seal(k Key, adapterVersion, schemaVersion, status string) (Manifest, error) {
	if err := w.canon.Close(); err != nil {
		return Manifest{}, fmt.Errorf("close canonical: %w", err)
	}
	m := Manifest{
		Key:            k,
		FetchedAt:      time.Now().UTC(),
		AdapterVersion: adapterVersion,
		SchemaVersion:  schemaVersion,
		RawPages:       w.rawPages,
		RecordCount:    w.records,
		Status:         status,
	}
	buf, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return Manifest{}, err
	}
	if err := os.WriteFile(filepath.Join(w.dir, "manifest.json"), append(buf, '\n'), 0o644); err != nil {
		return Manifest{}, fmt.Errorf("manifest: %w", err)
	}
	return m, nil
}

// Manifest reads the manifest for k. Missing manifest (including abandoned
// writes) or a failed status yields ErrMissing.
func (s *Store) Manifest(k Key) (Manifest, error) {
	buf, err := os.ReadFile(filepath.Join(s.Dir(k), "manifest.json"))
	if errors.Is(err, os.ErrNotExist) {
		return Manifest{}, ErrMissing
	}
	if err != nil {
		return Manifest{}, err
	}
	var m Manifest
	if err := json.Unmarshal(buf, &m); err != nil {
		return Manifest{}, fmt.Errorf("manifest for %s: %w", k.Hash(), err)
	}
	if m.Status != StatusComplete {
		return m, ErrMissing
	}
	return m, nil
}

// Records replays the canonical records of a complete snapshot, decoding
// each JSONL line into a fresh T. It fails loudly on a missing or failed
// snapshot (offline replay never falls back to a live pull).
func Records[T any](s *Store, k Key) ([]T, error) {
	if _, err := s.Manifest(k); err != nil {
		return nil, err
	}
	f, err := os.Open(filepath.Join(s.Dir(k), "canonical", "records.jsonl"))
	if err != nil {
		return nil, fmt.Errorf("canonical records: %w", err)
	}
	defer f.Close() //nolint:errcheck // read-only close
	dec := json.NewDecoder(f)
	var out []T
	for dec.More() {
		var rec T
		if err := dec.Decode(&rec); err != nil {
			return nil, fmt.Errorf("canonical record %d: %w", len(out)+1, err)
		}
		out = append(out, rec)
	}
	return out, nil
}

// RawPages returns the verbatim vendor pages of a snapshot in pull order,
// regardless of status — raw is the source of truth and stays available for
// canonical regeneration even from failed pulls.
func (s *Store) RawPages(k Key) ([][]byte, error) {
	entries, err := os.ReadDir(filepath.Join(s.Dir(k), "raw"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrMissing
	}
	if err != nil {
		return nil, err
	}
	var pages [][]byte
	for _, e := range entries { // ReadDir sorts by name; page-%04d preserves pull order
		buf, err := os.ReadFile(filepath.Join(s.Dir(k), "raw", e.Name()))
		if err != nil {
			return nil, err
		}
		pages = append(pages, buf)
	}
	return pages, nil
}
