// claudecli_test.go — coverage for the importer. Uses t.TempDir() to
// build a simulated ~/.claude/projects layout, runs Import against a
// fake MemoryStore, and asserts the resulting MemoryEntries match
// expectations. Round-trips through a real SQLite store in the
// integration test at the end to confirm WriteMemory accepts our
// payload shape.
//
// Fixtures + fake store live in helpers_test.go.
package claudecli_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/memory/claudecli"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

func TestImportHappyPath(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	fixtureProject(t, base, "Users/dev/github/example/mcplexer", map[string]string{
		"MEMORY.md":                             indexFile,
		"project_workers_overnight_complete.md": projectFrontmatter,
		"reference_hosts.md":                    referenceFrontmatter,
		"plain.md":                              noFrontmatter,
	})
	fs := newFakeStore()
	res, err := claudecli.Import(context.Background(), fs, claudecli.ImportOptions{
		BaseDir: base,
	})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if res.Imported != 3 {
		t.Fatalf("expected 3 imported (excluding MEMORY.md), got %d (errors=%v)",
			res.Imported, res.Errors)
	}
	if res.Skipped != 0 {
		t.Fatalf("expected 0 skipped, got %d", res.Skipped)
	}
	if len(res.Errors) != 0 {
		t.Fatalf("unexpected errors: %v", res.Errors)
	}
}

func TestImportSkipsMemoryIndex(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	fixtureProject(t, base, "Users/foo/bar", map[string]string{
		"MEMORY.md": indexFile,
	})
	fs := newFakeStore()
	res, err := claudecli.Import(context.Background(), fs, claudecli.ImportOptions{
		BaseDir: base,
	})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if res.Imported != 0 {
		t.Fatalf("expected 0 imported, got %d", res.Imported)
	}
}

func TestImportIdempotent(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	fixtureProject(t, base, "Users/max/foo", map[string]string{
		"project_foo.md": projectFrontmatter,
	})
	fs := newFakeStore()
	for i := 0; i < 3; i++ {
		res, err := claudecli.Import(context.Background(), fs, claudecli.ImportOptions{
			BaseDir: base,
		})
		if err != nil {
			t.Fatalf("Import #%d: %v", i, err)
		}
		if i == 0 {
			if res.Imported != 1 || res.Skipped != 0 {
				t.Fatalf("first run: want 1 imported / 0 skipped, got %d/%d",
					res.Imported, res.Skipped)
			}
		} else {
			if res.Imported != 0 || res.Skipped != 1 {
				t.Fatalf("repeat #%d: want 0 imported / 1 skipped, got %d/%d",
					i, res.Imported, res.Skipped)
			}
		}
	}
	if len(fs.writes) != 1 {
		t.Fatalf("store should have exactly 1 row, got %d", len(fs.writes))
	}
}

func TestImportDryRun(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	fixtureProject(t, base, "Users/max/foo", map[string]string{
		"project_foo.md": projectFrontmatter,
	})
	fs := newFakeStore()
	res, err := claudecli.Import(context.Background(), fs, claudecli.ImportOptions{
		BaseDir: base, DryRun: true,
	})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if res.Imported != 1 {
		t.Fatalf("dryrun: want 1 imported (planned), got %d", res.Imported)
	}
	if len(fs.writes) != 0 {
		t.Fatalf("dryrun should not have written rows, got %d", len(fs.writes))
	}
}

func TestImportMissingBaseDir(t *testing.T) {
	t.Parallel()
	base := filepath.Join(t.TempDir(), "does-not-exist")
	fs := newFakeStore()
	res, err := claudecli.Import(context.Background(), fs, claudecli.ImportOptions{
		BaseDir: base,
	})
	if err != nil {
		t.Fatalf("Import: %v (should not error on missing base)", err)
	}
	if res.Imported != 0 || res.Skipped != 0 {
		t.Fatalf("expected zero counts, got %+v", res)
	}
}

func TestImportFrontmatterFields(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	fixtureProject(t, base, "Users/max/foo", map[string]string{
		"project_workers_overnight_complete.md": projectFrontmatter,
	})
	fs := newFakeStore()
	if _, err := claudecli.Import(context.Background(), fs, claudecli.ImportOptions{
		BaseDir: base,
	}); err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(fs.writes) != 1 {
		t.Fatalf("expected 1 row, got %d", len(fs.writes))
	}
	assertEntryFields(t, fs.writes[0])
}

// assertEntryFields verifies the per-row mapping that buildEntry applies:
// name, kind, source_kind, source_session_id, body stripped of
// frontmatter, derived tags + metadata.
func assertEntryFields(t *testing.T, e *store.MemoryEntry) {
	t.Helper()
	if e.Name != "workers-overnight-complete" {
		t.Errorf("name from frontmatter: got %q", e.Name)
	}
	if e.Kind != store.MemoryKindNote {
		t.Errorf("kind: got %q want note", e.Kind)
	}
	if e.SourceKind != store.MemorySourceImported {
		t.Errorf("source_kind: got %q want imported", e.SourceKind)
	}
	if e.SourceSessionID != "sess-abc-123" {
		t.Errorf("originSessionId: got %q", e.SourceSessionID)
	}
	if !strings.Contains(e.Content, "Workers M0-M3 all shipped") {
		t.Errorf("content should contain body, got %q", e.Content)
	}
	if strings.Contains(e.Content, "originSessionId") {
		t.Errorf("content should NOT include frontmatter, got %q", e.Content)
	}
	if !strings.Contains(string(e.TagsJSON), "claude-cli-project") {
		t.Errorf("tags should include claude-cli-project, got %s", e.TagsJSON)
	}
	if !strings.Contains(string(e.MetadataJSON), "imported_from") {
		t.Errorf("metadata should include imported_from, got %s", e.MetadataJSON)
	}
}

func TestImportFilenameFallback(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	fixtureProject(t, base, "Users/max/foo", map[string]string{
		"orphan_no_name.md": "Body without frontmatter at all.",
	})
	fs := newFakeStore()
	if _, err := claudecli.Import(context.Background(), fs, claudecli.ImportOptions{
		BaseDir: base,
	}); err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(fs.writes) != 1 {
		t.Fatalf("expected 1 row, got %d", len(fs.writes))
	}
	if got := fs.writes[0].Name; got != "orphan_no_name" {
		t.Errorf("name fallback: got %q want orphan_no_name", got)
	}
}

func TestImportWorkspaceScope(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	fixtureProject(t, base, "Users/max/foo", map[string]string{
		"project_foo.md": projectFrontmatter,
	})
	ws := "ws-abc"
	fs := newFakeStore()
	if _, err := claudecli.Import(context.Background(), fs, claudecli.ImportOptions{
		BaseDir: base, WorkspaceID: &ws,
	}); err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(fs.writes) != 1 {
		t.Fatalf("expected 1 row, got %d", len(fs.writes))
	}
	if fs.writes[0].WorkspaceID == nil || *fs.writes[0].WorkspaceID != ws {
		t.Errorf("workspace_id not preserved: %+v", fs.writes[0].WorkspaceID)
	}
}

func TestImportNilStore(t *testing.T) {
	t.Parallel()
	if _, err := claudecli.Import(context.Background(), nil, claudecli.ImportOptions{}); err == nil {
		t.Fatal("expected error on nil store")
	}
}

// TestImportRoundTripSQLite confirms the importer's MemoryEntry payload
// is accepted by the real SQLite WriteMemory. Catches drift between
// the in-memory fake and the actual schema.
func TestImportRoundTripSQLite(t *testing.T) {
	t.Parallel()
	d, err := sqlite.New(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	base := t.TempDir()
	fixtureProject(t, base, "Users/max/foo", map[string]string{
		"project_workers_overnight_complete.md": projectFrontmatter,
		"reference_hosts.md":                    referenceFrontmatter,
	})
	res, err := claudecli.Import(context.Background(), d, claudecli.ImportOptions{
		BaseDir: base,
	})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if res.Imported != 2 {
		t.Fatalf("expected 2 imported, got %d (errors=%v)", res.Imported, res.Errors)
	}

	res2, err := claudecli.Import(context.Background(), d, claudecli.ImportOptions{
		BaseDir: base,
	})
	if err != nil {
		t.Fatalf("Import #2: %v", err)
	}
	if res2.Imported != 0 || res2.Skipped != 2 {
		t.Fatalf("repeat: want 0 imported / 2 skipped, got %d/%d",
			res2.Imported, res2.Skipped)
	}
}
