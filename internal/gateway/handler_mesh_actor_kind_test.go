package gateway

// mesh__send must stamp actor_kind. It never did: the handler built a
// SendRequest with 13 named fields and ActorKind was not one of them, so
// mesh.Send defaulted every send to "agent" — including sends from delegated
// CLI workers, for which mesh__send is in the default allowlist.
//
// Three mechanisms silently no-opped as a result: exclude_actor_kinds:
// "worker" matched nothing, the 24h ArchiveOldWorkerFindings reaper (which
// filters actor_kind='worker') never swept worker chatter, and the UI could
// not tell worker traffic from agent traffic.
//
// The pre-existing coverage injected ActorKind straight into mgr.Send,
// bypassing the handler — which is exactly why this was invisible. These
// tests drive it THROUGH the handler.

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/mesh"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

// newMeshSendHandler wires a handler over a real sqlite store with a mesh
// manager, returning both plus the session id it sends as.
func newMeshSendHandler(t *testing.T, sessionID string) (*handler, *sqlite.DB, *mesh.Manager) {
	t.Helper()
	ctx := context.Background()
	db, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "mesh.db"))
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	mgr := mesh.NewManager(db)
	h, _ := newTestHandler(&mockToolLister{tools: map[string]json.RawMessage{}}, nil)
	h.mesh = mgr
	h.sessions.session = &store.Session{ID: sessionID, ClientType: "test"}
	return h, db, mgr
}

// sendThroughHandler drives handleMeshSend and returns the stored message.
func sendThroughHandler(t *testing.T, h *handler, db *sqlite.DB, ctx context.Context, content string) store.MeshMessage {
	t.Helper()
	_, rpcErr := h.handleMeshSend(ctx, mustJSON(t, map[string]any{
		"kind":    "finding",
		"content": content,
	}))
	if rpcErr != nil {
		t.Fatalf("handleMeshSend: %v", rpcErr)
	}
	msgs, err := db.QueryMeshMessages(ctx, store.MeshMessageFilter{Limit: 100})
	if err != nil {
		t.Fatalf("QueryMeshMessages: %v", err)
	}
	for _, m := range msgs {
		if m.Content == content {
			return m
		}
	}
	t.Fatalf("message %q was not stored", content)
	return store.MeshMessage{}
}

// workerCtx builds a context that looks like an in-process delegated worker
// call, granted write on workspaceID.
func workerCtx(workspaceID string) context.Context {
	return WithWorkerWorkspaceAccess(context.Background(), workspaceID,
		[]WorkerWorkspaceGrant{{WorkspaceID: workspaceID, Access: "write"}})
}

func TestMeshSendStampsActorKind(t *testing.T) {
	t.Run("worker context is stamped worker", func(t *testing.T) {
		h, db, _ := newMeshSendHandler(t, "worker-session")
		got := sendThroughHandler(t, h, db, workerCtx("ws-global"), "worker finding")
		if got.ActorKind != "worker" {
			t.Fatalf("actor_kind = %q, want worker", got.ActorKind)
		}
	})

	t.Run("normal agent session is stamped agent", func(t *testing.T) {
		h, db, _ := newMeshSendHandler(t, "agent-session")
		got := sendThroughHandler(t, h, db, context.Background(), "agent finding")
		if got.ActorKind != "agent" {
			t.Fatalf("actor_kind = %q, want agent", got.ActorKind)
		}
	})
}

// TestExcludeActorKindsFiltersWorkerTraffic proves the advertised filter
// (builtin_tools.go: "exclude_actor_kinds:'worker' hides worker chatter")
// actually does something now.
func TestExcludeActorKindsFiltersWorkerTraffic(t *testing.T) {
	ctx := context.Background()
	h, db, mgr := newMeshSendHandler(t, "worker-session")
	if _, err := mgr.Receive(ctx, mesh.SessionMeta{
		SessionID: "reader", WorkspaceIDs: []string{"ws-global"}, ClientType: "test",
	}, mesh.ReceiveRequest{Name: "reader"}); err != nil {
		t.Fatalf("register reader: %v", err)
	}

	sendThroughHandler(t, h, db, workerCtx("ws-global"), "worker chatter")

	h2, _, _ := newMeshSendHandler(t, "agent-session")
	h2.mesh = mgr
	sendThroughHandler(t, h2, db, context.Background(), "agent signal")

	res, err := mgr.Receive(ctx, mesh.SessionMeta{
		SessionID: "reader", WorkspaceIDs: []string{"ws-global"}, ClientType: "test",
	}, mesh.ReceiveRequest{Filter: "new", ExcludeActorKinds: "worker"})
	if err != nil {
		t.Fatalf("Receive: %v", err)
	}
	for _, m := range res.Messages {
		if m.Content == "worker chatter" {
			t.Fatal("exclude_actor_kinds:'worker' did not filter worker traffic")
		}
	}
}

// TestArchiveReaperSweepsHandlerWorkerFindings closes the loop on the
// backlog symptom: the 24h reaper filters actor_kind='worker', so worker
// sends made through the tool were never swept and accumulated unread.
func TestArchiveReaperSweepsHandlerWorkerFindings(t *testing.T) {
	ctx := context.Background()
	h, db, _ := newMeshSendHandler(t, "worker-session")
	sendThroughHandler(t, h, db, workerCtx("ws-global"), "stale worker finding")

	// Cutoff in the future so the just-written row counts as older than it.
	archived, err := db.ArchiveOldWorkerFindings(ctx, time.Now().UTC().Add(time.Hour))
	if err != nil {
		t.Fatalf("ArchiveOldWorkerFindings: %v", err)
	}
	if archived != 1 {
		t.Fatalf("reaper archived %d rows, want 1 — worker findings sent via mesh__send are not being swept", archived)
	}
}

// TestArchiveReaperLeavesAgentFindings is the counterweight: stamping must
// not make the reaper eat ordinary agent traffic.
func TestArchiveReaperLeavesAgentFindings(t *testing.T) {
	ctx := context.Background()
	h, db, _ := newMeshSendHandler(t, "agent-session")
	sendThroughHandler(t, h, db, context.Background(), "agent finding worth keeping")

	archived, err := db.ArchiveOldWorkerFindings(ctx, time.Now().UTC().Add(time.Hour))
	if err != nil {
		t.Fatalf("ArchiveOldWorkerFindings: %v", err)
	}
	if archived != 0 {
		t.Fatalf("reaper archived %d agent rows, want 0", archived)
	}
}
