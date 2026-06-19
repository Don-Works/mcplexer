package brain_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/brain"
	"github.com/don-works/mcplexer/internal/store"
)

// writeMemoryFile serializes a memory into its canonical dir and returns
// the path. notes → memory/<name>.md, facts → memory/facts/<name>.md.
func writeMemoryFile(t *testing.T, cfg brain.Config, m *store.MemoryEntry) string {
	t.Helper()
	data, err := brain.SerializeMemory(m, nil)
	if err != nil {
		t.Fatalf("serialize memory: %v", err)
	}
	ws := "global"
	if m.WorkspaceID != nil {
		ws = *m.WorkspaceID
	}
	wsDir, err := cfg.WorkspaceDir(ws)
	if err != nil {
		t.Fatalf("WorkspaceDir: %v", err)
	}
	dir := filepath.Join(wsDir, "memory")
	if m.Kind == store.MemoryKindFact {
		dir = filepath.Join(dir, "facts")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, m.Name+".md")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

func TestIndexFact_Bitemporal(t *testing.T) {
	st := newStore(t)
	dir := t.TempDir()
	cfg := brain.Config{Enabled: true, Dir: dir}
	ix := brain.NewIndexer(cfg, st, nil)
	ctx := context.Background()

	start := time.Date(2026, 6, 3, 0, 0, 0, 0, time.UTC)
	path := writeMemoryFile(t, cfg, &store.MemoryEntry{
		ID: "01FACT01", Name: "stack", Kind: store.MemoryKindFact,
		WorkspaceID: strptr("ws"), Content: "Go", TValidStart: start,
		CreatedAt: start, UpdatedAt: start,
	})
	if err := ix.IndexFile(ctx, path); err != nil {
		t.Fatalf("IndexFile: %v", err)
	}
	got, err := st.GetMemory(ctx, "01FACT01")
	if err != nil {
		t.Fatalf("GetMemory: %v", err)
	}
	if got.Kind != store.MemoryKindFact {
		t.Errorf("kind = %q", got.Kind)
	}
	if !got.TValidStart.Equal(start) {
		t.Errorf("t_valid_start = %v, want %v", got.TValidStart, start)
	}
}

func TestIndexFact_SupersessionWritesHistory(t *testing.T) {
	st := newStore(t)
	dir := t.TempDir()
	cfg := brain.Config{Enabled: true, Dir: dir}
	ix := brain.NewIndexer(cfg, st, nil)
	ctx := context.Background()

	start := time.Date(2026, 6, 3, 0, 0, 0, 0, time.UTC)
	m := &store.MemoryEntry{
		ID: "01FACT02", Name: "db", Kind: store.MemoryKindFact,
		WorkspaceID: strptr("ws"), Content: "Postgres", TValidStart: start,
		CreatedAt: start, UpdatedAt: start,
	}
	path := writeMemoryFile(t, cfg, m)
	if err := ix.IndexFile(ctx, path); err != nil {
		t.Fatalf("first IndexFile: %v", err)
	}

	// A human edits the fact's content in place. Re-index: the prior version
	// is archived under memory/facts/.history/ before the upsert.
	m.Content = "SQLite"
	edited, _ := brain.SerializeMemory(m, nil)
	if err := os.WriteFile(path, edited, 0o644); err != nil {
		t.Fatalf("edit: %v", err)
	}
	if err := ix.IndexFile(ctx, path); err != nil {
		t.Fatalf("second IndexFile: %v", err)
	}

	wsDir, err := cfg.WorkspaceDir("ws")
	if err != nil {
		t.Fatalf("WorkspaceDir: %v", err)
	}
	histDir := filepath.Join(wsDir, "memory", "facts", ".history")
	matches, _ := filepath.Glob(filepath.Join(histDir, "db-*.md"))
	if len(matches) == 0 {
		t.Fatal("expected a .history archive of the superseded fact")
	}
	// Live row reflects the new content.
	got, err := st.GetMemory(ctx, "01FACT02")
	if err != nil {
		t.Fatalf("GetMemory: %v", err)
	}
	if got.Content != "SQLite" {
		t.Errorf("content = %q, want SQLite", got.Content)
	}
}

// TestHistoryArchive_NotIndexed guards the watcher/IndexFile path against
// the .history/ archive that archiveSupersededFact writes on every fact
// supersession. The watcher watches subdirs recursively, so without the
// dot-dir skip it would try to index the timestamped archive — whose
// filename stem (db-<ts>) never matches the memory name (db) — and record
// a phantom brain_errors row on each fact edit.
func TestHistoryArchive_NotIndexed(t *testing.T) {
	st := newStore(t)
	dir := t.TempDir()
	cfg := brain.Config{Enabled: true, Dir: dir}
	ix := brain.NewIndexer(cfg, st, nil)
	ctx := context.Background()

	wsDir, err := cfg.WorkspaceDir("ws")
	if err != nil {
		t.Fatalf("WorkspaceDir: %v", err)
	}
	histDir := filepath.Join(wsDir, "memory", "facts", ".history")
	if err := os.MkdirAll(histDir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, _ := brain.SerializeMemory(&store.MemoryEntry{
		ID: "01FACTX", Name: "db", Kind: store.MemoryKindFact,
		WorkspaceID: strptr("ws"), Content: "Postgres",
	}, nil)
	histPath := filepath.Join(histDir, "db-20260603T000000Z.md")
	if err := os.WriteFile(histPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ix.IndexFile(ctx, histPath); err != nil {
		t.Fatalf("indexing a .history archive should be a silent no-op, got %v", err)
	}
	errs, _ := st.ListBrainErrors(ctx)
	if len(errs) != 0 {
		t.Fatalf(".history archive recorded brain_errors: %+v", errs)
	}
	if _, err := st.GetMemory(ctx, "01FACTX"); err == nil {
		t.Error(".history archive must not create a live memory row")
	}
}

// TestIndexFact_NewIDSameName_Supersedes is the bi-temporal finding guard:
// when a NEW fact file (new id) supersedes a prior fact by reusing the same
// name+workspace, the prior row must be closed out (t_valid_end +
// invalidated_by stamped), its file archived + retired so a reindex can't
// resurrect it, and exactly ONE active fact must remain for that name.
func TestIndexFact_NewIDSameName_Supersedes(t *testing.T) {
	st := newStore(t)
	dir := t.TempDir()
	cfg := brain.Config{Enabled: true, Dir: dir}
	ix := brain.NewIndexer(cfg, st, nil)
	ctx := context.Background()

	start := time.Date(2026, 6, 3, 0, 0, 0, 0, time.UTC)
	old := &store.MemoryEntry{
		ID: "01OLDFACT", Name: "stack", Kind: store.MemoryKindFact,
		WorkspaceID: strptr("ws"), Content: "Go", TValidStart: start,
		CreatedAt: start, UpdatedAt: start,
	}
	oldPath := writeMemoryFile(t, cfg, old)
	if err := ix.IndexFile(ctx, oldPath); err != nil {
		t.Fatalf("index old fact: %v", err)
	}

	// A NEW fact (new id) supersedes the prior one by rewriting the SAME
	// canonical file (the validator requires name==filename stem, so a
	// same-name supersession reuses memory/facts/stack.md with a new id).
	newFact := &store.MemoryEntry{
		ID: "01NEWFACT", Name: "stack", Kind: store.MemoryKindFact,
		WorkspaceID: strptr("ws"), Content: "Rust",
		TValidStart: start.Add(24 * time.Hour),
		CreatedAt:   start.Add(24 * time.Hour), UpdatedAt: start.Add(24 * time.Hour),
	}
	newData, _ := brain.SerializeMemory(newFact, nil)
	if err := os.WriteFile(oldPath, newData, 0o644); err != nil {
		t.Fatalf("rewrite fact: %v", err)
	}
	if err := ix.IndexFile(ctx, oldPath); err != nil {
		t.Fatalf("index new fact: %v", err)
	}

	// The prior row must be invalidated (excluded from a default Get).
	if _, err := st.GetMemory(ctx, "01OLDFACT"); err == nil {
		// GetMemory excludes soft-deleted but not invalidated rows; assert it
		// is now historical via t_valid_end instead.
		old2, _ := st.GetMemory(ctx, "01OLDFACT")
		if old2 != nil && old2.TValidEnd == nil {
			t.Error("prior fact still active (t_valid_end nil) after supersession")
		}
	}

	// Exactly one ACTIVE fact named "stack" must remain, and it is the new one.
	active, err := st.ListMemories(ctx, store.MemoryFilter{
		Kind: store.MemoryKindFact, Name: "stack",
		Scope: store.SkillScope{WorkspaceIDs: []string{"ws"}}, ScopeFilter: "workspace_only",
	})
	if err != nil {
		t.Fatalf("ListMemories: %v", err)
	}
	activeCount := 0
	var activeID string
	for i := range active {
		if active[i].TValidEnd == nil {
			activeCount++
			activeID = active[i].ID
		}
	}
	if activeCount != 1 {
		t.Fatalf("want exactly 1 active fact named stack, got %d", activeCount)
	}
	if activeID != "01NEWFACT" {
		t.Errorf("active fact = %q, want 01NEWFACT", activeID)
	}

	// The prior fact's version must have been archived to .history/.
	wsDir, err := cfg.WorkspaceDir("ws")
	if err != nil {
		t.Fatalf("WorkspaceDir: %v", err)
	}
	histDir := filepath.Join(wsDir, "memory", "facts", ".history")
	if matches, _ := filepath.Glob(filepath.Join(histDir, "stack-*.md")); len(matches) == 0 {
		t.Error("expected the superseded fact archived under .history/")
	}
	// The canonical file persists (rewritten in place, rebound to the new id).
	if _, err := os.Stat(oldPath); err != nil {
		t.Errorf("canonical fact file should persist after in-place supersession: %v", err)
	}
	// Its index binding now points at the NEW id.
	idx, err := st.GetIndexFile(ctx, oldPath)
	if err != nil {
		t.Fatalf("GetIndexFile: %v", err)
	}
	if idx.EntityID != "01NEWFACT" {
		t.Errorf("index binding = %q, want 01NEWFACT", idx.EntityID)
	}
}

// TestIndexer_UpsertMemory_ReEmbedHook mirrors TestEditor_SaveMemory_ReEmbedHook
// for the file-watch path (wave-B2 item 3): re-indexing a note .md whose CONTENT
// changed fires the re-embed hook (store.UpdateMemory drops the stale vector, so
// the memory Service must rebuild it), while the first index (insert) and a
// metadata-only edit (tags change, same content) do NOT — their vectors are
// absent / still valid. Both editor + indexer share fireReEmbedIfContentChanged,
// so this proves the indexer half of that one content-change rule.
func TestIndexer_UpsertMemory_ReEmbedHook(t *testing.T) {
	st := newStore(t)
	dir := t.TempDir()
	cfg := brain.Config{Enabled: true, Dir: dir}
	ix := brain.NewIndexer(cfg, st, nil)
	ctx := context.Background()

	var fired []string
	ix.SetReEmbedHook(func(_ context.Context, e *store.MemoryEntry) {
		fired = append(fired, e.ID)
	})

	now := time.Date(2026, 6, 3, 0, 0, 0, 0, time.UTC)
	note := &store.MemoryEntry{
		ID: "01NOTE01", Name: "debug-log", Kind: store.MemoryKindNote,
		WorkspaceID: strptr("ws"), Content: "original body about the payment flow",
		CreatedAt: now, UpdatedAt: now,
	}
	path := writeMemoryFile(t, cfg, note)

	// Insert (no prior row) → hook must NOT fire (WriteMemory path, no vector
	// to drop).
	if err := ix.IndexFile(ctx, path); err != nil {
		t.Fatalf("first IndexFile: %v", err)
	}
	if len(fired) != 0 {
		t.Fatalf("insert must not fire re-embed hook, got %v", fired)
	}

	// Content-changing edit → hook fires once.
	note.Content = "completely different body about the auth flow"
	edited, _ := brain.SerializeMemory(note, nil)
	if err := os.WriteFile(path, edited, 0o644); err != nil {
		t.Fatalf("content edit write: %v", err)
	}
	if err := ix.IndexFile(ctx, path); err != nil {
		t.Fatalf("content-edit IndexFile: %v", err)
	}
	if len(fired) != 1 || fired[0] != "01NOTE01" {
		t.Fatalf("content edit must fire re-embed hook once for 01NOTE01, got %v", fired)
	}

	// Metadata-only edit (tags change, SAME content) → hook must NOT fire again.
	note.TagsJSON = json.RawMessage(`["payments"]`)
	metaEdited, _ := brain.SerializeMemory(note, nil)
	if err := os.WriteFile(path, metaEdited, 0o644); err != nil {
		t.Fatalf("metadata edit write: %v", err)
	}
	if err := ix.IndexFile(ctx, path); err != nil {
		t.Fatalf("metadata-edit IndexFile: %v", err)
	}
	if len(fired) != 1 {
		t.Fatalf("metadata-only edit must NOT fire re-embed hook, got %v", fired)
	}
}

func TestIndexMemory_Malformed_RecordsError(t *testing.T) {
	st := newStore(t)
	dir := t.TempDir()
	cfg := brain.Config{Enabled: true, Dir: dir}
	ix := brain.NewIndexer(cfg, st, nil)
	ctx := context.Background()

	// A fact file with no t_valid_start fails ValidateMemory.
	bad := "---\nid: 01BADFACT\nschema: memory/v1\nkind: fact\nname: badfact\nworkspace: ws\npinned: false\ncreated_at: 2026-06-03T10:00:00Z\nupdated_at: 2026-06-03T10:00:00Z\n---\n\nbody\n"
	wsDir, err := cfg.WorkspaceDir("ws")
	if err != nil {
		t.Fatalf("WorkspaceDir: %v", err)
	}
	memDir := filepath.Join(wsDir, "memory", "facts")
	if err := os.MkdirAll(memDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(memDir, "badfact.md")
	if err := os.WriteFile(path, []byte(bad), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := ix.IndexFile(ctx, path); err == nil {
		t.Fatal("expected validation error")
	}
	if _, err := st.GetMemory(ctx, "01BADFACT"); err == nil {
		t.Error("malformed fact should not have created a row")
	}
	errs, _ := st.ListBrainErrors(ctx)
	if len(errs) == 0 {
		t.Fatal("expected a brain_errors row")
	}
}
