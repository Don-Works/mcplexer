package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const worktreeCommandOutputLimit = 4096

// WorktreeLease is one run's isolated git checkout. Cleanup is idempotent;
// implementations must never remove a path owned by another lease.
type WorktreeLease interface {
	RootPath() string
	WorkspacePath() string
	Branch() string
	ConfigureSnapshotPolicy([]string, bool) error
	Snapshot(context.Context) (WorktreeSnapshot, error)
	Abandon(context.Context) error
	Cleanup(context.Context) error
}

// WorktreeSnapshot is the authoritative trusted-runner git result. Changed is
// true when the runner created a new commit for uncommitted worker changes.
type WorktreeSnapshot struct {
	Branch  string
	Commit  string
	Changed bool
}

// WorktreeManager creates isolated checkouts for delegated worker runs.
type WorktreeManager interface {
	Prepare(context.Context, string, string) (WorktreeLease, error)
}

type gitWorktreeManager struct {
	baseDir string
	gitMu   sync.Mutex
}

// NewGitWorktreeManager returns the production manager. An empty baseDir uses
// a private directory in the current user's cache (falling back to os.TempDir
// when the cache directory is unavailable). The worktrees live outside the
// source checkout so creation never adds an untracked directory to it.
func NewGitWorktreeManager(baseDir string) WorktreeManager {
	if strings.TrimSpace(baseDir) == "" {
		baseDir = defaultGitWorktreeBaseDir()
	}
	return &gitWorktreeManager{baseDir: baseDir}
}

func defaultGitWorktreeBaseDir() string {
	if cacheDir, err := os.UserCacheDir(); err == nil && strings.TrimSpace(cacheDir) != "" {
		return filepath.Join(cacheDir, "mcplexer", "worker-worktrees")
	}
	return filepath.Join(os.TempDir(), "mcplexer-worker-worktrees")
}

type gitWorktreeLease struct {
	repoRoot      string
	rootPath      string
	workspacePath string
	branch        string
	branchCommit  string
	gitDir        string
	commonDir     string
	hooksDir      string
	gitMu         *sync.Mutex

	stateMu          sync.Mutex
	cleaned          bool
	snapshotComplete bool
	snapshotChanged  bool
	worktreeRemoved  bool
	branchRemoved    bool
	claimedPaths     []string
	reviewOnly       bool
}

func (l *gitWorktreeLease) RootPath() string      { return l.rootPath }
func (l *gitWorktreeLease) WorkspacePath() string { return l.workspacePath }
func (l *gitWorktreeLease) Branch() string        { return l.branch }

func (l *gitWorktreeLease) ConfigureSnapshotPolicy(claims []string, reviewOnly bool) error {
	l.stateMu.Lock()
	defer l.stateMu.Unlock()
	if l.snapshotComplete || l.cleaned {
		return errors.New("worktree snapshot policy can no longer be changed")
	}
	for _, claim := range claims {
		if !pathWithin(l.rootPath, claim) {
			return fmt.Errorf("snapshot claim %q is outside worktree", claim)
		}
	}
	l.claimedPaths = append([]string(nil), claims...)
	l.reviewOnly = reviewOnly
	return nil
}

const trustedSnapshotMessage = "chore(delegation): snapshot isolated worker changes"

// Snapshot records all worker changes from the trusted host side. The model
// sandbox never receives access to the linked worktree's common git directory.
func (l *gitWorktreeLease) Snapshot(ctx context.Context) (WorktreeSnapshot, error) {
	l.stateMu.Lock()
	defer l.stateMu.Unlock()
	if l.cleaned {
		return WorktreeSnapshot{}, errors.New("worktree was already cleaned")
	}
	if l.snapshotComplete {
		return WorktreeSnapshot{Branch: l.branch, Commit: l.branchCommit, Changed: l.snapshotChanged}, nil
	}
	if l.gitMu != nil {
		l.gitMu.Lock()
		defer l.gitMu.Unlock()
	}
	if err := l.verifyIdentity(ctx); err != nil {
		return WorktreeSnapshot{}, err
	}
	hooksDir := l.hooksDir
	if hooksDir == "" || pathWithin(l.rootPath, hooksDir) {
		return WorktreeSnapshot{}, errors.New("trusted hooks directory is invalid")
	}
	currentBranch, err := runTrustedGit(ctx, l.rootPath, hooksDir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return WorktreeSnapshot{}, fmt.Errorf("verify worktree branch: %w", err)
	}
	if strings.TrimSpace(currentBranch) != l.branch {
		return WorktreeSnapshot{}, fmt.Errorf("refusing snapshot: branch changed from %q to %q", l.branch, strings.TrimSpace(currentBranch))
	}
	// Revalidate repository-local includes, executable filters, and
	// core.worktree immediately before git add. These settings can execute code
	// or redirect the worktree if modified after checkout preparation.
	if err := validateTrustedCheckoutConfig(ctx, l.rootPath, hooksDir); err != nil {
		return WorktreeSnapshot{}, fmt.Errorf("refusing snapshot: %w", err)
	}
	// Re-assert the owned root immediately adjacent to the first mutating Git
	// operation, closing any config/worktree redirection TOCTOU window.
	if err := l.verifyIdentity(ctx); err != nil {
		return WorktreeSnapshot{}, err
	}

	if _, err := runTrustedGit(ctx, l.rootPath, hooksDir, "add", "-A", "--", ":/"); err != nil {
		return WorktreeSnapshot{}, fmt.Errorf("stage isolated worktree: %w", err)
	}
	changed, err := trustedGitHasStagedChanges(ctx, l.rootPath, hooksDir)
	if err != nil {
		return WorktreeSnapshot{}, err
	}
	paths, err := trustedGitChangedPaths(ctx, l.rootPath, hooksDir)
	if err != nil {
		return WorktreeSnapshot{}, err
	}
	if l.reviewOnly && len(paths) > 0 {
		return WorktreeSnapshot{}, errors.New("review-mode isolated worker modified repository files")
	}
	for _, changedPath := range paths {
		if !l.claimsAllow(changedPath) {
			return WorktreeSnapshot{}, fmt.Errorf("changed path %q is outside declared touches_files", changedPath)
		}
	}
	if err := trustedGitNoResidualChanges(ctx, l.rootPath, hooksDir); err != nil {
		return WorktreeSnapshot{}, err
	}
	if changed {
		if _, err := runTrustedGit(ctx, l.rootPath, hooksDir, "commit", "--no-verify", "--no-gpg-sign", "-m", trustedSnapshotMessage); err != nil {
			return WorktreeSnapshot{}, fmt.Errorf("commit isolated worktree snapshot: %w", err)
		}
	}
	if err := trustedGitNoResidualChanges(ctx, l.rootPath, hooksDir); err != nil {
		return WorktreeSnapshot{}, err
	}
	commit, err := runTrustedGit(ctx, l.rootPath, hooksDir, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return WorktreeSnapshot{}, fmt.Errorf("resolve isolated snapshot commit: %w", err)
	}
	commit = strings.TrimSpace(commit)
	if commit == "" {
		return WorktreeSnapshot{}, errors.New("isolated snapshot commit is empty")
	}
	l.snapshotComplete = true
	l.snapshotChanged = changed
	l.branchCommit = commit
	return WorktreeSnapshot{Branch: l.branch, Commit: commit, Changed: changed}, nil
}

func (l *gitWorktreeLease) claimsAllow(rel string) bool {
	if len(l.claimedPaths) == 0 {
		return true
	}
	abs := filepath.Join(l.rootPath, filepath.FromSlash(rel))
	for _, claim := range l.claimedPaths {
		if pathWithin(claim, abs) {
			return true
		}
	}
	return false
}

// Abandon is the explicit pre-execution rollback path. It is intentionally
// separate from Cleanup so preparation failures do not create empty snapshot
// commits or retain a checkout that no model ever touched.
func (l *gitWorktreeLease) Abandon(ctx context.Context) error {
	l.stateMu.Lock()
	defer l.stateMu.Unlock()
	if l.cleaned {
		return nil
	}
	if l.snapshotComplete {
		return errors.New("cannot abandon a snapshotted worktree")
	}
	if l.gitMu != nil {
		l.gitMu.Lock()
		defer l.gitMu.Unlock()
	}
	if err := l.removeOwnedWorktreeLocked(ctx); err != nil {
		return err
	}
	if !l.branchRemoved {
		if err := trustedDeleteBranch(ctx, l.repoRoot, l.hooksDir, l.branch, l.branchCommit); err != nil {
			return err
		}
		l.branchRemoved = true
	}
	if err := os.RemoveAll(l.hooksDir); err != nil {
		return err
	}
	l.cleaned = true
	return nil
}

func (m *gitWorktreeManager) Prepare(
	ctx context.Context, workspacePath, runID string,
) (WorktreeLease, error) {
	workspacePath, err := existingDirectory(workspacePath)
	if err != nil {
		return nil, fmt.Errorf("worktree workspace: %w", err)
	}
	bootstrapHooks, err := newTrustedHooksDir(os.TempDir(), "")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(bootstrapHooks) //nolint:errcheck
	// Inspect the source checkout's own local configuration before trusting
	// rev-parse's idea of the repository root. In particular, core.worktree
	// can redirect --show-toplevel away from the checkout whose config caused
	// the redirection.
	if err := validateTrustedCheckoutConfig(ctx, workspacePath, bootstrapHooks); err != nil {
		return nil, err
	}
	repoText, err := runTrustedGit(ctx, workspacePath, bootstrapHooks, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, fmt.Errorf("worktree repository lookup: %w", err)
	}
	repoRoot, err := existingDirectory(strings.TrimSpace(repoText))
	if err != nil {
		return nil, fmt.Errorf("worktree repository root: %w", err)
	}
	relWorkspace, err := filepath.Rel(repoRoot, workspacePath)
	if err != nil || pathEscapes(relWorkspace) {
		return nil, fmt.Errorf("workspace %q is outside repository %q", workspacePath, repoRoot)
	}
	baseDir, err := prepareGitWorktreeBase(m.baseDir, repoRoot)
	if err != nil {
		return nil, err
	}
	hooksDir, err := newTrustedHooksDir(baseDir, repoRoot)
	if err != nil {
		return nil, err
	}
	keepHooks := false
	defer func() {
		if !keepHooks {
			_ = os.RemoveAll(hooksDir)
		}
	}()
	if err := validateTrustedCheckoutConfig(ctx, repoRoot, hooksDir); err != nil {
		return nil, err
	}
	commonDir, err := trustedGitCommonDir(ctx, repoRoot, hooksDir)
	if err != nil {
		return nil, fmt.Errorf("resolve repository common git dir: %w", err)
	}
	head, err := runTrustedGit(ctx, repoRoot, hooksDir, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return nil, fmt.Errorf("worktree HEAD lookup: %w", err)
	}
	head = strings.TrimSpace(head)
	if head == "" {
		return nil, errors.New("worktree HEAD is empty")
	}
	prefix := safeWorktreeComponent(runID)
	reserved, err := os.MkdirTemp(baseDir, prefix+"-")
	if err != nil {
		return nil, fmt.Errorf("reserve worktree path: %w", err)
	}
	if err := os.Remove(reserved); err != nil {
		return nil, fmt.Errorf("release reserved worktree path: %w", err)
	}
	branch := "mcplexer/delegation/" + prefix
	branchRef := "refs/heads/" + branch
	zeroOID := strings.Repeat("0", len(head))

	m.gitMu.Lock()
	_, reserveErr := runTrustedGit(ctx, repoRoot, hooksDir, "update-ref", branchRef, head, zeroOID)
	if reserveErr != nil {
		m.gitMu.Unlock()
		_ = os.Remove(reserved)
		return nil, fmt.Errorf("reserve isolated branch %q: %w", branch, reserveErr)
	}
	_, addErr := runTrustedGit(ctx, repoRoot, hooksDir, "worktree", "add", reserved, branch)
	m.gitMu.Unlock()
	if addErr != nil {
		return nil, cleanupFailedPreparation(ctx, repoRoot, reserved, branch, head, hooksDir,
			fmt.Errorf("create isolated worktree: %w", addErr))
	}
	rootPath, err := existingDirectory(reserved)
	if err != nil {
		return nil, cleanupFailedPreparation(ctx, repoRoot, reserved, branch, head, hooksDir, err)
	}
	if filepath.Clean(rootPath) != filepath.Clean(reserved) {
		return nil, cleanupFailedPreparation(ctx, repoRoot, reserved, branch, head, hooksDir,
			fmt.Errorf("reserved worktree path was substituted: got %q want %q", rootPath, reserved))
	}
	gitDirText, err := runTrustedGit(ctx, rootPath, hooksDir, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return nil, cleanupFailedPreparation(ctx, repoRoot, rootPath, branch, head, hooksDir, err)
	}
	gitDir, err := existingDirectory(strings.TrimSpace(gitDirText))
	if err != nil {
		return nil, cleanupFailedPreparation(ctx, repoRoot, rootPath, branch, head, hooksDir, err)
	}
	lease := &gitWorktreeLease{
		repoRoot: repoRoot, rootPath: rootPath, workspacePath: rootPath,
		branch: branch, branchCommit: head, gitDir: gitDir, commonDir: commonDir,
		hooksDir: hooksDir, gitMu: &m.gitMu,
	}
	if err := verifyPreparedWorktree(ctx, lease, branchRef, head); err != nil {
		return nil, cleanupFailedPreparation(ctx, repoRoot, rootPath, branch, head, hooksDir, err)
	}
	isolatedWorkspace := rootPath
	if relWorkspace != "." {
		isolatedWorkspace = filepath.Join(rootPath, relWorkspace)
	}
	isolatedWorkspace, err = existingDirectory(isolatedWorkspace)
	if err != nil {
		return nil, cleanupFailedPreparation(ctx, repoRoot, rootPath, branch, head, hooksDir, err)
	}
	lease.workspacePath = isolatedWorkspace
	keepHooks = true
	return lease, nil
}

func verifyPreparedWorktree(ctx context.Context, lease *gitWorktreeLease, branchRef, expectedCommit string) error {
	if err := validateTrustedCheckoutConfig(ctx, lease.rootPath, lease.hooksDir); err != nil {
		return fmt.Errorf("validate created isolated checkout: %w", err)
	}
	if err := lease.verifyIdentity(ctx); err != nil {
		return err
	}
	currentBranch, err := runTrustedGit(ctx, lease.rootPath, lease.hooksDir, "symbolic-ref", "-q", "HEAD")
	if err != nil {
		return fmt.Errorf("read created isolated checkout branch: %w", err)
	}
	if strings.TrimSpace(currentBranch) != branchRef {
		return fmt.Errorf("created isolated checkout branch = %q, want %q", strings.TrimSpace(currentBranch), branchRef)
	}
	worktreeHead, err := runTrustedGit(ctx, lease.rootPath, lease.hooksDir, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return fmt.Errorf("read created isolated checkout HEAD: %w", err)
	}
	if strings.TrimSpace(worktreeHead) != expectedCommit {
		return fmt.Errorf("created isolated checkout HEAD = %q, want %q", strings.TrimSpace(worktreeHead), expectedCommit)
	}
	reservedHead, err := runTrustedGit(ctx, lease.repoRoot, lease.hooksDir, "rev-parse", "--verify", branchRef+"^{commit}")
	if err != nil {
		return fmt.Errorf("read reserved isolated branch: %w", err)
	}
	if strings.TrimSpace(reservedHead) != expectedCommit {
		return fmt.Errorf("reserved isolated branch = %q, want %q", strings.TrimSpace(reservedHead), expectedCommit)
	}
	return nil
}

func newTrustedHooksDir(parent, forbiddenRoot string) (string, error) {
	dir, err := os.MkdirTemp(parent, ".mcplexer-hooks-")
	if err != nil {
		return "", fmt.Errorf("create trusted hooks directory: %w", err)
	}
	fail := func(cause error) (string, error) {
		_ = os.RemoveAll(dir)
		return "", cause
	}
	dir, err = existingDirectory(dir)
	if err != nil {
		return fail(err)
	}
	if forbiddenRoot != "" && pathWithin(forbiddenRoot, dir) {
		return fail(errors.New("trusted hooks directory is inside repository"))
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fail(err)
	}
	return dir, nil
}

func validateTrustedCheckoutConfig(ctx context.Context, repoRoot, hooksDir string) error {
	includes, err := trustedGitConfiguredIncludes(ctx, repoRoot, hooksDir)
	if err != nil {
		return err
	}
	if includes != "" {
		return errors.New("refusing isolated checkout: repository-local git config includes are active")
	}
	filters, err := trustedGitConfiguredFilters(ctx, repoRoot, hooksDir)
	if err != nil {
		return err
	}
	if filters != "" {
		return fmt.Errorf("refusing isolated checkout: executable git clean/smudge/process filters are active: %s", filters)
	}
	cmd := trustedGitCommand(ctx, repoRoot, hooksDir, "config", "--no-includes", "--get", "core.worktree")
	if out, err := cmd.Output(); err == nil {
		if value := strings.TrimSpace(string(out)); value != "" {
			return fmt.Errorf("refusing isolated checkout: effective core.worktree is set to %q", value)
		}
	} else {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
			return fmt.Errorf("inspect effective core.worktree: %w", err)
		}
	}
	return nil
}

// prepareGitWorktreeBase resolves every existing path component before it
// creates anything. This prevents an apparently external base path from being
// a symlink into the source repository. It resolves and checks the completed
// directory a second time before chmod or worktree creation to fail closed if
// the path changed while it was being made.
func prepareGitWorktreeBase(baseDir, repoRoot string) (string, error) {
	baseDir, err := canonicalPathForCreate(baseDir)
	if err != nil {
		return "", fmt.Errorf("worktree base path: %w", err)
	}
	if pathWithin(repoRoot, baseDir) {
		return "", fmt.Errorf("worktree base %q must be outside repository %q", baseDir, repoRoot)
	}
	if err := os.MkdirAll(baseDir, 0o700); err != nil {
		return "", fmt.Errorf("create worktree base: %w", err)
	}
	baseDir, err = existingDirectory(baseDir)
	if err != nil {
		return "", fmt.Errorf("canonicalize worktree base: %w", err)
	}
	if pathWithin(repoRoot, baseDir) {
		return "", fmt.Errorf("worktree base %q must be outside repository %q", baseDir, repoRoot)
	}
	if err := os.Chmod(baseDir, 0o700); err != nil {
		return "", fmt.Errorf("secure worktree base: %w", err)
	}
	return baseDir, nil
}

func canonicalPathForCreate(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("directory is empty")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	probe := filepath.Clean(abs)
	var missing []string
	for {
		if _, err := os.Lstat(probe); err == nil {
			break
		} else if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			return "", fmt.Errorf("no existing ancestor for %q", path)
		}
		missing = append(missing, filepath.Base(probe))
		probe = parent
	}
	resolved, err := filepath.EvalSymlinks(probe)
	if err != nil {
		return "", err
	}
	for i := len(missing) - 1; i >= 0; i-- {
		resolved = filepath.Join(resolved, missing[i])
	}
	return filepath.Clean(resolved), nil
}

func (l *gitWorktreeLease) Cleanup(ctx context.Context) error {
	l.stateMu.Lock()
	defer l.stateMu.Unlock()
	if l.cleaned {
		return nil
	}
	if !l.snapshotComplete {
		return errors.New("refusing worktree cleanup before trusted snapshot")
	}
	if l.gitMu != nil {
		l.gitMu.Lock()
		defer l.gitMu.Unlock()
	}
	if err := l.removeOwnedWorktreeLocked(ctx); err != nil {
		return err
	}
	if !l.snapshotChanged && !l.branchRemoved {
		if err := trustedDeleteBranch(ctx, l.repoRoot, l.hooksDir, l.branch, l.branchCommit); err != nil {
			return err
		}
		l.branchRemoved = true
	}
	if err := os.RemoveAll(l.hooksDir); err != nil {
		return err
	}
	l.cleaned = true
	return nil
}

func (l *gitWorktreeLease) removeOwnedWorktreeLocked(ctx context.Context) error {
	if l.worktreeRemoved {
		return nil
	}
	if err := l.verifyIdentity(ctx); err != nil {
		return err
	}
	if _, err := runTrustedGit(ctx, l.repoRoot, l.hooksDir, "worktree", "remove", "--force", l.rootPath); err != nil {
		return err
	}
	l.worktreeRemoved = true
	return nil
}

func trustedDeleteBranch(ctx context.Context, repoRoot, hooksDir, branch, expectedCommit string) error {
	if strings.TrimSpace(expectedCommit) == "" {
		return errors.New("refusing to delete isolated branch without expected commit")
	}
	ref := "refs/heads/" + branch
	if _, err := runTrustedGit(ctx, repoRoot, hooksDir, "update-ref", "-d", ref, expectedCommit); err != nil {
		return fmt.Errorf("delete isolated branch %q at expected commit %s: %w", branch, expectedCommit, err)
	}
	return nil
}

func (l *gitWorktreeLease) verifyIdentity(ctx context.Context) error {
	if l.rootPath == "" || l.repoRoot == "" || l.gitDir == "" || l.commonDir == "" || l.hooksDir == "" {
		return errors.New("worktree lease is incomplete")
	}
	if _, err := os.Lstat(l.rootPath); err != nil {
		return fmt.Errorf("inspect worktree: %w", err)
	}
	actualText, err := runTrustedGit(ctx, l.rootPath, l.hooksDir, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return fmt.Errorf("verify worktree: %w", err)
	}
	actual, err := existingDirectory(strings.TrimSpace(actualText))
	if err != nil {
		return fmt.Errorf("canonicalize worktree git dir: %w", err)
	}
	if actual != l.gitDir {
		return fmt.Errorf("refusing worktree operation: git dir changed from %q to %q", l.gitDir, actual)
	}
	actualCommon, err := trustedGitCommonDir(ctx, l.rootPath, l.hooksDir)
	if err != nil {
		return fmt.Errorf("verify worktree common git dir: %w", err)
	}
	if actualCommon != l.commonDir {
		return fmt.Errorf("refusing worktree operation: common git dir changed from %q to %q", l.commonDir, actualCommon)
	}
	topText, err := runTrustedGit(ctx, l.rootPath, l.hooksDir, "rev-parse", "--show-toplevel")
	if err != nil {
		return fmt.Errorf("verify worktree top-level: %w", err)
	}
	top, err := existingDirectory(strings.TrimSpace(topText))
	if err != nil {
		return fmt.Errorf("canonicalize worktree top-level: %w", err)
	}
	if top != l.rootPath {
		return fmt.Errorf("refusing worktree operation: top-level changed from %q to %q", l.rootPath, top)
	}
	return nil
}

func trustedGitCommonDir(ctx context.Context, dir, hooksDir string) (string, error) {
	text, err := runTrustedGit(ctx, dir, hooksDir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", err
	}
	common := strings.TrimSpace(text)
	if common == "" || !filepath.IsAbs(common) {
		return "", fmt.Errorf("git common dir is not absolute: %q", common)
	}
	return existingDirectory(common)
}

func trustedGitHasStagedChanges(ctx context.Context, dir, hooksDir string) (bool, error) {
	cmd := trustedGitCommand(ctx, dir, hooksDir, "diff", "--cached", "--quiet", "--exit-code", "--no-ext-diff", "--no-textconv", "--", ":/")
	err := cmd.Run()
	if err == nil {
		return false, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return true, nil
	}
	return false, fmt.Errorf("inspect staged isolated changes: %w", err)
}

func trustedGitChangedPaths(ctx context.Context, dir, hooksDir string) ([]string, error) {
	cmd := trustedGitCommand(ctx, dir, hooksDir, "diff", "--cached", "--name-only", "--no-renames", "--no-ext-diff", "--no-textconv", "-z", "--", ":/")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("list staged isolated paths: %w", err)
	}
	parts := bytes.Split(out, []byte{0})
	paths := make([]string, 0, len(parts))
	for _, part := range parts {
		if len(part) == 0 {
			continue
		}
		path := filepath.ToSlash(string(part))
		if filepath.IsAbs(path) || pathEscapes(filepath.FromSlash(path)) || strings.ContainsRune(path, '\x00') {
			return nil, fmt.Errorf("invalid staged path %q", path)
		}
		paths = append(paths, path)
	}
	return paths, nil
}

func trustedGitConfiguredFilters(ctx context.Context, dir, hooksDir string) (string, error) {
	keys, err := trustedGitConfigKeys(ctx, dir, hooksDir)
	if err != nil {
		return "", fmt.Errorf("inspect executable git filters: %w", err)
	}
	var found []string
	for _, key := range keys {
		lower := strings.ToLower(key)
		if strings.HasPrefix(lower, "filter.") &&
			(strings.HasSuffix(lower, ".clean") || strings.HasSuffix(lower, ".smudge") || strings.HasSuffix(lower, ".process")) {
			found = append(found, key)
		}
	}
	return strings.Join(found, ", "), nil
}

func trustedGitConfiguredIncludes(ctx context.Context, dir, hooksDir string) (string, error) {
	keys, err := trustedGitConfigKeys(ctx, dir, hooksDir)
	if err != nil {
		return "", fmt.Errorf("inspect repository git includes: %w", err)
	}
	var found []string
	for _, key := range keys {
		if strings.HasPrefix(strings.ToLower(key), "include") {
			found = append(found, key)
		}
	}
	return strings.Join(found, ", "), nil
}

func trustedGitConfigKeys(ctx context.Context, dir, hooksDir string) ([]string, error) {
	cmd := trustedGitCommand(ctx, dir, hooksDir, "config", "--no-includes", "--name-only", "--null", "--list")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	parts := bytes.Split(out, []byte{0})
	keys := make([]string, 0, len(parts))
	for _, part := range parts {
		if key := strings.TrimSpace(string(part)); key != "" {
			keys = append(keys, key)
		}
	}
	return keys, nil
}

func trustedGitNoResidualChanges(ctx context.Context, dir, hooksDir string) error {
	for _, args := range [][]string{
		{"diff", "--quiet", "--exit-code", "--ignore-submodules=none", "--no-ext-diff", "--no-textconv", "--", ":/"},
		{"ls-files", "--others", "--exclude-standard", "-z"},
		{"ls-files", "--others", "--ignored", "--exclude-standard", "-z"},
	} {
		cmd := trustedGitCommand(ctx, dir, hooksDir, args...)
		out, err := cmd.Output()
		if err != nil {
			return errors.New("isolated worktree has residual unstaged or submodule changes")
		}
		if len(out) > 0 {
			return errors.New("isolated worktree has untracked or ignored files that cannot be snapshotted")
		}
	}
	return nil
}

func runTrustedGit(ctx context.Context, dir, hooksDir string, args ...string) (string, error) {
	cmd := trustedGitCommand(ctx, dir, hooksDir, args...)
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if len(text) > worktreeCommandOutputLimit {
		text = text[:worktreeCommandOutputLimit] + "…"
	}
	if err != nil {
		if text == "" {
			return "", err
		}
		return "", fmt.Errorf("%w: %s", err, text)
	}
	return text, nil
}

func trustedGitCommand(ctx context.Context, dir, hooksDir string, args ...string) *exec.Cmd {
	trusted := []string{
		"-c", "core.hooksPath=" + hooksDir,
		"-c", "commit.gpgSign=false",
		"-c", "tag.gpgSign=false",
		"-c", "core.fsmonitor=false",
		"-c", "core.untrackedCache=false",
		"-c", "credential.helper=",
		"-c", "gc.auto=0",
		"-c", "maintenance.auto=0",
		"-c", "user.name=MCPlexer Delegated Worker",
		"-c", "user.email=opensource@mcplexer.dev",
	}
	gitBinary := "git"
	if resolved, err := exec.LookPath("git"); err == nil {
		gitBinary = resolved
	}
	cmd := exec.CommandContext(ctx, gitBinary, append(trusted, args...)...)
	cmd.Dir = dir
	// Resolve git once from the trusted daemon launch environment, then expose
	// only its directory to child lookup. Git locates built-in helpers via its
	// compiled exec-path; hooks and executable filters are disabled/rejected.
	pathEnv := filepath.Dir(gitBinary)
	if gitBinary == "git" {
		pathEnv = ""
	}
	cmd.Env = []string{
		"PATH=" + pathEnv,
		"HOME=" + os.TempDir(),
		"LANG=C",
		"LC_ALL=C",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=" + os.DevNull,
		"GIT_TERMINAL_PROMPT=0",
		"GIT_NO_LAZY_FETCH=1",
		"GIT_AUTHOR_NAME=MCPlexer Delegated Worker",
		"GIT_AUTHOR_EMAIL=opensource@mcplexer.dev",
		"GIT_COMMITTER_NAME=MCPlexer Delegated Worker",
		"GIT_COMMITTER_EMAIL=opensource@mcplexer.dev",
		"GIT_AUTHOR_DATE=" + time.Now().UTC().Format(time.RFC3339),
		"GIT_COMMITTER_DATE=" + time.Now().UTC().Format(time.RFC3339),
	}
	for _, key := range []string{"SYSTEMROOT", "WINDIR", "COMSPEC", "PATHEXT", "TEMP", "TMP", "TMPDIR"} {
		if value := os.Getenv(key); value != "" {
			cmd.Env = append(cmd.Env, key+"="+value)
		}
	}
	return cmd
}

func cleanupFailedPreparation(
	ctx context.Context, repoRoot, worktreePath, branch, expectedCommit, hooksDir string, cause error,
) error {
	// Preparation has not yet transferred a positively verified lease. A
	// concurrent process may have reused or swapped the reserved path/ref, so
	// rollback must not remove either merely because it exists. Retain both as
	// recovery evidence; an operator can inspect them without risking an
	// unrelated checkout.
	_ = ctx
	_ = hooksDir
	return fmt.Errorf(
		"prepare isolated worktree failed; retained recovery path %q and branch %q at expected commit %s in repository %q: %w",
		worktreePath, branch, expectedCommit, repoRoot, cause,
	)
}

func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if len(text) > worktreeCommandOutputLimit {
		text = text[:worktreeCommandOutputLimit] + "…"
	}
	if err != nil {
		if text == "" {
			return "", err
		}
		return "", fmt.Errorf("%w: %s", err, text)
	}
	return text, nil
}

func existingDirectory(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("directory is empty")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%q is not a directory", path)
	}
	return filepath.Clean(resolved), nil
}

func safeWorktreeComponent(runID string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(runID)) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "run"
	}
	return b.String()
}

func pathWithin(root, candidate string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(candidate))
	return err == nil && !pathEscapes(rel)
}

func pathEscapes(rel string) bool {
	return rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel)
}
