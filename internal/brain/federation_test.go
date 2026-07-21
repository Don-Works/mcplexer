package brain

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

// TestDiscoverRepoBrain_WalksAncestors verifies that a .mcplexer/ dir is
// found from a nested rootPath (mirrors git's nearest-.git discovery).
func TestDiscoverRepoBrain_WalksAncestors(t *testing.T) {
	root := t.TempDir()
	repoBrain := filepath.Join(root, "repo", RepoBrainDirName)
	if err := os.MkdirAll(repoBrain, 0o755); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(root, "repo", "src", "deep")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		from string
		want string
		ok   bool
	}{
		{"from repo root", filepath.Join(root, "repo"), repoBrain, true},
		{"from nested dir", nested, repoBrain, true},
		{"from outside (no brain)", filepath.Join(root, "other"), "", false},
		{"empty path", "", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := DiscoverRepoBrain(tc.from)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if ok && got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// TestDiscoverRepoBrain_ExcludesDataDir verifies the locked-down gateway
// data dir (~/.mcplexer) is never returned even when it matches by shape —
// the breach the federation/lockdown finding flagged. A legitimate repo
// brain higher in the tree is still found past the excluded candidate.
func TestDiscoverRepoBrain_ExcludesDataDir(t *testing.T) {
	root := t.TempDir()
	// Simulate HOME with the protected data dir HOME/.mcplexer present.
	dataDir := filepath.Join(root, RepoBrainDirName)
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// A session CWD directly under HOME with no nearer .mcplexer.
	cwd := filepath.Join(root, "project")
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}

	// Without exclusion the walk would (wrongly) match HOME/.mcplexer.
	if got, ok := DiscoverRepoBrain(cwd); !ok || got != dataDir {
		t.Fatalf("baseline: got (%q,%v), want (%q,true)", got, ok, dataDir)
	}
	// With the data dir excluded, discovery must NOT return it.
	if got, ok := DiscoverRepoBrain(cwd, dataDir); ok {
		t.Fatalf("excluded data dir was still returned: %q", got)
	}

	// A real repo brain above the excluded candidate is still found: put one
	// at root/outer/.mcplexer and exclude only root/.mcplexer.
	outerBrain := filepath.Join(root, "outer", RepoBrainDirName)
	deep := filepath.Join(root, "outer", "inner", "x")
	if err := os.MkdirAll(outerBrain, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	if got, ok := DiscoverRepoBrain(deep, dataDir); !ok || got != outerBrain {
		t.Fatalf("legit brain not found past exclusion: got (%q,%v), want (%q,true)", got, ok, outerBrain)
	}
}

// TestDiscoverRepoBrain_DataDirIsItsOwnCandidate pins the exact shape that the
// serve.go call site got wrong: the gateway data dir IS ~/.mcplexer, whose
// basename already equals RepoBrainDirName. So when discovery walks up to HOME
// it forms candidate HOME/.mcplexer == the data dir itself — and the exclude
// entry must be the data dir PATH, not filepath.Join(dataDir, ".mcplexer").
// Excluding the join form (the original bug) leaves the data dir discoverable
// and the watcher indexes its protected internals (memory-exports/, secrets/).
func TestDiscoverRepoBrain_DataDirIsItsOwnCandidate(t *testing.T) {
	home := t.TempDir()
	dataDir := filepath.Join(home, RepoBrainDirName) // HOME/.mcplexer
	// The data dir's internals the watcher must never reach.
	if err := os.MkdirAll(filepath.Join(dataDir, "memory-exports"), 0o755); err != nil {
		t.Fatal(err)
	}
	cwd := filepath.Join(home, "anywhere") // a $HOME-rooted session, no nearer brain
	if err := os.MkdirAll(cwd, 0o755); err != nil {
		t.Fatal(err)
	}

	// The OLD (buggy) exclude value: dataDir/.mcplexer — a nonexistent path that
	// never matches the candidate, so the data dir is still (wrongly) returned.
	buggyExclude := filepath.Join(dataDir, RepoBrainDirName)
	if got, ok := DiscoverRepoBrain(cwd, buggyExclude); !ok || got != dataDir {
		t.Fatalf("precondition: buggy exclude should still leak the data dir; got (%q,%v)", got, ok)
	}

	// The CORRECT exclude value: the data dir path itself.
	if got, ok := DiscoverRepoBrain(cwd, dataDir); ok {
		t.Fatalf("data dir leaked through correct exclusion: %q", got)
	}
}

// TestDiscoverRepoBrain_NearestWins verifies a nested .mcplexer/ shadows an
// outer one (nearest to rootPath wins).
func TestDiscoverRepoBrain_NearestWins(t *testing.T) {
	root := t.TempDir()
	outer := filepath.Join(root, RepoBrainDirName)
	inner := filepath.Join(root, "sub", RepoBrainDirName)
	if err := os.MkdirAll(outer, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	got, ok := DiscoverRepoBrain(filepath.Join(root, "sub", "x"))
	if !ok || got != inner {
		t.Fatalf("got (%q,%v), want (%q,true)", got, ok, inner)
	}
}

// TestDirRegistry_PrecedenceRepoOverCentral verifies the dir registry
// resolves a registered repo dir to its (workspace, source) and that the
// longest-prefix (nearest) registered root wins for nested repos.
func TestDirRegistry_PrecedenceRepoOverCentral(t *testing.T) {
	r := newDirRegistry()
	r.add("/code/acme-api/.mcplexer", "acme-api", store.IndexSourceRepo)
	r.add("/code/acme-api/sub/.mcplexer", "acme-sub", store.IndexSourceRepo)

	tests := []struct {
		name   string
		path   string
		wantWS string
		wantOK bool
	}{
		{"file under repo dir", "/code/acme-api/.mcplexer/tasks/t.md", "acme-api", true},
		{"file under nested repo dir wins", "/code/acme-api/sub/.mcplexer/tasks/t.md", "acme-sub", true},
		{"central path not registered", "/brain/workspaces/acme-api/tasks/t.md", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d, ok := r.resolve(tc.path)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if ok {
				if d.workspaceID != tc.wantWS {
					t.Errorf("workspaceID = %q, want %q", d.workspaceID, tc.wantWS)
				}
				if d.source != store.IndexSourceRepo {
					t.Errorf("source = %q, want repo", d.source)
				}
			}
		})
	}
}

// TestDirRegistry_AddIdempotent verifies re-adding the same root updates the
// mapping and reports not-newly-added.
func TestDirRegistry_AddIdempotent(t *testing.T) {
	r := newDirRegistry()
	if !r.add("/x/.mcplexer", "ws1", store.IndexSourceRepo) {
		t.Fatal("first add should report newly-added")
	}
	if r.add("/x/.mcplexer", "ws2", store.IndexSourceRepo) {
		t.Fatal("re-add should report NOT newly-added")
	}
	d, ok := r.resolve("/x/.mcplexer/tasks/t.md")
	if !ok || d.workspaceID != "ws2" {
		t.Fatalf("re-add should update workspace: got (%q,%v)", d.workspaceID, ok)
	}
}

// TestRegisterDir_IndexesRepoTaskAndSource is an end-to-end check: a repo
// .mcplexer/ dir with a task file gets indexed, the task row appears, and
// the index_files row is tagged source=repo with the registered workspace.
func TestRegisterDir_IndexesRepoTaskAndSource(t *testing.T) {
	db, err := sqlite.New(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ix := NewIndexer(Config{Enabled: true, Dir: t.TempDir()}, db, nil)

	repoDir := filepath.Join(t.TempDir(), RepoBrainDirName)
	tasksDir := filepath.Join(repoDir, "tasks")
	if err := os.MkdirAll(tasksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	taskID := "01HZZZZZZZZZZZZZZZZZZZZZZZZ"
	taskPath := filepath.Join(tasksDir, taskID+"-fix.md")
	doc := "---\n" +
		"id: " + taskID + "\n" +
		"schema: task/v1\n" +
		"workspace: acme-api\n" +
		"title: Fix it\n" +
		"status: open\n" +
		"pinned: false\n" +
		"created_at: 2026-06-03T10:00:00Z\n" +
		"updated_at: 2026-06-03T10:00:00Z\n" +
		"---\n\nBody.\n"
	if err := os.WriteFile(taskPath, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}

	var notified string
	ix.SetRegisterDirNotify(func(dir string) { notified = dir })

	if err := ix.RegisterDir(context.Background(), repoDir, "acme-api", store.IndexSourceRepo); err != nil {
		t.Fatalf("RegisterDir: %v", err)
	}

	if notified != filepath.Clean(repoDir) {
		t.Errorf("register notify = %q, want %q", notified, filepath.Clean(repoDir))
	}
	ctx := context.Background()
	if _, err := db.GetTask(ctx, taskID); err != nil {
		t.Fatalf("task %s not indexed: %v", taskID, err)
	}
	idx, err := db.GetIndexFile(ctx, taskPath)
	if err != nil {
		t.Fatalf("index_files row missing for %s: %v", taskPath, err)
	}
	if idx.Source != store.IndexSourceRepo {
		t.Errorf("index source = %q, want repo", idx.Source)
	}
	if idx.WorkspaceID != "acme-api" {
		t.Errorf("index workspace = %q, want acme-api", idx.WorkspaceID)
	}
}

func TestEnsureRepoWorkspace_CreatesChildProjectFromMarker(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	root := t.TempDir()
	parentRoot := filepath.Join(root, "workspace1")
	projectRoot := filepath.Join(parentRoot, "folder1", "folder2")
	repoDir := filepath.Join(projectRoot, RepoBrainDirName)
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateWorkspace(ctx, &store.Workspace{
		ID: "workspace1", Name: "Workspace 1", RootPath: parentRoot, DefaultPolicy: "deny",
	}); err != nil {
		t.Fatalf("create parent: %v", err)
	}

	got, err := EnsureRepoWorkspace(ctx, db, repoDir, "workspace1")
	if err != nil {
		t.Fatalf("EnsureRepoWorkspace: %v", err)
	}
	if !got.Created || !got.WroteFile {
		t.Fatalf("result = %+v, want Created+WroteFile", got)
	}
	if got.WorkspaceID != "folder2" {
		t.Fatalf("WorkspaceID = %q, want folder2", got.WorkspaceID)
	}
	ws, err := db.GetWorkspace(ctx, got.WorkspaceID)
	if err != nil {
		t.Fatalf("get child workspace: %v", err)
	}
	if ws.RootPath != projectRoot || ws.ParentID != "workspace1" || ws.DefaultPolicy != "deny" {
		t.Fatalf("child workspace = %+v", ws)
	}
	data, err := os.ReadFile(filepath.Join(repoDir, workspaceFile))
	if err != nil {
		t.Fatalf("workspace.md not written: %v", err)
	}
	fm, _, err := ParseWorkspace(data)
	if err != nil {
		t.Fatalf("parse written workspace.md: %v", err)
	}
	if fm.ID != "folder2" || fm.Parent != "workspace1" || fm.RootPath != projectRoot {
		t.Fatalf("written workspace.md = %+v", fm)
	}
}

func TestEnsureRepoWorkspace_ParentRootMarkerReusesParent(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	parentRoot := filepath.Join(t.TempDir(), "workspace1")
	repoDir := filepath.Join(parentRoot, RepoBrainDirName)
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateWorkspace(ctx, &store.Workspace{
		ID: "workspace1", Name: "Workspace 1", RootPath: parentRoot,
	}); err != nil {
		t.Fatalf("create parent: %v", err)
	}
	before, err := db.ListWorkspaces(ctx)
	if err != nil {
		t.Fatalf("list workspaces before: %v", err)
	}

	got, err := EnsureRepoWorkspace(ctx, db, repoDir, "workspace1")
	if err != nil {
		t.Fatalf("EnsureRepoWorkspace: %v", err)
	}
	if got.WorkspaceID != "workspace1" || got.Created || got.Updated || got.WroteFile {
		t.Fatalf("result = %+v, want parent reuse without mutation", got)
	}
	rows, err := db.ListWorkspaces(ctx)
	if err != nil {
		t.Fatalf("list workspaces: %v", err)
	}
	if len(rows) != len(before) {
		t.Fatalf("workspace count changed from %d to %d", len(before), len(rows))
	}
}

func TestEnsureRepoWorkspace_WorkspaceFileControlsIDAndName(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	parentRoot := filepath.Join(t.TempDir(), "workspace1")
	projectRoot := filepath.Join(parentRoot, "folder1", "folder2")
	repoDir := filepath.Join(projectRoot, RepoBrainDirName)
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateWorkspace(ctx, &store.Workspace{
		ID: "workspace1", Name: "Workspace 1", RootPath: parentRoot,
	}); err != nil {
		t.Fatalf("create parent: %v", err)
	}
	doc := "---\n" +
		"id: api\n" +
		"schema: workspace/v1\n" +
		"name: API Project\n" +
		"created_at: 2026-06-03T10:00:00Z\n" +
		"updated_at: 2026-06-03T10:00:00Z\n" +
		"---\n"
	if err := os.WriteFile(filepath.Join(repoDir, workspaceFile), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := EnsureRepoWorkspace(ctx, db, repoDir, "workspace1")
	if err != nil {
		t.Fatalf("EnsureRepoWorkspace: %v", err)
	}
	if got.WorkspaceID != "api" || !got.Created || got.WroteFile {
		t.Fatalf("result = %+v, want workspace.md-backed creation", got)
	}
	ws, err := db.GetWorkspace(ctx, "api")
	if err != nil {
		t.Fatalf("get api workspace: %v", err)
	}
	if ws.Name != "API Project" || ws.RootPath != projectRoot || ws.ParentID != "workspace1" {
		t.Fatalf("workspace = %+v", ws)
	}
}

func TestEnsureRepoWorkspace_IDCollisionUsesStableSuffix(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	root := t.TempDir()
	parentRoot := filepath.Join(root, "workspace1")
	projectRoot := filepath.Join(parentRoot, "folder2")
	repoDir := filepath.Join(projectRoot, RepoBrainDirName)
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = db.CreateWorkspace(ctx, &store.Workspace{ID: "workspace1", Name: "Workspace 1", RootPath: parentRoot})
	_ = db.CreateWorkspace(ctx, &store.Workspace{ID: "folder2", Name: "Other Folder 2", RootPath: filepath.Join(root, "other", "folder2")})

	got, err := EnsureRepoWorkspace(ctx, db, repoDir, "workspace1")
	if err != nil {
		t.Fatalf("EnsureRepoWorkspace: %v", err)
	}
	if got.WorkspaceID == "folder2" || !strings.HasPrefix(got.WorkspaceID, "folder2-") {
		t.Fatalf("WorkspaceID = %q, want folder2-<hash>", got.WorkspaceID)
	}
	ws, err := db.GetWorkspace(ctx, got.WorkspaceID)
	if err != nil {
		t.Fatalf("get suffixed workspace: %v", err)
	}
	if ws.RootPath != projectRoot || ws.ParentID != "workspace1" {
		t.Fatalf("workspace = %+v", ws)
	}
}

func TestEnsureRepoWorkspace_TaskRowsSegregateByProjectWorkspace(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	parentRoot := filepath.Join(t.TempDir(), "workspace1")
	if err := db.CreateWorkspace(ctx, &store.Workspace{
		ID: "workspace1", Name: "Workspace 1", RootPath: parentRoot,
	}); err != nil {
		t.Fatalf("create parent: %v", err)
	}
	folder2Dir := filepath.Join(parentRoot, "folder1", "folder2", RepoBrainDirName)
	folder3Dir := filepath.Join(parentRoot, "folder1", "folder3", RepoBrainDirName)
	if err := os.MkdirAll(folder2Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(folder3Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	folder2, err := EnsureRepoWorkspace(ctx, db, folder2Dir, "workspace1")
	if err != nil {
		t.Fatalf("ensure folder2: %v", err)
	}
	folder3, err := EnsureRepoWorkspace(ctx, db, folder3Dir, "workspace1")
	if err != nil {
		t.Fatalf("ensure folder3: %v", err)
	}

	if err := db.CreateTask(ctx, &store.Task{ID: "01FOLDER2TASK00000000000000", WorkspaceID: folder2.WorkspaceID, Title: "Folder 2", Status: "open"}); err != nil {
		t.Fatalf("create folder2 task: %v", err)
	}
	if err := db.CreateTask(ctx, &store.Task{ID: "01FOLDER3TASK00000000000000", WorkspaceID: folder3.WorkspaceID, Title: "Folder 3", Status: "open"}); err != nil {
		t.Fatalf("create folder3 task: %v", err)
	}
	parentRows, err := db.ListTasks(ctx, store.TaskFilter{WorkspaceID: "workspace1"})
	if err != nil {
		t.Fatalf("list parent tasks: %v", err)
	}
	folder2Rows, err := db.ListTasks(ctx, store.TaskFilter{WorkspaceID: folder2.WorkspaceID})
	if err != nil {
		t.Fatalf("list folder2 tasks: %v", err)
	}
	folder3Rows, err := db.ListTasks(ctx, store.TaskFilter{WorkspaceID: folder3.WorkspaceID})
	if err != nil {
		t.Fatalf("list folder3 tasks: %v", err)
	}
	if len(parentRows) != 0 || len(folder2Rows) != 1 || len(folder3Rows) != 1 {
		t.Fatalf("counts parent/folder2/folder3 = %d/%d/%d, want 0/1/1",
			len(parentRows), len(folder2Rows), len(folder3Rows))
	}
}
