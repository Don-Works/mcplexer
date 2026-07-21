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

func TestIndexWorkspace_CreatesAndInvalidates(t *testing.T) {
	st := newStore(t)
	dir := t.TempDir()
	cfg := brain.Config{Enabled: true, Dir: dir}
	ix := brain.NewIndexer(cfg, st, nil)
	ctx := context.Background()

	var invalidatedFor string
	ix.SetWorkspaceInvalidate(func(ws string) { invalidatedFor = ws })

	now := time.Now().UTC()
	data, err := brain.SerializeWorkspace(&store.Workspace{
		ID: "acme", Name: "Acme", RootPath: "/repos/acme",
		DefaultPolicy: "allow", CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatalf("serialize workspace: %v", err)
	}
	wsDir, err := cfg.WorkspaceDir("acme")
	if err != nil {
		t.Fatalf("WorkspaceDir: %v", err)
	}
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(wsDir, "workspace.md")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := ix.IndexFile(ctx, path); err != nil {
		t.Fatalf("IndexFile: %v", err)
	}

	got, err := st.GetWorkspace(ctx, "acme")
	if err != nil {
		t.Fatalf("GetWorkspace: %v", err)
	}
	if got.Name != "Acme" || got.DefaultPolicy != "allow" {
		t.Errorf("workspace mismatch: %+v", got)
	}
	if got.Source != "brain" {
		t.Errorf("source = %q, want brain", got.Source)
	}
	if invalidatedFor != "acme" {
		t.Errorf("invalidate callback fired for %q, want acme", invalidatedFor)
	}

	// A frontmatter edit (policy change) round-trips + re-invalidates.
	invalidatedFor = ""
	edited, _ := brain.SerializeWorkspace(&store.Workspace{
		ID: "acme", Name: "Acme", RootPath: "/repos/acme",
		DefaultPolicy: "deny", CreatedAt: now, UpdatedAt: now,
	})
	if err := os.WriteFile(path, edited, 0o644); err != nil {
		t.Fatalf("edit: %v", err)
	}
	if err := ix.IndexFile(ctx, path); err != nil {
		t.Fatalf("re-IndexFile: %v", err)
	}
	got2, _ := st.GetWorkspace(ctx, "acme")
	if got2.DefaultPolicy != "deny" {
		t.Errorf("policy edit did not round-trip: %+v", got2)
	}
	if invalidatedFor != "acme" {
		t.Error("invalidate callback did not fire on edit")
	}
}

func TestWriteWorkspace_SerializesFile(t *testing.T) {
	st := newStore(t)
	dir := t.TempDir()
	cfg := brain.Config{Enabled: true, Dir: dir}
	ser := brain.NewSerializer(cfg, st, nil)
	ctx := context.Background()

	// WriteWorkspace is the OUTBOUND path: it presumes the DB row exists
	// (created via the workspace store) and serializes its canonical file.
	now := time.Now().UTC()
	w := &store.Workspace{
		ID: "proj", Name: "Project", RootPath: "/p",
		DefaultPolicy: "deny", Source: "brain", CreatedAt: now, UpdatedAt: now,
	}
	if err := st.CreateWorkspace(ctx, w); err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	if err := ser.WriteWorkspace(ctx, w); err != nil {
		t.Fatalf("WriteWorkspace: %v", err)
	}
	wsDir, err := cfg.WorkspaceDir("proj")
	if err != nil {
		t.Fatalf("WorkspaceDir: %v", err)
	}
	path := filepath.Join(wsDir, "workspace.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("workspace.md not written: %v", err)
	}
	if !strings.Contains(string(data), "name: Project") {
		t.Errorf("workspace.md missing name:\n%s", data)
	}
	if !strings.Contains(string(data), "default_policy: deny") {
		t.Errorf("workspace.md missing default_policy:\n%s", data)
	}
}
