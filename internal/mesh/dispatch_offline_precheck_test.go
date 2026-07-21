package mesh

// Regression coverage for B2: a targeted mesh__send to a KNOWN-OFFLINE peer
// must park in the outbound queue WITHOUT paying the 10s SendToPeer dial. The
// dispatch precheck (dispatchP2P → peerDisconnected via the transport's
// non-dialing IsConnected probe) is what makes this cheap; before it, every
// targeted send to a down peer blocked the caller's tool-call goroutine for
// the full dial and then queued the message anyway.

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/p2p"
)

// connAwareTransport is a p2pTransport that ALSO implements the optional
// IsConnected(string) probe dispatchP2P's precheck consults. It records how
// many times SendToPeer (the dialing path) was invoked so a test can prove the
// precheck skipped the dial for an offline peer. Offline SendToPeer calls sleep
// briefly then error, mimicking a dial timeout — so a reverted precheck is
// slow AND observable, while the fixed path (which never calls it) stays fast.
type connAwareTransport struct {
	mu          sync.Mutex
	sendCalls   int
	connected   map[string]bool
	offlineDial time.Duration
	ch          chan p2p.MeshEnvelope
}

func newConnAwareTransport() *connAwareTransport {
	return &connAwareTransport{
		connected:   map[string]bool{},
		offlineDial: 200 * time.Millisecond,
		ch:          make(chan p2p.MeshEnvelope, 1),
	}
}

func (t *connAwareTransport) setConnected(peerID string, up bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.connected[peerID] = up
}

func (t *connAwareTransport) sendToPeerN() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.sendCalls
}

func (t *connAwareTransport) IsConnected(peerID string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.connected[peerID]
}

func (t *connAwareTransport) SendToPeer(_ context.Context, peerID string, _ *p2p.MeshEnvelope) error {
	t.mu.Lock()
	t.sendCalls++
	up := t.connected[peerID]
	delay := t.offlineDial
	t.mu.Unlock()
	if !up {
		// Mimic the libp2p dial blocking for the timeout then failing.
		time.Sleep(delay)
		return errors.New("dial timeout")
	}
	return nil
}

func (t *connAwareTransport) SendBroadcast(_ context.Context, _ *p2p.MeshEnvelope) (int, error) {
	return 0, nil
}

func (t *connAwareTransport) Subscribe() <-chan p2p.MeshEnvelope { return t.ch }

const (
	precheckSelfPeer    = "12D3KooWSelfTestPeerOfReasonableLengthAAA"
	precheckOfflinePeer = "12D3KooWOfflineTestPeerOfReasonableLenBBB"
	precheckOnlinePeer  = "12D3KooWOnlineTestPeerOfReasonableLengCCC"
)

// TestDispatchOfflinePrecheck_EnqueuesWithoutDialing is the headline B2
// regression: sending to an offline peer must NOT call SendToPeer (no dial),
// must return fast, and must still land the envelope in the outbound queue.
// Revert the precheck and SendToPeer is invoked → sendToPeerN()==1 and the
// call sleeps offlineDial → both the count and the timing assertions fail.
func TestDispatchOfflinePrecheck_EnqueuesWithoutDialing(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := newTestDB(t)
	mgr := NewManager(db)
	tr := newConnAwareTransport()
	tr.setConnected(precheckOfflinePeer, false) // explicitly offline
	mgr.SetP2PTransport(tr, precheckSelfPeer)
	mgr.SetOutboundQueue(NewOutboundQueue(db, tr, nil, nil, nil))

	meta := SessionMeta{SessionID: "s1", WorkspaceIDs: []string{"global"}, ClientType: "test"}
	start := time.Now()
	if _, err := mgr.Send(ctx, meta, SendRequest{
		Kind:    "event",
		Content: "to offline peer",
		ToPeer:  precheckOfflinePeer,
	}); err != nil {
		t.Fatalf("Send to offline peer should succeed via the queue: %v", err)
	}
	elapsed := time.Since(start)

	if n := tr.sendToPeerN(); n != 0 {
		t.Fatalf("precheck should skip the dial for an offline peer: SendToPeer called %d times, want 0", n)
	}
	// The fixed path never dials, so it can't have slept offlineDial.
	if elapsed >= tr.offlineDial {
		t.Fatalf("Send took %v (>= simulated dial %v): the 10s-dial precheck was bypassed", elapsed, tr.offlineDial)
	}

	rows, err := db.ListPendingMeshOutbound(ctx, time.Now().UTC(), 10)
	if err != nil {
		t.Fatalf("ListPendingMeshOutbound: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 queued row for the offline peer, got %d", len(rows))
	}
	if rows[0].TargetPeerID != precheckOfflinePeer {
		t.Fatalf("queued row peer = %q, want %q", rows[0].TargetPeerID, precheckOfflinePeer)
	}
}

// TestDispatchOnlinePeer_StillDials guards the other side: a peer the probe
// reports CONNECTED must take the normal dial path (SendToPeer invoked) and NOT
// be queued. Without this, a too-aggressive precheck could divert live traffic
// into the offline queue and add latency to every targeted send.
func TestDispatchOnlinePeer_StillDials(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := newTestDB(t)
	mgr := NewManager(db)
	tr := newConnAwareTransport()
	tr.setConnected(precheckOnlinePeer, true) // connected
	mgr.SetP2PTransport(tr, precheckSelfPeer)
	mgr.SetOutboundQueue(NewOutboundQueue(db, tr, nil, nil, nil))

	meta := SessionMeta{SessionID: "s1", WorkspaceIDs: []string{"global"}, ClientType: "test"}
	if _, err := mgr.Send(ctx, meta, SendRequest{
		Kind:    "event",
		Content: "to online peer",
		ToPeer:  precheckOnlinePeer,
	}); err != nil {
		t.Fatalf("Send to online peer: %v", err)
	}

	if n := tr.sendToPeerN(); n != 1 {
		t.Fatalf("connected peer must be dialed: SendToPeer called %d times, want 1", n)
	}
	rows, err := db.ListPendingMeshOutbound(ctx, time.Now().UTC(), 10)
	if err != nil {
		t.Fatalf("ListPendingMeshOutbound: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("a delivered online-peer send must not be queued, got %d rows", len(rows))
	}
}
