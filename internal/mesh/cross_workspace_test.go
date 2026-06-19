// cross_workspace_test.go pins the to_workspace + global mesh
// surface. Default sends scope to the sender's workspace; to_workspace
// "*"/"global" lands in the global namespace (WorkspaceID="") and is
// visible to every receiving session regardless of their workspace;
// a specific to_workspace targets that workspace's mesh.
//
// Cross-workspace writes are intentionally OPEN in the MVP (single
// daemon, single user); the sender's session id is recorded on the
// row so writes remain auditable. Lock that property into a test too.
package mesh_test

import (
	"context"
	"testing"

	"github.com/don-works/mcplexer/internal/mesh"
	"github.com/don-works/mcplexer/internal/store"
)

// TestSendGlobalIsVisibleAcrossWorkspaces: a message sent with
// to_workspace:"*" lands with empty WorkspaceID and shows up in a
// receiver bound to a completely different workspace.
func TestSendGlobalIsVisibleAcrossWorkspaces(t *testing.T) {
	t.Parallel()
	db := newDB(t)
	mgr := mesh.NewManager(db)
	ctx := context.Background()

	sender := mesh.SessionMeta{
		SessionID:    "sender-session",
		WorkspaceIDs: []string{"ws-alpha"},
		ClientType:   "test",
	}
	msg, err := mgr.Send(ctx, sender, mesh.SendRequest{
		Kind:        "finding",
		Content:     "global broadcast from alpha",
		ToWorkspace: "*",
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if msg.WorkspaceID != "" {
		t.Errorf("global send should write WorkspaceID=\"\", got %q", msg.WorkspaceID)
	}
	if msg.SessionID != sender.SessionID {
		t.Errorf("sender session id not recorded: got %q want %q", msg.SessionID, sender.SessionID)
	}

	// A receiver in a different workspace should still see it.
	receiver := mesh.SessionMeta{
		SessionID:    "receiver-session",
		WorkspaceIDs: []string{"ws-bravo"},
		ClientType:   "test",
	}
	result, err := mgr.Receive(ctx, receiver, mesh.ReceiveRequest{Filter: "new", MaxResults: 50})
	if err != nil {
		t.Fatalf("receive: %v", err)
	}
	if !containsMessageID(result.Messages, msg.ID) {
		t.Fatalf("global message %s not visible to receiver bound to %q (got %d msgs)", msg.ID, receiver.WorkspaceIDs[0], len(result.Messages))
	}
}

// TestSendGlobalAcceptsScopeAlias: scope:"global" is the ergonomic
// alias for to_workspace:"*"; both must land at WorkspaceID="".
func TestSendGlobalAcceptsScopeAlias(t *testing.T) {
	t.Parallel()
	db := newDB(t)
	mgr := mesh.NewManager(db)
	ctx := context.Background()

	sender := mesh.SessionMeta{
		SessionID:    "sender",
		WorkspaceIDs: []string{"ws-alpha"},
		ClientType:   "test",
	}
	// `scope:"global"` is mapped to ToWorkspace="*" in the handler;
	// at the manager layer we test the canonical ToWorkspace="global"
	// spelling, which also routes to the empty namespace.
	msg, err := mgr.Send(ctx, sender, mesh.SendRequest{
		Kind:        "event",
		Content:     "global via 'global' literal",
		ToWorkspace: "global",
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if msg.WorkspaceID != "" {
		t.Errorf("ToWorkspace=\"global\" should land in empty namespace; got %q", msg.WorkspaceID)
	}
}

// TestSendToSpecificWorkspaceTargetsThatWorkspace: when ToWorkspace
// is a concrete ID, the message is filed under that workspace — a
// session bound to that workspace sees it; a session bound to a
// third unrelated workspace does NOT.
func TestSendToSpecificWorkspaceTargetsThatWorkspace(t *testing.T) {
	t.Parallel()
	db := newDB(t)
	mgr := mesh.NewManager(db)
	ctx := context.Background()

	sender := mesh.SessionMeta{
		SessionID:    "alpha-session",
		WorkspaceIDs: []string{"ws-alpha"},
		ClientType:   "test",
	}
	msg, err := mgr.Send(ctx, sender, mesh.SendRequest{
		Kind:        "finding",
		Content:     "alpha → bravo direct",
		ToWorkspace: "ws-bravo",
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if msg.WorkspaceID != "ws-bravo" {
		t.Errorf("targeted message should land at ws-bravo, got %q", msg.WorkspaceID)
	}

	bravoReceiver := mesh.SessionMeta{
		SessionID:    "bravo-session",
		WorkspaceIDs: []string{"ws-bravo"},
		ClientType:   "test",
	}
	bravoSees, err := mgr.Receive(ctx, bravoReceiver, mesh.ReceiveRequest{Filter: "new", MaxResults: 50})
	if err != nil {
		t.Fatalf("bravo receive: %v", err)
	}
	if !containsMessageID(bravoSees.Messages, msg.ID) {
		t.Errorf("bravo should see message targeted at its workspace")
	}

	charlieReceiver := mesh.SessionMeta{
		SessionID:    "charlie-session",
		WorkspaceIDs: []string{"ws-charlie"},
		ClientType:   "test",
	}
	charlieSees, err := mgr.Receive(ctx, charlieReceiver, mesh.ReceiveRequest{Filter: "new", MaxResults: 50})
	if err != nil {
		t.Fatalf("charlie receive: %v", err)
	}
	if containsMessageID(charlieSees.Messages, msg.ID) {
		t.Errorf("charlie (unrelated workspace) must not see ws-bravo–targeted message")
	}
}

// TestDefaultSendStaysWorkspaceScoped: omitting ToWorkspace keeps
// the historical behaviour — message lands in the sender's first
// workspace, NOT global. Guard against accidental scope expansion.
func TestDefaultSendStaysWorkspaceScoped(t *testing.T) {
	t.Parallel()
	db := newDB(t)
	mgr := mesh.NewManager(db)
	ctx := context.Background()

	sender := mesh.SessionMeta{
		SessionID:    "sender-default",
		WorkspaceIDs: []string{"ws-alpha"},
		ClientType:   "test",
	}
	msg, err := mgr.Send(ctx, sender, mesh.SendRequest{
		Kind:    "event",
		Content: "default scope",
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if msg.WorkspaceID != "ws-alpha" {
		t.Errorf("default send should remain workspace-scoped; got WorkspaceID=%q want %q", msg.WorkspaceID, "ws-alpha")
	}

	// A receiver in an unrelated workspace must NOT see it (it is not
	// global). This locks the contract that you have to OPT IN to
	// cross-workspace by passing to_workspace.
	other := mesh.SessionMeta{
		SessionID:    "other",
		WorkspaceIDs: []string{"ws-bravo"},
		ClientType:   "test",
	}
	result, err := mgr.Receive(ctx, other, mesh.ReceiveRequest{Filter: "new", MaxResults: 50})
	if err != nil {
		t.Fatalf("receive: %v", err)
	}
	if containsMessageID(result.Messages, msg.ID) {
		t.Errorf("workspace-scoped message must not leak to an unrelated workspace receiver")
	}
}

// TestSendUnboundSenderDoesNotLeakToGlobal pins the fix for the
// cross-workspace chatter footgun: a sender whose SessionMeta carries NO
// workspace (a hand-built meta — e.g. a daemon-internal alerter), sending
// with the DEFAULT scope (no to_workspace/scope:"global"), must NOT land
// in the global namespace (""). Before the fix it fell through to "" and
// was delivered to every session on the daemon. It must now be isolated
// to a per-session sentinel and invisible to an unrelated-workspace
// receiver.
func TestSendUnboundSenderDoesNotLeakToGlobal(t *testing.T) {
	t.Parallel()
	db := newDB(t)
	mgr := mesh.NewManager(db)
	ctx := context.Background()

	// nil WorkspaceIDs and a blank [""] must behave identically — both
	// represent "no workspace bound" and must not reach "".
	for _, tc := range []struct {
		name         string
		workspaceIDs []string
	}{
		{"nil_workspace_ids", nil},
		{"blank_workspace_id", []string{""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sender := mesh.SessionMeta{
				SessionID:    "internal-alerter-" + tc.name,
				WorkspaceIDs: tc.workspaceIDs,
				ClientType:   "system",
			}
			msg, err := mgr.Send(ctx, sender, mesh.SendRequest{
				Kind:    "alert",
				Content: "daemon-internal noise that used to flood every session",
			})
			if err != nil {
				t.Fatalf("send: %v", err)
			}
			if msg.WorkspaceID == "" {
				t.Fatalf("unbound default-scope send leaked to the global namespace (WorkspaceID=\"\")")
			}
			if want := "session:" + sender.SessionID; msg.WorkspaceID != want {
				t.Errorf("unbound sender should isolate to %q, got %q", want, msg.WorkspaceID)
			}

			// A receiver in an unrelated workspace must not see it.
			receiver := mesh.SessionMeta{
				SessionID:    "dev-session-" + tc.name,
				WorkspaceIDs: []string{"ws-unrelated"},
				ClientType:   "test",
			}
			result, err := mgr.Receive(ctx, receiver, mesh.ReceiveRequest{Filter: "new", MaxResults: 50})
			if err != nil {
				t.Fatalf("receive: %v", err)
			}
			if containsMessageID(result.Messages, msg.ID) {
				t.Fatalf("unbound-sender message %s leaked to an unrelated-workspace receiver", msg.ID)
			}
		})
	}
}

// TestSendUnboundSenderStillReachesGlobalWhenExplicit confirms the escape
// hatch survives: an unbound sender that EXPLICITLY asks for global
// ("*"/"global") still reaches "" — only the implicit fall-through was
// removed.
func TestSendUnboundSenderStillReachesGlobalWhenExplicit(t *testing.T) {
	t.Parallel()
	db := newDB(t)
	mgr := mesh.NewManager(db)
	ctx := context.Background()

	sender := mesh.SessionMeta{SessionID: "explicit-broadcaster", ClientType: "system"}
	msg, err := mgr.Send(ctx, sender, mesh.SendRequest{
		Kind:        "alert",
		Content:     "deliberate global broadcast",
		ToWorkspace: "*",
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if msg.WorkspaceID != "" {
		t.Errorf("explicit global send should write WorkspaceID=\"\", got %q", msg.WorkspaceID)
	}
}

// containsMessageID reports whether the receive result includes a
// message with the given id.
func containsMessageID(haystack []store.MeshMessage, id string) bool {
	for _, m := range haystack {
		if m.ID == id {
			return true
		}
	}
	return false
}
