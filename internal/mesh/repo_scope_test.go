package mesh_test

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/mesh"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

func newDB(t *testing.T) *sqlite.DB {
	t.Helper()
	db, err := sqlite.New(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// TestSendWithExplicitRepoBranch verifies that explicit repo/branch on the
// SendRequest land on the persisted row.
func TestSendWithExplicitRepoBranch(t *testing.T) {
	t.Parallel()
	db := newDB(t)
	mgr := mesh.NewManager(db)
	ctx := context.Background()
	meta := mesh.SessionMeta{
		SessionID:    "test-explicit",
		WorkspaceIDs: []string{"global"},
		ClientType:   "test",
	}
	msg, err := mgr.Send(ctx, meta, mesh.SendRequest{
		Kind:          "finding",
		Content:       "explicit repo wins",
		Repo:          "github.com/don-works/mcplexer",
		Branch:        "feat/m73-mesh-repo-scope",
		WorkspacePath: "/tmp/whatever",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	got, err := db.GetMeshMessage(ctx, msg.ID)
	if err != nil {
		t.Fatalf("GetMeshMessage: %v", err)
	}
	if got.Repo != "github.com/don-works/mcplexer" {
		t.Errorf("Repo = %q", got.Repo)
	}
	if got.Branch != "feat/m73-mesh-repo-scope" {
		t.Errorf("Branch = %q", got.Branch)
	}
	if got.WorkspacePath != "/tmp/whatever" {
		t.Errorf("WorkspacePath = %q", got.WorkspacePath)
	}
}

// TestSendAutoFillFromGit creates a real git repo, sends without explicit
// repo/branch but with workspace_path set, and verifies the fields auto-fill.
func TestSendAutoFillFromGit(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	runGitOrFatal(t, dir, "init", "-b", "feat/auto")
	runGitOrFatal(t, dir, "remote", "add", "origin",
		"https://github.com/don-works/mcplexer.git")

	// Pre-probe + bounded retry: ensures the repoMetaCache (see repo_meta.go)
	// is populated with a successful detection for this workspacePath before
	// the Send under test. The probe uses a tight per-call timeout even under
	// parent ctx; under heavy -parallel load the git execs can be scheduled
	// late. A few short retries here make TestSendAutoFillFromGit deterministic
	// without altering production mesh__send latency (subsequent Send hits
	// cache) or relaxing the bounded prod timeout. If pre-probe cannot
	// succeed we fail explicitly rather than flake on the later assert.
	ctx := context.Background()
	probed := mesh.FillRepoMetadata(ctx, dir)
	for i := 0; i < 5 && probed.Repo == ""; i++ {
		time.Sleep(20 * time.Millisecond)
		probed = mesh.FillRepoMetadata(ctx, dir)
	}
	if probed.Repo == "" {
		t.Fatalf("pre-probe could not auto-detect repo from temp git dir %s (got %+v); test env too slow or timeout still insufficient", dir, probed)
	}

	db := newDB(t)
	mgr := mesh.NewManager(db)
	meta := mesh.SessionMeta{
		SessionID:    "test-auto",
		WorkspaceIDs: []string{"global"},
		ClientType:   "test",
	}
	msg, err := mgr.Send(ctx, meta, mesh.SendRequest{
		Kind:          "finding",
		Content:       "auto-fill ping",
		WorkspacePath: dir,
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	got, err := db.GetMeshMessage(ctx, msg.ID)
	if err != nil {
		t.Fatalf("GetMeshMessage: %v", err)
	}
	if got.Repo != "github.com/don-works/mcplexer" {
		t.Errorf("auto-detected Repo = %q", got.Repo)
	}
	if got.Branch != "feat/auto" {
		t.Errorf("auto-detected Branch = %q", got.Branch)
	}
}

// TestReceiveFilteredByRepo seeds two messages from different repos and
// verifies receive with Repo filter excludes the non-matching one.
func TestReceiveFilteredByRepo(t *testing.T) {
	t.Parallel()
	db := newDB(t)
	mgr := mesh.NewManager(db)
	ctx := context.Background()

	send := func(session, content, repo string) {
		meta := mesh.SessionMeta{
			SessionID:    session,
			WorkspaceIDs: []string{"global"},
			ClientType:   "test",
		}
		if _, err := mgr.Send(ctx, meta, mesh.SendRequest{
			Kind:    "finding",
			Content: content,
			Repo:    repo,
			Branch:  "main",
		}); err != nil {
			t.Fatalf("Send: %v", err)
		}
	}
	send("alice", "frontend signal", "github.com/don-works/mcplexer-web")
	send("bob", "backend signal", "github.com/don-works/mcplexer")

	rxMeta := mesh.SessionMeta{
		SessionID:    "carol",
		WorkspaceIDs: []string{"global"},
		ClientType:   "test",
	}
	res, err := mgr.Receive(ctx, rxMeta, mesh.ReceiveRequest{
		Filter: "all",
		Repo:   "github.com/don-works/mcplexer",
	})
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}
	if len(res.Messages) != 1 {
		t.Fatalf("got %d messages, want 1; %+v", len(res.Messages), res.Messages)
	}
	if res.Messages[0].Content != "backend signal" {
		t.Errorf("wrong message: %q", res.Messages[0].Content)
	}
}

// TestSendNoWorkspaceLeavesEmpty verifies that without workspace_path, no
// git probe runs and the message persists with empty repo fields. This is
// the backward-compat path for callers that don't opt into M7.3.
func TestSendNoWorkspaceLeavesEmpty(t *testing.T) {
	t.Parallel()
	db := newDB(t)
	mgr := mesh.NewManager(db)
	ctx := context.Background()
	msg, err := mgr.Send(ctx, mesh.SessionMeta{
		SessionID:    "test-empty",
		WorkspaceIDs: []string{"global"},
		ClientType:   "test",
	}, mesh.SendRequest{Kind: "event", Content: "no scope"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	got, err := db.GetMeshMessage(ctx, msg.ID)
	if err != nil {
		t.Fatalf("GetMeshMessage: %v", err)
	}
	if got.Repo != "" || got.Branch != "" || got.WorkspacePath != "" {
		t.Errorf("expected empty repo scope; got %+v", got)
	}
}

// TestQueryFilterReposOR verifies the multi-repo filter (used by the UI to
// show "messages from any repo I have open") returns the union.
func TestQueryFilterReposOR(t *testing.T) {
	t.Parallel()
	db := newDB(t)
	mgr := mesh.NewManager(db)
	ctx := context.Background()

	for _, r := range []string{"a", "b", "c"} {
		_, err := mgr.Send(ctx, mesh.SessionMeta{
			SessionID:    "s-" + r,
			WorkspaceIDs: []string{"global"},
			ClientType:   "test",
		}, mesh.SendRequest{Kind: "event", Content: "from-" + r, Repo: r})
		if err != nil {
			t.Fatalf("Send: %v", err)
		}
	}

	msgs, err := db.QueryMeshMessages(ctx, store.MeshMessageFilter{
		Repos:      []string{"a", "c"},
		StatusLive: true,
		Limit:      10,
	})
	if err != nil {
		t.Fatalf("QueryMeshMessages: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
}

func runGitOrFatal(t *testing.T, dir string, args ...string) {
	t.Helper()
	all := append([]string{"-C", dir}, args...)
	if out, err := exec.Command("git", all...).CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}
