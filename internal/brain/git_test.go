package brain

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// requireGit skips the test when the git binary is not on PATH, so CI
// without git still passes (the daemon degrades to no-op the same way).
func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not on PATH")
	}
}

// gitOut runs a read-only git command in dir and returns trimmed stdout,
// failing the test on error.
func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestGit_Init(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	g := NewGit(dir, nil)
	if !g.Available() {
		t.Fatal("git should be available")
	}
	if err := g.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if !isGitRepo(dir) {
		t.Fatal("expected .git after Init")
	}
	// Scaffold files committed.
	if _, err := os.Stat(filepath.Join(dir, ".gitignore")); err != nil {
		t.Fatalf("scaffold .gitignore missing: %v", err)
	}
	// Re-init is a no-op (no error, no second base commit).
	if err := g.Init(context.Background()); err != nil {
		t.Fatalf("re-Init: %v", err)
	}
	if n := gitOut(t, dir, "rev-list", "--count", "HEAD"); n != "1" {
		t.Fatalf("expected exactly 1 commit after idempotent re-init, got %s", n)
	}
}

func TestGit_Commit_ScopedAddOnly(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	g := NewGit(dir, nil)
	if err := g.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}

	// Two new files: one we ask to commit, one stray untracked file.
	tracked := filepath.Join(dir, "tracked.md")
	stray := filepath.Join(dir, "stray.md")
	if err := os.WriteFile(tracked, []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stray, []byte("nope\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := g.Commit(context.Background(), []string{tracked}, "chore(brain): test  [machine]"); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	// tracked.md is in HEAD; stray.md is NOT.
	files := gitOut(t, dir, "ls-tree", "--name-only", "HEAD")
	if !strings.Contains(files, "tracked.md") {
		t.Fatalf("tracked.md should be committed, got tree: %q", files)
	}
	if strings.Contains(files, "stray.md") {
		t.Fatalf("stray.md must NOT be committed (scoped add), got tree: %q", files)
	}
	// stray.md remains untracked in the working tree.
	if porcelain := gitOut(t, dir, "status", "--porcelain"); !strings.Contains(porcelain, "stray.md") {
		t.Fatalf("expected stray.md untracked, porcelain: %q", porcelain)
	}
}

func TestGit_Commit_MachineIdentity(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	g := NewGit(dir, nil)
	if err := g.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	f := filepath.Join(dir, "x.md")
	if err := os.WriteFile(f, []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := g.Commit(context.Background(), []string{f}, "chore(brain): identity  [machine]"); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if an := gitOut(t, dir, "log", "-1", "--format=%an"); an != machineAuthorName {
		t.Fatalf("author name = %q, want %q", an, machineAuthorName)
	}
	if ae := gitOut(t, dir, "log", "-1", "--format=%ae"); ae != machineAuthorEmail {
		t.Fatalf("author email = %q, want %q", ae, machineAuthorEmail)
	}
}

func TestGit_Commit_NoStagedIsNoop(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	g := NewGit(dir, nil)
	if err := g.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	before := gitOut(t, dir, "rev-list", "--count", "HEAD")
	// Commit a path that doesn't exist / has no changes — must not create
	// an empty commit.
	if err := g.Commit(context.Background(), []string{filepath.Join(dir, "ghost.md")}, "x"); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	after := gitOut(t, dir, "rev-list", "--count", "HEAD")
	if before != after {
		t.Fatalf("commit count changed %s -> %s; expected no-op", before, after)
	}
}

func TestGit_Commit_RejectsPathOutsideRepo(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	g := NewGit(dir, nil)
	if err := g.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	rel := g.relPaths([]string{"/etc/passwd", filepath.Join(dir, "ok.md")})
	if len(rel) != 1 || rel[0] != "ok.md" {
		t.Fatalf("relPaths should drop the outside-repo path, got %v", rel)
	}
}

func TestGit_PullRebase_ConflictSurfaces(t *testing.T) {
	requireGit(t)
	ctx := context.Background()

	// Build an upstream "remote" repo with a base commit on branch `main`,
	// then two clones diverge on the same line so a rebase conflicts.
	remote := t.TempDir()
	mustRun(t, remote, "init", "--bare", "--initial-branch=main")

	// Seed the remote via a throwaway clone.
	seed := t.TempDir()
	clone(t, remote, seed)
	mustRunEnv(t, seed, "checkout", "-b", "main")
	writeCommit(t, seed, filepath.Join(seed, "c.md"), "v1\n", "base")
	mustRunEnv(t, seed, "push", "-u", "origin", "main")

	// `other` clones the seeded remote, edits the same line, pushes — this
	// moves the upstream ahead of what `work` will have.
	other := t.TempDir()
	clone(t, remote, other)
	writeCommit(t, other, filepath.Join(other, "c.md"), "remote-change\n", "remote edit")
	mustRunEnv(t, other, "push", "origin", "main")

	// `work` clones BEFORE other's push is visible... we clone after, so
	// reset it back to base to simulate a stale local branch, then make a
	// conflicting local commit.
	work := t.TempDir()
	clone(t, remote, work)
	mustRunEnv(t, work, "reset", "--hard", "HEAD~1") // back to base
	conflictFile := filepath.Join(work, "c.md")
	writeCommit(t, work, conflictFile, "local-change\n", "local edit")

	g := NewGit(work, nil)
	err := g.PullRebase(ctx)
	if err == nil {
		t.Fatal("expected a conflict error from PullRebase")
	}
	var ce *ConflictError
	if !asConflict(err, &ce) {
		t.Fatalf("expected *ConflictError, got %T: %v", err, err)
	}
	// The rebase should have been aborted, leaving a clean tree (no
	// in-progress rebase dir).
	if _, statErr := os.Stat(filepath.Join(work, ".git", "rebase-merge")); statErr == nil {
		t.Fatal("rebase should have been aborted, but rebase-merge dir remains")
	}
}

func TestGit_Status(t *testing.T) {
	requireGit(t)
	dir := t.TempDir()
	g := NewGit(dir, nil)

	// Before init: zero value, not initialised.
	st, err := g.Status(context.Background())
	if err != nil {
		t.Fatalf("Status pre-init: %v", err)
	}
	if st.Initialized {
		t.Fatal("expected not initialised before Init")
	}

	if err := g.Init(context.Background()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	st, err = g.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !st.Initialized {
		t.Fatal("expected initialised after Init")
	}
	if st.LastCommit == "" {
		t.Fatal("expected a last-commit subject")
	}
	if st.Dirty {
		t.Fatal("clean repo reported dirty")
	}
	if st.HasRemote {
		t.Fatal("no remote configured; HasRemote should be false")
	}

	// Make a working-tree change → dirty.
	if err := os.WriteFile(filepath.Join(dir, "dirty.md"), []byte("d\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, _ = g.Status(context.Background())
	if !st.Dirty {
		t.Fatal("expected dirty after writing an untracked file")
	}
}

func TestGit_Unavailable_Degrades(t *testing.T) {
	g := &Git{dir: t.TempDir(), bin: ""}
	if g.Available() {
		t.Fatal("empty bin should be unavailable")
	}
	if err := g.Init(context.Background()); err != ErrGitUnavailable {
		t.Fatalf("Init want ErrGitUnavailable, got %v", err)
	}
	if err := g.Commit(context.Background(), []string{"x"}, "m"); err != ErrGitUnavailable {
		t.Fatalf("Commit want ErrGitUnavailable, got %v", err)
	}
	if err := g.Push(context.Background()); err != ErrGitUnavailable {
		t.Fatalf("Push want ErrGitUnavailable, got %v", err)
	}
	if _, err := g.Status(context.Background()); err != ErrGitUnavailable {
		t.Fatalf("Status want ErrGitUnavailable, got %v", err)
	}
}

// --- test helpers ---

func asConflict(err error, target **ConflictError) bool {
	for err != nil {
		if ce, ok := err.(*ConflictError); ok {
			*target = ce
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

func mustRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
}

// mustRunEnv runs git with the machine identity env so commits in test
// helpers don't depend on a configured user.name/email.
func mustRunEnv(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME="+machineAuthorName, "GIT_AUTHOR_EMAIL="+machineAuthorEmail,
		"GIT_COMMITTER_NAME="+machineAuthorName, "GIT_COMMITTER_EMAIL="+machineAuthorEmail,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
}

func clone(t *testing.T, remote, dst string) {
	t.Helper()
	cmd := exec.Command("git", "clone", remote, dst)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git clone: %v: %s", err, out)
	}
}

func writeCommit(t *testing.T, dir, path, content, msg string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	rel, _ := filepath.Rel(dir, path)
	mustRunEnv(t, dir, "add", rel)
	mustRunEnv(t, dir, "commit", "-m", msg)
}
