//go:build p2p

package mesh_test

// End-to-end test for the libp2p mesh transport bridged into mesh.Manager:
// spin up two hosts, wire each into a sqlite-backed mesh.Manager, send a
// message from A targeting B's peer ID, then verify B's mesh_messages
// table contains the row inside the 1s SLO.

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/mesh"
	"github.com/don-works/mcplexer/internal/p2p"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

// TestMeshBridgeEndToEnd is the headline acceptance for M1.5: two paired
// hosts on localhost exchange a signed envelope and the receiving side
// persists it to mesh_messages within the SLO.
func TestMeshBridgeEndToEnd(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	hostA, dbA := bringUpHost(t, ctx, "a")
	hostB, dbB := bringUpHost(t, ctx, "b")

	pairPeer(t, ctx, dbA, hostA.PeerID())
	pairPeer(t, ctx, dbA, hostB.PeerID())
	pairPeer(t, ctx, dbB, hostA.PeerID())
	pairPeer(t, ctx, dbB, hostB.PeerID())

	mgrA, transA := wireBridge(ctx, dbA, hostA)
	defer func() { _ = transA.Close() }()
	mgrB, transB := wireBridge(ctx, dbB, hostB)
	defer func() { _ = transB.Close() }()
	_ = mgrB

	target := fmt.Sprintf("%s/p2p/%s", hostB.Addrs()[0], hostB.ID())
	if _, err := hostA.Connect(ctx, target); err != nil {
		t.Fatalf("connect A→B: %v", err)
	}

	meta := mesh.SessionMeta{
		SessionID:    "test-A",
		WorkspaceIDs: []string{"global"},
		ClientType:   "test",
	}
	startedAt := time.Now()
	if _, err := mgrA.Send(ctx, meta, mesh.SendRequest{
		Kind:    "finding",
		Content: "cross-machine ping",
		ToPeer:  hostB.PeerID(),
	}); err != nil {
		t.Fatalf("mgrA.Send: %v", err)
	}

	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		msgs, err := dbB.QueryMeshMessages(ctx, store.MeshMessageFilter{
			StatusLive: true,
			Limit:      10,
		})
		if err != nil {
			t.Fatalf("query B: %v", err)
		}
		for _, m := range msgs {
			if m.Content == "cross-machine ping" && m.SessionID == hostA.PeerID() {
				t.Logf("delivery latency: %v", time.Since(startedAt))
				return
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("B never persisted the cross-machine envelope within 1s SLO")
}

// bringUpHost boots a libp2p host backed by a fresh sqlite db.
func bringUpHost(t *testing.T, ctx context.Context, name string) (*p2p.Host, *sqlite.DB) {
	t.Helper()
	keyPath := filepath.Join(t.TempDir(), name+"-identity.key")
	cfg := p2p.Config{
		Enabled:      true,
		IdentityPath: keyPath,
		ListenAddrs:  []string{"/ip4/127.0.0.1/tcp/0"},
	}
	h, err := p2p.NewHost(ctx, cfg, nil, nil)
	if err != nil {
		t.Fatalf("NewHost(%s): %v", name, err)
	}
	t.Cleanup(func() { _ = h.Close() })

	dbPath := filepath.Join(t.TempDir(), name+".db")
	db, err := sqlite.New(ctx, dbPath)
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return h, db
}

func pairPeer(t *testing.T, ctx context.Context, db *sqlite.DB, peerID string) {
	t.Helper()
	err := db.AddPeer(ctx, &store.P2PPeer{
		PeerID:      peerID,
		DisplayName: peerID,
		PairedAt:    time.Now().UTC(),
		TrustLevel:  1,
		Scopes:      []string{"mesh"},
	})
	if err != nil {
		t.Fatalf("AddPeer(%s): %v", peerID, err)
	}
}

func wireBridge(
	ctx context.Context, db *sqlite.DB, h *p2p.Host,
) (*mesh.Manager, *p2p.MeshTransport) {
	mgr := mesh.NewManager(db)
	lookup := p2p.NewSQLPeerLookup(db.Raw(), nil)
	trans := p2p.NewMeshTransport(h, lookup, nil, nil)
	trans.Start()
	mgr.SetP2PTransport(trans, h.PeerID())
	mgr.StartP2PBridge(ctx, nil)
	return mgr, trans
}
