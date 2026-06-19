package harnessimport

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
)

// mockStore implements MemoryWriter for testing.
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

func TestSplitIntoSections(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantLen   int
		wantTitle string
	}{
		{
			name:      "no headers returns whole body",
			input:     "just some text\nmore text",
			wantLen:   1,
			wantTitle: "MiMoCode Memory",
		},
		{
			name:      "h1 header used as title for single section",
			input:     "# My Title\n\nsome content",
			wantLen:   1,
			wantTitle: "My Title",
		},
		{
			name:      "multiple h2 headers split into sections",
			input:     "## Section A\ncontent A\n## Section B\ncontent B",
			wantLen:   2,
			wantTitle: "Section A",
		},
		{
			name:      "empty content between headers skipped",
			input:     "## Empty\n\n## Has Content\nhere",
			wantLen:   1,
			wantTitle: "Has Content",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := splitIntoSections(tt.input)
			if len(got) != tt.wantLen {
				t.Fatalf("splitIntoSections() len = %d, want %d", len(got), tt.wantLen)
			}
			if got[0].title != tt.wantTitle {
				t.Errorf("splitIntoSections()[0].title = %q, want %q", got[0].title, tt.wantTitle)
			}
		})
	}
}

func TestDeterministicID(t *testing.T) {
	id1 := deterministicID("/path/file.md", "title", []byte("content"))
	id2 := deterministicID("/path/file.md", "title", []byte("content"))
	if id1 != id2 {
		t.Errorf("deterministicID not stable: %s != %s", id1, id2)
	}
	id3 := deterministicID("/path/file.md", "title", []byte("different"))
	if id1 == id3 {
		t.Error("deterministicID should differ for different content")
	}
	if !strings.HasPrefix(id1, "himp-") {
		t.Errorf("deterministicID prefix = %q, want 'himp-'", id1[:5])
	}
}

func TestDeriveNameFromPath(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"/home/.local/share/mimocode/memory/projects/abc123/MEMORY.md", "abc123 Memory"},
		{"/home/.local/share/mimocode/memory/global/MEMORY.md", "Global Memory"},
		{"/home/.local/share/mimocode/memory/projects/abc123/my-notes.md", "my-notes"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := deriveNameFromPath(tt.path)
			if got != tt.want {
				t.Errorf("deriveNameFromPath(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestImportMiMoCodeFile(t *testing.T) {
	// Create a temp MiMoCode memory structure
	tmpDir := t.TempDir()
	projDir := filepath.Join(tmpDir, "projects", "test-ws-123")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	memFile := filepath.Join(projDir, "MEMORY.md")
	content := "# Project Memory\n\n## Rules\n\nAlways do X.\n\n## Architecture\n\nUse Y.\n"
	if err := os.WriteFile(memFile, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	s := newMockStore()
	res := &ImportResult{Harness: HarnessMiMoCode}
	ctx := context.Background()

	if err := importMiMoCodeFile(ctx, s, memFile, tmpDir, res); err != nil {
		t.Fatalf("importMiMoCodeFile() error: %v", err)
	}
	if res.Imported != 2 {
		t.Errorf("imported = %d, want 2", res.Imported)
	}
	if res.Skipped != 0 {
		t.Errorf("skipped = %d, want 0", res.Skipped)
	}
	// Verify entries
	found := 0
	for _, e := range s.entries {
		if strings.Contains(e.Content, "Always do X") || strings.Contains(e.Content, "Use Y") {
			found++
		}
	}
	if found != 2 {
		t.Errorf("found %d entries with expected content, want 2", found)
	}
}

func TestImportIdempotent(t *testing.T) {
	tmpDir := t.TempDir()
	projDir := filepath.Join(tmpDir, "projects", "ws-1")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	memFile := filepath.Join(projDir, "MEMORY.md")
	content := "## Facts\n\nProject uses Go.\n"
	if err := os.WriteFile(memFile, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	s := newMockStore()
	ctx := context.Background()

	// First import
	res1 := &ImportResult{Harness: HarnessMiMoCode}
	if err := importMiMoCodeFile(ctx, s, memFile, tmpDir, res1); err != nil {
		t.Fatal(err)
	}
	if res1.Imported != 1 {
		t.Fatalf("first import: imported = %d, want 1", res1.Imported)
	}

	// Second import — should skip
	res2 := &ImportResult{Harness: HarnessMiMoCode}
	if err := importMiMoCodeFile(ctx, s, memFile, tmpDir, res2); err != nil {
		t.Fatal(err)
	}
	if res2.Skipped != 1 {
		t.Errorf("second import: skipped = %d, want 1", res2.Skipped)
	}
	if res2.Imported != 0 {
		t.Errorf("second import: imported = %d, want 0", res2.Imported)
	}
}

func TestDiscoverMiMoCodeFiles(t *testing.T) {
	tmpDir := t.TempDir()
	// Create global memory
	globalDir := filepath.Join(tmpDir, "global")
	if err := os.MkdirAll(globalDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(globalDir, "MEMORY.md"), []byte("global"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Create project memory
	projDir := filepath.Join(tmpDir, "projects", "ws-1")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projDir, "MEMORY.md"), []byte("project"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Create session checkpoint (should be excluded)
	sessDir := filepath.Join(tmpDir, "sessions", "sess-1")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessDir, "checkpoint.md"), []byte("checkpoint"), 0o644); err != nil {
		t.Fatal(err)
	}

	files, err := discoverMiMoCodeFiles(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Errorf("discovered %d files, want 2 (global + project, not checkpoint): %v", len(files), files)
	}
}

func TestImportAllEmpty(t *testing.T) {
	tmpDir := t.TempDir()
	s := newMockStore()
	ctx := context.Background()
	results, err := ImportAll(ctx, s, tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Errorf("ImportAll on empty dir = %d results, want 0", len(results))
	}
}
