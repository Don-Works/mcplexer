package main

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/mesh"
	"github.com/don-works/mcplexer/internal/p2p"
	"github.com/don-works/mcplexer/internal/store/sqlite"
	"github.com/don-works/mcplexer/internal/workers/runner"
)

type workerMeshCaptureTransport struct {
	mu         sync.Mutex
	broadcasts int
	targeted   []string
	ch         chan p2p.MeshEnvelope
}

func (t *workerMeshCaptureTransport) SendToPeer(
	_ context.Context, peerID string, _ *p2p.MeshEnvelope,
) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.targeted = append(t.targeted, peerID)
	return nil
}

func (t *workerMeshCaptureTransport) SendBroadcast(
	_ context.Context, _ *p2p.MeshEnvelope,
) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.broadcasts++
	return 1, nil
}

func (t *workerMeshCaptureTransport) Subscribe() <-chan p2p.MeshEnvelope { return t.ch }

func (t *workerMeshCaptureTransport) snapshot() (int, []string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	targeted := append([]string(nil), t.targeted...)
	return t.broadcasts, targeted
}

func TestMeshSenderAdapterWorkerOutputLocalOnlyByDefault(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "worker-mesh.db"))
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	mgr := mesh.NewManager(db)
	transport := &workerMeshCaptureTransport{ch: make(chan p2p.MeshEnvelope, 1)}
	mgr.SetP2PTransport(transport, "12D3KooWSelfTestPeerOfReasonableLengthAAA")

	adapter := meshSenderAdapter{mgr: mgr}
	_, err = adapter.Send(ctx, runner.MeshOutbound{
		WorkerID:    "wkr-weekly",
		Kind:        "finding",
		Priority:    "normal",
		Content:     "weekly friday report complete",
		WorkspaceID: "ws-reports",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	time.Sleep(50 * time.Millisecond)
	broadcasts, targeted := transport.snapshot()
	if broadcasts != 0 {
		t.Fatalf("default worker output broadcast count = %d, want 0", broadcasts)
	}
	if len(targeted) != 0 {
		t.Fatalf("default worker output targeted sends = %d, want 0", len(targeted))
	}
}

func TestMeshSenderAdapterExplicitPeerDelivery(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "worker-peer.db"))
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	mgr := mesh.NewManager(db)
	transport := &workerMeshCaptureTransport{ch: make(chan p2p.MeshEnvelope, 1)}
	mgr.SetP2PTransport(transport, "12D3KooWSelfTestPeerOfReasonableLengthAAA")

	target := "12D3KooWTargetTestPeerOfReasonableLenBBB"
	adapter := meshSenderAdapter{mgr: mgr}
	_, err = adapter.Send(ctx, runner.MeshOutbound{
		WorkerID:    "wkr-weekly",
		Kind:        "finding",
		Priority:    "normal",
		Content:     "send this to the paired laptop",
		WorkspaceID: "ws-reports",
		ToPeer:      target,
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	_, targeted := transport.snapshot()
	if len(targeted) != 1 || targeted[0] != target {
		t.Fatalf("targeted sends = %#v, want [%q]", targeted, target)
	}
}

func TestMeshSenderAdapterExplicitBroadcastPeers(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "worker-broadcast.db"))
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	mgr := mesh.NewManager(db)
	transport := &workerMeshCaptureTransport{ch: make(chan p2p.MeshEnvelope, 1)}
	mgr.SetP2PTransport(transport, "12D3KooWSelfTestPeerOfReasonableLengthAAA")

	adapter := meshSenderAdapter{mgr: mgr}
	_, err = adapter.Send(ctx, runner.MeshOutbound{
		WorkerID:       "wkr-provenance",
		Kind:           "finding",
		Priority:       "low",
		Content:        "intentional peer-visible provenance",
		WorkspaceID:    "ws-memory",
		BroadcastPeers: true,
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		broadcasts, targeted := transport.snapshot()
		if broadcasts == 1 && len(targeted) == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	broadcasts, targeted := transport.snapshot()
	t.Fatalf("broadcasts=%d targeted=%#v, want one broadcast and no targeted sends", broadcasts, targeted)
}
