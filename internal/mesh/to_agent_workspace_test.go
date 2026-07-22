// to_agent_workspace_test.go pins that a targeted to_agent send is filed in
// the RESOLVED TARGET's workspace, not the sender's — closing a silent
// cross-workspace drop where a child-workspace session addressed an agent
// registered under an ancestor (parent) workspace and the message reached no
// one while the sender got a success receipt.
package mesh_test

import (
	"context"
	"testing"

	"github.com/don-works/mcplexer/internal/mesh"
)

// TestToAgentFilesInTargetWorkspaceNotSender: resolution succeeds because the
// sender's readable set spans its whole ancestor chain, but filing under the
// sender's most-specific (descendant) workspace put the row where the parent
// target could not read it. The row must land in the target's workspace, be
// delivered to the target, and — via the audience gate — to the target alone.
func TestToAgentFilesInTargetWorkspaceNotSender(t *testing.T) {
	t.Parallel()
	db := newDB(t)
	mgr := mesh.NewManager(db)
	ctx := context.Background()

	// Target T registers under the PARENT workspace only.
	target := mesh.SessionMeta{
		SessionID:    "target-session",
		WorkspaceIDs: []string{"ws-parent"},
		ClientType:   "test",
	}
	if _, err := mgr.Receive(ctx, target, mesh.ReceiveRequest{Name: "worker-T", Filter: "new", MaxResults: 50}); err != nil {
		t.Fatalf("register target: %v", err)
	}

	// Sender carries the ancestor chain, child most-specific first — the shape
	// a nested-workspace worker session has.
	sender := mesh.SessionMeta{
		SessionID:    "sender-session",
		WorkspaceIDs: []string{"ws-child", "ws-parent"},
		ClientType:   "test",
	}
	msg, err := mgr.Send(ctx, sender, mesh.SendRequest{
		Kind:    "event",
		Content: "targeted coordination for the parent-workspace agent",
		ToAgent: "worker-T",
	})
	if err != nil {
		t.Fatalf("send to_agent: %v", err)
	}
	if msg.WorkspaceID != "ws-parent" {
		t.Fatalf("to_agent send must file in the target's workspace ws-parent, got %q (would be a silent drop)", msg.WorkspaceID)
	}

	// The target must actually receive it.
	got, err := mgr.Receive(ctx, target, mesh.ReceiveRequest{Filter: "new", MaxResults: 50})
	if err != nil {
		t.Fatalf("target receive: %v", err)
	}
	if !containsMessageID(got.Messages, msg.ID) {
		t.Fatalf("target did not receive the message addressed to it — silent drop persists")
	}

	// A different agent, also in ws-parent, must NOT see it: the audience gate
	// keeps a to_agent send targeted even though it now lands in a workspace
	// shared with other agents. (No over-broadcast.)
	bystander := mesh.SessionMeta{
		SessionID:    "bystander-session",
		WorkspaceIDs: []string{"ws-parent"},
		ClientType:   "test",
	}
	if _, err := mgr.Receive(ctx, bystander, mesh.ReceiveRequest{Name: "bystander", Filter: "new", MaxResults: 50}); err != nil {
		t.Fatalf("register bystander: %v", err)
	}
	seen, err := mgr.Receive(ctx, bystander, mesh.ReceiveRequest{Filter: "new", MaxResults: 50})
	if err != nil {
		t.Fatalf("bystander receive: %v", err)
	}
	if containsMessageID(seen.Messages, msg.ID) {
		t.Fatalf("to_agent send leaked to a bystander in the same workspace — audience gate not enforced")
	}
}

// TestToAgentSameWorkspaceUnchanged guards that the fix is a no-op for the
// common case: sender and target share a workspace, so filing in the target's
// workspace is the same workspace the sender would have used anyway.
func TestToAgentSameWorkspaceUnchanged(t *testing.T) {
	t.Parallel()
	db := newDB(t)
	mgr := mesh.NewManager(db)
	ctx := context.Background()

	target := mesh.SessionMeta{
		SessionID:    "peer-session",
		WorkspaceIDs: []string{"ws-alpha"},
		ClientType:   "test",
	}
	if _, err := mgr.Receive(ctx, target, mesh.ReceiveRequest{Name: "peer-a", Filter: "new", MaxResults: 50}); err != nil {
		t.Fatalf("register target: %v", err)
	}
	sender := mesh.SessionMeta{
		SessionID:    "sender-alpha",
		WorkspaceIDs: []string{"ws-alpha"},
		ClientType:   "test",
	}
	msg, err := mgr.Send(ctx, sender, mesh.SendRequest{
		Kind:    "event",
		Content: "same-workspace targeted send",
		ToAgent: "peer-a",
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if msg.WorkspaceID != "ws-alpha" {
		t.Fatalf("same-workspace to_agent send should stay in ws-alpha, got %q", msg.WorkspaceID)
	}
	got, err := mgr.Receive(ctx, target, mesh.ReceiveRequest{Filter: "new", MaxResults: 50})
	if err != nil {
		t.Fatalf("target receive: %v", err)
	}
	if !containsMessageID(got.Messages, msg.ID) {
		t.Fatalf("target did not receive the same-workspace targeted message")
	}
}
