package index

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestShouldIndexPathTable is the regression harness for the centralized deny
// predicate: path normalization, nested deps, noise files, allowed lookalikes.
func TestShouldIndexPathTable(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		// allowed source and similarly-named paths
		{"internal/index/build.go", true},
		{"pkg/outlier/out.go", true},
		{"cmd/main.go", true},
		{"README.md", true},
		{"go.mod", true},
		{"web/package.json", true},
		{"src/main.rs", true},
		{"app/pyproject.toml", true},
		{"internal/buildutil/helper.go", true},
		{"pkg/node_modules_parser/parse.go", true},

		// path normalization
		{`internal\index\walk.go`, true},
		{"./cmd/main.go", true},
		{"cmd//main.go", true},
		{"", false},
		{"../escape.go", false},
		{"foo/../../etc/passwd", false},
		{"node_modules/../src/main.go", true},

		// dependency / build / cache dirs (even if committed)
		{"node_modules/react/index.js", false},
		{"NODE_MODULES/pkg/x.js", false},
		{"vendor/x/y.go", false},
		{"web/dist/bundle.js", false},
		{"web/DIST/app.js", false},
		{"target/debug/foo.rs", false},
		{"src/__pycache__/mod.cpython-311.pyc", false},
		{".venv/lib/python3.11/site-packages/x.py", false},
		{"app/venv/bin/python", false},
		{"app/.gradle/cache.bin", false},
		{"ios/Pods/Alamofire/Source/x.swift", false},
		{".terraform/providers/y.go", false},
		{"a/node_modules/b/node_modules/c.js", false},
		{"internal/index/testdata/fix.go", false},
		{".git/config", false},
		{".claude/worktrees/w/main.go", false},
		{"a/.hidden/file.go", false},

		// locks, checksums, minified, maps
		{"web/package-lock.json", false},
		{"YARN.LOCK", false},
		{"go.sum", false},
		{"Cargo.lock", false},
		{"poetry.lock", false},
		{"Gemfile.lock", false},
		{"flake.lock", false},
		{"checksums.txt", false},
		{"sha256sums", false},
		{"web/dist/app.min.js", false},
		{"assets/style.min.css", false},
		{"web/src/app.js.map", false},
		{"internal/foo.lock", false},
	}
	for _, tc := range tests {
		got := ShouldIndexPath(tc.path)
		if got != tc.want {
			t.Errorf("ShouldIndexPath(%q) = %v, want %v (normalized %q, isDenied=%v)",
				tc.path, got, tc.want, normalizeIndexPath(tc.path), isDenied(tc.path))
		}
	}
}

func TestNormalizeIndexPath(t *testing.T) {
	tests := []struct{ in, want string }{
		{"./foo/bar.go", "foo/bar.go"},
		{`foo\bar.go`, "foo/bar.go"},
		{"/abs/trim.go", "abs/trim.go"},
		{"foo//bar.go", "foo/bar.go"},
		{"a/../b.go", "b.go"},
		{"../outside.go", ""},
		{"", ""},
	}
	for _, tc := range tests {
		if got := normalizeIndexPath(tc.in); got != tc.want {
			t.Errorf("normalizeIndexPath(%q) = %q, want %q", tc.in, got, tc.want)
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
	writeWorkspaceFile(t, dir, "nested/node_modules/deep.js", "x")
	writeWorkspaceFile(t, dir, "testdata/fix.go", "package fix")
	writeWorkspaceFile(t, dir, ".git/HEAD", "ref")
	writeWorkspaceFile(t, dir, "vendor/v.go", "package v")
	if err := os.Symlink(filepath.Join(dir, "keep.go"), filepath.Join(dir, "link.go")); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	// Symlink dir escape: node_modules -> outside tree
	outside := t.TempDir()
	writeWorkspaceFile(t, outside, "evil.go", "package evil")
	if err := os.Symlink(outside, filepath.Join(dir, "linked_modules")); err != nil {
		t.Skipf("symlink dir unsupported: %v", err)
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

func TestWalkDirPrefixScopedStillDenies(t *testing.T) {
	dir := t.TempDir()
	writeWorkspaceFile(t, dir, "internal/index/keep.go", "package a")
	writeWorkspaceFile(t, dir, "internal/index/node_modules/dep.js", "x")
	writeWorkspaceFile(t, dir, "internal/index/dist/bundle.js", "x")
	got, err := walkDir(dir, []string{"internal/index"})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(got)
	if len(got) != 1 || got[0] != "internal/index/keep.go" {
		t.Errorf("prefix-scoped walkDir = %v, want [internal/index/keep.go]", got)
	}
}

func TestEnumerateGitAndWalkParity(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	writeWorkspaceFile(t, dir, "src/keep.go", "package a")
	writeWorkspaceFile(t, dir, "src/node_modules/dep.js", "x")
	writeWorkspaceFile(t, dir, "src/dist/bundle.min.js", "x")
	writeWorkspaceFile(t, dir, "src/yarn.lock", "x")
	writeWorkspaceFile(t, dir, "src/go.sum", "x")
	runGit(t, dir, "init")
	// Force-commit denied paths to prove git path filters them too.
	runGit(t, dir, "add", "-f", "src/keep.go", "src/node_modules/dep.js",
		"src/dist/bundle.min.js", "src/yarn.lock", "src/go.sum")
	git := newGitRunner(dir, slog.New(slog.NewTextHandler(io.Discard, nil)))
	gitFiles, usedGit, err := enumerate(context.Background(), dir, git, []string{"src"})
	if err != nil {
		t.Fatal(err)
	}
	if !usedGit {
		t.Fatal("expected git enumeration in a git repo")
	}
	walkFiles, err := walkDir(dir, []string{"src"})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(gitFiles)
	sort.Strings(walkFiles)
	if len(gitFiles) != 1 || gitFiles[0] != "src/keep.go" {
		t.Errorf("git enumerate = %v, want [src/keep.go]", gitFiles)
	}
	if len(walkFiles) != 1 || walkFiles[0] != "src/keep.go" {
		t.Errorf("walkDir = %v, want [src/keep.go]", walkFiles)
	}
	for _, f := range gitFiles {
		if !ShouldIndexPath(f) {
			t.Errorf("git path %q passed filter but ShouldIndexPath=false", f)
		}
	}
}

func TestFilterPathsGitCommittedDenied(t *testing.T) {
	raw := []string{
		"tracked.go",
		"node_modules/dep.js",
		"./vendor/x.go",
		`web\dist\app.js`,
		"package-lock.json",
		"internal/index/build.go",
	}
	got := filterPaths(raw, nil)
	sort.Strings(got)
	want := []string{"internal/index/build.go", "tracked.go"}
	if len(got) != len(want) {
		t.Fatalf("filterPaths = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("filterPaths = %v, want %v", got, want)
		}
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

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func TestIsDeniedDirNameMatchesPathPolicy(t *testing.T) {
	for _, name := range []string{"node_modules", "NODE_MODULES", "vendor", ".venv", "testdata", ".hidden"} {
		if !isDeniedDirName(name) {
			t.Errorf("isDeniedDirName(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"src", "internal", "buildutil", "outlier"} {
		if isDeniedDirName(name) {
			t.Errorf("isDeniedDirName(%q) = true, want false", name)
		}
	}
}

func TestEnumerateFallbackWhenNoGit(t *testing.T) {
	dir := t.TempDir()
	writeWorkspaceFile(t, dir, "only.go", "package a")
	writeWorkspaceFile(t, dir, "node_modules/nope.js", "x")
	git := &gitRunner{dir: dir, bin: ""}
	files, usedGit, err := enumerate(context.Background(), dir, git, nil)
	if err != nil {
		t.Fatal(err)
	}
	if usedGit {
		t.Fatal("expected walk fallback when git unavailable")
	}
	if len(files) != 1 || files[0] != "only.go" {
		t.Errorf("enumerate fallback = %v, want [only.go]", files)
	}
}

func TestFilterPathsNormalizesOutput(t *testing.T) {
	got := filterPaths([]string{`./foo\bar.go`, "node_modules/x.js"}, nil)
	if len(got) != 1 || got[0] != "foo/bar.go" || strings.Contains(got[0], `\`) {
		t.Errorf("filterPaths normalize = %v, want [foo/bar.go] without backslashes", got)
	}
}