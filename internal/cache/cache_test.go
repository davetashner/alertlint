package cache

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/davetashner/alertlint/internal/adapter"
	"github.com/davetashner/alertlint/internal/model"
)

func testKey() Key {
	end := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	return NewKey("fakevendor",
		adapter.Scope{Tenant: "tenant-1", Selector: "team:payments"},
		adapter.TimeWindow{Start: end.AddDate(0, 0, -90), End: end})
}

func sampleRecord(id string) model.AlertConfig {
	return model.AlertConfig{
		Envelope: model.Envelope{
			SchemaVersion: model.CanonicalSchemaVersion,
			Source:        model.Source{Provider: "fakevendor", Tenant: "tenant-1"},
			SourceRef:     model.SourceRef{Kind: "monitor", NativeID: id},
			IdentityHints: model.IdentityHints{
				Tags:         map[string]string{"service": "checkout-api"},
				Names:        []string{"checkout-api"},
				ExternalRefs: []model.ExternalRef{},
			},
		},
		Name:         "p95 latency high",
		ConditionRaw: "p95 > 2.5",
		Severity:     model.Severity{Native: "P2", Normalized: model.SeverityHigh},
		Routing:      []model.Route{},
		Status:       model.StatusEnabled,
	}
}

func writeSnapshot(t *testing.T, s *Store, k Key, status string, ids ...string) Manifest {
	t.Helper()
	w, err := s.NewWriter(k)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	if err := w.WriteRawPage([]byte(`{"page":1}`)); err != nil {
		t.Fatalf("WriteRawPage: %v", err)
	}
	if err := w.WriteRawPage([]byte(`{"page":2}`)); err != nil {
		t.Fatalf("WriteRawPage: %v", err)
	}
	for _, id := range ids {
		if err := w.WriteRecord(sampleRecord(id)); err != nil {
			t.Fatalf("WriteRecord: %v", err)
		}
	}
	m, err := w.Seal(k, "test-adapter/0.0.1", model.CanonicalSchemaVersion, status)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	return m
}

func TestRecordThenReplayIsByteIdentical(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	k := testKey()
	m := writeSnapshot(t, s, k, StatusComplete, "m-1", "m-2", "m-3")
	if m.RawPages != 2 || m.RecordCount != 3 {
		t.Fatalf("manifest counts = %d pages / %d records, want 2 / 3", m.RawPages, m.RecordCount)
	}

	first, err := os.ReadFile(filepath.Join(s.Dir(k), "canonical", "records.jsonl"))
	if err != nil {
		t.Fatal(err)
	}

	recs, err := Records[model.AlertConfig](s, k)
	if err != nil {
		t.Fatalf("Records: %v", err)
	}
	if len(recs) != 3 || recs[0].SourceRef.NativeID != "m-1" || recs[2].SourceRef.NativeID != "m-3" {
		t.Fatalf("replay = %d records, order/content wrong: %+v", len(recs), recs)
	}

	// Re-writing the same snapshot from the same inputs must produce
	// byte-identical canonical output (REQ-SCORE-007 reproducibility).
	writeSnapshot(t, s, k, StatusComplete, "m-1", "m-2", "m-3")
	second, err := os.ReadFile(filepath.Join(s.Dir(k), "canonical", "records.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Error("canonical output not byte-identical across identical re-records")
	}
}

func TestOfflineReplayFailsLoudlyOnMissingKey(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Records[model.AlertConfig](s, testKey()); !errors.Is(err, ErrMissing) {
		t.Errorf("missing key: err = %v, want ErrMissing", err)
	}
	if _, err := s.RawPages(testKey()); !errors.Is(err, ErrMissing) {
		t.Errorf("missing raw: err = %v, want ErrMissing", err)
	}
}

func TestFailedSnapshotIsNeverUsable(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	k := testKey()
	writeSnapshot(t, s, k, StatusFailed, "m-1")

	if _, err := s.Manifest(k); !errors.Is(err, ErrMissing) {
		t.Errorf("failed snapshot manifest: err = %v, want ErrMissing", err)
	}
	if _, err := Records[model.AlertConfig](s, k); !errors.Is(err, ErrMissing) {
		t.Errorf("failed snapshot records: err = %v, want ErrMissing", err)
	}
	// Raw stays available: it is the source of truth for regeneration.
	pages, err := s.RawPages(k)
	if err != nil || len(pages) != 2 {
		t.Errorf("raw pages of failed snapshot: %d, err=%v; raw must survive", len(pages), err)
	}
}

func TestAbandonedWriterIsInvisible(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	k := testKey()
	w, err := s.NewWriter(k)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.WriteRecord(sampleRecord("m-1")); err != nil {
		t.Fatal(err)
	}
	// No Seal: readers must treat the snapshot as missing.
	if _, err := s.Manifest(k); !errors.Is(err, ErrMissing) {
		t.Errorf("unsealed snapshot: err = %v, want ErrMissing", err)
	}
}

func TestKeyHashStableAndDistinct(t *testing.T) {
	k := testKey()
	h := k.Hash()
	if again := k.Hash(); again != h {
		t.Errorf("hash not stable: %q then %q", h, again)
	}
	variants := []Key{k, k, k, k}
	variants[1].Selector = ""
	variants[2].Tenant = "tenant-2"
	variants[3].End = k.End.Add(time.Hour)
	seen := map[string]int{}
	for i, v := range variants {
		seen[v.Hash()] = i
	}
	if len(seen) != 4 {
		t.Errorf("key variants produced %d distinct hashes, want 4", len(seen))
	}
}

func TestRawPagesPreservePullOrder(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	k := testKey()
	writeSnapshot(t, s, k, StatusComplete, "m-1")
	pages, err := s.RawPages(k)
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 2 || string(pages[0]) != `{"page":1}` || string(pages[1]) != `{"page":2}` {
		t.Errorf("raw pages out of order: %q", pages)
	}
}
