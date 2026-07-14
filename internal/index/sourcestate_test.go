package index

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	runGit(t, dir, "init")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "test")
}

func TestParsePorcelainPathsRename(t *testing.T) {
	porcelain := "R  old.go -> new.go\n M touched.go"
	got := parsePorcelainPaths(porcelain)
	want := []string{"new.go", "touched.go"}
	if len(got) != len(want) {
		t.Fatalf("paths = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("paths[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestParsePorcelainZPathsUnusualNamesAndRename(t *testing.T) {
	porcelain := " M ordinary.go\x00?? dir/a file.go\x00R  new name.go\x00old\nname.go\x00"
	got := parsePorcelainPaths(porcelain)
	want := []string{"ordinary.go", "dir/a file.go", "new name.go", "old\nname.go"}
	if len(got) != len(want) {
		t.Fatalf("paths = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("paths[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestContextStaleSameDirtyCountDifferentFiles(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)
	writeWorkspaceFile(t, dir, "a.go", goFileA)
	writeWorkspaceFile(t, dir, "web/b.ts", tsFileB)
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "init")

	svc, ms := testService(t)
	ctx := context.Background()
	indexID := indexIDForRoot(dir)
	if _, err := svc.Build(ctx, BuildRequest{WorkspaceID: "ws", Root: dir}); err != nil {
		t.Fatal(err)
	}
	build, err := ms.GetCodeIndexBuild(ctx, indexID)
	if err != nil {
		t.Fatal(err)
	}
	git := newGitRunner(dir, svc.logger)
	if contextStale(ctx, git, build, ms, indexID, dir) {
		t.Fatal("clean tree should be fresh")
	}

	// Dirty file A, then swap to dirty file B while keeping dirty count at 1.
	writeWorkspaceFile(t, dir, "a.go", goFileA+"\nfunc Extra() {}\n")
	if n, _ := git.dirtyCount(ctx); n != 1 {
		t.Fatalf("dirty count = %d, want 1 after editing a.go", n)
	}
	if !contextStale(ctx, git, build, ms, indexID, dir) {
		t.Fatal("edited a.go must be stale")
	}
	writeWorkspaceFile(t, dir, "a.go", goFileA) // restore tracked file
	writeWorkspaceFile(t, dir, "web/b.ts", tsFileB+"\nexport const swapped = 1;\n")
	if n, _ := git.dirtyCount(ctx); n != 1 {
		t.Fatalf("dirty count = %d, want 1 after swapping to b.ts", n)
	}
	if !contextStale(ctx, git, build, ms, indexID, dir) {
		t.Fatal("different dirty file at same count must be stale")
	}
}

func TestContextStaleDirtyBuildBecomesClean(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)
	writeWorkspaceFile(t, dir, "a.go", goFileA)
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "-m", "init")
	writeWorkspaceFile(t, dir, "a.go", goFileA+"\nfunc DirtyBuild() {}\n")

	svc, ms := testService(t)
	ctx := context.Background()
	indexID := indexIDForRoot(dir)
	if _, err := svc.Build(ctx, BuildRequest{WorkspaceID: "ws", Root: dir}); err != nil {
		t.Fatal(err)
	}
	build, _ := ms.GetCodeIndexBuild(ctx, indexID)
	if build.DirtyCount != 1 {
		t.Fatalf("dirty build count = %d, want 1", build.DirtyCount)
	}
	runGit(t, dir, "checkout", "--", "a.go")
	if !contextStale(ctx, newGitRunner(dir, svc.logger), build, ms, indexID, dir) {
		t.Fatal("cleaning a tree indexed while dirty must make the index stale")
	}
}

func TestContextStaleIgnoresDeniedDirtyPath(t *testing.T) {
	dir := t.TempDir()
	initGitRepo(t, dir)
	writeWorkspaceFile(t, dir, "a.go", goFileA)
	writeWorkspaceFile(t, dir, "node_modules/dep/index.js", "old")
	runGit(t, dir, "add", "a.go")
	runGit(t, dir, "add", "-f", "node_modules/dep/index.js")
	runGit(t, dir, "commit", "-m", "init")

	svc, ms := testService(t)
	ctx := context.Background()
	indexID := indexIDForRoot(dir)
	if _, err := svc.Build(ctx, BuildRequest{WorkspaceID: "ws", Root: dir}); err != nil {
		t.Fatal(err)
	}
	build, _ := ms.GetCodeIndexBuild(ctx, indexID)
	writeWorkspaceFile(t, dir, "node_modules/dep/index.js", "new")
	if contextStale(ctx, newGitRunner(dir, svc.logger), build, ms, indexID, dir) {
		t.Fatal("a denied dependency-only edit must not stale the code index")
	}
}

func TestContextStaleNonGitWorkspace(t *testing.T) {
	svc, ms := testService(t)
	dir := newWorkspace(t)
	ctx := context.Background()
	indexID := indexIDForRoot(dir)
	if _, err := svc.Build(ctx, BuildRequest{WorkspaceID: "ws", Root: dir}); err != nil {
		t.Fatal(err)
	}
	build, err := ms.GetCodeIndexBuild(ctx, indexID)
	if err != nil {
		t.Fatal(err)
	}
	git := newGitRunner(dir, svc.logger)
	if contextStale(ctx, git, build, ms, indexID, dir) {
		t.Fatal("unchanged non-git tree should be fresh")
	}
	writeWorkspaceFile(t, dir, "a.go", goFileA+"\nfunc Extra() {}\n")
	if !contextStale(ctx, git, build, ms, indexID, dir) {
		t.Fatal("edited non-git file must be stale")
	}
}

func TestStatusNonGitDetectsStale(t *testing.T) {
	svc, _ := testService(t)
	dir := newWorkspace(t)
	ctx := context.Background()
	if _, err := svc.Build(ctx, BuildRequest{WorkspaceID: "ws", Root: dir}); err != nil {
		t.Fatal(err)
	}
	st, err := svc.Status(ctx, "ws", dir)
	if err != nil {
		t.Fatal(err)
	}
	if st.Stale {
		t.Fatal("fresh non-git status should not be stale")
	}
	writeWorkspaceFile(t, dir, "web/b.ts", tsFileB+"\nexport const x = 1;\n")
	st, err = svc.Status(ctx, "ws", dir)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Stale {
		t.Fatal("edited non-git workspace must report stale")
	}
}

func TestNonGitWorkspaceStaleNewFile(t *testing.T) {
	svc, ms := testService(t)
	dir := newWorkspace(t)
	ctx := context.Background()
	indexID := indexIDForRoot(dir)
	if _, err := svc.Build(ctx, BuildRequest{WorkspaceID: "ws", Root: dir}); err != nil {
		t.Fatal(err)
	}
	build, _ := ms.GetCodeIndexBuild(ctx, indexID)
	writeWorkspaceFile(t, dir, "extra.go", "package main\n")
	if !nonGitWorkspaceStale(ctx, ms, indexID, dir, build) {
		t.Fatal("new file not in index must be stale")
	}
}

func seedIndexedFileStat(t *testing.T, ms *memStore, ws, path string, size int, mtime int64, hash string) {
	t.Helper()
	err := ms.UpsertCodeIndexedFiles(context.Background(), ws, []store.IndexedFile{{
		File: store.CodeIndexFile{
			WorkspaceID: ws, Path: path, PathTokens: tokenString(path),
			SizeBytes: size, MtimeUnix: mtime, ContentHash: hash, IndexedAt: time.Now().UTC(),
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
}

func TestFileStatStaleContentHash(t *testing.T) {
	dir := t.TempDir()
	rel := "a.go"
	content := []byte(goFileA)
	writeWorkspaceFile(t, dir, rel, string(content))
	info, err := os.Stat(filepath.Join(dir, rel))
	if err != nil {
		t.Fatal(err)
	}
	stored := store.CodeIndexFileStat{
		Path: rel, SizeBytes: int(info.Size()), MtimeUnix: info.ModTime().Unix(),
		ContentHash: hashBytes(content),
	}
	if fileStatStale(dir, rel, stored) {
		t.Fatal("matching stat should be fresh")
	}
	writeWorkspaceFile(t, dir, rel, goFileA+"\n// changed\n")
	if !fileStatStale(dir, rel, stored) {
		t.Fatal("content change with same mtime/size must be stale when hash differs")
	}
}
