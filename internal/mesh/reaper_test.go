package mesh

import (
	"context"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// TestReaperSweepRetentionPolicy locks in the v1.0 retention policy: the
// sweep flips expired live rows to 'archived', keeps archived rows for the
// retention window (so the mesh stays a useful audit trail), and prunes
// archived rows older than that window so the table cannot grow forever.
func TestReaperSweepRetentionPolicy(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := newTestDB(t)

	now := time.Now().UTC()
	rows := []*store.MeshMessage{
		{
			// Live + expired → must flip to archived, NOT be deleted.
			ID: "01ARCHIVE_ME", WorkspaceID: "ws", SessionID: "sess",
			AgentName: "a", Kind: "event", Priority: "low",
			Content: "expiring soon", Audience: "*", Status: "live",
			ExpiresAt: now.Add(-1 * time.Minute), CreatedAt: now.Add(-2 * time.Hour),
		},
		{
			// Archived inside the retention window → must survive.
			ID: "01KEEP_ME", WorkspaceID: "ws", SessionID: "sess",
			AgentName: "a", Kind: "finding", Priority: "normal",
			Content: "recent history", Audience: "*", Status: "archived",
			ExpiresAt: now.Add(-2 * 24 * time.Hour), CreatedAt: now.Add(-3 * 24 * time.Hour),
		},
		{
			// Archived beyond the retention window → must be pruned.
			ID: "01PRUNE_ME", WorkspaceID: "ws", SessionID: "sess",
			AgentName: "a", Kind: "finding", Priority: "normal",
			Content: "from last year", Audience: "*", Status: "archived",
			ExpiresAt: now.Add(-365 * 24 * time.Hour), CreatedAt: now.Add(-366 * 24 * time.Hour),
		},
	}
	for _, r := range rows {
		if err := db.InsertMeshMessage(ctx, r); err != nil {
			t.Fatalf("insert %s: %v", r.ID, err)
		}
	}

	// Drive one sweep directly (the production goroutine ticks at 60s).
	r := &Reaper{store: db, retention: DefaultArchivedRetention}
	r.sweep(ctx)

	got, err := db.GetMeshMessage(ctx, "01ARCHIVE_ME")
	if err != nil {
		t.Fatalf("expired live row vanished — sweep must archive, never delete fresh rows: %v", err)
	}
	if got.Status != "archived" {
		t.Fatalf("expired live row status = %q, want archived", got.Status)
	}

	if got, err = db.GetMeshMessage(ctx, "01KEEP_ME"); err != nil || got.Status != "archived" {
		t.Fatalf("recently-archived row must survive the retention window (err=%v)", err)
	}

	if _, err = db.GetMeshMessage(ctx, "01PRUNE_ME"); err == nil {
		t.Fatal("archived row older than the retention window was not pruned")
	}
}

// TestNewReaperWithRetentionClampsNonPositive guards against a zero window
// deleting rows the same tick they archive.
func TestNewReaperWithRetentionClampsNonPositive(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := NewReaperWithRetention(ctx, newTestDB(t), 0)
	t.Cleanup(r.Stop)
	if r.retention != DefaultArchivedRetention {
		t.Fatalf("retention = %v, want default %v", r.retention, DefaultArchivedRetention)
	}
}

// TestReaperSweepArchivesOldWorkerFindings verifies the 24h safety net:
// worker finding/reply messages older than 24h are archived even when
// their TTL has not expired, so unreviewed delegation worker messages
// don't pile up indefinitely.
func TestReaperSweepArchivesOldWorkerFindings(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := newTestDB(t)

	now := time.Now().UTC()
	rows := []*store.MeshMessage{
		{
			ID: "01OLD_WORKER_FINDING", WorkspaceID: "ws", SessionID: "worker:w1",
			AgentName: "w1", Kind: "finding", Priority: "high",
			Content: "stale result", Audience: "*", Status: "live",
			ExpiresAt: now.Add(48 * time.Hour), CreatedAt: now.Add(-25 * time.Hour),
			ActorKind: "worker",
		},
		{
			ID: "01OLD_WORKER_REPLY", WorkspaceID: "ws", SessionID: "worker:w1",
			AgentName: "w1", Kind: "reply", Priority: "high",
			Content: "stale reply", Audience: "*", Status: "live",
			ExpiresAt: now.Add(48 * time.Hour), CreatedAt: now.Add(-26 * time.Hour),
			ActorKind: "worker",
		},
		{
			ID: "01RECENT_WORKER_FINDING", WorkspaceID: "ws", SessionID: "worker:w2",
			AgentName: "w2", Kind: "finding", Priority: "high",
			Content: "fresh result", Audience: "*", Status: "live",
			ExpiresAt: now.Add(8 * time.Hour), CreatedAt: now.Add(-1 * time.Hour),
			ActorKind: "worker",
		},
		{
			ID: "01OLD_AGENT_FINDING", WorkspaceID: "ws", SessionID: "agent-a",
			AgentName: "alice", Kind: "finding", Priority: "normal",
			Content: "agent finding", Audience: "*", Status: "live",
			ExpiresAt: now.Add(2 * time.Hour), CreatedAt: now.Add(-30 * time.Hour),
			ActorKind: "agent",
		},
	}
	for _, r := range rows {
		if err := db.InsertMeshMessage(ctx, r); err != nil {
			t.Fatalf("insert %s: %v", r.ID, err)
		}
	}

	r := &Reaper{store: db, retention: DefaultArchivedRetention}
	r.sweep(ctx)

	assertStatus := func(id, want string) {
		t.Helper()
		msg, err := db.GetMeshMessage(ctx, id)
		if err != nil {
			t.Fatalf("get %s: %v", id, err)
		}
		if msg.Status != want {
			t.Errorf("%s status = %q, want %q", id, msg.Status, want)
		}
	}
	assertStatus("01OLD_WORKER_FINDING", "archived")
	assertStatus("01OLD_WORKER_REPLY", "archived")
	assertStatus("01RECENT_WORKER_FINDING", "live")
	assertStatus("01OLD_AGENT_FINDING", "live")
}
