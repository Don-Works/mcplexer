package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/brain"
)

// requireGitBin skips when the git binary is absent so CI without git still
// passes (the daemon degrades to a 503 no-op the same way).
func requireGitBin(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not on PATH")
	}
}

func newBrowserSyncMux(h *brainBrowserHandler) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/brain/browser-sync", h.browserSync)
	return mux
}

// git runs a git subcommand in dir, failing the test on error.
func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t",
		"GIT_TERMINAL_PROMPT=0",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
}

func writeRepoFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// newConflictRepo builds a local clone whose branch has diverged from origin
// on the SAME line, so a subsequent `git pull --rebase --autostash` is
// guaranteed to hit a rebase conflict. Returns the clone dir.
func newConflictRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	origin := filepath.Join(root, "origin")
	clone := filepath.Join(root, "clone")
	if err := os.MkdirAll(origin, 0o755); err != nil {
		t.Fatal(err)
	}

	// Seed origin with one commit on a known branch.
	git(t, origin, "init", "-q", "-b", "main")
	writeRepoFile(t, origin, "f.txt", "base\n")
	git(t, origin, "add", "f.txt")
	git(t, origin, "commit", "-qm", "base")

	// Clone it (clone now tracks origin/main at the base commit).
	git(t, root, "clone", "-q", origin, "clone")

	// Advance origin with a conflicting change to the same line.
	writeRepoFile(t, origin, "f.txt", "origin change\n")
	git(t, origin, "commit", "-aqm", "origin")

	// Advance the clone with a DIFFERENT change to the same line, so a
	// rebase of clone onto the new origin/main conflicts.
	writeRepoFile(t, clone, "f.txt", "clone change\n")
	git(t, clone, "commit", "-aqm", "clone")
	return clone
}

// TestBrowserSync covers every branch of browserSync: the disabled-503 and
// nil-git-503 guards, the rebase-conflict 409 ConflictError mapping, and the
// reindex-after-pull happy path.
func TestBrowserSync(t *testing.T) {
	tests := []struct {
		name       string
		handler    func(t *testing.T) *brainBrowserHandler
		wantStatus int
		// assertBody, when set, runs against the decoded JSON body.
		assertBody func(t *testing.T, body map[string]any)
	}{
		{
			name: "disabled returns 503",
			handler: func(t *testing.T) *brainBrowserHandler {
				return &brainBrowserHandler{editor: &brain.Editor{}, enabled: false}
			},
			wantStatus: http.StatusServiceUnavailable,
		},
		{
			name: "nil git returns 503",
			handler: func(t *testing.T) *brainBrowserHandler {
				// enabled + editor wired, but git nil — mirrors brain disabled
				// git backplane.
				return &brainBrowserHandler{editor: &brain.Editor{}, enabled: true, git: nil}
			},
			wantStatus: http.StatusServiceUnavailable,
		},
		{
			name: "rebase conflict maps to 409 with conflict:true synced:false",
			handler: func(t *testing.T) *brainBrowserHandler {
				requireGitBin(t)
				clone := newConflictRepo(t)
				g := brain.NewGit(clone, nil)
				if !g.Available() {
					t.Skip("git unavailable")
				}
				return &brainBrowserHandler{editor: &brain.Editor{}, enabled: true, git: g}
			},
			wantStatus: http.StatusConflict,
			assertBody: func(t *testing.T, body map[string]any) {
				if conflict, _ := body["conflict"].(bool); !conflict {
					t.Errorf("conflict = %v, want true; body = %+v", body["conflict"], body)
				}
				if synced, _ := body["synced"].(bool); synced {
					t.Errorf("synced = %v, want false", body["synced"])
				}
			},
		},
		{
			name: "clean pull + reindex happy path returns 200 synced:true",
			handler: func(t *testing.T) *brainBrowserHandler {
				requireGitBin(t)
				// Origin + clone with NO divergence — pull --rebase is a clean
				// no-op (already up to date), so browserSync reaches the reindex
				// step. ReindexAll on a dir with no workspaces/ returns nil.
				root := t.TempDir()
				origin := filepath.Join(root, "origin")
				clone := filepath.Join(root, "clone")
				if err := os.MkdirAll(origin, 0o755); err != nil {
					t.Fatal(err)
				}
				git(t, origin, "init", "-q", "-b", "main")
				writeRepoFile(t, origin, "f.txt", "base\n")
				git(t, origin, "add", "f.txt")
				git(t, origin, "commit", "-qm", "base")
				git(t, root, "clone", "-q", origin, "clone")

				g := brain.NewGit(clone, nil)
				if !g.Available() {
					t.Skip("git unavailable")
				}
				ix := brain.NewIndexer(brain.Config{Dir: clone}, newBrainTestStore(t), nil)
				return &brainBrowserHandler{editor: &brain.Editor{}, enabled: true, git: g, indexer: ix}
			},
			wantStatus: http.StatusOK,
			assertBody: func(t *testing.T, body map[string]any) {
				if synced, _ := body["synced"].(bool); !synced {
					t.Errorf("synced = %v, want true; body = %+v", body["synced"], body)
				}
				if conflict, _ := body["conflict"].(bool); conflict {
					t.Errorf("conflict = %v, want false", body["conflict"])
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := tt.handler(t)
			mux := newBrowserSyncMux(h)

			req := httptest.NewRequestWithContext(context.Background(),
				http.MethodPost, "/api/v1/brain/browser-sync", nil)
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)

			if w.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", w.Code, tt.wantStatus, w.Body.String())
			}
			if tt.assertBody != nil {
				var body map[string]any
				if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
					t.Fatalf("decode body: %v; raw = %s", err, w.Body.String())
				}
				tt.assertBody(t, body)
			}
		})
	}
}
