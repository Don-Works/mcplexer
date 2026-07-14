package index

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"time"
)

// errGitUnavailable is returned when the system git binary is not on PATH or
// the directory is not a repo. Callers degrade to empty results, never fail.
var errGitUnavailable = errors.New("index: git binary unavailable")

// gitRunner is a read-only os/exec wrapper around the system git binary scoped
// to one directory. It mirrors internal/brain/git.go's run() pattern
// (GIT_TERMINAL_PROMPT=0, arg-slice exec never a shell string) but adds a 5s
// per-command timeout when the caller's ctx has no sooner deadline (P4).
type gitRunner struct {
	dir string
	bin string
	log *slog.Logger
}

// newGitRunner resolves the git binary eagerly; when absent, available()
// reports false and every call returns errGitUnavailable.
func newGitRunner(dir string, log *slog.Logger) *gitRunner {
	if log == nil {
		log = slog.Default()
	}
	bin, err := exec.LookPath("git")
	if err != nil {
		bin = ""
	}
	return &gitRunner{dir: dir, bin: bin, log: log}
}

func (g *gitRunner) available() bool { return g != nil && g.bin != "" }

// isRepo reports whether dir is inside a git work tree.
func (g *gitRunner) isRepo(ctx context.Context) bool {
	if !g.available() {
		return false
	}
	out, err := g.run(ctx, "rev-parse", "--is-inside-work-tree")
	return err == nil && strings.TrimSpace(out) == "true"
}

// run executes a git subcommand in the repo dir, capturing combined output.
func (g *gitRunner) run(ctx context.Context, args ...string) (string, error) {
	if !g.available() {
		return "", errGitUnavailable
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, g.bin, args...)
	cmd.Dir = g.dir
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=", "SSH_ASKPASS=", "GCM_INTERACTIVE=never")
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	// Preserve byte-exact output. Porcelain -z begins with the XY status
	// columns, whose first byte is often a significant space; trimming here
	// corrupts the record and can turn " M path" into a different path.
	out := buf.String()
	if err != nil {
		return out, fmt.Errorf("index: git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(out))
	}
	return out, nil
}

// lsFiles enumerates tracked + untracked-not-ignored files under the repo dir,
// honoring .gitignore. The pathspec "." keeps a subdirectory-rooted workspace
// from indexing the whole repo (P3). Paths are returned repo-dir-relative with
// forward slashes.
func (g *gitRunner) lsFiles(ctx context.Context) ([]string, error) {
	out, err := g.run(ctx, "ls-files", "-z", "--cached", "--others", "--exclude-standard", ".")
	if err != nil {
		return nil, err
	}
	parts := strings.Split(out, "\x00")
	files := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			files = append(files, p)
		}
	}
	return files, nil
}

// head returns the current HEAD commit hash, or "" (no error) when git is
// unavailable or the repo has no commits yet.
func (g *gitRunner) head(ctx context.Context) (string, error) {
	if !g.available() {
		return "", nil
	}
	out, err := g.run(ctx, "rev-parse", "HEAD")
	if err != nil {
		return "", nil
	}
	return strings.TrimSpace(out), nil
}

// statusPorcelain returns the raw `git status --porcelain` output. Empty (no
// error) when git is unavailable or the tree is clean.
func (g *gitRunner) statusPorcelain(ctx context.Context) (string, error) {
	if !g.available() {
		return "", nil
	}
	out, err := g.run(ctx, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return "", nil
	}
	return out, nil
}

// dirtyCount returns the number of changed (staged/unstaged/untracked-visible)
// entries via `git status --porcelain`. 0 (no error) when git is unavailable.
func (g *gitRunner) dirtyCount(ctx context.Context) (int, error) {
	out, err := g.statusPorcelain(ctx)
	if err != nil || out == "" {
		return 0, err
	}
	if strings.ContainsRune(out, '\x00') {
		n := 0
		parts := strings.Split(out, "\x00")
		for i := 0; i < len(parts); i++ {
			rec := parts[i]
			if len(rec) < 4 {
				continue
			}
			n++
			if strings.ContainsAny(rec[:2], "RC") {
				i++ // the second NUL field is the other rename/copy path
			}
		}
		return n, nil
	}
	n := 0
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) != "" {
			n++
		}
	}
	return n, nil
}

const gitLogSep = "\x1f"

// recentChanges returns up to limit commits within the day window (optionally
// scoped to path), newest first, each with its changed files.
func (g *gitRunner) recentChanges(ctx context.Context, path string, days, limit int) ([]CommitRef, error) {
	if !g.available() {
		return nil, errGitUnavailable
	}
	args := []string{
		"log", "--no-color", "--date=short", "--name-only",
		"--pretty=format:" + strings.Join([]string{"%H", "%an", "%ad", "%s"}, gitLogSep),
		fmt.Sprintf("-n%d", limit),
		fmt.Sprintf("--since=%d days ago", days),
	}
	if path != "" {
		args = append(args, "--", path)
	}
	out, err := g.run(ctx, args...)
	if err != nil {
		return nil, err
	}
	return parseGitLog(out), nil
}

// parseGitLog parses the --name-only --pretty output into commits. A line with
// the field separator starts a commit header; subsequent non-empty lines are
// its files until the next header or blank line.
func parseGitLog(out string) []CommitRef {
	var commits []CommitRef
	var cur *CommitRef
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, gitLogSep) {
			if cur != nil {
				commits = append(commits, *cur)
			}
			f := strings.SplitN(line, gitLogSep, 4)
			cur = &CommitRef{Hash: field(f, 0), Author: field(f, 1), Date: field(f, 2), Subject: field(f, 3)}
			continue
		}
		if cur != nil && strings.TrimSpace(line) != "" {
			cur.Files = append(cur.Files, strings.TrimSpace(line))
		}
	}
	if cur != nil {
		commits = append(commits, *cur)
	}
	return commits
}

func field(f []string, i int) string {
	if i < len(f) {
		return f[i]
	}
	return ""
}

// churnCounts returns a path -> commit-touch-count map over the day window
// (one git log call). Empty (no error) when git is unavailable.
func (g *gitRunner) churnCounts(ctx context.Context, days int) (map[string]int, error) {
	if !g.available() {
		return map[string]int{}, nil
	}
	out, err := g.run(ctx, "log", "--no-color", "--name-only", "--format=",
		fmt.Sprintf("--since=%d days ago", days))
	if err != nil {
		return map[string]int{}, nil
	}
	counts := make(map[string]int)
	for _, line := range strings.Split(out, "\n") {
		p := strings.TrimSpace(line)
		if p != "" {
			counts[p]++
		}
	}
	return counts, nil
}
