package brain_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/don-works/mcplexer/internal/brain"
	"github.com/don-works/mcplexer/internal/store"
)

func TestVerify_NoDrift(t *testing.T) {
	st := newStore(t)
	cfg, tasksDir := brainRepo(t)
	ix := brain.NewIndexer(cfg, st, nil)
	ctx := context.Background()

	task := &store.Task{ID: "01VER01", WorkspaceID: "ws", Title: "Verify", Status: "open", Description: "p"}
	path := writeTaskFile(t, tasksDir, task, "p")
	if err := ix.IndexFile(ctx, path); err != nil {
		t.Fatalf("IndexFile: %v", err)
	}

	rep, err := brain.Verify(ctx, cfg, st)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !rep.OK() {
		t.Errorf("expected no drift, got %+v", rep.Drifts)
	}
	if rep.FilesChecked != 1 {
		t.Errorf("FilesChecked = %d, want 1", rep.FilesChecked)
	}
}

func TestVerify_DetectsMissingRow(t *testing.T) {
	st := newStore(t)
	cfg, tasksDir := brainRepo(t)
	ix := brain.NewIndexer(cfg, st, nil)
	ctx := context.Background()

	task := &store.Task{ID: "01VER02", WorkspaceID: "ws", Title: "Verify", Status: "open"}
	path := writeTaskFile(t, tasksDir, task, "")
	if err := ix.IndexFile(ctx, path); err != nil {
		t.Fatalf("IndexFile: %v", err)
	}

	// Soft-delete the row out from under the index so the file references a
	// row that Verify can no longer find.
	if err := st.SoftDeleteTask(ctx, "01VER02"); err != nil {
		t.Fatalf("SoftDeleteTask: %v", err)
	}

	rep, err := brain.Verify(ctx, cfg, st)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if rep.OK() {
		t.Fatal("expected drift for the missing row")
	}
	if rep.Drifts[0].Kind != brain.DriftMissingRow {
		t.Errorf("drift kind = %q, want %q", rep.Drifts[0].Kind, brain.DriftMissingRow)
	}
}

// TestVerify_CoversMemory is the parity-gate finding guard: Verify must
// diff memory rows, not just tasks. An indexed memory whose DB content was
// mutated out-of-band must surface as content drift (before the fix Verify
// skipped every non-task entity, making ParityOK hollow).
func TestVerify_CoversMemory(t *testing.T) {
	st := newStore(t)
	cfg, _ := brainRepo(t)
	ix := brain.NewIndexer(cfg, st, nil)
	ctx := context.Background()

	m := &store.MemoryEntry{
		ID: "01VERMEM", Name: "pref", Kind: store.MemoryKindNote,
		WorkspaceID: strptr("ws"), Content: "likes dark mode",
	}
	path := writeMemoryFile(t, cfg, m)
	if err := ix.IndexFile(ctx, path); err != nil {
		t.Fatalf("IndexFile: %v", err)
	}

	// No drift right after indexing.
	rep, err := brain.Verify(ctx, cfg, st)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !rep.OK() {
		t.Fatalf("expected no drift, got %+v", rep.Drifts)
	}
	if rep.FilesChecked != 1 {
		t.Errorf("FilesChecked = %d, want 1 (memory must be checked)", rep.FilesChecked)
	}

	// Mutate the DB row so it diverges from the file → content drift.
	m.Content = "likes light mode"
	if err := st.UpdateMemory(ctx, m); err != nil {
		t.Fatalf("UpdateMemory: %v", err)
	}
	rep, err = brain.Verify(ctx, cfg, st)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if rep.OK() {
		t.Fatal("expected content drift for the mutated memory row")
	}
	if rep.Drifts[0].Kind != brain.DriftContentMismatch {
		t.Errorf("drift kind = %q, want %q", rep.Drifts[0].Kind, brain.DriftContentMismatch)
	}
}

// TestVerify_CoversWorkspace verifies workspace.md rows are diffed too.
func TestVerify_CoversWorkspace(t *testing.T) {
	st := newStore(t)
	cfg, _ := brainRepo(t)
	ix := brain.NewIndexer(cfg, st, nil)
	ctx := context.Background()

	w := &store.Workspace{ID: "ws", Name: "Acme", RootPath: "/code/acme"}
	data, err := brain.SerializeWorkspace(w)
	if err != nil {
		t.Fatalf("SerializeWorkspace: %v", err)
	}
	wsDir, err := cfg.WorkspaceDir("ws")
	if err != nil {
		t.Fatalf("WorkspaceDir: %v", err)
	}
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(wsDir, "workspace.md")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ix.IndexFile(ctx, path); err != nil {
		t.Fatalf("IndexFile: %v", err)
	}

	rep, err := brain.Verify(ctx, cfg, st)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if !rep.OK() {
		t.Fatalf("expected no drift, got %+v", rep.Drifts)
	}
	if rep.FilesChecked != 1 {
		t.Errorf("FilesChecked = %d, want 1 (workspace must be checked)", rep.FilesChecked)
	}
}

func TestVerify_DetectsMissingFile(t *testing.T) {
	st := newStore(t)
	cfg, tasksDir := brainRepo(t)
	ix := brain.NewIndexer(cfg, st, nil)
	ctx := context.Background()

	task := &store.Task{ID: "01VER03", WorkspaceID: "ws", Title: "Verify", Status: "open"}
	path := writeTaskFile(t, tasksDir, task, "")
	if err := ix.IndexFile(ctx, path); err != nil {
		t.Fatalf("IndexFile: %v", err)
	}
	// Delete the file but leave the index_files row.
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove: %v", err)
	}

	rep, err := brain.Verify(ctx, cfg, st)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if rep.OK() || rep.Drifts[0].Kind != brain.DriftMissingFile {
		t.Errorf("expected missing_file drift, got %+v", rep.Drifts)
	}
}
