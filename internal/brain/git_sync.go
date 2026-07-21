package brain

import (
	"context"
	"strconv"
	"strings"
)

// PullRebase fetches and rebases the local branch onto its upstream with
// --autostash, so the daemon's uncommitted index files don't block the
// pull. A rebase conflict is returned as a *ConflictError (surfaced, not
// auto-resolved — SPEC §7). The mutation is serialized through g.mu so it
// never interleaves with an autocommit or push.
func (g *Git) PullRebase(ctx context.Context) error {
	if !g.Available() {
		return ErrGitUnavailable
	}
	g.mu.Lock()
	defer g.mu.Unlock()

	out, err := g.run(ctx, nil, "pull", "--rebase", "--autostash")
	if err == nil {
		return nil
	}
	if isRebaseConflict(out) {
		// Abort the in-progress rebase so the tree is left clean for a human
		// to re-attempt after resolving; best-effort.
		_, _ = g.run(ctx, nil, "rebase", "--abort")
		return &ConflictError{Output: out}
	}
	return err
}

// Push pushes the current branch to origin. Manual only — invoked by the
// admin tool / dashboard, never on a timer (Appendix B decision #6: AUTO
// local commit, MANUAL push). Serialized through g.mu.
func (g *Git) Push(ctx context.Context) error {
	if !g.Available() {
		return ErrGitUnavailable
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	_, err := g.run(ctx, nil, "push")
	return err
}

// Status reports ahead/behind/dirty for the dashboard. Read-only — it does
// not take the mutation mutex (it never writes), so the dashboard can poll
// it while an autocommit is in flight.
func (g *Git) Status(ctx context.Context) (GitStatus, error) {
	var st GitStatus
	if !g.Available() {
		return st, ErrGitUnavailable
	}
	if !isGitRepo(g.dir) {
		return st, nil // not initialised — zero value
	}

	// Current branch (empty on detached HEAD or before first commit).
	if branch, err := g.run(ctx, nil, "rev-parse", "--abbrev-ref", "HEAD"); err == nil {
		if branch != "HEAD" {
			st.Branch = branch
		}
	}

	// HEAD subject — also our "has at least one commit" probe.
	if subj, err := g.run(ctx, nil, "log", "-1", "--format=%s"); err == nil {
		st.Initialized = true
		st.LastCommit = subj
	}

	// Dirty working tree (porcelain output non-empty ⇒ dirty).
	if porcelain, err := g.run(ctx, nil, "status", "--porcelain"); err == nil {
		st.Dirty = strings.TrimSpace(porcelain) != ""
	}

	// Remote + upstream.
	if remotes, err := g.run(ctx, nil, "remote"); err == nil {
		st.HasRemote = strings.TrimSpace(remotes) != ""
	}
	g.fillAheadBehind(ctx, &st)
	return st, nil
}

// fillAheadBehind populates Ahead/Behind/HasUpstream from the
// rev-list left/right count against @{upstream}. Missing upstream is not an
// error (a fresh repo with no remote tracking branch).
func (g *Git) fillAheadBehind(ctx context.Context, st *GitStatus) {
	out, err := g.run(ctx, nil, "rev-list", "--left-right", "--count", "HEAD...@{upstream}")
	if err != nil {
		return // no upstream configured — leave zero, HasUpstream false
	}
	st.HasUpstream = true
	fields := strings.Fields(out)
	if len(fields) == 2 {
		if a, e := strconv.Atoi(fields[0]); e == nil {
			st.Ahead = a
		}
		if b, e := strconv.Atoi(fields[1]); e == nil {
			st.Behind = b
		}
	}
}

// isRebaseConflict heuristically detects a rebase/merge conflict in git's
// combined output. git does not give a stable exit code for "conflict vs
// other failure", so we scan for the canonical phrases.
func isRebaseConflict(out string) bool {
	low := strings.ToLower(out)
	for _, marker := range []string{
		"conflict",
		"could not apply",
		"needs merge",
		"fix conflicts and then run",
	} {
		if strings.Contains(low, marker) {
			return true
		}
	}
	return false
}
