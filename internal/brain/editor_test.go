package brain_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/brain"
	"github.com/don-works/mcplexer/internal/store"
)

// newEditor builds an Editor sharing the indexer's self-write set so the
// outbound write -> index_files binding behaves exactly like the live
// daemon. It also seeds a "ws" workspace so task/memory rows have a home.
func newEditor(t *testing.T) (*brain.Editor, store.Store, brain.Config, context.Context) {
	t.Helper()
	st := newStore(t)
	cfg, _ := brainRepo(t)
	ctx := context.Background()
	if err := st.CreateWorkspace(ctx, &store.Workspace{ID: "ws", Name: "Workspace"}); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	ix := brain.NewIndexer(cfg, st, nil)
	ser := brain.NewSerializer(cfg, st, nil)
	ser.ShareSelfWrites(ix)
	return brain.NewEditor(st, ser), st, cfg, ctx
}

func TestEditor_SaveTask_CreatesFileAndRow(t *testing.T) {
	ed, st, _, ctx := newEditor(t)

	saved, err := ed.SaveTask(ctx, brain.TaskRecord{
		Workspace:   "ws",
		Title:       "Fix the scheduler",
		Status:      "open",
		Tags:        []string{"bug", "scheduler", "bug"}, // dup dropped
		Description: "Cron jobs fire once.",
	}, nil)
	if err != nil {
		t.Fatalf("SaveTask: %v", err)
	}
	if saved.ID == "" {
		t.Fatal("expected a minted id")
	}
	if saved.Path == "" || !strings.HasSuffix(saved.Path, ".md") {
		t.Fatalf("expected a resolved .md path, got %q", saved.Path)
	}
	if len(saved.Tags) != 2 {
		t.Fatalf("expected deduped tags, got %v", saved.Tags)
	}

	// The DB row exists (FTS triggers fired through CreateTask).
	row, err := st.GetTask(ctx, saved.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if row.Title != "Fix the scheduler" || row.Status != "open" {
		t.Fatalf("row mismatch: %+v", row)
	}
	// The file is on disk.
	if _, err := os.Stat(saved.Path); err != nil {
		t.Fatalf("expected file at %s: %v", saved.Path, err)
	}
}

func TestEditor_SaveTask_UpdatePreservesStatusHistory(t *testing.T) {
	ed, st, _, ctx := newEditor(t)

	// Seed a task WITH status history directly so we can assert the editor
	// preserves server-owned fields on update.
	seed := &store.Task{
		ID:                "01EDITHIST0000000000000000",
		WorkspaceID:       "ws",
		Title:             "Original",
		Status:            "open",
		StatusHistoryJSON: []byte(`[{"at":"2026-06-03T10:00:00Z","evt":"created","to":"open"}]`),
	}
	if err := st.CreateTask(ctx, seed); err != nil {
		t.Fatalf("seed CreateTask: %v", err)
	}

	saved, err := ed.SaveTask(ctx, brain.TaskRecord{
		ID:        seed.ID,
		Workspace: "ws",
		Title:     "Renamed",
		Status:    "doing",
	}, nil)
	if err != nil {
		t.Fatalf("SaveTask update: %v", err)
	}
	if saved.Title != "Renamed" || saved.Status != "doing" {
		t.Fatalf("update not applied: %+v", saved)
	}
	row, _ := st.GetTask(ctx, seed.ID)
	if len(row.StatusHistoryJSON) == 0 || !strings.Contains(string(row.StatusHistoryJSON), "created") {
		t.Fatalf("status history not preserved: %s", row.StatusHistoryJSON)
	}
}

func TestEditor_SaveTask_ValidationRejectsBadStatus(t *testing.T) {
	ed, _, _, ctx := newEditor(t)
	cases := []struct {
		name  string
		rec   brain.TaskRecord
		vocab []string
		field string
	}{
		{"empty title", brain.TaskRecord{Workspace: "ws", Status: "open"}, nil, "title"},
		{"empty status", brain.TaskRecord{Workspace: "ws", Title: "X"}, nil, "status"},
		{"status not in vocab", brain.TaskRecord{Workspace: "ws", Title: "X", Status: "bogus"}, []string{"open", "done"}, "status"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ed.SaveTask(ctx, tc.rec, tc.vocab)
			var ve *brain.ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("expected ValidationError, got %v", err)
			}
			if ve.Field != tc.field {
				t.Fatalf("expected field %q, got %q", tc.field, ve.Field)
			}
		})
	}
}

func TestEditor_SaveTask_ConflictSurfaces(t *testing.T) {
	ed, st, _, ctx := newEditor(t)

	saved, err := ed.SaveTask(ctx, brain.TaskRecord{
		Workspace: "ws", Title: "First", Status: "open", Description: "v1",
	}, nil)
	if err != nil {
		t.Fatalf("initial SaveTask: %v", err)
	}

	// Simulate a concurrent human edit: overwrite the file so its sha no
	// longer matches the recorded index_files.sha. The next GUI save must
	// detect the CAS divergence and surface ErrConflict (the GUI's content
	// goes to a .conflict sidecar, the original is untouched).
	if err := os.WriteFile(saved.Path, []byte("--- hand edited, divergent ---\n"), 0o644); err != nil {
		t.Fatalf("simulate human edit: %v", err)
	}

	_, err = ed.SaveTask(ctx, brain.TaskRecord{
		ID: saved.ID, Workspace: "ws", Title: "Second", Status: "open", Description: "v2",
	}, nil)
	if !errors.Is(err, brain.ErrConflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
	// A .conflict sidecar was written.
	if _, sErr := os.Stat(saved.Path + ".conflict"); sErr != nil {
		t.Fatalf("expected .conflict sidecar: %v", sErr)
	}
	// A brain_errors row was recorded.
	errs, _ := st.ListBrainErrors(ctx)
	if len(errs) == 0 {
		t.Fatal("expected a brain_errors row for the conflict")
	}
}

// TestEditor_SaveTask_ConflictDoesNotPersistRow is the files-are-canonical
// finding guard: on a CAS conflict the DB row must NOT be updated to the
// GUI's value (the pre-check aborts BEFORE persisting). Before the fix the
// row was written first and the index diverged from the canonical .md until
// a reindex silently reverted it — losing the user's intent invisibly.
func TestEditor_SaveTask_ConflictDoesNotPersistRow(t *testing.T) {
	ed, st, _, ctx := newEditor(t)

	saved, err := ed.SaveTask(ctx, brain.TaskRecord{
		Workspace: "ws", Title: "Original", Status: "open", Description: "v1",
	}, nil)
	if err != nil {
		t.Fatalf("initial SaveTask: %v", err)
	}

	// Concurrent human edit diverges the file from the recorded sha.
	if err := os.WriteFile(saved.Path, []byte("--- hand edited ---\n"), 0o644); err != nil {
		t.Fatalf("simulate human edit: %v", err)
	}

	_, err = ed.SaveTask(ctx, brain.TaskRecord{
		ID: saved.ID, Workspace: "ws", Title: "GUI wants this", Status: "review", Description: "v2",
	}, nil)
	if !errors.Is(err, brain.ErrConflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}

	// The DB row must still hold the pre-conflict values — the index never
	// diverged from the canonical file.
	row, err := st.GetTask(ctx, saved.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if row.Title != "Original" || row.Status != "open" {
		t.Fatalf("DB row diverged on conflict: title=%q status=%q (want Original/open)", row.Title, row.Status)
	}
	// The GUI's intended content is preserved in the sidecar for reconcile.
	if _, sErr := os.Stat(saved.Path + ".conflict"); sErr != nil {
		t.Fatalf("expected .conflict sidecar holding GUI intent: %v", sErr)
	}
}

// TestEditor_SaveTask_ClearsStaleConflict guards the false-positive: a
// resolved CAS conflict (the _file brain_errors row from an earlier
// divergent save) must NOT make the next clean save report ErrConflict. A
// successful outbound write clears the stale error for the path.
func TestEditor_SaveTask_ClearsStaleConflict(t *testing.T) {
	ed, st, _, ctx := newEditor(t)

	saved, err := ed.SaveTask(ctx, brain.TaskRecord{
		Workspace: "ws", Title: "First", Status: "open", Description: "v1",
	}, nil)
	if err != nil {
		t.Fatalf("initial SaveTask: %v", err)
	}

	// Simulate a leftover conflict marker from an earlier divergent write.
	if rerr := st.RecordBrainError(ctx, &store.BrainError{
		Path: saved.Path, EntityKind: "task", Field: "_file", Reason: "stale conflict",
	}); rerr != nil {
		t.Fatalf("seed stale conflict: %v", rerr)
	}

	// A clean update (no on-disk divergence) must succeed without surfacing
	// the stale conflict, and must clear the marker.
	if _, err := ed.SaveTask(ctx, brain.TaskRecord{
		ID: saved.ID, Workspace: "ws", Title: "Second", Status: "open", Description: "v2",
	}, nil); err != nil {
		t.Fatalf("expected clean save after stale conflict cleared, got %v", err)
	}
	errs, _ := st.ListBrainErrors(ctx)
	for _, be := range errs {
		if be.Path == saved.Path && be.Field == "_file" {
			t.Fatalf("stale _file conflict not cleared by successful write: %+v", be)
		}
	}
}

func TestEditor_SaveMemory_RoundTripAndEntities(t *testing.T) {
	ed, st, _, ctx := newEditor(t)

	saved, err := ed.SaveMemory(ctx, brain.MemoryRecord{
		Kind:      "note",
		Name:      "deploy-hygiene",
		Workspace: "ws",
		Tags:      []string{"ops"},
		Content:   "Never deploy from a dirty tree.",
		Entities:  []brain.EntityLinkFM{{Kind: "workspace", ID: "ws", Role: "subject"}},
	})
	if err != nil {
		t.Fatalf("SaveMemory: %v", err)
	}
	if saved.ID == "" {
		t.Fatal("expected minted memory id")
	}

	links, err := st.ListMemoryEntities(ctx, saved.ID)
	if err != nil {
		t.Fatalf("ListMemoryEntities: %v", err)
	}
	if len(links) != 1 || links[0].EntityID != "ws" {
		t.Fatalf("expected one ws entity link, got %+v", links)
	}

	// Update dropping the entity link — the file/DB link set must shrink.
	if _, err := ed.SaveMemory(ctx, brain.MemoryRecord{
		ID: saved.ID, Kind: "note", Name: "deploy-hygiene", Workspace: "ws",
		Content: "Updated.", Entities: nil,
	}); err != nil {
		t.Fatalf("SaveMemory update: %v", err)
	}
	links, _ = st.ListMemoryEntities(ctx, saved.ID)
	if len(links) != 0 {
		t.Fatalf("expected entity link removed, got %+v", links)
	}
}

// TestEditor_SaveMemory_ReEmbedHook proves the re-embed-on-edit hook
// (wave-B2 item 3): editing a memory's CONTENT fires the hook (so the memory
// Service can rebuild the vector store.UpdateMemory dropped), while a
// metadata-only edit (content unchanged) does NOT — its vector is still
// valid, so re-embedding would be wasted work.
func TestEditor_SaveMemory_ReEmbedHook(t *testing.T) {
	ed, _, _, ctx := newEditor(t)
	var fired []string
	ed.SetReEmbedHook(func(_ context.Context, e *store.MemoryEntry) {
		fired = append(fired, e.ID)
	})

	saved, err := ed.SaveMemory(ctx, brain.MemoryRecord{
		Kind: "note", Name: "n", Workspace: "ws", Content: "original body text",
	})
	if err != nil {
		t.Fatalf("SaveMemory create: %v", err)
	}
	if len(fired) != 0 {
		t.Fatalf("create must not fire re-embed hook (insert path), got %v", fired)
	}

	// Content-changing update → hook fires.
	if _, err := ed.SaveMemory(ctx, brain.MemoryRecord{
		ID: saved.ID, Kind: "note", Name: "n", Workspace: "ws",
		Content: "completely different body text",
	}); err != nil {
		t.Fatalf("SaveMemory content edit: %v", err)
	}
	if len(fired) != 1 || fired[0] != saved.ID {
		t.Fatalf("content edit must fire re-embed hook once for %s, got %v", saved.ID, fired)
	}

	// Metadata-only update (same content) → hook does NOT fire again.
	if _, err := ed.SaveMemory(ctx, brain.MemoryRecord{
		ID: saved.ID, Kind: "note", Name: "n-renamed", Workspace: "ws",
		Content: "completely different body text", Tags: []string{"x"},
	}); err != nil {
		t.Fatalf("SaveMemory metadata edit: %v", err)
	}
	if len(fired) != 1 {
		t.Fatalf("metadata-only edit must NOT fire re-embed hook, got %v", fired)
	}
}

func TestEditor_TreeAndList(t *testing.T) {
	ed, _, _, ctx := newEditor(t)

	if _, err := ed.SaveTask(ctx, brain.TaskRecord{Workspace: "ws", Title: "T1", Status: "open"}, nil); err != nil {
		t.Fatalf("seed task: %v", err)
	}
	if _, err := ed.SaveMemory(ctx, brain.MemoryRecord{Kind: "note", Name: "m1", Workspace: "ws", Content: "x"}); err != nil {
		t.Fatalf("seed memory: %v", err)
	}

	nodes, err := ed.Tree(ctx)
	if err != nil {
		t.Fatalf("Tree: %v", err)
	}
	var found *brain.TreeNode
	for i := range nodes {
		if nodes[i].Workspace == "ws" {
			found = &nodes[i]
		}
	}
	if found == nil {
		t.Fatal("ws node missing from tree")
	}
	if found.TaskCount != 1 || found.MemoryCount != 1 {
		t.Fatalf("expected 1 task + 1 memory, got %d/%d", found.TaskCount, found.MemoryCount)
	}

	tasks, err := ed.ListTasks(ctx, "ws")
	if err != nil || len(tasks) != 1 {
		t.Fatalf("ListTasks: %v len=%d", err, len(tasks))
	}
	mems, err := ed.ListMemories(ctx, "ws")
	if err != nil || len(mems) != 1 {
		t.Fatalf("ListMemories: %v len=%d", err, len(mems))
	}
}

func TestEditor_ListTasksKeepsNewestValidationError(t *testing.T) {
	ed, st, _, ctx := newEditor(t)

	saved, err := ed.SaveTask(ctx, brain.TaskRecord{Workspace: "ws", Title: "T1", Status: "open"}, nil)
	if err != nil {
		t.Fatalf("seed task: %v", err)
	}
	if err := st.RecordBrainError(ctx, &store.BrainError{
		Path:      saved.Path,
		Field:     "title",
		Reason:    "old title error",
		CreatedAt: time.Unix(100, 0),
	}); err != nil {
		t.Fatalf("seed old error: %v", err)
	}
	if err := st.RecordBrainError(ctx, &store.BrainError{
		Path:      saved.Path,
		Field:     "status",
		Reason:    "new status error",
		CreatedAt: time.Unix(200, 0),
	}); err != nil {
		t.Fatalf("seed new error: %v", err)
	}

	tasks, err := ed.ListTasks(ctx, "ws")
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(tasks) != 1 {
		t.Fatalf("ListTasks: want one task, got %d", len(tasks))
	}
	if tasks[0].ValidationError != "new status error" || tasks[0].ValidationField != "status" {
		t.Fatalf("ListTasks kept wrong validation error: %+v", tasks[0])
	}
}

func TestEditor_NoSerializer_FailsClosed(t *testing.T) {
	st := newStore(t)
	ed := brain.NewEditor(st, nil)
	if _, err := ed.SaveTask(context.Background(), brain.TaskRecord{Workspace: "ws", Title: "x", Status: "open"}, nil); err == nil {
		t.Fatal("expected error when serializer is nil (brain disabled)")
	}
}
