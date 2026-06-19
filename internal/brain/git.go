package brain

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// Machine commit identity. Per-commit GIT_AUTHOR_*/GIT_COMMITTER_* env on
// the exec call keeps the daemon's commits distinct from the user's
// without ever touching ~/.gitconfig (SPEC §7).
const (
	machineAuthorName  = "MCPlexer Daemon"
	machineAuthorEmail = "daemon@mcplexer.local"
)

// ErrGitUnavailable is returned when the system git binary cannot be
// found on PATH. Callers degrade to a no-op rather than failing the brain.
var ErrGitUnavailable = errors.New("brain: git binary not found on PATH")

// ConflictError is returned by PullRebase when a rebase hits a conflict
// that the daemon must NOT auto-resolve. The conflict is surfaced (e.g. to
// the dashboard) for a human to resolve, never silently clobbered.
type ConflictError struct {
	// Output is the captured combined stdout+stderr of the failing git
	// invocation, for surfacing the conflicting paths to a human.
	Output string
}

func (e *ConflictError) Error() string {
	return "brain: git rebase conflict (surfaced, not auto-resolved): " + e.Output
}

// GitStatus is the dashboard-facing view of the repo's sync state.
type GitStatus struct {
	// Initialized reports whether dir is a git repo with at least one commit.
	Initialized bool `json:"initialized"`
	// Dirty reports whether the working tree has uncommitted changes.
	Dirty bool `json:"dirty"`
	// Ahead is the number of local commits not on the upstream.
	Ahead int `json:"ahead"`
	// Behind is the number of upstream commits not local.
	Behind int `json:"behind"`
	// HasRemote reports whether an "origin" remote is configured.
	HasRemote bool `json:"has_remote"`
	// HasUpstream reports whether the current branch tracks an upstream.
	HasUpstream bool `json:"has_upstream"`
	// Branch is the current branch name (empty on a detached HEAD).
	Branch string `json:"branch"`
	// LastCommit is the subject line of HEAD (empty before the first commit).
	LastCommit string `json:"last_commit"`
}

// Git is an os/exec wrapper around the system git binary scoped to a single
// repo. All mutations are serialized through mu so two autocommits (or an
// autocommit racing a manual push) never interleave (SPEC §7
// "per-repo serialized command queue").
//
// go-git is deliberately NOT used: it cannot do non-fast-forward
// merge/rebase/gc/hooks (SPEC §7). The shell-guard metacharacter block is
// irrelevant — every invocation passes an arg slice to exec.CommandContext,
// never a shell string.
type Git struct {
	dir string
	mu  sync.Mutex
	log *slog.Logger
	bin string // resolved git binary path; "" when unavailable
}

// NewGit constructs a Git wrapper for dir. The git binary is resolved
// eagerly; when absent, Available() reports false and every mutation
// returns ErrGitUnavailable (the daemon degrades to no-op, never panics).
func NewGit(dir string, log *slog.Logger) *Git {
	if log == nil {
		log = slog.Default()
	}
	bin, err := exec.LookPath("git")
	if err != nil {
		log.Warn("brain: git binary not found — git backplane disabled", "error", err)
		bin = ""
	}
	return &Git{dir: dir, log: log, bin: bin}
}

// Available reports whether the git binary was resolved.
func (g *Git) Available() bool { return g != nil && g.bin != "" }

// run executes a git subcommand in the repo dir with the given extra env
// appended to the process env. The mutex MUST already be held by the
// caller for mutating commands; read-only status calls may run unlocked.
func (g *Git) run(ctx context.Context, env []string, args ...string) (string, error) {
	if !g.Available() {
		return "", ErrGitUnavailable
	}
	cmd := exec.CommandContext(ctx, g.bin, args...)
	cmd.Dir = g.dir
	// Never block on an interactive credential / passphrase prompt. A daemon
	// git op that needs auth (e.g. a push to a remote) must fail fast and be
	// surfaced to the dashboard — never pop a GitHub-token dialog or hang the
	// serialized queue waiting on a human. Applied to EVERY invocation.
	noPrompt := []string{
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=",
		"SSH_ASKPASS=",
		"GCM_INTERACTIVE=never",
	}
	cmd.Env = append(append(os.Environ(), noPrompt...), env...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	out := strings.TrimSpace(buf.String())
	if err != nil {
		return out, fmt.Errorf("brain: git %s: %w: %s", strings.Join(args, " "), err, out)
	}
	return out, nil
}

// Init makes dir a git repo if it is not already one, scaffolds the repo
// skeleton, and creates an initial commit of the scaffold. Idempotent: a
// repo that already exists is left untouched (no empty re-commit).
func (g *Git) Init(ctx context.Context) error {
	if !g.Available() {
		return ErrGitUnavailable
	}
	g.mu.Lock()
	defer g.mu.Unlock()

	if isGitRepo(g.dir) {
		return nil
	}
	if err := ScaffoldRepo(g.dir); err != nil {
		return err
	}
	if _, err := g.run(ctx, nil, "init"); err != nil {
		return err
	}
	// Commit the scaffold so the repo has a base commit. Scope the add to
	// the scaffold files only (never -A) — consistent with Commit.
	scaffold := []string{".gitignore", ".gitattributes", "brain.json", "README.md"}
	if err := g.add(ctx, scaffold); err != nil {
		return err
	}
	return g.commitStaged(ctx, "chore(brain): initialise brain repo  [machine]")
}

// isGitRepo reports whether dir contains a .git directory (or file, for
// worktrees/submodules).
func isGitRepo(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}

// Commit stages exactly the given paths (relative to the repo root or
// absolute under it) and commits them with the machine identity. It NEVER
// runs `git add -A`, so a human's half-finished edit outside paths is never
// snapshotted (SPEC §7). A no-op (nothing staged) returns nil without
// creating an empty commit.
func (g *Git) Commit(ctx context.Context, paths []string, msg string) error {
	if !g.Available() {
		return ErrGitUnavailable
	}
	g.mu.Lock()
	defer g.mu.Unlock()

	rel := g.relPaths(paths)
	rel = g.stageable(ctx, rel)
	if len(rel) == 0 {
		return nil
	}
	if err := g.add(ctx, rel); err != nil {
		return err
	}
	if !g.hasStaged(ctx) {
		return nil // nothing actually changed — no empty commit
	}
	return g.commitStaged(ctx, msg)
}

// stageable filters repo-relative pathspecs to those git can actually
// stage: a path that exists on disk (add/modify) OR is already tracked in
// the index (so a deletion is staged). A path that neither exists nor is
// tracked is dropped, so `git add` never aborts with "pathspec did not
// match any files" on a notify for a transiently-named temp file.
func (g *Git) stageable(ctx context.Context, rel []string) []string {
	out := make([]string, 0, len(rel))
	for _, r := range rel {
		if _, err := os.Stat(filepath.Join(g.dir, r)); err == nil {
			out = append(out, r)
			continue
		}
		// Not on disk — keep only if git tracks it (a deletion to stage).
		if _, err := g.run(ctx, nil, "ls-files", "--error-unmatch", "--", r); err == nil {
			out = append(out, r)
		}
	}
	return out
}

// add stages the given (repo-relative) pathspecs. `--all` scoped to the
// pathspecs records additions, modifications AND deletions for exactly
// those paths (so a serializer-deleted task file is staged), while the `--`
// separator guards against a path that looks like a flag. It is NOT a bare
// `git add -A`: nothing outside the listed paths is ever staged, so a
// human's half-finished edit elsewhere is never snapshotted (SPEC §7).
func (g *Git) add(ctx context.Context, rel []string) error {
	args := append([]string{"add", "--all", "--"}, rel...)
	_, err := g.run(ctx, nil, args...)
	return err
}

// hasStaged reports whether the index has staged changes to commit.
func (g *Git) hasStaged(ctx context.Context) bool {
	// `git diff --cached --quiet` exits non-zero when there ARE staged
	// changes; we want a boolean, so inspect the error.
	_, err := g.run(ctx, nil, "diff", "--cached", "--quiet")
	return err != nil
}

// commitStaged commits whatever is staged with the machine identity env.
// The caller MUST hold g.mu.
func (g *Git) commitStaged(ctx context.Context, msg string) error {
	env := []string{
		"GIT_AUTHOR_NAME=" + machineAuthorName,
		"GIT_AUTHOR_EMAIL=" + machineAuthorEmail,
		"GIT_COMMITTER_NAME=" + machineAuthorName,
		"GIT_COMMITTER_EMAIL=" + machineAuthorEmail,
	}
	_, err := g.run(ctx, env, "commit", "-m", msg)
	return err
}

// relPaths normalises a mix of absolute/relative paths to repo-relative
// pathspecs, dropping any that escape the repo dir. Order is preserved and
// duplicates removed.
func (g *Git) relPaths(paths []string) []string {
	seen := make(map[string]struct{}, len(paths))
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		r := p
		if filepath.IsAbs(p) {
			rp, err := filepath.Rel(g.dir, p)
			if err != nil || strings.HasPrefix(rp, "..") {
				continue // outside the repo — never stage it
			}
			r = rp
		}
		r = filepath.ToSlash(r)
		if r == "" || r == "." {
			continue
		}
		if _, dup := seen[r]; dup {
			continue
		}
		seen[r] = struct{}{}
		out = append(out, r)
	}
	return out
}
