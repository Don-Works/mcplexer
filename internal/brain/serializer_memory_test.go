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

// writeNote inserts a note via the store and returns its id.
func writeNote(t *testing.T, st store.Store, ws, name, content string) string {
	t.Helper()
	ctx := context.Background()
	wsp := ws
	e := &store.MemoryEntry{Name: name, Kind: store.MemoryKindNote, Content: content, WorkspaceID: &wsp}
	if err := st.WriteMemory(ctx, e); err != nil {
		t.Fatalf("WriteMemory: %v", err)
	}
	return e.ID
}

func TestWriteNote_RoundTrip(t *testing.T) {
	st := newStore(t)
	dir := t.TempDir()
	cfg := brain.Config{Enabled: true, Dir: dir}
	ix := brain.NewIndexer(cfg, st, nil)
	ser := brain.NewSerializer(cfg, st, nil)
	ctx := context.Background()

	id := writeNote(t, st, "ws", "deploy-hygiene", "Never deploy from a dirty tree.")
	if err := ser.WriteMemory(ctx, id); err != nil {
		t.Fatalf("WriteMemory serialize: %v", err)
	}

	wsDir, err := cfg.WorkspaceDir("ws")
	if err != nil {
		t.Fatalf("WorkspaceDir: %v", err)
	}
	path := filepath.Join(wsDir, "memory", "deploy-hygiene.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read note file: %v", err)
	}
	if !strings.Contains(string(data), "kind: note") {
		t.Errorf("file missing kind: note:\n%s", data)
	}
	if !strings.Contains(string(data), "Never deploy from a dirty tree.") {
		t.Errorf("file missing body:\n%s", data)
	}

	// Edit out-of-band + re-index → the edit round-trips into the DB.
	edited, _ := brain.SerializeMemory(&store.MemoryEntry{
		ID: id, Name: "deploy-hygiene", Kind: store.MemoryKindNote,
		WorkspaceID: strptr("ws"), Content: "Edited content.",
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}, nil)
	if err := os.WriteFile(path, edited, 0o644); err != nil {
		t.Fatalf("edit: %v", err)
	}
	if err := ix.IndexFile(ctx, path); err != nil {
		t.Fatalf("IndexFile: %v", err)
	}
	got, err := st.GetMemory(ctx, id)
	if err != nil {
		t.Fatalf("GetMemory: %v", err)
	}
	if got.Content != "Edited content." {
		t.Errorf("content = %q, want edited", got.Content)
	}
}

func TestWriteMemory_FactGoesToFactsDir(t *testing.T) {
	st := newStore(t)
	dir := t.TempDir()
	cfg := brain.Config{Enabled: true, Dir: dir}
	ser := brain.NewSerializer(cfg, st, nil)
	ctx := context.Background()

	wsp := "ws"
	fact := &store.MemoryEntry{
		Name: "primary-stack", Kind: store.MemoryKindFact,
		Content: "Go + TypeScript", WorkspaceID: &wsp,
		TValidStart: time.Now().UTC(),
	}
	if err := st.WriteMemory(ctx, fact); err != nil {
		t.Fatalf("WriteMemory: %v", err)
	}
	if err := ser.WriteMemory(ctx, fact.ID); err != nil {
		t.Fatalf("serialize fact: %v", err)
	}
	wsDir, err := cfg.WorkspaceDir("ws")
	if err != nil {
		t.Fatalf("WorkspaceDir: %v", err)
	}
	path := filepath.Join(wsDir, "memory", "facts", "primary-stack.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fact file (expected under facts/): %v", err)
	}
	if !strings.Contains(string(data), "t_valid_start:") {
		t.Errorf("fact file missing bi-temporal frontmatter:\n%s", data)
	}
}

func TestEntityLinksReDerived(t *testing.T) {
	st := newStore(t)
	dir := t.TempDir()
	cfg := brain.Config{Enabled: true, Dir: dir}
	ix := brain.NewIndexer(cfg, st, nil)
	ser := brain.NewSerializer(cfg, st, nil)
	ctx := context.Background()

	id := writeNote(t, st, "ws", "linked", "About a task.")
	// Link an entity in the DB, serialize → the file's `entities:` reflects it.
	if err := st.LinkMemoryEntity(ctx, id, store.EntityRef{Kind: "task", ID: "01ABC", Role: "subject"}, ""); err != nil {
		t.Fatalf("LinkMemoryEntity: %v", err)
	}
	if err := ser.WriteMemory(ctx, id); err != nil {
		t.Fatalf("serialize: %v", err)
	}
	wsDir, err := cfg.WorkspaceDir("ws")
	if err != nil {
		t.Fatalf("WorkspaceDir: %v", err)
	}
	path := filepath.Join(wsDir, "memory", "linked.md")
	data, _ := os.ReadFile(path)
	// The store lowercases entity ids; assert the link round-tripped into
	// the file's `entities:` block (case-insensitive on the id).
	if !strings.Contains(string(data), "kind: task") || !strings.Contains(strings.ToLower(string(data)), "id: 01abc") {
		t.Fatalf("file missing entity link:\n%s", data)
	}

	// Now a human removes the entity from the file + re-indexes → the join
	// row is reconciled away (the file is canonical for `entities:`).
	noLink, _ := brain.SerializeMemory(&store.MemoryEntry{
		ID: id, Name: "linked", Kind: store.MemoryKindNote,
		WorkspaceID: strptr("ws"), Content: "About a task.",
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}, nil)
	if err := os.WriteFile(path, noLink, 0o644); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if err := ix.IndexFile(ctx, path); err != nil {
		t.Fatalf("IndexFile: %v", err)
	}
	links, err := st.ListMemoryEntities(ctx, id)
	if err != nil {
		t.Fatalf("ListMemoryEntities: %v", err)
	}
	if len(links) != 0 {
		t.Errorf("entity links not reconciled away: %+v", links)
	}
}

// TestEntityLink_RoleOmittedSurvivesReconcile guards the reconcile path
// against the canonicalisation mismatch: a human-authored memory file
// whose `entities:` link omits `role:` must NOT be unlinked on index.
// The store defaults role to "subject" + lowercases kind/id, so the
// reconcile want-set has to canonicalise identically or it would link
// then immediately unlink the row.
func TestEntityLink_RoleOmittedSurvivesReconcile(t *testing.T) {
	st := newStore(t)
	dir := t.TempDir()
	cfg := brain.Config{Enabled: true, Dir: dir}
	ix := brain.NewIndexer(cfg, st, nil)
	ctx := context.Background()

	wsDir, err := cfg.WorkspaceDir("ws")
	if err != nil {
		t.Fatalf("WorkspaceDir: %v", err)
	}
	memDir := filepath.Join(wsDir, "memory")
	if err := os.MkdirAll(memDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// entities link with NO role (and a mixed-case id the store lowercases).
	body := "---\nid: 01ROLE01\nschema: memory/v1\nkind: note\nname: roled\nworkspace: ws\npinned: false\nentities:\n  - kind: task\n    id: 01ABC\ncreated_at: 2026-06-03T10:00:00Z\nupdated_at: 2026-06-03T10:00:00Z\n---\n\nbody\n"
	path := filepath.Join(memDir, "roled.md")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ix.IndexFile(ctx, path); err != nil {
		t.Fatalf("IndexFile: %v", err)
	}
	links, err := st.ListMemoryEntities(ctx, "01ROLE01")
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 1 {
		t.Fatalf("role-omitted link should survive reconcile, got %d: %+v", len(links), links)
	}
	// A no-op re-index (same content) must also leave the link intact.
	if err := ix.IndexFile(ctx, path); err != nil {
		t.Fatalf("re-IndexFile: %v", err)
	}
	links, _ = st.ListMemoryEntities(ctx, "01ROLE01")
	if len(links) != 1 {
		t.Fatalf("re-index dropped the role-omitted link: %+v", links)
	}
}

func strptr(s string) *string { return &s }
