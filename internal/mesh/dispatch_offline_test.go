package mesh

// B2 regression coverage: a targeted mesh__send to an OFFLINE peer must NOT
// eat the full 10s SendToPeer dial timeout on the caller's goroutine. When the
// transport can cheaply confirm the peer is disconnected (the liveness probe),
// dispatchP2P skips the dial and parks the envelope in the durable outbound
// queue immediately. Online peers are still dialed directly, and a dial that
// fails still falls back to the queue.

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/p2p"
)

// livenessFakeTransport is a p2pTransport that ALSO implements the optional
// peerConnectivityProbe (IsConnected), so dispatchP2P's liveness precheck is
// exercised. It counts SendToPeer calls so a test can prove the dial was
// skipped for an offline peer. Unknown peers default to offline (false).
type livenessFakeTransport struct {
	mu            sync.Mutex
	online        map[string]bool
	sendToPeerN   int64
	sendToPeerErr error
	ch            chan p2p.MeshEnvelope
}

func newLivenessFakeTransport() *livenessFakeTransport {
	return &livenessFakeTransport{online: map[string]bool{}, ch: make(chan p2p.MeshEnvelope, 1)}
}

func (f *livenessFakeTransport) markOnline(peerID string, up bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.online[peerID] = up
}

func (f *livenessFakeTransport) IsConnected(peerID string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.online[peerID]
}

func (f *livenessFakeTransport) SendToPeer(_ context.Context, _ string, _ *p2p.MeshEnvelope) error {
	atomic.AddInt64(&f.sendToPeerN, 1)
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sendToPeerErr
}

func (f *livenessFakeTransport) sendToPeerCalls() int64 { return atomic.LoadInt64(&f.sendToPeerN) }

func (f *livenessFakeTransport) SendBroadcast(_ context.Context, _ *p2p.MeshEnvelope) (int, error) {
	return 0, nil
}

func (f *livenessFakeTransport) Subscribe() <-chan p2p.MeshEnvelope { return f.ch }

const offlineTestSelfPeer = "12D3KooWSelfTestPeerOfReasonableLengthAAA"

// TestDispatchP2P_OfflinePeerSkipsDialAndQueues is the headline B2 regression:
// a send to a known-offline peer must park in the queue WITHOUT dialing.
func TestDispatchP2P_OfflinePeerSkipsDialAndQueues(t *testing.T) {
	t.Parallel()
	const target = "12D3KooWBobOfflineTestPeerOfReasonableLenBBB"
	db := newTestDB(t)
	mgr := NewManager(db)
	ft := newLivenessFakeTransport() // target left offline (default false)
	mgr.SetP2PTransport(ft, offlineTestSelfPeer)
	mgr.SetOutboundQueue(NewOutboundQueue(db, ft, nil, nil, nil))
	ctx := context.Background()

	meta := SessionMeta{SessionID: "s1", WorkspaceIDs: []string{"global"}, ClientType: "test"}
	if _, err := mgr.Send(ctx, meta, SendRequest{
		Kind: "event", Content: "to offline bob", ToPeer: target,
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	if n := ft.sendToPeerCalls(); n != 0 {
		t.Fatalf("SendToPeer was dialed %d time(s) for an OFFLINE peer; "+
			"the liveness precheck must skip the 10s dial", n)
	}
	rows, err := db.ListPendingMeshOutbound(ctx, time.Now().UTC(), 10)
	if err != nil {
		t.Fatalf("ListPendingMeshOutbound: %v", err)
	}
	if len(rows) != 1 || rows[0].TargetPeerID != target {
		t.Fatalf("offline send did not park in the queue: rows=%+v", rows)
	}
}

// TestDispatchP2P_OnlinePeerDialsDirectly proves the precheck stays inert for a
// connected peer: it is dialed via SendToPeer and never queued.
func TestDispatchP2P_OnlinePeerDialsDirectly(t *testing.T) {
	t.Parallel()
	const target = "12D3KooWBobOnlineTestPeerOfReasonableLenCCC"
	db := newTestDB(t)
	mgr := NewManager(db)
	ft := newLivenessFakeTransport()
	ft.markOnline(target, true)
	mgr.SetP2PTransport(ft, offlineTestSelfPeer)
	mgr.SetOutboundQueue(NewOutboundQueue(db, ft, nil, nil, nil))
	ctx := context.Background()

	meta := SessionMeta{SessionID: "s1", WorkspaceIDs: []string{"global"}, ClientType: "test"}
	if _, err := mgr.Send(ctx, meta, SendRequest{
		Kind: "event", Content: "to online bob", ToPeer: target,
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if n := ft.sendToPeerCalls(); n != 1 {
		t.Fatalf("SendToPeer calls = %d, want 1 (a connected peer must be dialed directly)", n)
	}
	rows, err := db.ListPendingMeshOutbound(ctx, time.Now().UTC(), 10)
	if err != nil {
		t.Fatalf("ListPendingMeshOutbound: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("a successful online send must not queue: rows=%+v", rows)
	}
}

// TestDispatchP2P_OnlinePeerDialFailureStillQueues proves the precheck did not
// remove the existing failure fallback: a connected peer whose dial fails is
// dialed once, then parked in the queue.
func TestDispatchP2P_OnlinePeerDialFailureStillQueues(t *testing.T) {
	t.Parallel()
	const target = "12D3KooWBobFlakyTestPeerOfReasonableLenDDD"
	db := newTestDB(t)
	mgr := NewManager(db)
	ft := newLivenessFakeTransport()
	ft.markOnline(target, true)
	ft.sendToPeerErr = errors.New("write deadline exceeded")
	mgr.SetP2PTransport(ft, offlineTestSelfPeer)
	mgr.SetOutboundQueue(NewOutboundQueue(db, ft, nil, nil, nil))
	ctx := context.Background()

	meta := SessionMeta{SessionID: "s1", WorkspaceIDs: []string{"global"}, ClientType: "test"}
	if _, err := mgr.Send(ctx, meta, SendRequest{
		Kind: "event", Content: "to flaky bob", ToPeer: target,
	}); err != nil {
		t.Fatalf("Send should swallow the dial error via the queue: %v", err)
	}
	if n := ft.sendToPeerCalls(); n != 1 {
		t.Fatalf("SendToPeer calls = %d, want 1 (online peer is dialed, then the failure is queued)", n)
	}
	rows, _ := db.ListPendingMeshOutbound(ctx, time.Now().UTC(), 10)
	if len(rows) != 1 {
		t.Fatalf("dial failure did not fall back to the queue: rows=%+v", rows)
	}
}
