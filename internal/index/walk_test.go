package index

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"testing"
)

func TestIsDenied(t *testing.T) {
	denied := []string{
		"node_modules/react/index.js", "vendor/x/y.go", "internal/index/testdata/f.go",
		".git/config", ".claude/worktrees/w/main.go", "web/dist/bundle.js",
		"a/.hidden/file.go",
	}
	for _, p := range denied {
		if !isDenied(p) {
			t.Errorf("isDenied(%q) = false, want true", p)
		}
	}
	allowed := []string{"internal/index/build.go", "web/src/App.tsx", "cmd/main.go", "README.md"}
	for _, p := range allowed {
		if isDenied(p) {
			t.Errorf("isDenied(%q) = true, want false", p)
		}
	}
}

func TestMatchesPrefixes(t *testing.T) {
	if !matchesPrefixes("internal/index/build.go", nil) {
		t.Error("empty prefixes should match everything")
	}
	if !matchesPrefixes("internal/index/build.go", []string{"internal/index"}) {
		t.Error("path under prefix should match")
	}
	if matchesPrefixes("cmd/main.go", []string{"internal/index"}) {
		t.Error("path outside prefix should not match")
	}
	if matchesPrefixes("internal/indexer/x.go", []string{"internal/index"}) {
		t.Error("sibling with shared prefix substring must not match (dir boundary)")
	}
}

func TestWalkDirDenylistAndSymlink(t *testing.T) {
	dir := t.TempDir()
	writeWorkspaceFile(t, dir, "keep.go", "package a")
	writeWorkspaceFile(t, dir, "node_modules/dep.go", "package dep")
	writeWorkspaceFile(t, dir, "testdata/fix.go", "package fix")
	writeWorkspaceFile(t, dir, ".git/HEAD", "ref")
	if err := os.Symlink(filepath.Join(dir, "keep.go"), filepath.Join(dir, "link.go")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	got, err := walkDir(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(got)
	if len(got) != 1 || got[0] != "keep.go" {
		t.Errorf("walkDir = %v, want [keep.go] (denylist + symlink pruned)", got)
	}
}

func TestSniffBinaryAndHash(t *testing.T) {
	if sniffBinary([]byte("plain text\nno nulls")) {
		t.Error("text wrongly classified as binary")
	}
	if !sniffBinary([]byte("has\x00nul")) {
		t.Error("NUL-containing data should be binary")
	}
	h1, h2, hDiff := hashBytes([]byte("abc")), hashBytes([]byte("abc")), hashBytes([]byte("abd"))
	if h1 == hDiff {
		t.Error("different content should hash differently")
	}
	if h1 != h2 {
		t.Error("hash should be deterministic")
	}
	if len(h1) != 64 {
		t.Errorf("sha256 hex should be 64 chars, got %d", len(h1))
	}
}

func TestEnumerateGitPath(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	writeWorkspaceFile(t, dir, "tracked.go", "package a")
	writeWorkspaceFile(t, dir, "node_modules/dep.js", "x") // untracked-but-denied
	runGit(t, dir, "init")
	runGit(t, dir, "add", "tracked.go")
	git := newGitRunner(dir, slog.New(slog.NewTextHandler(io.Discard, nil)))
	files, usedGit, err := enumerate(context.Background(), dir, git, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !usedGit {
		t.Fatal("expected git enumeration in a git repo")
	}
	for _, f := range files {
		if f == "node_modules/dep.js" {
			t.Error("denied dir should be filtered even on the git path")
		}
	}
	found := false
	for _, f := range files {
		if f == "tracked.go" {
			found = true
		}
	}
	if !found {
		t.Errorf("tracked.go missing from git enumeration: %v", files)
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}
