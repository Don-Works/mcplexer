package memsync

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

type mockStore struct {
	entries map[string]*store.MemoryEntry
}

func newMockStore() *mockStore {
	return &mockStore{entries: make(map[string]*store.MemoryEntry)}
}

func (m *mockStore) WriteMemory(_ context.Context, e *store.MemoryEntry) error {
	m.entries[e.ID] = e
	return nil
}

func (m *mockStore) GetMemory(_ context.Context, id string) (*store.MemoryEntry, error) {
	if e, ok := m.entries[id]; ok {
		return e, nil
	}
	return nil, store.ErrNotFound
}

func TestScannerStartStop(t *testing.T) {
	s := newMockStore()
	scanner := NewScanner(s, t.TempDir(), 100*time.Millisecond, nil)
	scanner.Start()
	time.Sleep(50 * time.Millisecond)
	scanner.Stop()
	// Should not panic on double stop
	scanner.Stop()
}

func TestScannerImportsMiMoCode(t *testing.T) {
	tmpDir := t.TempDir()
	// Create MiMoCode memory structure
	projDir := filepath.Join(tmpDir, ".local", "share", "mimocode", "memory", "projects", "ws-1")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	memFile := filepath.Join(projDir, "MEMORY.md")
	if err := os.WriteFile(memFile, []byte("## Facts\n\nProject uses Go.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	s := newMockStore()
	scanner := NewScanner(s, tmpDir, 50*time.Millisecond, nil)
	scanner.Start()
	time.Sleep(150 * time.Millisecond) // wait for at least 2 scans
	scanner.Stop()

	if len(s.entries) == 0 {
		t.Error("scanner did not import any entries")
	}
	imported, skipped := scanner.Stats()
	t.Logf("stats: imported=%d, skipped=%d", imported, skipped)
}

func TestScannerIdempotent(t *testing.T) {
	tmpDir := t.TempDir()
	projDir := filepath.Join(tmpDir, ".local", "share", "mimocode", "memory", "projects", "ws-1")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	memFile := filepath.Join(projDir, "MEMORY.md")
	if err := os.WriteFile(memFile, []byte("## Facts\n\nTest.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	s := newMockStore()
	scanner := NewScanner(s, tmpDir, 50*time.Millisecond, nil)
	scanner.Start()
	time.Sleep(150 * time.Millisecond)
	count1 := len(s.entries)
	// Second scan should not add more entries (file unchanged)
	time.Sleep(100 * time.Millisecond)
	count2 := len(s.entries)
	scanner.Stop()

	if count1 != count2 {
		t.Errorf("scanner not idempotent: first=%d, second=%d", count1, count2)
	}
}

func TestNewScannerFromEnvDisabled(t *testing.T) {
	t.Setenv("MCPLEXER_SYNC_ENABLED", "0")
	s := newMockStore()
	scanner := NewScannerFromEnv(s)
	if scanner != nil {
		t.Error("expected nil scanner when MCPLEXER_SYNC_ENABLED=0")
	}
}
