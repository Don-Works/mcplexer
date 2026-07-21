package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// TestQueryMeshMessagesOrderOldest verifies MeshMessageFilter.OrderOldest
// switches QueryMeshMessages to strict oldest-first ordering (id ASC).
//
// This backs the filter=new cursor-scan fix: when there are more new-since-
// cursor messages than the scan limit, the LIMIT must drop the NEWEST rows so
// the scanned window stays a contiguous block from the cursor. Under the
// default priority-first ordering a LIMIT keeps high-priority NEW rows and
// drops an older low-priority one; advancing the cursor past it loses that
// message forever.
func TestQueryMeshMessagesOrderOldest(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := newTestDB(t, "mesh-order-oldest.db")
	now := time.Now().UTC()

	rows := []*store.MeshMessage{
		{
			ID: "01AAA_OLD_LOW", WorkspaceID: "ws", SessionID: "agent-a",
			AgentName: "alice", Kind: "event", Priority: "low",
			Content: "oldest low-priority", Audience: "*", Status: "live",
			ExpiresAt: now.Add(8 * time.Hour), CreatedAt: now.Add(-30 * time.Minute), ActorKind: "agent",
		},
		{
			ID: "01BBB_NEW_HIGH", WorkspaceID: "ws", SessionID: "user",
			AgentName: "user", Kind: "event", Priority: "high",
			Content: "newer high-priority B", Audience: "*", Status: "live",
			ExpiresAt: now.Add(8 * time.Hour), CreatedAt: now.Add(-10 * time.Minute), ActorKind: "user",
		},
		{
			ID: "01CCC_NEW_HIGH", WorkspaceID: "ws", SessionID: "user",
			AgentName: "user", Kind: "event", Priority: "high",
			Content: "newest high-priority C", Audience: "*", Status: "live",
			ExpiresAt: now.Add(8 * time.Hour), CreatedAt: now, ActorKind: "user",
		},
	}
	for _, r := range rows {
		if err := db.InsertMeshMessage(ctx, r); err != nil {
			t.Fatalf("insert %s: %v", r.ID, err)
		}
	}

	// OrderOldest:true with a small limit keeps the OLDEST row (even though it
	// is low priority) and drops the newest — the contiguity guarantee.
	oldest, err := db.QueryMeshMessages(ctx, store.MeshMessageFilter{
		WorkspaceIDs: []string{"ws"},
		OrderOldest:  true,
		Limit:        1,
	})
	if err != nil {
		t.Fatalf("QueryMeshMessages OrderOldest: %v", err)
	}
	if len(oldest) != 1 || oldest[0].ID != "01AAA_OLD_LOW" {
		t.Fatalf("OrderOldest Limit=1 = %v, want [01AAA_OLD_LOW] (oldest kept)", idsOf(oldest))
	}

	// Sanity: the DEFAULT priority-first ordering would instead drop the old
	// low-priority row (the bug this fix prevents).
	byPriority, err := db.QueryMeshMessages(ctx, store.MeshMessageFilter{
		WorkspaceIDs: []string{"ws"},
		Limit:        1,
	})
	if err != nil {
		t.Fatalf("QueryMeshMessages default: %v", err)
	}
	if len(byPriority) != 1 || byPriority[0].Priority != "high" {
		t.Fatalf("default Limit=1 = %v, want a single high-priority row", idsOf(byPriority))
	}
}
