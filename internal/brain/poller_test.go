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

func startPoller(t *testing.T, ix *brain.Indexer) *brain.Poller {
	t.Helper()
	p := brain.NewPoller(ix, 50*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := p.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p
}

func TestPoller_IndexesOnExternalWrite(t *testing.T) {
	st := newStore(t)
	cfg, tasksDir := brainRepo(t)
	ix := brain.NewIndexer(cfg, st, nil)
	startPoller(t, ix)

	task := &store.Task{ID: "01POLL01", WorkspaceID: "ws", Title: "Polled", Status: "open"}
	writeTaskFile(t, tasksDir, task, "body")

	if !waitFor(3*time.Second, func() bool {
		_, err := st.GetTask(context.Background(), "01POLL01")
		return err == nil
	}) {
		t.Fatal("poller did not index the new task within timeout")
	}
}

func TestPoller_BaselineDoesNotReindexExisting(t *testing.T) {
	st := &countingStore{Store: newStore(t)}
	cfg, tasksDir := brainRepo(t)
	ix := brain.NewIndexer(cfg, st, nil)

	// Pre-existing content belongs to the startup reindex sweep, not the
	// poller: files present before Start must not be re-indexed by ticks.
	task := &store.Task{ID: "01PRE01", WorkspaceID: "ws", Title: "Existing", Status: "open"}
	writeTaskFile(t, tasksDir, task, "body")

	startPoller(t, ix)
	time.Sleep(250 * time.Millisecond) // several sweep intervals
	if st.creates != 0 {
		t.Errorf("baseline re-indexed existing content: %d creates, want 0", st.creates)
	}
}

func TestPoller_NewSubdirSwept(t *testing.T) {
	st := newStore(t)
	dir := t.TempDir()
	cfg := brain.Config{Enabled: true, Dir: dir}
	if err := os.MkdirAll(filepath.Join(dir, "workspaces"), 0o755); err != nil {
		t.Fatalf("mkdir workspaces: %v", err)
	}
	ix := brain.NewIndexer(cfg, st, nil)
	startPoller(t, ix)

	// Directories created after Start need no dynamic registration — the
	// sweep's WalkDir sees them naturally.
	wsDir, err := cfg.WorkspaceDir("ws")
	if err != nil {
		t.Fatalf("WorkspaceDir: %v", err)
	}
	tasksDir := filepath.Join(wsDir, "tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatalf("mkdir new tasks dir: %v", err)
	}
	data, _ := brain.SerializeTask(&store.Task{
		ID: "01PSUB01", WorkspaceID: "ws", Title: "In new subdir", Status: "open",
	}, "")
	if err := os.WriteFile(filepath.Join(tasksDir, "01PSUB01.md"), data, 0o644); err != nil {
		t.Fatalf("write in new subdir: %v", err)
	}

	if !waitFor(3*time.Second, func() bool {
		_, err := st.GetTask(context.Background(), "01PSUB01")
		return err == nil
	}) {
		t.Fatal("poller did not index a file in a post-start subdir")
	}
}
