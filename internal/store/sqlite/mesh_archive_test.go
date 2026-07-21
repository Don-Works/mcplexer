package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

func TestArchiveMessagesBySenderAndKinds(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := newTestDB(t, "mesh-archive.db")
	now := time.Now().UTC()

	rows := []*store.MeshMessage{
		{
			ID: "01WORKER_FINDING", WorkspaceID: "ws", SessionID: "worker:w1",
			AgentName: "w1", Kind: "finding", Priority: "high",
			Content: "result", Audience: "*", Status: "live",
			ExpiresAt: now.Add(8 * time.Hour), CreatedAt: now, ActorKind: "worker",
		},
		{
			ID: "01WORKER_REPLY", WorkspaceID: "ws", SessionID: "worker:w1",
			AgentName: "w1", Kind: "reply", Priority: "high",
			Content: "reply", Audience: "*", Status: "live",
			ExpiresAt: now.Add(8 * time.Hour), CreatedAt: now, ActorKind: "worker",
		},
		{
			ID: "01WORKER_EVENT", WorkspaceID: "ws", SessionID: "worker:w1",
			AgentName: "w1", Kind: "event", Priority: "normal",
			Content: "started", Audience: "*", Status: "live",
			ExpiresAt: now.Add(2 * time.Hour), CreatedAt: now, ActorKind: "worker",
		},
		{
			ID: "01AGENT_FINDING", WorkspaceID: "ws", SessionID: "agent-a",
			AgentName: "alice", Kind: "finding", Priority: "normal",
			Content: "agent finding", Audience: "*", Status: "live",
			ExpiresAt: now.Add(2 * time.Hour), CreatedAt: now, ActorKind: "agent",
		},
		{
			ID: "01WORKER2_FINDING", WorkspaceID: "ws", SessionID: "worker:w2",
			AgentName: "w2", Kind: "finding", Priority: "high",
			Content: "w2 result", Audience: "*", Status: "live",
			ExpiresAt: now.Add(8 * time.Hour), CreatedAt: now, ActorKind: "worker",
		},
	}
	for _, r := range rows {
		if err := db.InsertMeshMessage(ctx, r); err != nil {
			t.Fatalf("insert %s: %v", r.ID, err)
		}
	}

	archived, err := db.ArchiveMessagesBySenderAndKinds(ctx,
		[]string{"worker:w1", "worker:w2"},
		[]string{"finding", "reply"},
	)
	if err != nil {
		t.Fatalf("ArchiveMessagesBySenderAndKinds: %v", err)
	}
	if archived != 3 {
		t.Fatalf("archived = %d, want 3 (w1 finding + w1 reply + w2 finding)", archived)
	}

	assertArchived := func(id string) {
		t.Helper()
		msg, err := db.GetMeshMessage(ctx, id)
		if err != nil {
			t.Fatalf("get %s: %v", id, err)
		}
		if msg.Status != "archived" {
			t.Errorf("%s status = %q, want archived", id, msg.Status)
		}
	}
	assertLive := func(id string) {
		t.Helper()
		msg, err := db.GetMeshMessage(ctx, id)
		if err != nil {
			t.Fatalf("get %s: %v", id, err)
		}
		if msg.Status != "live" {
			t.Errorf("%s status = %q, want live", id, msg.Status)
		}
	}

	assertArchived("01WORKER_FINDING")
	assertArchived("01WORKER_REPLY")
	assertArchived("01WORKER2_FINDING")
	assertLive("01WORKER_EVENT")
	assertLive("01AGENT_FINDING")
}

func TestArchiveMessagesBySenderAndKinds_EmptyInput(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := newTestDB(t, "mesh-archive-empty.db")

	n, err := db.ArchiveMessagesBySenderAndKinds(ctx, nil, []string{"finding"})
	if err != nil {
		t.Fatalf("nil senders: %v", err)
	}
	if n != 0 {
		t.Fatalf("nil senders archived %d, want 0", n)
	}

	n, err = db.ArchiveMessagesBySenderAndKinds(ctx, []string{"worker:w1"}, nil)
	if err != nil {
		t.Fatalf("nil kinds: %v", err)
	}
	if n != 0 {
		t.Fatalf("nil kinds archived %d, want 0", n)
	}
}

func TestArchiveMessagesBySenderAndKinds_Idempotent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := newTestDB(t, "mesh-archive-idempotent.db")
	now := time.Now().UTC()

	if err := db.InsertMeshMessage(ctx, &store.MeshMessage{
		ID: "01FINDING", WorkspaceID: "ws", SessionID: "worker:w1",
		AgentName: "w1", Kind: "finding", Priority: "high",
		Content: "result", Audience: "*", Status: "live",
		ExpiresAt: now.Add(8 * time.Hour), CreatedAt: now, ActorKind: "worker",
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	n1, err := db.ArchiveMessagesBySenderAndKinds(ctx, []string{"worker:w1"}, []string{"finding"})
	if err != nil {
		t.Fatalf("first call: %v", err)
	}
	if n1 != 1 {
		t.Fatalf("first call archived %d, want 1", n1)
	}

	n2, err := db.ArchiveMessagesBySenderAndKinds(ctx, []string{"worker:w1"}, []string{"finding"})
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if n2 != 0 {
		t.Fatalf("second call archived %d, want 0 (idempotent)", n2)
	}
}

func TestArchiveOldWorkerFindings(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := newTestDB(t, "mesh-old-worker.db")
	now := time.Now().UTC()

	rows := []*store.MeshMessage{
		{
			ID: "01OLD_WORKER_FINDING", WorkspaceID: "ws", SessionID: "worker:w1",
			AgentName: "w1", Kind: "finding", Priority: "high",
			Content: "old finding", Audience: "*", Status: "live",
			ExpiresAt: now.Add(48 * time.Hour), CreatedAt: now.Add(-25 * time.Hour), ActorKind: "worker",
		},
		{
			ID: "01OLD_WORKER_REPLY", WorkspaceID: "ws", SessionID: "worker:w1",
			AgentName: "w1", Kind: "reply", Priority: "high",
			Content: "old reply", Audience: "*", Status: "live",
			ExpiresAt: now.Add(48 * time.Hour), CreatedAt: now.Add(-26 * time.Hour), ActorKind: "worker",
		},
		{
			ID: "01OLD_WORKER_EVENT", WorkspaceID: "ws", SessionID: "worker:w1",
			AgentName: "w1", Kind: "event", Priority: "normal",
			Content: "old event", Audience: "*", Status: "live",
			ExpiresAt: now.Add(2 * time.Hour), CreatedAt: now.Add(-30 * time.Hour), ActorKind: "worker",
		},
		{
			ID: "01RECENT_WORKER_FINDING", WorkspaceID: "ws", SessionID: "worker:w2",
			AgentName: "w2", Kind: "finding", Priority: "high",
			Content: "recent finding", Audience: "*", Status: "live",
			ExpiresAt: now.Add(8 * time.Hour), CreatedAt: now.Add(-1 * time.Hour), ActorKind: "worker",
		},
		{
			ID: "01OLD_AGENT_FINDING", WorkspaceID: "ws", SessionID: "agent-a",
			AgentName: "alice", Kind: "finding", Priority: "normal",
			Content: "agent finding", Audience: "*", Status: "live",
			ExpiresAt: now.Add(2 * time.Hour), CreatedAt: now.Add(-30 * time.Hour), ActorKind: "agent",
		},
	}
	for _, r := range rows {
		if err := db.InsertMeshMessage(ctx, r); err != nil {
			t.Fatalf("insert %s: %v", r.ID, err)
		}
	}

	archived, err := db.ArchiveOldWorkerFindings(ctx, now.Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("ArchiveOldWorkerFindings: %v", err)
	}
	if archived != 2 {
		t.Fatalf("archived = %d, want 2 (old worker finding + old worker reply)", archived)
	}

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
	assertStatus("01OLD_WORKER_EVENT", "live")
	assertStatus("01RECENT_WORKER_FINDING", "live")
	assertStatus("01OLD_AGENT_FINDING", "live")
}

func newTestDB(t *testing.T, name string) *DB {
	t.Helper()
	db, err := New(context.Background(), t.TempDir()+"/"+name)
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// TestGetMeshAgentNotFound verifies that GetMeshAgent returns
// store.ErrNotFound when the session_id does not exist.
func TestGetMeshAgentNotFound(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := newTestDB(t, "mesh-notfound.db")

	_, err := db.GetMeshAgent(ctx, "nonexistent-session")
	if err != store.ErrNotFound {
		t.Fatalf("expected store.ErrNotFound, got %v", err)
	}
}

// TestGetMeshMessageNotFound verifies that GetMeshMessage returns
// store.ErrNotFound when the message id does not exist.
func TestGetMeshMessageNotFound(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := newTestDB(t, "mesh-msg-notfound.db")

	_, err := db.GetMeshMessage(ctx, "nonexistent-message-id")
	if err != store.ErrNotFound {
		t.Fatalf("expected store.ErrNotFound, got %v", err)
	}
}

// TestGetMeshAgentRoundTrip verifies that GetMeshAgent returns a valid
// agent after InsertMeshAgent, and returns ErrNotFound before.
func TestGetMeshAgentRoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := newTestDB(t, "mesh-agent-roundtrip.db")

	agent := &store.MeshAgent{
		SessionID:   "sess-1",
		WorkspaceID: "ws-1",
		Name:        "test-agent",
		Role:        "worker",
		ClientType:  "codex",
		Status:      "active",
		Origin:      store.MeshAgentOriginLocal,
	}
	if err := db.UpsertMeshAgent(ctx, agent); err != nil {
		t.Fatalf("UpsertMeshAgent: %v", err)
	}

	got, err := db.GetMeshAgent(ctx, "sess-1")
	if err != nil {
		t.Fatalf("GetMeshAgent: %v", err)
	}
	if got.Name != "test-agent" {
		t.Fatalf("name = %q, want test-agent", got.Name)
	}

	_, err = db.GetMeshAgent(ctx, "wrong-session")
	if err != store.ErrNotFound {
		t.Fatalf("expected ErrNotFound for missing agent, got %v", err)
	}
}
