package brain_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/don-works/mcplexer/internal/brain"
	"github.com/don-works/mcplexer/internal/store"
)

// seedImportData inserts one workspace, two tasks, and one note into the
// store, returning the workspace id used as the folder slug.
func seedImportData(t *testing.T, st store.Store) string {
	t.Helper()
	ctx := context.Background()

	ws := &store.Workspace{ID: "imp-ws", Name: "Import WS"}
	if err := st.CreateWorkspace(ctx, ws); err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	for _, tk := range []*store.Task{
		{ID: "01IMPTASK001", WorkspaceID: "imp-ws", Title: "First", Status: "open", Description: "Body one."},
		{ID: "01IMPTASK002", WorkspaceID: "imp-ws", Title: "Second", Status: "doing", Description: "Body two."},
	} {
		if err := st.CreateTask(ctx, tk); err != nil {
			t.Fatalf("CreateTask %s: %v", tk.ID, err)
		}
	}
	wsp := "imp-ws"
	mem := &store.MemoryEntry{Name: "imp-note", Kind: store.MemoryKindNote, Content: "A note.", WorkspaceID: &wsp}
	if err := st.WriteMemory(ctx, mem); err != nil {
		t.Fatalf("WriteMemory: %v", err)
	}
	return "imp-ws"
}

func TestBrainImport_ParityVerified(t *testing.T) {
	st := newStore(t)
	dir := t.TempDir()
	cfg := brain.Config{Enabled: true, Dir: dir}
	ix := brain.NewIndexer(cfg, st, nil)
	ser := brain.NewSerializer(cfg, st, nil)
	ser.ShareSelfWrites(ix)
	ctx := context.Background()

	seedImportData(t, st)

	im := brain.NewImporter(cfg, st, ser, ix)
	rep, err := im.Run(ctx, st)
	if err != nil {
		t.Fatalf("Importer.Run: %v", err)
	}

	if !rep.ParityOK {
		t.Fatalf("ParityOK = false; drifts=%v errors=%v", rep.Drifts, rep.Errors)
	}
	if rep.Tasks != 2 {
		t.Errorf("Tasks = %d, want 2", rep.Tasks)
	}
	if rep.Memories != 1 {
		t.Errorf("Memories = %d, want 1", rep.Memories)
	}
	if rep.Workspaces != 2 {
		t.Errorf("Workspaces = %d, want 2", rep.Workspaces)
	}
	if len(rep.Drifts) != 0 {
		t.Errorf("expected no drift, got %v", rep.Drifts)
	}

	// The task files must exist on disk after import.
	wsDir, err := cfg.WorkspaceDir("imp-ws")
	if err != nil {
		t.Fatalf("WorkspaceDir: %v", err)
	}
	tasksDir := filepath.Join(wsDir, "tasks")
	matches, _ := filepath.Glob(filepath.Join(tasksDir, "*.md"))
	if len(matches) != 2 {
		t.Errorf("want 2 task files on disk, got %d (%v)", len(matches), matches)
	}
	notePath := filepath.Join(wsDir, "memory", "imp-note.md")
	if _, err := os.Stat(notePath); err != nil {
		t.Errorf("note file not written: %v", err)
	}
}

// TestBrainImport_AbortsOnDrift confirms that when a file is corrupted after
// the import write (simulating a serializer bug or a divergence), the verify
// pass catches it and ParityOK flips false rather than blessing a bad import.
func TestBrainImport_AbortsOnDrift(t *testing.T) {
	st := newStore(t)
	dir := t.TempDir()
	cfg := brain.Config{Enabled: true, Dir: dir}
	ix := brain.NewIndexer(cfg, st, nil)
	ser := brain.NewSerializer(cfg, st, nil)
	ser.ShareSelfWrites(ix)
	ctx := context.Background()

	seedImportData(t, st)

	// Run a normal import first so the files + index exist and match.
	im := brain.NewImporter(cfg, st, ser, ix)
	if rep, err := im.Run(ctx, st); err != nil || !rep.ParityOK {
		t.Fatalf("baseline import: err=%v parity=%v", err, rep.ParityOK)
	}

	// Corrupt one task file's title so the re-derived row diverges from the
	// authoritative DB row, then re-verify.
	wsDir, err := cfg.WorkspaceDir("imp-ws")
	if err != nil {
		t.Fatalf("WorkspaceDir: %v", err)
	}
	tasksDir := filepath.Join(wsDir, "tasks")
	matches, _ := filepath.Glob(filepath.Join(tasksDir, "*.md"))
	if len(matches) == 0 {
		t.Fatal("no task files to corrupt")
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	corrupted := []byte(string(data) + "\n\nEXTRA BODY THAT DIVERGES FROM THE DB ROW\n")
	if err := os.WriteFile(matches[0], corrupted, 0o644); err != nil {
		t.Fatalf("corrupt: %v", err)
	}

	vr, err := brain.Verify(ctx, cfg, st)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if vr.OK() {
		t.Fatal("Verify should detect the corrupted file as drift, but reported OK")
	}
}
