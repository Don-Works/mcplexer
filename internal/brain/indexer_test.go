package brain_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/don-works/mcplexer/internal/brain"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

// newStore opens a fresh on-disk SQLite store in a temp dir (real
// migrations run) and returns it as a store.Store.
func newStore(t *testing.T) store.Store {
	t.Helper()
	db, err := sqlite.New(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// brainRepo creates a brain dir skeleton under a temp dir and returns the
// Config + the workspace's tasks dir. The workspace slug is "ws".
func brainRepo(t *testing.T) (brain.Config, string) {
	t.Helper()
	dir := t.TempDir()
	cfg := brain.Config{Enabled: true, Dir: dir}
	wsDir, err := cfg.WorkspaceDir("ws")
	if err != nil {
		t.Fatalf("WorkspaceDir: %v", err)
	}
	tasksDir := filepath.Join(wsDir, "tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatalf("mkdir tasks: %v", err)
	}
	return cfg, tasksDir
}

// writeTaskFile serializes a task into the tasks dir and returns its path.
func writeTaskFile(t *testing.T, tasksDir string, task *store.Task, body string) string {
	t.Helper()
	data, err := brain.SerializeTask(task, body)
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}
	name := task.ID + ".md"
	path := filepath.Join(tasksDir, name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	return path
}

func TestIndexFile_NewTask(t *testing.T) {
	st := newStore(t)
	cfg, tasksDir := brainRepo(t)
	ix := brain.NewIndexer(cfg, st, nil)
	ctx := context.Background()

	task := &store.Task{ID: "01TASKAAA", WorkspaceID: "ws", Title: "Hello", Status: "open"}
	path := writeTaskFile(t, tasksDir, task, "Body text.")

	if err := ix.IndexFile(ctx, path); err != nil {
		t.Fatalf("IndexFile: %v", err)
	}

	got, err := st.GetTask(ctx, "01TASKAAA")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.Title != "Hello" || got.Status != "open" {
		t.Errorf("task mismatch: %+v", got)
	}
	if got.Description != "Body text." {
		t.Errorf("description = %q", got.Description)
	}

	// index_files row recorded.
	f, err := st.GetIndexFile(ctx, path)
	if err != nil {
		t.Fatalf("GetIndexFile: %v", err)
	}
	if f.EntityKind != "task" || f.EntityID != "01TASKAAA" {
		t.Errorf("index file row mismatch: %+v", f)
	}
}

func TestIndexFile_UnchangedSkips(t *testing.T) {
	st := &countingStore{Store: newStore(t)}
	cfg, tasksDir := brainRepo(t)
	ix := brain.NewIndexer(cfg, st, nil)
	ctx := context.Background()

	task := &store.Task{ID: "01TASKBBB", WorkspaceID: "ws", Title: "Skip", Status: "open"}
	path := writeTaskFile(t, tasksDir, task, "")

	if err := ix.IndexFile(ctx, path); err != nil {
		t.Fatalf("first IndexFile: %v", err)
	}
	creates := st.creates
	updates := st.updates

	// Re-index the identical bytes — must skip (no Create/Update).
	if err := ix.IndexFile(ctx, path); err != nil {
		t.Fatalf("second IndexFile: %v", err)
	}
	if st.creates != creates || st.updates != updates {
		t.Errorf("unchanged file triggered a store write: creates %d->%d updates %d->%d",
			creates, st.creates, updates, st.updates)
	}
}

func TestIndexFile_Malformed_RecordsError(t *testing.T) {
	st := newStore(t)
	cfg, tasksDir := brainRepo(t)
	ix := brain.NewIndexer(cfg, st, nil)
	ctx := context.Background()

	// Frontmatter with a blank title fails ValidateTask.
	bad := "---\nid: 01BAD\nschema: task/v1\nworkspace: ws\ntitle: \"\"\nstatus: open\npinned: false\ncreated_at: 2026-06-03T10:00:00Z\nupdated_at: 2026-06-03T10:00:00Z\n---\n\nbody\n"
	path := filepath.Join(tasksDir, "01BAD.md")
	if err := os.WriteFile(path, []byte(bad), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := ix.IndexFile(ctx, path); err == nil {
		t.Fatal("expected validation error")
	}

	// No task row.
	if _, err := st.GetTask(ctx, "01BAD"); err == nil {
		t.Error("malformed file should not have created a task row")
	}
	// brain_errors recorded.
	errs, err := st.ListBrainErrors(ctx)
	if err != nil {
		t.Fatalf("ListBrainErrors: %v", err)
	}
	if len(errs) == 0 {
		t.Fatal("expected a brain_errors row")
	}
	if errs[0].Field != "title" {
		t.Errorf("error field = %q, want title", errs[0].Field)
	}
}

func TestReindexAll(t *testing.T) {
	st := newStore(t)
	cfg, tasksDir := brainRepo(t)
	ix := brain.NewIndexer(cfg, st, nil)
	ctx := context.Background()

	for _, id := range []string{"01AAA", "01BBB", "01CCC"} {
		writeTaskFile(t, tasksDir, &store.Task{ID: id, WorkspaceID: "ws", Title: id, Status: "open"}, "")
	}

	if err := ix.ReindexAll(ctx); err != nil {
		t.Fatalf("ReindexAll: %v", err)
	}
	files, err := st.ListIndexFiles(ctx, "ws")
	if err != nil {
		t.Fatalf("ListIndexFiles: %v", err)
	}
	if len(files) != 3 {
		t.Fatalf("indexed %d files, want 3", len(files))
	}
}

// TestReindexAll_PrunesOrphanedErrors verifies the self-heal for brain_errors
// recorded against paths outside the brain root (e.g. the gateway data dir's
// memory-exports/, briefly mis-indexed as tasks). A reindex sweep must drain
// the orphaned row while leaving an error for a real in-brain record intact.
func TestReindexAll_PrunesOrphanedErrors(t *testing.T) {
	st := newStore(t)
	cfg, tasksDir := brainRepo(t)
	ix := brain.NewIndexer(cfg, st, nil)
	ctx := context.Background()

	// An orphaned error: a path OUTSIDE the brain Dir (simulating
	// ~/.mcplexer/memory-exports/workspace-x.md indexed as a phantom task).
	orphan := filepath.Join(t.TempDir(), "memory-exports", "workspace-x.md")
	if err := st.RecordBrainError(ctx, &store.BrainError{
		Path: orphan, EntityKind: "task", Reason: "brain: parse task: not found",
	}); err != nil {
		t.Fatalf("seed orphan error: %v", err)
	}
	// A legitimate in-brain error: a path UNDER the brain Dir.
	inBrain := filepath.Join(tasksDir, "01BAD-bad.md")
	if err := st.RecordBrainError(ctx, &store.BrainError{
		Path: inBrain, EntityKind: "task", Field: "id", Reason: "must not be empty",
	}); err != nil {
		t.Fatalf("seed in-brain error: %v", err)
	}

	if err := ix.ReindexAll(ctx); err != nil {
		t.Fatalf("ReindexAll: %v", err)
	}

	errs, err := st.ListBrainErrors(ctx)
	if err != nil {
		t.Fatalf("ListBrainErrors: %v", err)
	}
	if len(errs) != 1 {
		t.Fatalf("after prune got %d errors, want 1 (in-brain only): %+v", len(errs), errs)
	}
	if filepath.Clean(errs[0].Path) != filepath.Clean(inBrain) {
		t.Errorf("wrong error survived prune: got %q, want %q", errs[0].Path, inBrain)
	}
}

func TestRemoveFile_SoftDeletes(t *testing.T) {
	st := newStore(t)
	cfg, tasksDir := brainRepo(t)
	ix := brain.NewIndexer(cfg, st, nil)
	ctx := context.Background()

	task := &store.Task{ID: "01DELME", WorkspaceID: "ws", Title: "DelMe", Status: "open"}
	path := writeTaskFile(t, tasksDir, task, "")
	if err := ix.IndexFile(ctx, path); err != nil {
		t.Fatalf("IndexFile: %v", err)
	}

	if err := os.Remove(path); err != nil {
		t.Fatalf("remove file: %v", err)
	}
	if err := ix.RemoveFile(ctx, path); err != nil {
		t.Fatalf("RemoveFile: %v", err)
	}

	// Task row soft-deleted — GetTask filters deleted rows, so it now
	// returns ErrNotFound.
	if _, err := st.GetTask(ctx, "01DELME"); err == nil {
		t.Error("task should be soft-deleted (GetTask should return ErrNotFound)")
	}
	// index_files row gone.
	if _, err := st.GetIndexFile(ctx, path); err == nil {
		t.Error("index_files row should be removed")
	}
}

// countingStore wraps a store.Store and counts CreateTask/UpdateTask
// calls so the unchanged-skip fast-path can be asserted.
type countingStore struct {
	store.Store
	creates int
	updates int
}

func (c *countingStore) CreateTask(ctx context.Context, t *store.Task) error {
	c.creates++
	return c.Store.CreateTask(ctx, t)
}

func (c *countingStore) UpdateTask(ctx context.Context, t *store.Task) error {
	c.updates++
	return c.Store.UpdateTask(ctx, t)
}

func (c *countingStore) ClaimTask(ctx context.Context, t *store.Task, sessionID string) error {
	c.updates++
	return c.Store.ClaimTask(ctx, t, sessionID)
}
