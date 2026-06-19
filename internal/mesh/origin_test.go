package mesh_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/mesh"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

// TestRegisterAgentTagsLocalOrigin verifies a session that registers
// through the local stdio path lands in mesh_agents with origin="local".
// Pre-fix this column was absent and the UI couldn't tell remote agents
// (reached via libp2p) apart from local socket sessions, so a broken
// libp2p transport silently looked like a healthy local-only mesh.
func TestRegisterAgentTagsLocalOrigin(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "origin.db"))
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	mgr := mesh.NewManager(db)
	meta := mesh.SessionMeta{
		SessionID:    "local-session-1",
		WorkspaceIDs: []string{"global"},
		ClientType:   "claude-code",
	}
	if err := mgr.RegisterAgent(ctx, meta); err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}

	got, err := db.GetMeshAgent(ctx, "local-session-1")
	if err != nil {
		t.Fatalf("GetMeshAgent: %v", err)
	}
	if got.Origin != store.MeshAgentOriginLocal {
		t.Fatalf("origin = %q; want %q", got.Origin, store.MeshAgentOriginLocal)
	}
}

// TestUpsertPreservesPeerOrigin guards against a regression where a
// follow-up local Touch/Receive overwrites a remote agent's origin column
// back to "local". The UPSERT keeps the existing origin when the incoming
// row's origin string is empty (the legacy zero value).
func TestUpsertPreservesPeerOrigin(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "preserve.db"))
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	now := time.Now().UTC()
	if err := db.UpsertMeshAgent(ctx, &store.MeshAgent{
		SessionID:   "peerA",
		WorkspaceID: "global",
		Name:        "remote-coder",
		ClientType:  "p2p",
		Origin:      store.MeshAgentOriginPeerPrefix + "12D3PeerA",
		LastSeenAt:  now,
		CreatedAt:   now,
	}); err != nil {
		t.Fatalf("seed remote: %v", err)
	}

	// A second upsert with empty origin (e.g. an unrelated metadata
	// touch) must NOT erase the recorded peer origin.
	if err := db.UpsertMeshAgent(ctx, &store.MeshAgent{
		SessionID:   "peerA",
		WorkspaceID: "global",
		Name:        "remote-coder",
		ClientType:  "p2p",
		Origin:      "", // legacy / undefined
		LastSeenAt:  now.Add(time.Minute),
		CreatedAt:   now,
	}); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	got, err := db.GetMeshAgent(ctx, "peerA")
	if err != nil {
		t.Fatalf("GetMeshAgent: %v", err)
	}
	want := store.MeshAgentOriginPeerPrefix + "12D3PeerA"
	if got.Origin != want {
		t.Fatalf("origin = %q; want preserved %q", got.Origin, want)
	}
}

// TestActiveMeshAgentsCarryOrigin ensures the multi-row read path also
// hydrates Origin so the REST handler exposes it to the UI.
func TestActiveMeshAgentsCarryOrigin(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "list.db"))
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	now := time.Now().UTC()
	rows := []store.MeshAgent{
		{SessionID: "local-1", WorkspaceID: "global", ClientType: "claude-code",
			Origin: store.MeshAgentOriginLocal, LastSeenAt: now, CreatedAt: now},
		{SessionID: "12D3KooWPeerA", WorkspaceID: "global", ClientType: "p2p",
			Origin:     store.MeshAgentOriginPeerPrefix + "12D3KooWPeerA",
			LastSeenAt: now, CreatedAt: now},
	}
	for i := range rows {
		if err := db.UpsertMeshAgent(ctx, &rows[i]); err != nil {
			t.Fatalf("upsert %d: %v", i, err)
		}
	}

	since := now.Add(-time.Hour)
	listed, err := db.ListActiveMeshAgents(ctx, "global", since)
	if err != nil {
		t.Fatalf("ListActiveMeshAgents: %v", err)
	}
	got := map[string]string{}
	for _, a := range listed {
		got[a.SessionID] = a.Origin
	}
	if got["local-1"] != store.MeshAgentOriginLocal {
		t.Fatalf("local origin lost in list: %v", got)
	}
	if got["12D3KooWPeerA"] != store.MeshAgentOriginPeerPrefix+"12D3KooWPeerA" {
		t.Fatalf("peer origin lost in list: %v", got)
	}
}
