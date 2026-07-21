package mesh

import (
	"context"
	"testing"
)

// TestReceiveThreadEnforcesAudience is the regression for the filter=thread
// audience-bypass: a reply addressed to a specific session must NOT be
// returned to another agent in the same workspace that reads the thread.
// Broadcast (audience "*") messages in the thread stay visible.
func TestReceiveThreadEnforcesAudience(t *testing.T) {
	ctx := context.Background()
	db := newTestDB(t)
	mgr := NewManager(db)

	sender := SessionMeta{SessionID: "sender", WorkspaceIDs: []string{"ws"}, ClientType: "test"}
	root, err := mgr.Send(ctx, sender, SendRequest{
		Kind: "event", Content: "thread root", Audience: "*",
	})
	if err != nil {
		t.Fatalf("send root: %v", err)
	}
	// A broadcast reply (audience "*") — must stay visible to any reader.
	pub, err := mgr.Send(ctx, sender, SendRequest{
		Kind: "event", Content: "public reply", Audience: "*", ReplyTo: root.ID,
	})
	if err != nil {
		t.Fatalf("send public reply: %v", err)
	}
	// A reply addressed to session-b — must NOT leak to other readers.
	if _, err := mgr.Send(ctx, sender, SendRequest{
		Kind: "event", Content: "secret reply for session-b", Audience: "session-b", ReplyTo: root.ID,
	}); err != nil {
		t.Fatalf("send secret reply: %v", err)
	}

	// Agent C in the same workspace, holding neither session-b nor a matching
	// role, reads the thread.
	other := SessionMeta{SessionID: "agent-c", WorkspaceIDs: []string{"ws"}, ClientType: "test"}
	res, err := mgr.Receive(ctx, other, ReceiveRequest{Filter: "thread", ThreadID: pub.ThreadRoot, MaxResults: 50})
	if err != nil {
		t.Fatalf("receive thread: %v", err)
	}
	sawPublic := false
	for _, m := range res.Messages {
		if m.Content == "secret reply for session-b" {
			t.Fatalf("audience-restricted reply leaked to non-addressee via filter=thread")
		}
		if m.Content == "public reply" {
			sawPublic = true
		}
	}
	if !sawPublic {
		t.Fatalf("broadcast reply should remain visible in filter=thread")
	}
}
