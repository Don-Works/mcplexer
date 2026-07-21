package brain_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/brain"
	"github.com/don-works/mcplexer/internal/store"
)

// writeNestedMemoryFile serializes a memory into an (illegitimate) nested
// path under the workspace memory dir, simulating the pre-slugify bug where
// a raw name containing '/' materialised as a subdirectory.
func writeNestedMemoryFile(t *testing.T, cfg brain.Config, relPath string, m *store.MemoryEntry) string {
	t.Helper()
	data, err := brain.SerializeMemory(m, nil)
	if err != nil {
		t.Fatalf("SerializeMemory: %v", err)
	}
	wsDir, err := cfg.WorkspaceDir("ws")
	if err != nil {
		t.Fatalf("WorkspaceDir: %v", err)
	}
	path := filepath.Join(wsDir, "memory", filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write nested: %v", err)
	}
	return path
}

// TestReindexAll_RepairsNestedMemoryFile verifies the recovery sweep: a
// mis-pathed memory file under an unexpected subdirectory is relocated to a
// sanitized flat filename, re-indexed, and the empty subdirectory pruned.
func TestReindexAll_RepairsNestedMemoryFile(t *testing.T) {
	st := newStore(t)
	cfg, _ := brainRepo(t)
	ix := brain.NewIndexer(cfg, st, nil)
	ctx := context.Background()

	now := time.Now().UTC()
	nested := writeNestedMemoryFile(t, cfg, "notes/clean-note.md", &store.MemoryEntry{
		ID: "01cleannote01", Name: "clean-note", Kind: store.MemoryKindNote,
		WorkspaceID: strptr("ws"), Content: "recovered body",
		CreatedAt: now, UpdatedAt: now,
	})

	if err := ix.ReindexAll(ctx); err != nil {
		t.Fatalf("ReindexAll: %v", err)
	}

	wsDir, err := cfg.WorkspaceDir("ws")
	if err != nil {
		t.Fatalf("WorkspaceDir: %v", err)
	}
	memDir := filepath.Join(wsDir, "memory")
	flat := filepath.Join(memDir, "clean-note.md")
	if _, err := os.Stat(flat); err != nil {
		t.Fatalf("repaired file missing at %s: %v", flat, err)
	}
	if _, err := os.Stat(nested); !os.IsNotExist(err) {
		t.Fatalf("nested original still present: %v", err)
	}
	if _, err := os.Stat(filepath.Dir(nested)); !os.IsNotExist(err) {
		t.Fatalf("emptied nested dir not pruned: %v", err)
	}
	// The record is indexed (it was permanently invisible before the sweep).
	got, err := st.GetMemory(ctx, "01cleannote01")
	if err != nil {
		t.Fatalf("repaired memory not indexed: %v", err)
	}
	if got.Content != "recovered body" {
		t.Errorf("content = %q, want recovered body", got.Content)
	}
	if _, err := st.GetIndexFile(ctx, flat); err != nil {
		t.Errorf("no index_files row for repaired path: %v", err)
	}
}

// TestReindexAll_RepairedSlashyNameSurfacesLoudly covers the live incident
// shape: the nested file's frontmatter name itself contains '/'. The sweep
// relocates it flat (so it is no longer invisible), and the inbound
// validator rejects the unsafe name LOUDLY via a brain_errors row instead
// of silently skipping it.
func TestReindexAll_RepairedSlashyNameSurfacesLoudly(t *testing.T) {
	st := newStore(t)
	cfg, _ := brainRepo(t)
	ix := brain.NewIndexer(cfg, st, nil)
	ctx := context.Background()

	now := time.Now().UTC()
	hostile := "Brain cross-machine sync: canonical remote = example/memory-repo (private)"
	writeNestedMemoryFile(t, cfg,
		"Brain cross-machine sync: canonical remote = example/memory-repo (private).md",
		&store.MemoryEntry{
			ID: "01slashy01", Name: hostile, Kind: store.MemoryKindNote,
			WorkspaceID: strptr("ws"), Content: "remote info",
			CreatedAt: now, UpdatedAt: now,
		})

	if err := ix.ReindexAll(ctx); err != nil {
		t.Fatalf("ReindexAll: %v", err)
	}

	wsDir, err := cfg.WorkspaceDir("ws")
	if err != nil {
		t.Fatalf("WorkspaceDir: %v", err)
	}
	memDir := filepath.Join(wsDir, "memory")
	entries, err := os.ReadDir(memDir)
	if err != nil {
		t.Fatalf("read memory dir: %v", err)
	}
	var repaired string
	for _, e := range entries {
		if e.IsDir() {
			t.Fatalf("nested dir survived the repair sweep: %s", e.Name())
		}
		if strings.HasPrefix(e.Name(), "brain-cross-machine-sync") {
			repaired = filepath.Join(memDir, e.Name())
		}
	}
	if repaired == "" {
		t.Fatalf("no repaired flat file found in %s", memDir)
	}
	// Loud, not silent: the unsafe frontmatter name is a recorded error.
	errs, err := st.ListBrainErrors(ctx)
	if err != nil {
		t.Fatalf("ListBrainErrors: %v", err)
	}
	found := false
	for _, be := range errs {
		if filepath.Clean(be.Path) == filepath.Clean(repaired) && be.Field == "name" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a 'name' brain_error for %s, got: %+v", repaired, errs)
	}
}

// TestReindexAll_FactsSubdirIsNotRepaired guards the sweep's allowlist: the
// legitimate facts/ subdir under memory/ must be left alone.
func TestReindexAll_FactsSubdirIsNotRepaired(t *testing.T) {
	st := newStore(t)
	cfg, _ := brainRepo(t)
	ix := brain.NewIndexer(cfg, st, nil)
	ctx := context.Background()

	now := time.Now().UTC()
	factPath := writeNestedMemoryFile(t, cfg, "facts/primary-stack.md", &store.MemoryEntry{
		ID: "01factok01", Name: "primary-stack", Kind: store.MemoryKindFact,
		WorkspaceID: strptr("ws"), Content: "Go + TS",
		TValidStart: now, CreatedAt: now, UpdatedAt: now,
	})

	if err := ix.ReindexAll(ctx); err != nil {
		t.Fatalf("ReindexAll: %v", err)
	}
	if _, err := os.Stat(factPath); err != nil {
		t.Fatalf("facts/ file was disturbed by the sweep: %v", err)
	}
	if _, err := st.GetMemory(ctx, "01factok01"); err != nil {
		t.Fatalf("fact not indexed in place: %v", err)
	}
}

// TestReindexAll_WritesVaultIndex verifies the Obsidian-readability bridge:
// a full reindex generates <Dir>/INDEX.md mapping each workspace dir to its
// display name + counts with relative links — and the generated file is
// never itself indexed as a record.
func TestReindexAll_WritesVaultIndex(t *testing.T) {
	st := newStore(t)
	cfg, tasksDir := brainRepo(t)
	ix := brain.NewIndexer(cfg, st, nil)
	ctx := context.Background()

	if err := st.CreateWorkspace(ctx, &store.Workspace{ID: "ws", Name: "My Workspace"}); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	for _, id := range []string{"01VIA", "01VIB"} {
		writeTaskFile(t, tasksDir, &store.Task{ID: id, WorkspaceID: "ws", Title: id, Status: "open"}, "")
	}

	if err := ix.ReindexAll(ctx); err != nil {
		t.Fatalf("ReindexAll: %v", err)
	}

	idxPath := filepath.Join(cfg.Dir, "INDEX.md")
	data, err := os.ReadFile(idxPath)
	if err != nil {
		t.Fatalf("INDEX.md not generated: %v", err)
	}
	body := string(data)
	for _, want := range []string{"My Workspace", "[ws](workspaces/ws/)", "| 2 |"} {
		if !strings.Contains(body, want) {
			t.Errorf("INDEX.md missing %q:\n%s", want, body)
		}
	}
	// The generated projection must never be indexed as a record.
	if err := ix.IndexFile(ctx, idxPath); err != nil {
		t.Fatalf("IndexFile(INDEX.md) should be a no-op, got: %v", err)
	}
	if _, err := st.GetIndexFile(ctx, idxPath); err == nil {
		t.Fatal("INDEX.md was indexed as a record")
	}
	// Refresh is idempotent: a second reindex leaves identical content.
	if err := ix.ReindexAll(ctx); err != nil {
		t.Fatalf("second ReindexAll: %v", err)
	}
	again, err := os.ReadFile(idxPath)
	if err != nil {
		t.Fatalf("INDEX.md vanished on refresh: %v", err)
	}
	if string(again) != body {
		t.Errorf("INDEX.md not stable across reindex:\n--- first ---\n%s\n--- second ---\n%s", body, again)
	}
}
