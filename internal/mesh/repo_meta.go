package mesh

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// RepoMetadata captures the workspace's VCS context for a mesh send.
// All fields are optional — empty values mean "unknown" (e.g. cwd is not
// inside a git repo, or the git probe timed out).
type RepoMetadata struct {
	WorkspacePath string // absolute path to the workspace root
	Repo          string // canonical repo identifier, e.g. "github.com/don-works/mcplexer"
	Branch        string // current branch (HEAD)
	RepoRemote    string // raw `remote.origin.url`
}

// gitProbeTimeout caps how long FillRepoMetadata waits for git to respond.
// On a slow disk we'd rather ship an envelope without repo scope than
// stall mesh__send for seconds. 250ms is still tightly bounded (<<1s worst
// case across the 3 calls) while greatly reducing flakes under parallel
// test load / contended macOS tmp.
const gitProbeTimeout = 250 * time.Millisecond

// repoMetaCache is a tiny per-workspace cache so back-to-back mesh__send
// calls don't re-shell out. We keep the cache TTL short — branch can
// change at any time when the user runs `git checkout`.
type repoMetaCache struct {
	mu      sync.Mutex
	entries map[string]repoMetaCacheEntry
}

type repoMetaCacheEntry struct {
	meta     RepoMetadata
	cachedAt time.Time
}

// repoMetaCacheTTL is the cache freshness window. Branch flips on
// `git checkout` so we keep this short — empirical mesh__send rates are
// well under 1 Hz so the cache mostly elides duplicate shells from a
// single tool call's chatter.
const repoMetaCacheTTL = 2 * time.Second

var defaultRepoMetaCache = &repoMetaCache{
	entries: make(map[string]repoMetaCacheEntry),
}

func (c *repoMetaCache) get(path string) (RepoMetadata, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[path]
	if !ok || time.Since(e.cachedAt) > repoMetaCacheTTL {
		return RepoMetadata{}, false
	}
	return e.meta, true
}

func (c *repoMetaCache) set(path string, meta RepoMetadata) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[path] = repoMetaCacheEntry{meta: meta, cachedAt: time.Now()}
}

// FillRepoMetadata probes the workspace at `path` for git metadata and
// returns a RepoMetadata. Results are cached briefly so back-to-back
// mesh__send calls don't pay the fork-exec cost. Empty path returns a
// zero-value struct.
func FillRepoMetadata(ctx context.Context, workspacePath string) RepoMetadata {
	if workspacePath == "" {
		return RepoMetadata{}
	}
	abs, err := filepath.Abs(workspacePath)
	if err != nil {
		abs = workspacePath
	}
	if cached, ok := defaultRepoMetaCache.get(abs); ok {
		return cached
	}
	meta := probeRepoMetadata(ctx, abs)
	defaultRepoMetaCache.set(abs, meta)
	return meta
}

// probeRepoMetadata runs `git -C <path>` up to three times (origin URL,
// branch, unborn-branch fallback). Each subprocess gets its own timeout
// so one slow git call doesn't starve the others — they all get the full
// gitProbeTimeout budget independently.
func probeRepoMetadata(parent context.Context, path string) RepoMetadata {
	meta := RepoMetadata{WorkspacePath: path}

	gitCall := func(args ...string) string {
		runCtx, cancel := context.WithTimeout(parent, gitProbeTimeout)
		defer cancel()
		return runGit(runCtx, path, args...)
	}

	if remote := gitCall("config", "--get", "remote.origin.url"); remote != "" {
		meta.RepoRemote = remote
		meta.Repo = canonicalRepoFromRemote(remote)
	}
	if branch := gitCall("rev-parse", "--abbrev-ref", "HEAD"); branch != "" && branch != "HEAD" {
		meta.Branch = branch
	}
	// Fallback for unborn branches (fresh repo, no commits yet).
	if meta.Branch == "" {
		if ref := gitCall("symbolic-ref", "--short", "HEAD"); ref != "" {
			meta.Branch = ref
		}
	}
	return meta
}

// runGit executes `git -C <path> <args...>` and returns trimmed stdout.
// Returns empty string on any error (including timeout).
func runGit(ctx context.Context, path string, args ...string) string {
	all := append([]string{"-C", path}, args...)
	out, err := exec.CommandContext(ctx, "git", all...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// canonicalRepoFromRemote turns a git remote URL into a canonical
// `host/org/repo` identifier. Strips ".git" suffix and protocol
// scheme. Falls back to the raw remote when parsing fails.
//
// Examples:
//
//	git@github.com:don-works/mcplexer.git → github.com/don-works/mcplexer
//	https://github.com/foo/bar           → github.com/foo/bar
//	https://github.com/foo/bar.git       → github.com/foo/bar
func canonicalRepoFromRemote(remote string) string {
	r := strings.TrimSpace(remote)
	if r == "" {
		return ""
	}
	r = strings.TrimSuffix(r, ".git")

	// SSH form: git@host:org/repo
	if strings.HasPrefix(r, "git@") {
		rest := strings.TrimPrefix(r, "git@")
		if i := strings.Index(rest, ":"); i > 0 {
			host := rest[:i]
			path := strings.TrimPrefix(rest[i+1:], "/")
			return host + "/" + path
		}
		return rest
	}

	// scp-like (no scheme but contains :)
	if !strings.Contains(r, "://") && strings.Contains(r, ":") {
		i := strings.Index(r, ":")
		return r[:i] + "/" + strings.TrimPrefix(r[i+1:], "/")
	}

	// URL form (https://, ssh://, git://) — drop scheme + optional userinfo.
	if i := strings.Index(r, "://"); i > 0 {
		rest := strings.TrimPrefix(r[i+3:], "/")
		if at := strings.LastIndex(rest, "@"); at >= 0 {
			// Strip userinfo only when it precedes the host (i.e. there's
			// still a "/" after the "@").
			if strings.Contains(rest[at:], "/") {
				rest = rest[at+1:]
			}
		}
		return rest
	}
	return r
}

// repoMetaCtxKey scopes mesh-related metadata onto a context so
// gateway handlers can attach the active workspace path without
// changing every Send signature.
type repoMetaCtxKey struct{}

// WithRepoMetadata stamps explicit repo metadata onto a context.
// Useful when the caller already knows the repo (e.g. a test, or a
// caller that wants to override auto-detection).
func WithRepoMetadata(ctx context.Context, meta RepoMetadata) context.Context {
	return context.WithValue(ctx, repoMetaCtxKey{}, meta)
}

// repoMetaFromContext returns metadata previously stamped via
// WithRepoMetadata, or the zero value when none is present.
func repoMetaFromContext(ctx context.Context) (RepoMetadata, bool) {
	v, ok := ctx.Value(repoMetaCtxKey{}).(RepoMetadata)
	return v, ok
}

// resolveRepoMeta merges explicit overrides from a SendRequest with
// auto-detected metadata. Precedence: explicit field on the request →
// metadata stamped on the context via WithRepoMetadata → live git probe
// rooted at the request's WorkspacePath. Each field is resolved
// independently so a caller can set Branch="release" while letting Repo
// auto-detect.
func resolveRepoMeta(ctx context.Context, req SendRequest) RepoMetadata {
	out := RepoMetadata{
		Repo:          req.Repo,
		Branch:        req.Branch,
		WorkspacePath: req.WorkspacePath,
		RepoRemote:    req.RepoRemote,
	}
	if ctxMeta, ok := repoMetaFromContext(ctx); ok {
		fillEmpty(&out.Repo, ctxMeta.Repo)
		fillEmpty(&out.Branch, ctxMeta.Branch)
		fillEmpty(&out.WorkspacePath, ctxMeta.WorkspacePath)
		fillEmpty(&out.RepoRemote, ctxMeta.RepoRemote)
	}
	if out.WorkspacePath != "" && (out.Repo == "" || out.Branch == "") {
		probed := FillRepoMetadata(ctx, out.WorkspacePath)
		fillEmpty(&out.Repo, probed.Repo)
		fillEmpty(&out.Branch, probed.Branch)
		fillEmpty(&out.RepoRemote, probed.RepoRemote)
	}
	return out
}

func fillEmpty(dst *string, src string) {
	if dst != nil && *dst == "" {
		*dst = src
	}
}
