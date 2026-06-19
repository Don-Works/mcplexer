// reply_workspace_test.go — pins the M2 cross-workspace reply gate.
// A worker in workspace A must NOT be able to reply to a message in
// workspace B; doing so would let it (a) extend B's TTL toward its 2x
// ceiling and (b) inflate B's reply_count without any peer-scope grant.
// The reply content itself lives in the sender's workspace so this is
// not direct content injection — but cross-workspace metadata mutation
// is enough of a side channel to deserve a hard reject.
package mesh_test

import (
	"context"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/mesh"
)

func TestReplyToOtherWorkspaceIsRejected(t *testing.T) {
	t.Parallel()
	db := newDB(t)
	mgr := mesh.NewManager(db)
	ctx := context.Background()

	// Workspace B sends the parent message.
	parentMeta := mesh.SessionMeta{
		SessionID:    "session-bravo",
		WorkspaceIDs: []string{"ws-bravo"},
		ClientType:   "test",
	}
	parent, err := mgr.Send(ctx, parentMeta, mesh.SendRequest{
		Kind:    "finding",
		Content: "parent in workspace B",
	})
	if err != nil {
		t.Fatalf("seed parent: %v", err)
	}
	// Re-read so the baseline timestamps come from the store (SQLite
	// truncates to second precision); compare against this snapshot,
	// not the in-memory parent struct, to avoid a precision-mismatch
	// false positive.
	baseline, err := db.GetMeshMessage(ctx, parent.ID)
	if err != nil {
		t.Fatalf("read baseline parent: %v", err)
	}

	// Workspace A attempts to reply.
	attackerMeta := mesh.SessionMeta{
		SessionID:    "session-alpha",
		WorkspaceIDs: []string{"ws-alpha"},
		ClientType:   "test",
	}
	_, err = mgr.Send(ctx, attackerMeta, mesh.SendRequest{
		Kind:    "reply",
		Content: "hostile cross-workspace reply",
		ReplyTo: parent.ID,
	})
	if err == nil {
		t.Fatal("expected cross-workspace reply to be rejected, got nil error")
	}
	if !strings.Contains(err.Error(), "different workspace") {
		t.Fatalf("error message should mention cross-workspace rejection: %v", err)
	}

	// Parent metadata must be untouched by the rejected attempt.
	got, err := db.GetMeshMessage(ctx, parent.ID)
	if err != nil {
		t.Fatalf("re-read parent: %v", err)
	}
	if got.ReplyCount != baseline.ReplyCount {
		t.Errorf("parent reply_count mutated: baseline=%d after=%d (rejected reply must not mutate parent)", baseline.ReplyCount, got.ReplyCount)
	}
	if !got.ExpiresAt.Equal(baseline.ExpiresAt) {
		t.Errorf("parent expires_at mutated by rejected reply: baseline %v, now %v", baseline.ExpiresAt, got.ExpiresAt)
	}
}

// TestReplyToSameWorkspaceStillWorks confirms the gate doesn't
// accidentally break the normal in-workspace reply flow.
func TestReplyToSameWorkspaceStillWorks(t *testing.T) {
	t.Parallel()
	db := newDB(t)
	mgr := mesh.NewManager(db)
	ctx := context.Background()

	meta := mesh.SessionMeta{
		SessionID:    "session-alpha-1",
		WorkspaceIDs: []string{"ws-alpha"},
		ClientType:   "test",
	}
	parent, err := mgr.Send(ctx, meta, mesh.SendRequest{
		Kind:    "finding",
		Content: "parent in workspace A",
	})
	if err != nil {
		t.Fatalf("seed parent: %v", err)
	}

	replyMeta := mesh.SessionMeta{
		SessionID:    "session-alpha-2",
		WorkspaceIDs: []string{"ws-alpha"},
		ClientType:   "test",
	}
	reply, err := mgr.Send(ctx, replyMeta, mesh.SendRequest{
		Kind:    "reply",
		Content: "legitimate same-workspace reply",
		ReplyTo: parent.ID,
	})
	if err != nil {
		t.Fatalf("same-workspace reply should succeed: %v", err)
	}
	if reply.ReplyTo != parent.ID {
		t.Errorf("reply_to = %q, want %q", reply.ReplyTo, parent.ID)
	}
	if reply.ThreadRoot != parent.ID {
		t.Errorf("thread_root = %q, want %q (first-level reply roots to parent)", reply.ThreadRoot, parent.ID)
	}
}
