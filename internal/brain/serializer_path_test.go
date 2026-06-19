package brain_test

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/don-works/mcplexer/internal/brain"
	"github.com/don-works/mcplexer/internal/store"
)

// flatStemRE is the shape every serialized record filename stem must have:
// kebab slug or ULID (store-minted ids are uppercase) — never raw free-form
// text.
var flatStemRE = regexp.MustCompile(`^[A-Za-z0-9-]+$`)

// indexPathFor finds the index_files row bound to entity (kind, id).
func indexPathFor(t *testing.T, st store.Store, kind, id string) string {
	t.Helper()
	files, err := st.ListIndexFiles(context.Background(), "")
	if err != nil {
		t.Fatalf("ListIndexFiles: %v", err)
	}
	for i := range files {
		if files[i].EntityKind == kind && files[i].EntityID == id {
			return files[i].Path
		}
	}
	t.Fatalf("no index_files row for %s %s", kind, id)
	return ""
}

// assertFlatRecordFile asserts path is a real file DIRECTLY under wantDir
// with a sanitized stem (the path-traversal regression contract).
func assertFlatRecordFile(t *testing.T, path, wantDir string) {
	t.Helper()
	if filepath.Dir(path) != filepath.Clean(wantDir) {
		t.Fatalf("record file not flat: got dir %q, want %q (path %q)",
			filepath.Dir(path), wantDir, path)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("record file missing at %s: %v", path, err)
	}
	if info.IsDir() {
		t.Fatalf("record path is a directory: %s", path)
	}
	stem := filepath.Base(path)
	stem = stem[:len(stem)-len(filepath.Ext(stem))]
	if !flatStemRE.MatchString(stem) {
		t.Fatalf("filename stem %q is not a sanitized slug", stem)
	}
}

// TestWriteMemory_HostileNamesStayFlat is the path-traversal regression for
// the live incident: a memory named "...remote = example/memory-repo
// (private)" created an unindexable SUBDIRECTORY because the raw name was
// joined into the path. Names must slugify into a flat filename.
func TestWriteMemory_HostileNamesStayFlat(t *testing.T) {
	cases := []struct {
		name    string
		memName string
	}{
		{name: "live regression slash", memName: "Brain cross-machine sync: canonical remote = example/memory-repo (private)"},
		{name: "simple slash", memName: "a/b"},
		{name: "parent traversal", memName: "../escape"},
		{name: "backslash", memName: `back\slash name`},
		{name: "all symbols falls back to id", memName: "../../.."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := newStore(t)
			cfg := brain.Config{Enabled: true, Dir: t.TempDir()}
			ser := brain.NewSerializer(cfg, st, nil)
			ctx := context.Background()

			id := writeNote(t, st, "ws", tc.memName, "body")
			if err := ser.WriteMemory(ctx, id); err != nil {
				t.Fatalf("WriteMemory: %v", err)
			}
			wsDir, err := cfg.WorkspaceDir("ws")
			if err != nil {
				t.Fatalf("WorkspaceDir: %v", err)
			}
			memDir := filepath.Join(wsDir, "memory")
			assertFlatRecordFile(t, indexPathFor(t, st, brain.EntityKindMemory, id), memDir)

			// No subdirectory may have been created by the name.
			entries, err := os.ReadDir(memDir)
			if err != nil {
				t.Fatalf("read memory dir: %v", err)
			}
			for _, e := range entries {
				if e.IsDir() {
					t.Fatalf("hostile name created subdirectory %q", e.Name())
				}
			}
		})
	}
}

// TestWriteMemory_SlugStemRoundTripsInbound verifies a slug-named file
// written outbound re-indexes cleanly (the validator accepts the slug stem).
func TestWriteMemory_SlugStemRoundTripsInbound(t *testing.T) {
	st := newStore(t)
	cfg := brain.Config{Enabled: true, Dir: t.TempDir()}
	ix := brain.NewIndexer(cfg, st, nil)
	ser := brain.NewSerializer(cfg, st, nil)
	ctx := context.Background()

	id := writeNote(t, st, "ws", "My Deploy Hygiene", "body")
	if err := ser.WriteMemory(ctx, id); err != nil {
		t.Fatalf("WriteMemory: %v", err)
	}
	wsDir, err := cfg.WorkspaceDir("ws")
	if err != nil {
		t.Fatalf("WorkspaceDir: %v", err)
	}
	path := filepath.Join(wsDir, "memory", "my-deploy-hygiene.md")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected slugified file at %s: %v", path, err)
	}
	// A fresh indexer (no self-write mark) must accept the slug stem.
	if err := ix.IndexFile(ctx, path); err != nil {
		t.Fatalf("slug-named file failed inbound validation: %v", err)
	}
}

// TestWritePerson_HostileNameStaysFlat mirrors the memory regression for the
// CRM people dir.
func TestWritePerson_HostileNameStaysFlat(t *testing.T) {
	st := newStore(t)
	cfg := brain.Config{Enabled: true, Dir: t.TempDir()}
	ser := brain.NewSerializer(cfg, st, nil)
	ctx := context.Background()

	if err := st.CreateWorkspace(ctx, &store.Workspace{ID: "ws", Name: "Workspace"}); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	p := &store.PersonEntry{Name: "Evil/../Person", WorkspaceID: "ws", Company: "ACME"}
	if err := st.WritePerson(ctx, p); err != nil {
		t.Fatalf("WritePerson: %v", err)
	}
	if err := ser.WritePerson(ctx, p.ID); err != nil {
		t.Fatalf("serialize person: %v", err)
	}
	peopleDir := filepath.Join(cfg.Dir, "workspaces", "ws", "crm", "people")
	assertFlatRecordFile(t, indexPathFor(t, st, brain.EntityKindPerson, p.ID), peopleDir)
}

// TestWritePerson_HostileWorkspaceStaysInDefault is tested via the internal
// TestPersonPath_HostileWorkspace in paths_test.go, which exercises
// safePersonWorkspace directly. The DB foreign key on workspace_id prevents
// hostile values from reaching WritePerson, but the path construction is
// hardened independently (defense in depth).
func TestWritePerson_HostileWorkspaceStaysInDefault(t *testing.T) {
	t.Log("covered by TestPersonPath_HostileWorkspace in paths_test.go (internal)")
}

// TestWritePerson_ValidWorkspaceStillWorks ensures legitimate workspace IDs
// still produce the correct path after the traversal hardening.
func TestWritePerson_ValidWorkspaceStillWorks(t *testing.T) {
	st := newStore(t)
	cfg := brain.Config{Enabled: true, Dir: t.TempDir()}
	ser := brain.NewSerializer(cfg, st, nil)
	ctx := context.Background()

	if err := st.CreateWorkspace(ctx, &store.Workspace{ID: "acme-corp", Name: "ACME Corp"}); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	p := &store.PersonEntry{Name: "Bob", WorkspaceID: "acme-corp", Company: "ACME"}
	if err := st.WritePerson(ctx, p); err != nil {
		t.Fatalf("WritePerson: %v", err)
	}
	if err := ser.WritePerson(ctx, p.ID); err != nil {
		t.Fatalf("serialize person: %v", err)
	}

	got := indexPathFor(t, st, brain.EntityKindPerson, p.ID)
	wantDir := filepath.Join(cfg.Dir, "workspaces", "acme-corp", "crm", "people")
	if filepath.Dir(got) != filepath.Clean(wantDir) {
		t.Fatalf("valid workspace path mismatch: got dir %q, want %q", filepath.Dir(got), wantDir)
	}
}
