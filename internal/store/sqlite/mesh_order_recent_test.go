package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// TestQueryMeshMessagesOrderRecent verifies that MeshMessageFilter.OrderRecent
// switches QueryMeshMessages from priority-first ordering to strict recency
// ordering (id DESC, where id is a ULID so lexicographic == time order).
//
// This is the telegram mesh_history fix: a RECENT normal-priority
// agent-outbound row must appear in a small recency-bounded window even though
// several OLDER high-priority inbound rows exist. Under the default
// priority-first ordering the high-priority rows fill the window and the
// recent normal row is lost — exactly the bug the responder hit (it could not
// see agent-outbound messages).
func TestQueryMeshMessagesOrderRecent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := newTestDB(t, "mesh-order-recent.db")
	now := time.Now().UTC()

	// IDs are ULIDs: lexicographic order == time order. The "_RECENT" row has
	// the largest id, so it is newest under id DESC. The three high-priority
	// rows are older (smaller ids) but higher priority.
	rows := []*store.MeshMessage{
		{
			ID: "01HIGH_A", WorkspaceID: "ws", SessionID: "user",
			AgentName: "user", Kind: "event", Priority: "high",
			Content: "old high A", Audience: "*", Status: "live",
			ExpiresAt: now.Add(8 * time.Hour), CreatedAt: now.Add(-30 * time.Minute), ActorKind: "user",
		},
		{
			ID: "01HIGH_B", WorkspaceID: "ws", SessionID: "user",
			AgentName: "user", Kind: "event", Priority: "high",
			Content: "old high B", Audience: "*", Status: "live",
			ExpiresAt: now.Add(8 * time.Hour), CreatedAt: now.Add(-20 * time.Minute), ActorKind: "user",
		},
		{
			ID: "01HIGH_C", WorkspaceID: "ws", SessionID: "user",
			AgentName: "user", Kind: "event", Priority: "high",
			Content: "old high C", Audience: "*", Status: "live",
			ExpiresAt: now.Add(8 * time.Hour), CreatedAt: now.Add(-10 * time.Minute), ActorKind: "user",
		},
		{
			ID: "01RECENT_NORMAL", WorkspaceID: "ws", SessionID: "agent-a",
			AgentName: "alice", Kind: "event", Priority: "normal",
			Content: "recent agent-outbound", Audience: "*", Status: "live",
			ExpiresAt: now.Add(8 * time.Hour), CreatedAt: now, ActorKind: "agent",
		},
	}
	for _, r := range rows {
		if err := db.InsertMeshMessage(ctx, r); err != nil {
			t.Fatalf("insert %s: %v", r.ID, err)
		}
	}

	// PART A: OrderRecent:true with a small limit must surface the recent
	// normal-priority row — recency beats priority.
	recent, err := db.QueryMeshMessages(ctx, store.MeshMessageFilter{
		WorkspaceIDs: []string{"ws"},
		OrderRecent:  true,
		Limit:        2,
	})
	if err != nil {
		t.Fatalf("QueryMeshMessages OrderRecent: %v", err)
	}
	if len(recent) != 2 {
		t.Fatalf("OrderRecent returned %d rows, want 2", len(recent))
	}
	if recent[0].ID != "01RECENT_NORMAL" {
		t.Errorf("OrderRecent[0].ID = %q, want 01RECENT_NORMAL (newest first)", recent[0].ID)
	}
	var foundRecent bool
	for _, m := range recent {
		if m.ID == "01RECENT_NORMAL" {
			foundRecent = true
		}
	}
	if !foundRecent {
		t.Errorf("OrderRecent window did not include the recent normal-priority row; got %v", idsOf(recent))
	}

	// Default (OrderRecent:false) keeps priority-first ordering: with the same
	// small limit the high-priority rows fill the window and the recent normal
	// row is excluded.
	byPriority, err := db.QueryMeshMessages(ctx, store.MeshMessageFilter{
		WorkspaceIDs: []string{"ws"},
		Limit:        2,
	})
	if err != nil {
		t.Fatalf("QueryMeshMessages default: %v", err)
	}
	if len(byPriority) != 2 {
		t.Fatalf("default returned %d rows, want 2", len(byPriority))
	}
	for _, m := range byPriority {
		if m.Priority != "high" {
			t.Errorf("default ordering window row %q has priority %q, want all high (priority-first)", m.ID, m.Priority)
		}
		if m.ID == "01RECENT_NORMAL" {
			t.Errorf("default ordering window unexpectedly included the recent normal row %q", m.ID)
		}
	}
}

func idsOf(msgs []store.MeshMessage) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = m.ID
	}
	return out
}
