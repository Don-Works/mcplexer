package gateway

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// A joined workspace is a local mirror, not necessarily an ancestor of the
// caller's current CWD. The publish handler must therefore defer to the exact
// membership capability check in tasks.Service instead of rejecting it via
// the older session-workspace gate.
func TestTaskPublishHome_AllowsJoinedWorkspaceOutsideSessionScope(t *testing.T) {
	ctx := context.Background()
	h, db, _ := newTasksHandler(t)

	mirror := &store.Workspace{
		Name: "remote-incident-feed", RootPath: "/tmp/remote-incident-feed",
		Tags: json.RawMessage(`[]`),
	}
	if err := db.CreateWorkspace(ctx, mirror); err != nil {
		t.Fatalf("create mirror workspace: %v", err)
	}
	if err := db.UpsertWorkspaceMembership(ctx, &store.WorkspaceMembership{
		ShareID: "share-remote", HomePeerID: "home-peer",
		RemoteWorkspaceID: "home-workspace", LocalWorkspaceID: mirror.ID,
		WorkspaceName: mirror.Name,
		Capabilities:  []string{store.CapabilityTasksPublish},
		AccessEpoch:   1, Status: store.WorkspaceShareStatusActive,
		JoinedAt: time.Now(), UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("create membership: %v", err)
	}
	task := &store.Task{WorkspaceID: mirror.ID, Title: "publish me"}
	if err := db.CreateTask(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	raw, rpcErr := h.handleTaskPublishHome(ctx, json.RawMessage(`{
		"workspace_id":"`+mirror.ID+`",
		"task_id":"`+task.ID+`"
	}`))
	if rpcErr != nil {
		t.Fatalf("joined workspace was rejected by session scope: %v", rpcErr)
	}
	// This unit handler deliberately has no P2P transport. Reaching that
	// service-level error proves the mirror membership passed the gateway.
	if !strings.Contains(string(raw), "task share service not wired") {
		t.Fatalf("expected publish to reach task service, got %s", raw)
	}
}
