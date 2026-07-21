package mesh

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestCanonicalRepoFromRemote(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, want string
	}{
		{"git@github.com:don-works/mcplexer.git", "github.com/don-works/mcplexer"},
		{"git@github.com:don-works/mcplexer", "github.com/don-works/mcplexer"},
		{"https://github.com/foo/bar.git", "github.com/foo/bar"},
		{"https://github.com/foo/bar", "github.com/foo/bar"},
		{"ssh://git@gitlab.com/grp/sub/proj.git", "gitlab.com/grp/sub/proj"},
		{"", ""},
	}
	for _, tc := range cases {
		got := canonicalRepoFromRemote(tc.in)
		if got != tc.want {
			t.Errorf("canonicalRepoFromRemote(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestFillRepoMetadataNonGitDir(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	meta := FillRepoMetadata(context.Background(), dir)
	if meta.Repo != "" || meta.Branch != "" || meta.RepoRemote != "" {
		t.Errorf("non-git dir should yield empty fields, got %+v", meta)
	}
	if meta.WorkspacePath == "" {
		t.Errorf("WorkspacePath should be set even when probe fails")
	}
}

// TestFillRepoMetadataGitRepo creates a real git repo on disk and verifies
// the auto-fill returns the configured remote + initial branch. Skips when
// `git` isn't on PATH (CI variants without git).
func TestFillRepoMetadataGitRepo(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available on PATH")
	}
	dir := t.TempDir()
	mustGit(t, dir, "init", "-b", "feat/m73-mesh-repo-scope")
	mustGit(t, dir, "remote", "add", "origin",
		"git@github.com:don-works/mcplexer.git")

	// drop the cache so a fresh probe runs
	defaultRepoMetaCache = &repoMetaCache{entries: map[string]repoMetaCacheEntry{}}

	start := time.Now()
	meta := FillRepoMetadata(context.Background(), dir)
	elapsed := time.Since(start)
	t.Logf("FillRepoMetadata latency: %v", elapsed)

	if meta.Repo != "github.com/don-works/mcplexer" {
		t.Errorf("Repo = %q", meta.Repo)
	}
	if meta.Branch != "feat/m73-mesh-repo-scope" {
		t.Errorf("Branch = %q", meta.Branch)
	}
	if meta.RepoRemote == "" {
		t.Errorf("RepoRemote should be populated")
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("auto-fill too slow: %v (cap is 3×100ms timeouts + fork overhead)", elapsed)
	}
}

// TestFillRepoMetadataCache verifies repeated probes hit the cache so
// back-to-back mesh__send calls don't re-shell out.
func TestFillRepoMetadataCache(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	mustGit(t, dir, "init", "-b", "main")
	defaultRepoMetaCache = &repoMetaCache{entries: map[string]repoMetaCacheEntry{}}
	abs, _ := filepath.Abs(dir)

	_ = FillRepoMetadata(context.Background(), abs)
	if _, ok := defaultRepoMetaCache.get(abs); !ok {
		t.Fatal("expected cache hit after first probe")
	}

	start := time.Now()
	for i := 0; i < 50; i++ {
		_ = FillRepoMetadata(context.Background(), abs)
	}
	avg := time.Since(start) / 50
	t.Logf("cached FillRepoMetadata avg latency: %v", avg)
	if avg > 200*time.Microsecond {
		t.Errorf("cached path too slow: %v", avg)
	}
}

func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	all := append([]string{"-C", dir}, args...)
	if out, err := exec.Command("git", all...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}
