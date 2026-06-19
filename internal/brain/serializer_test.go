package brain_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/brain"
	"github.com/don-works/mcplexer/internal/store"
)

// findTaskFile returns the single .md file the serializer wrote under the
// workspace tasks dir (fails if not exactly one).
func findTaskFile(t *testing.T, cfg brain.Config, ws string) string {
	t.Helper()
	wsDir, err := cfg.WorkspaceDir(ws)
	if err != nil {
		t.Fatalf("WorkspaceDir: %v", err)
	}
	dir := filepath.Join(wsDir, "tasks")
	matches, err := filepath.Glob(filepath.Join(dir, "*.md"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("want 1 task file, got %v (err %v)", matches, err)
	}
	return matches[0]
}

func TestWriteTask_AtomicAndSelfSuppressed(t *testing.T) {
	st := newStore(t)
	dir := t.TempDir()
	cfg := brain.Config{Enabled: true, Dir: dir}
	ix := brain.NewIndexer(cfg, st, nil)
	ser := brain.NewSerializer(cfg, st, nil)
	ser.ShareSelfWrites(ix)
	ctx := context.Background()

	task := &store.Task{ID: "01WRITE01", WorkspaceID: "ws", Title: "Write me", Status: "open", Description: "Prose."}
	if err := ser.WriteTask(ctx, task); err != nil {
		t.Fatalf("WriteTask: %v", err)
	}

	path := findTaskFile(t, cfg, "ws")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}
	if !strings.Contains(string(data), "title: Write me") {
		t.Errorf("file missing title:\n%s", data)
	}

	// The serializer marked the write self-induced; indexing it must be a
	// no-op (no task row created, since the row already conceptually
	// exists — here it does not, so a skip means the file's own write is
	// recognised). Verify no brain error + the index_files row carries the
	// self-write sha.
	if err := ix.IndexFile(ctx, path); err != nil {
		t.Fatalf("IndexFile after self-write: %v", err)
	}
	if _, err := st.GetTask(ctx, "01WRITE01"); err == nil {
		t.Error("self-write should have been suppressed (no inbound upsert)")
	}
}

// TestWriteTask_RoutesToRegisteredRepoDir is the federation/parity guard:
// when a workspace has a repo-local .mcplexer/ dir registered as canonical,
// a brand-new WriteTask must land the file UNDER that repo dir (tasks/), NOT
// in the central brain (workspaces/<ws>/). Before the fix the serializer was
// repo-unaware and every new record mis-routed to the central tree.
func TestWriteTask_RoutesToRegisteredRepoDir(t *testing.T) {
	st := newStore(t)
	central := t.TempDir()
	cfg := brain.Config{Enabled: true, Dir: central}
	ix := brain.NewIndexer(cfg, st, nil)
	ser := brain.NewSerializer(cfg, st, nil)
	ser.ShareSelfWrites(ix) // shares the indexer's dir registry too
	ctx := context.Background()

	repoDir := filepath.Join(t.TempDir(), brain.RepoBrainDirName)
	if err := ix.RegisterDir(ctx, repoDir, "acme-api", store.IndexSourceRepo); err != nil {
		t.Fatalf("RegisterDir: %v", err)
	}

	task := &store.Task{ID: "01REPO01", WorkspaceID: "acme-api", Title: "In repo", Status: "open"}
	if err := ser.WriteTask(ctx, task); err != nil {
		t.Fatalf("WriteTask: %v", err)
	}

	// File must be under the repo dir's tasks/, not the central tree.
	repoMatches, _ := filepath.Glob(filepath.Join(repoDir, "tasks", "*.md"))
	if len(repoMatches) != 1 {
		t.Fatalf("want 1 task file under repo dir, got %v", repoMatches)
	}
	wsDir, err := cfg.WorkspaceDir("acme-api")
	if err != nil {
		t.Fatalf("WorkspaceDir: %v", err)
	}
	centralMatches, _ := filepath.Glob(filepath.Join(wsDir, "tasks", "*.md"))
	if len(centralMatches) != 0 {
		t.Errorf("task wrongly mirrored into central brain: %v", centralMatches)
	}
	if !strings.HasPrefix(repoMatches[0], repoDir) {
		t.Errorf("task path %q not under repo dir %q", repoMatches[0], repoDir)
	}

	// The index_files row for the just-written file must record source=repo,
	// not the empty/default-central label. This is the regression guard for
	// the source-labeling bug: an outbound write that left Source="" got
	// coerced to central by UpsertIndexFile, clobbering the repo label until
	// the next inbound reindex reconciled it.
	f, err := st.GetIndexFile(ctx, repoMatches[0])
	if err != nil {
		t.Fatalf("GetIndexFile(%q): %v", repoMatches[0], err)
	}
	if f.Source != store.IndexSourceRepo {
		t.Errorf("index row source = %q, want %q", f.Source, store.IndexSourceRepo)
	}
}

// TestWriteTask_RecordsSourceForWorkspaceAndPath asserts the outbound write
// stamps the index_files row's source correctly for both a repo-backed and a
// central workspace — the field the federation layer relies on to keep
// outbound (Serializer) and inbound (Indexer) rows in agreement.
func TestWriteTask_RecordsSourceForWorkspaceAndPath(t *testing.T) {
	tests := []struct {
		name       string
		ws         string
		registered bool
		wantSource string
	}{
		{name: "repo-backed workspace records repo", ws: "acme-api", registered: true, wantSource: store.IndexSourceRepo},
		{name: "central workspace records central", ws: "plain-ws", registered: false, wantSource: store.IndexSourceCentral},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := newStore(t)
			central := t.TempDir()
			cfg := brain.Config{Enabled: true, Dir: central}
			ix := brain.NewIndexer(cfg, st, nil)
			ser := brain.NewSerializer(cfg, st, nil)
			ser.ShareSelfWrites(ix)
			ctx := context.Background()

			if tt.registered {
				repoDir := filepath.Join(t.TempDir(), brain.RepoBrainDirName)
				if err := ix.RegisterDir(ctx, repoDir, tt.ws, store.IndexSourceRepo); err != nil {
					t.Fatalf("RegisterDir: %v", err)
				}
			}

			task := &store.Task{ID: "01SRC0001", WorkspaceID: tt.ws, Title: "Source me", Status: "open"}
			if err := ser.WriteTask(ctx, task); err != nil {
				t.Fatalf("WriteTask: %v", err)
			}

			files, err := st.ListIndexFiles(ctx, "")
			if err != nil {
				t.Fatalf("ListIndexFiles: %v", err)
			}
			var got *store.IndexFile
			for i := range files {
				if files[i].EntityKind == brain.EntityKindTask && files[i].EntityID == task.ID {
					got = &files[i]
					break
				}
			}
			if got == nil {
				t.Fatalf("no index_files row for task %s", task.ID)
			}
			if got.Source != tt.wantSource {
				t.Errorf("index row source = %q, want %q", got.Source, tt.wantSource)
			}
		})
	}
}

func TestWriteTask_ConflictSidecar(t *testing.T) {
	st := newStore(t)
	dir := t.TempDir()
	cfg := brain.Config{Enabled: true, Dir: dir}
	ix := brain.NewIndexer(cfg, st, nil)
	ser := brain.NewSerializer(cfg, st, nil)
	ser.ShareSelfWrites(ix)
	ctx := context.Background()

	task := &store.Task{ID: "01CONF01", WorkspaceID: "ws", Title: "Conflict", Status: "open"}
	if err := ser.WriteTask(ctx, task); err != nil {
		t.Fatalf("initial WriteTask: %v", err)
	}
	path := findTaskFile(t, cfg, "ws")

	// Index the file so the index_files sha is recorded, then have a
	// "human" edit it out-of-band so the on-disk sha diverges.
	if err := ix.IndexFile(ctx, path); err != nil {
		t.Fatalf("index: %v", err)
	}
	humanEdit := []byte("---\nid: 01CONF01\nschema: task/v1\nworkspace: ws\ntitle: Human edited\nstatus: open\npinned: false\ncreated_at: 2026-06-03T10:00:00Z\nupdated_at: 2026-06-03T10:00:00Z\n---\n\nHuman body\n")
	if err := os.WriteFile(path, humanEdit, 0o644); err != nil {
		t.Fatalf("human edit: %v", err)
	}

	// Now an outbound write must NOT clobber — it writes a .conflict
	// sidecar and records a brain error, leaving the human file intact.
	task.Title = "Daemon wants this"
	if err := ser.WriteTask(ctx, task); err != nil {
		t.Fatalf("conflicting WriteTask: %v", err)
	}

	cur, _ := os.ReadFile(path)
	if !strings.Contains(string(cur), "Human edited") {
		t.Errorf("human edit was clobbered:\n%s", cur)
	}
	if _, err := os.Stat(path + ".conflict"); err != nil {
		t.Errorf("expected .conflict sidecar: %v", err)
	}
	errs, _ := st.ListBrainErrors(ctx)
	if len(errs) == 0 {
		t.Error("expected a brain_errors row for the conflict")
	}
}

func TestWriteTask_RoundTripThroughIndexer(t *testing.T) {
	st := newStore(t)
	dir := t.TempDir()
	cfg := brain.Config{Enabled: true, Dir: dir}
	ix := brain.NewIndexer(cfg, st, nil)
	ser := brain.NewSerializer(cfg, st, nil) // separate self-write set on purpose
	ctx := context.Background()

	task := &store.Task{
		ID: "01RT01", WorkspaceID: "ws", Title: "Round trip",
		Status: "doing", Priority: "high", Description: "The prose.",
	}
	if err := ser.WriteTask(ctx, task); err != nil {
		t.Fatalf("WriteTask: %v", err)
	}
	path := findTaskFile(t, cfg, "ws")

	// Simulate a human editing the serialized file (changes the on-disk
	// sha so the indexer's unchanged-skip fast-path does not fire), then
	// re-index: the edit must round-trip into the DB.
	edited, _ := brain.SerializeTask(&store.Task{
		ID: "01RT01", WorkspaceID: "ws", Title: "Round trip edited",
		Status: "review", Priority: "high",
	}, "Edited prose.")
	if err := os.WriteFile(path, edited, 0o644); err != nil {
		t.Fatalf("human edit: %v", err)
	}
	if err := ix.IndexFile(ctx, path); err != nil {
		t.Fatalf("IndexFile: %v", err)
	}
	got, err := st.GetTask(ctx, "01RT01")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.Title != "Round trip edited" || got.Status != "review" || got.Priority != "high" {
		t.Errorf("round-trip mismatch: %+v", got)
	}
	if got.Description != "Edited prose." {
		t.Errorf("description = %q", got.Description)
	}
}
