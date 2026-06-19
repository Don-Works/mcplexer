package brain_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/brain"
	"github.com/don-works/mcplexer/internal/store"
)

// waitFor polls cond up to timeout, returning true once it holds.
func waitFor(timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return cond()
}

func TestWatcher_IndexesOnWrite(t *testing.T) {
	st := newStore(t)
	cfg, tasksDir := brainRepo(t)
	ix := brain.NewIndexer(cfg, st, nil)
	w, err := brain.NewWatcher(ix, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := w.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = w.Close() }()

	task := &store.Task{ID: "01WATCH01", WorkspaceID: "ws", Title: "Watched", Status: "open"}
	writeTaskFile(t, tasksDir, task, "body")

	if !waitFor(3*time.Second, func() bool {
		_, err := st.GetTask(context.Background(), "01WATCH01")
		return err == nil
	}) {
		t.Fatal("watcher did not index the new task within timeout")
	}
}

func TestWatcher_DebounceCoalesces(t *testing.T) {
	st := &countingStore{Store: newStore(t)}
	cfg, tasksDir := brainRepo(t)
	ix := brain.NewIndexer(cfg, st, nil)
	w, err := brain.NewWatcher(ix, 200*time.Millisecond)
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := w.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = w.Close() }()

	path := filepath.Join(tasksDir, "01DEB01.md")
	// Rapid successive writes within the debounce window — should collapse
	// to a single index call (one Create, no repeated work).
	for i := 0; i < 4; i++ {
		data, _ := brain.SerializeTask(&store.Task{
			ID: "01DEB01", WorkspaceID: "ws", Title: "Debounced", Status: "open",
		}, "rev")
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
		time.Sleep(20 * time.Millisecond)
	}

	if !waitFor(3*time.Second, func() bool {
		_, err := st.GetTask(context.Background(), "01DEB01")
		return err == nil
	}) {
		t.Fatal("debounced task never indexed")
	}
	// Give any stray timers a beat, then assert exactly one create.
	time.Sleep(300 * time.Millisecond)
	if st.creates != 1 {
		t.Errorf("debounce did not coalesce: %d creates, want 1", st.creates)
	}
}

func TestWatcher_NewSubdirWatched(t *testing.T) {
	st := newStore(t)
	dir := t.TempDir()
	cfg := brain.Config{Enabled: true, Dir: dir}
	// Pre-create the workspaces root so the watcher has something to watch;
	// the per-workspace tasks dir is created AFTER Start to exercise the
	// dynamic subdir-add path.
	if err := os.MkdirAll(filepath.Join(dir, "workspaces"), 0o755); err != nil {
		t.Fatalf("mkdir workspaces: %v", err)
	}
	ix := brain.NewIndexer(cfg, st, nil)
	w, err := brain.NewWatcher(ix, 50*time.Millisecond)
	if err != nil {
		t.Fatalf("NewWatcher: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := w.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = w.Close() }()

	wsDir, err := cfg.WorkspaceDir("ws")
	if err != nil {
		t.Fatalf("WorkspaceDir: %v", err)
	}
	tasksDir := filepath.Join(wsDir, "tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatalf("mkdir new tasks dir: %v", err)
	}
	// Let the watcher observe + register the new subdirs.
	time.Sleep(200 * time.Millisecond)

	data, _ := brain.SerializeTask(&store.Task{
		ID: "01SUB01", WorkspaceID: "ws", Title: "In new subdir", Status: "open",
	}, "")
	if err := os.WriteFile(filepath.Join(tasksDir, "01SUB01.md"), data, 0o644); err != nil {
		t.Fatalf("write in new subdir: %v", err)
	}

	if !waitFor(3*time.Second, func() bool {
		_, err := st.GetTask(context.Background(), "01SUB01")
		return err == nil
	}) {
		t.Fatal("watcher did not index a file in a dynamically-created subdir")
	}
}
