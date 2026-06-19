package skillregistry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// GitSource clones git repos to a managed directory under the data dir
// so the registry can index SKILL.md files (and reference any bundled
// assets) without copying. The same (url, ref) pair always resolves to
// the same on-disk path — re-imports become a fast `git fetch + checkout`.
//
// Auth: relies on whatever credentials the local git already has (SSH
// agent for git@... URLs; system keychain for HTTPS). We deliberately
// do not handle credentials ourselves — the daemon should never see
// the user's git password.
type GitSource struct {
	dataDir string

	mu     sync.Mutex
	keyMus map[string]*sync.Mutex
}

// NewGitSource returns a source rooted at dataDir/git. dataDir must be
// an absolute path under the daemon's data directory (e.g. ~/.mcplexer).
func NewGitSource(dataDir string) *GitSource {
	return &GitSource{dataDir: dataDir, keyMus: map[string]*sync.Mutex{}}
}

// CloneResult records what Clone produced for downstream metadata.
type CloneResult struct {
	LocalPath string // absolute path to the working tree
	URL       string // canonicalised URL (input echoed)
	Ref       string // resolved ref name (empty = default branch)
	Commit    string // 40-char sha at HEAD after clone/checkout
}

// Clone clones url at ref into a stable per-(url,ref) directory and
// returns the local path + commit hash. If the directory already exists,
// the clone is fast-forwarded with `git fetch && git checkout`.
//
// ref is optional; an empty ref leaves the default branch in place.
// Concurrent calls for the same (url, ref) serialize on a per-key mutex.
func (g *GitSource) Clone(ctx context.Context, url, ref string) (*CloneResult, error) {
	if g == nil {
		return nil, errors.New("GitSource: nil receiver")
	}
	if g.dataDir == "" {
		return nil, errors.New("GitSource: empty dataDir")
	}
	url = strings.TrimSpace(url)
	if url == "" {
		return nil, errors.New("git url is required")
	}
	if _, err := exec.LookPath("git"); err != nil {
		return nil, fmt.Errorf("git binary not found in PATH (install git to import skills from git URLs)")
	}

	key := cacheKey(url, ref)
	mu := g.lockFor(key)
	mu.Lock()
	defer mu.Unlock()

	root := filepath.Join(g.dataDir, "git", key)
	if err := os.MkdirAll(filepath.Dir(root), 0o755); err != nil {
		return nil, fmt.Errorf("create git cache root: %w", err)
	}

	if _, err := os.Stat(filepath.Join(root, ".git")); errors.Is(err, os.ErrNotExist) {
		if err := runGit(ctx, "", "clone", url, root); err != nil {
			// Tidy up partial clone so the next attempt starts fresh.
			_ = os.RemoveAll(root)
			return nil, fmt.Errorf("git clone: %w", err)
		}
	} else if err != nil {
		return nil, fmt.Errorf("stat repo: %w", err)
	} else {
		if err := runGit(ctx, root, "fetch", "--prune", "origin"); err != nil {
			return nil, fmt.Errorf("git fetch: %w", err)
		}
	}

	if ref != "" {
		// Try the ref directly first; fall back to origin/<ref> for
		// branches that haven't been tracked locally yet.
		if err := runGit(ctx, root, "checkout", ref); err != nil {
			if err2 := runGit(ctx, root, "checkout", "origin/"+ref); err2 != nil {
				return nil, fmt.Errorf("git checkout %q: %w", ref, err)
			}
		}
	}

	commit, err := runGitOut(ctx, root, "rev-parse", "HEAD")
	if err != nil {
		return nil, fmt.Errorf("git rev-parse HEAD: %w", err)
	}

	return &CloneResult{
		LocalPath: root,
		URL:       url,
		Ref:       ref,
		Commit:    strings.TrimSpace(commit),
	}, nil
}

func (g *GitSource) lockFor(key string) *sync.Mutex {
	g.mu.Lock()
	defer g.mu.Unlock()
	if m, ok := g.keyMus[key]; ok {
		return m
	}
	m := &sync.Mutex{}
	g.keyMus[key] = m
	return m
}

// cacheKey is a deterministic short hex of (url, ref) used as the
// directory name. Treats every input as opaque bytes — no quoting, no
// path injection risk.
func cacheKey(url, ref string) string {
	h := sha256.Sum256([]byte(url + "\x00" + ref))
	return hex.EncodeToString(h[:])[:16]
}

// runGit runs `git <args>` in the given working directory (or the
// process cwd when dir is empty), forwarding stderr into the returned
// error so callers can surface the underlying message to the user.
func runGit(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0", // never block on credential prompts
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("%s", msg)
	}
	return nil
}

func runGitOut(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}
