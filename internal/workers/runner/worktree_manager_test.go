package runner

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestGitWorktreeManagerConcurrentLeasesAreDistinctAndCleanupIsOwned(t *testing.T) {
	ctx := context.Background()
	repo := newWorktreeTestRepo(t, ctx)

	manager := NewGitWorktreeManager(filepath.Join(t.TempDir(), "leases"))
	leases := make([]WorktreeLease, 2)
	errs := make([]error, 2)
	var wg sync.WaitGroup
	for i, runID := range []string{"01EXAMPLEA", "01EXAMPLEB"} {
		wg.Add(1)
		go func(i int, runID string) {
			defer wg.Done()
			leases[i], errs[i] = manager.Prepare(ctx, repo, runID)
		}(i, runID)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("Prepare[%d]: %v", i, err)
		}
	}
	if leases[0].RootPath() == leases[1].RootPath() {
		t.Fatalf("concurrent leases shared root %q", leases[0].RootPath())
	}
	if leases[0].Branch() == leases[1].Branch() {
		t.Fatalf("concurrent leases shared branch %q", leases[0].Branch())
	}
	if pathWithin(repo, leases[0].RootPath()) || pathWithin(repo, leases[1].RootPath()) {
		t.Fatal("isolated worktree was created inside the parent checkout")
	}
	if err := os.WriteFile(filepath.Join(leases[0].RootPath(), "one.txt"), []byte("one"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(leases[1].RootPath(), "two.txt"), []byte("two"), 0o600); err != nil {
		t.Fatal(err)
	}
	for i, lease := range leases {
		if _, err := lease.Snapshot(ctx); err != nil {
			t.Fatalf("snapshot lease %d: %v", i, err)
		}
	}

	if err := leases[0].Cleanup(ctx); err != nil {
		t.Fatalf("cleanup first: %v", err)
	}
	if err := leases[0].Cleanup(ctx); err != nil {
		t.Fatalf("idempotent cleanup first: %v", err)
	}
	if _, err := os.Stat(leases[0].RootPath()); !os.IsNotExist(err) {
		t.Fatalf("first root still exists or stat failed unexpectedly: %v", err)
	}
	if _, err := os.Stat(leases[1].RootPath()); err != nil {
		t.Fatalf("cleaning first lease damaged second: %v", err)
	}
	if _, err := runGit(ctx, repo, "show-ref", "--verify", "refs/heads/"+leases[0].Branch()); err != nil {
		t.Fatalf("cleanup removed persisted branch: %v", err)
	}
	if err := leases[1].Cleanup(ctx); err != nil {
		t.Fatalf("cleanup second: %v", err)
	}
}

func TestGitWorktreeManagerRejectsBaseSymlinkIntoRepository(t *testing.T) {
	ctx := context.Background()
	repo := newWorktreeTestRepo(t, ctx)
	baseLink := filepath.Join(t.TempDir(), "leases")
	if err := os.Symlink(repo, baseLink); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	before, err := os.Stat(repo)
	if err != nil {
		t.Fatal(err)
	}

	manager := NewGitWorktreeManager(baseLink)
	lease, err := manager.Prepare(ctx, repo, "01SYMLINKESCAPE")
	if err == nil {
		if lease != nil {
			_ = lease.Cleanup(ctx)
		}
		t.Fatal("base symlink into repository unexpectedly accepted")
	}
	after, statErr := os.Stat(repo)
	if statErr != nil {
		t.Fatal(statErr)
	}
	if after.Mode().Perm() != before.Mode().Perm() {
		t.Fatalf("repository mode changed through base symlink: got %o want %o", after.Mode().Perm(), before.Mode().Perm())
	}
	if _, branchErr := runGit(ctx, repo, "show-ref", "--verify", "refs/heads/mcplexer/delegation/01symlinkescape"); branchErr == nil {
		t.Fatal("worktree branch was created despite rejected base")
	}
}

func TestGitWorktreeManagerRejectsCoreWorktreeBeforeRootDiscovery(t *testing.T) {
	ctx := context.Background()
	repo := newWorktreeTestRepo(t, ctx)
	redirect := t.TempDir()
	if _, err := runGit(ctx, repo, "config", "core.worktree", redirect); err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(t.TempDir(), "leases")
	manager := NewGitWorktreeManager(base)
	lease, err := manager.Prepare(ctx, repo, "01COREWORKTREE")
	if err == nil {
		if lease != nil {
			_ = lease.Abandon(ctx)
		}
		t.Fatal("core.worktree redirect unexpectedly accepted")
	}
	if !strings.Contains(err.Error(), "core.worktree") {
		t.Fatalf("error = %v, want core.worktree rejection", err)
	}
	if entries, readErr := os.ReadDir(base); readErr == nil && len(entries) != 0 {
		t.Fatalf("worktree artifacts created before config rejection: %v", entries)
	} else if readErr != nil && !os.IsNotExist(readErr) {
		t.Fatal(readErr)
	}
	cmd := exec.CommandContext(ctx, "git", "--git-dir="+filepath.Join(repo, ".git"), "show-ref", "--verify", "refs/heads/mcplexer/delegation/01coreworktree")
	if err := cmd.Run(); err == nil {
		t.Fatal("branch created before core.worktree rejection")
	}
}

func TestGitWorktreeManagerCleanSnapshotDeletesOwnedBranch(t *testing.T) {
	ctx := context.Background()
	repo := newWorktreeTestRepo(t, ctx)
	lease, err := NewGitWorktreeManager(filepath.Join(t.TempDir(), "leases")).Prepare(ctx, repo, "01CLEAN")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := lease.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Changed || snapshot.Branch != lease.Branch() || snapshot.Commit == "" {
		t.Fatalf("clean snapshot = %+v", snapshot)
	}
	if err := lease.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(lease.RootPath()); !os.IsNotExist(err) {
		t.Fatalf("clean worktree retained: %v", err)
	}
	if _, err := runGit(ctx, repo, "show-ref", "--verify", "refs/heads/"+lease.Branch()); err == nil {
		t.Fatal("clean owned branch was retained")
	}
}

func TestGitWorktreeManagerChangedSnapshotUsesFOSSIdentityAndRetainsBranch(t *testing.T) {
	ctx := context.Background()
	repo := newWorktreeTestRepo(t, ctx)
	lease, err := NewGitWorktreeManager(filepath.Join(t.TempDir(), "leases")).Prepare(ctx, repo, "01CHANGED")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lease.RootPath(), "change.txt"), []byte("open source\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := lease.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.Changed || snapshot.Commit == "" {
		t.Fatalf("changed snapshot = %+v", snapshot)
	}
	secondSnapshot, err := lease.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if secondSnapshot != snapshot {
		t.Fatalf("second snapshot = %+v, want recorded %+v", secondSnapshot, snapshot)
	}
	identity, err := runGit(ctx, lease.RootPath(), "show", "-s", "--format=%an <%ae>", snapshot.Commit)
	if err != nil {
		t.Fatal(err)
	}
	if identity != "MCPlexer Delegated Worker <opensource@mcplexer.dev>" {
		t.Fatalf("snapshot identity = %q", identity)
	}
	if err := lease.Cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(ctx, repo, "show-ref", "--verify", "refs/heads/"+lease.Branch()); err != nil {
		t.Fatalf("changed recovery branch removed: %v", err)
	}
}

func TestGitWorktreeManagerDisablesRepositoryHooks(t *testing.T) {
	ctx := context.Background()
	repo := newWorktreeTestRepo(t, ctx)
	sentinel := filepath.Join(t.TempDir(), "hook-ran")
	hook := filepath.Join(repo, ".git", "hooks", "post-checkout")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nprintf hook-ran > \""+sentinel+"\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	lease, err := NewGitWorktreeManager(filepath.Join(t.TempDir(), "leases")).Prepare(ctx, repo, "01HOOK")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatalf("repository hook executed: %v", err)
	}
	if err := lease.Abandon(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestGitWorktreeManagerRejectsExecutableFilterBeforeCheckout(t *testing.T) {
	ctx := context.Background()
	repo := newWorktreeTestRepo(t, ctx)
	if _, err := runGit(ctx, repo, "config", "filter.evil.clean", "sh -c 'exit 99'"); err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(t.TempDir(), "leases")
	lease, err := NewGitWorktreeManager(base).Prepare(ctx, repo, "01FILTER")
	if err == nil {
		if lease != nil {
			_ = lease.Abandon(ctx)
		}
		t.Fatal("executable filter unexpectedly accepted")
	}
	if !strings.Contains(err.Error(), "filter.evil.clean") {
		t.Fatalf("error = %v, want filter rejection", err)
	}
	if _, err := runGit(ctx, repo, "show-ref", "--verify", "refs/heads/mcplexer/delegation/01filter"); err == nil {
		t.Fatal("branch created before filter rejection")
	}
}

func TestGitWorktreeManagerWillNotDeleteForeignMovedRef(t *testing.T) {
	ctx := context.Background()
	repo := newWorktreeTestRepo(t, ctx)
	lease, err := NewGitWorktreeManager(filepath.Join(t.TempDir(), "leases")).Prepare(ctx, repo, "01FOREIGNREF")
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := lease.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "foreign.txt"), []byte("foreign\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(ctx, repo, "add", "foreign.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(ctx, repo, "commit", "-m", "foreign owner commit"); err != nil {
		t.Fatal(err)
	}
	foreignCommit, err := runGit(ctx, repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(ctx, repo, "update-ref", "refs/heads/"+lease.Branch(), foreignCommit, snapshot.Commit); err != nil {
		t.Fatal(err)
	}
	if err := lease.Cleanup(ctx); err == nil {
		t.Fatal("cleanup deleted or accepted a foreign-moved branch")
	}
	got, err := runGit(ctx, repo, "rev-parse", "refs/heads/"+lease.Branch())
	if err != nil || got != foreignCommit {
		t.Fatalf("foreign ref = %q err=%v, want %q", got, err, foreignCommit)
	}
}

func TestFailedPreparationRetainsForeignOrReplacedPathAndBranch(t *testing.T) {
	ctx := context.Background()
	repo := newWorktreeTestRepo(t, ctx)
	expected, err := runGit(ctx, repo, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	branch := "mcplexer/delegation/01rollbackforeign"
	if _, err := runGit(ctx, repo, "update-ref", "refs/heads/"+branch, expected); err != nil {
		t.Fatal(err)
	}
	foreignPath := filepath.Join(t.TempDir(), "replaced-path")
	if err := os.Mkdir(foreignPath, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(foreignPath, "foreign-owner.txt")
	if err := os.WriteFile(sentinel, []byte("do not remove"), 0o600); err != nil {
		t.Fatal(err)
	}
	err = cleanupFailedPreparation(ctx, repo, foreignPath, branch, expected, t.TempDir(), errors.New("post-add verification failed"))
	if err == nil || !strings.Contains(err.Error(), "retained recovery") {
		t.Fatalf("error = %v, want retained recovery detail", err)
	}
	if data, err := os.ReadFile(sentinel); err != nil || string(data) != "do not remove" {
		t.Fatalf("foreign path was mutated: %q err=%v", data, err)
	}
	got, err := runGit(ctx, repo, "rev-parse", "refs/heads/"+branch)
	if err != nil || got != expected {
		t.Fatalf("recovery branch = %q err=%v, want %q", got, err, expected)
	}
}

func newWorktreeTestRepo(t *testing.T, ctx context.Context) string {
	t.Helper()
	repo := filepath.Join(t.TempDir(), "repo")
	if err := os.Mkdir(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(ctx, repo, "init", "-b", "main"); err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(ctx, repo, "config", "user.name", "Example Maintainer"); err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(ctx, repo, "config", "user.email", "maintainer@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("# Example\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(ctx, repo, "add", "README.md"); err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(ctx, repo, "commit", "-m", "initial"); err != nil {
		t.Fatal(err)
	}
	return repo
}
