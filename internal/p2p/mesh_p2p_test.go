//go:build p2p

package p2p

import (
	"context"
	"testing"
	"time"
)

// TestMeshSendToPeerDelivers is the headline acceptance test: A signs +
// sends; B receives the same envelope through its Subscribe channel.
func TestMeshSendToPeerDelivers(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	a := startTestHost(t, "a")
	defer func() { _ = a.Close() }()
	b := startTestHost(t, "b")
	defer func() { _ = b.Close() }()
	connectHosts(t, ctx, a, b)

	pairs := newMeshLookup(a.PeerID(), b.PeerID())
	aTrans := NewMeshTransport(a, pairs, nil, nil)
	bTrans := NewMeshTransport(b, pairs, nil, nil)
	bTrans.Start()
	defer func() { _ = aTrans.Close() }()
	defer func() { _ = bTrans.Close() }()

	rx := bTrans.Subscribe()
	env := &MeshEnvelope{
		ID:        newULID(),
		Kind:      "finding",
		Content:   "hello from A",
		Recipient: Recipient{Kind: "peer", Value: b.PeerID()},
	}
	if err := aTrans.SendToPeer(ctx, b.PeerID(), env); err != nil {
		t.Fatalf("SendToPeer: %v", err)
	}
	select {
	case got := <-rx:
		if got.ID != env.ID || got.Content != "hello from A" {
			t.Fatalf("rx mismatch: %+v", got)
		}
		if got.SenderPeerID != a.PeerID() {
			t.Fatalf("sender_peer_id = %q, want %q", got.SenderPeerID, a.PeerID())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for envelope")
	}
}

// TestMeshBroadcastFanout: A broadcasts; B + C each receive once.
func TestMeshBroadcastFanout(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	a := startTestHost(t, "a")
	defer func() { _ = a.Close() }()
	b := startTestHost(t, "b")
	defer func() { _ = b.Close() }()
	c := startTestHost(t, "c")
	defer func() { _ = c.Close() }()
	connectHosts(t, ctx, a, b)
	connectHosts(t, ctx, a, c)

	pairs := newMeshLookup(a.PeerID(), b.PeerID(), c.PeerID())
	aTrans := NewMeshTransport(a, pairs, nil, nil)
	bTrans := NewMeshTransport(b, pairs, nil, nil)
	cTrans := NewMeshTransport(c, pairs, nil, nil)
	bTrans.Start()
	cTrans.Start()
	defer func() { _ = aTrans.Close() }()
	defer func() { _ = bTrans.Close() }()
	defer func() { _ = cTrans.Close() }()

	bRx := bTrans.Subscribe()
	cRx := cTrans.Subscribe()
	env := &MeshEnvelope{
		ID:        newULID(),
		Kind:      "event",
		Content:   "broadcast!",
		Recipient: Recipient{Kind: "audience", Value: "*"},
	}
	if _, err := aTrans.SendBroadcast(ctx, env); err != nil {
		t.Fatalf("SendBroadcast: %v", err)
	}
	want := map[string]<-chan MeshEnvelope{"b": bRx, "c": cRx}
	for label, rx := range want {
		select {
		case got := <-rx:
			if got.ID != env.ID {
				t.Fatalf("%s rx id = %q, want %q", label, got.ID, env.ID)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s timed out waiting for envelope", label)
		}
	}
}

// TestMeshBroadcastSkipsOfflinePeer is the regression guard for the
// broadcast-freeze bug: A is PAIRED with B (B is in the peer lookup) but
// never connected to it. SendBroadcast must skip B entirely rather than
// dialing it — a dial would block for the full 10s timeout and stall the
// tool-call goroutine that triggered the broadcast. The call must return
// near-instantly with sent==0.
func TestMeshBroadcastSkipsOfflinePeer(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	a := startTestHost(t, "a")
	defer func() { _ = a.Close() }()
	b := startTestHost(t, "b")
	defer func() { _ = b.Close() }()
	// NOTE: deliberately NOT calling connectHosts(a, b) — B is paired but
	// offline from A's perspective.

	pairs := newMeshLookup(a.PeerID(), b.PeerID())
	aTrans := NewMeshTransport(a, pairs, nil, nil)
	defer func() { _ = aTrans.Close() }()

	env := &MeshEnvelope{
		ID:        newULID(),
		Kind:      "event",
		Content:   "broadcast to nobody",
		Recipient: Recipient{Kind: "audience", Value: "*"},
	}

	start := time.Now()
	sent, err := aTrans.SendBroadcast(ctx, env)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("SendBroadcast: %v", err)
	}
	if sent != 0 {
		t.Fatalf("sent = %d, want 0 (offline peer must be skipped)", sent)
	}
	// A real dial would burn the full 10s timeout; the skip path is sub-ms.
	if elapsed > 1*time.Second {
		t.Fatalf("SendBroadcast blocked %v dialing an offline peer; expected fast skip", elapsed)
	}
}

// TestMeshReplayRejected: same envelope id within window is dropped.
func TestMeshReplayRejected(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	a := startTestHost(t, "a")
	defer func() { _ = a.Close() }()
	b := startTestHost(t, "b")
	defer func() { _ = b.Close() }()
	connectHosts(t, ctx, a, b)

	pairs := newMeshLookup(a.PeerID(), b.PeerID())
	bAudit := &memAuditor{}
	aTrans := NewMeshTransport(a, pairs, nil, nil)
	bTrans := NewMeshTransport(b, pairs, bAudit, nil)
	bTrans.Start()
	defer func() { _ = aTrans.Close() }()
	defer func() { _ = bTrans.Close() }()

	rx := bTrans.Subscribe()
	env := &MeshEnvelope{
		ID:      newULID(),
		Kind:    "event",
		Content: "once",
	}
	if err := aTrans.SendToPeer(ctx, b.PeerID(), env); err != nil {
		t.Fatalf("SendToPeer: %v", err)
	}
	select {
	case <-rx:
	case <-time.After(2 * time.Second):
		t.Fatal("first send not received")
	}
	if err := aTrans.SendToPeer(ctx, b.PeerID(), env); err != nil {
		t.Fatalf("SendToPeer (replay): %v", err)
	}
	select {
	case got := <-rx:
		t.Fatalf("replay leaked through: %+v", got)
	case <-time.After(500 * time.Millisecond):
	}
	if !contains(bAudit.reasons(), "duplicate") {
		t.Fatalf("audit reasons = %v, want one to be 'duplicate'", bAudit.reasons())
	}
}

// TestMeshUnpairedRejected: B has not paired A, so A's stream is rejected.
func TestMeshUnpairedRejected(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	a := startTestHost(t, "a")
	defer func() { _ = a.Close() }()
	b := startTestHost(t, "b")
	defer func() { _ = b.Close() }()
	connectHosts(t, ctx, a, b)

	aLookup := newMeshLookup(a.PeerID(), b.PeerID())
	bLookup := newMeshLookup() // <-- B has no paired peers
	bAudit := &memAuditor{}
	aTrans := NewMeshTransport(a, aLookup, nil, nil)
	bTrans := NewMeshTransport(b, bLookup, bAudit, nil)
	bTrans.Start()
	defer func() { _ = aTrans.Close() }()
	defer func() { _ = bTrans.Close() }()

	rx := bTrans.Subscribe()
	env := &MeshEnvelope{ID: newULID(), Kind: "event", Content: "x"}
	if err := aTrans.SendToPeer(ctx, b.PeerID(), env); err != nil {
		t.Fatalf("SendToPeer: %v", err)
	}
	select {
	case got := <-rx:
		t.Fatalf("unpaired stream leaked: %+v", got)
	case <-time.After(500 * time.Millisecond):
	}
	if !contains(bAudit.reasons(), "unpaired_peer") {
		t.Fatalf("audit reasons = %v, want 'unpaired_peer'", bAudit.reasons())
	}
}

// TestMeshSignatureTamperRejected: B sees an invalid sig and audits it.
func TestMeshSignatureTamperRejected(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	a := startTestHost(t, "a")
	defer func() { _ = a.Close() }()
	b := startTestHost(t, "b")
	defer func() { _ = b.Close() }()
	connectHosts(t, ctx, a, b)

	pairs := newMeshLookup(a.PeerID(), b.PeerID())
	bAudit := &memAuditor{}
	bTrans := NewMeshTransport(b, pairs, bAudit, nil)
	bTrans.Start()
	defer func() { _ = bTrans.Close() }()

	stream, err := a.Inner().NewStream(ctx, b.ID(), MeshProtocol)
	if err != nil {
		t.Fatalf("NewStream: %v", err)
	}
	defer func() { _ = stream.Close() }()
	env := &MeshEnvelope{
		ID:           newULID(),
		SenderPeerID: a.PeerID(),
		Kind:         "event",
		Content:      "x",
		TS:           time.Now().UnixMilli(),
		Signature:    []byte("not-a-real-signature"),
	}
	if err := writeEnvelope(stream, env); err != nil {
		t.Fatalf("writeEnvelope: %v", err)
	}
	deadline := time.After(1 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatalf("audit reasons = %v, want 'invalid_signature'", bAudit.reasons())
		case <-time.After(50 * time.Millisecond):
			if contains(bAudit.reasons(), "invalid_signature") {
				return
			}
		}
	}
}

// TestMeshEnvelopeSenderDisplayNameRoundTrip pins the new SenderDisplayName
// surface: a sender stamps the friendly label, the receiver reads it back
// over the wire intact. NOT a trust signal — but the field must survive
// JSON round-trip and signature validation so the UI can render it.
func TestMeshEnvelopeSenderDisplayNameRoundTrip(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	a := startTestHost(t, "a")
	defer func() { _ = a.Close() }()
	b := startTestHost(t, "b")
	defer func() { _ = b.Close() }()
	connectHosts(t, ctx, a, b)

	pairs := newMeshLookup(a.PeerID(), b.PeerID())
	aTrans := NewMeshTransport(a, pairs, nil, nil)
	bTrans := NewMeshTransport(b, pairs, nil, nil)
	bTrans.Start()
	defer func() { _ = aTrans.Close() }()
	defer func() { _ = bTrans.Close() }()

	rx := bTrans.Subscribe()
	env := &MeshEnvelope{
		ID:                newULID(),
		SenderDisplayName: "peer-laptop",
		Kind:              "finding",
		Content:           "hello with name",
		Recipient:         Recipient{Kind: "peer", Value: b.PeerID()},
	}
	if err := aTrans.SendToPeer(ctx, b.PeerID(), env); err != nil {
		t.Fatalf("SendToPeer: %v", err)
	}
	select {
	case got := <-rx:
		if got.SenderDisplayName != "peer-laptop" {
			t.Fatalf("SenderDisplayName = %q, want peer-laptop",
				got.SenderDisplayName)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for envelope")
	}
}

// TestDedupeWindowEvicts verifies the FIFO eviction once cap is exceeded.
func TestDedupeWindowEvicts(t *testing.T) {
	t.Parallel()
	d := newDedupeWindow(3)
	if d.seen("a", "1") || d.seen("a", "2") || d.seen("a", "3") {
		t.Fatal("first-time keys flagged as seen")
	}
	// '1' is still in the window.
	if !d.seen("a", "1") {
		t.Fatal("'1' should still be in window")
	}
	// '4' is new and triggers eviction of the oldest entry ('1').
	if d.seen("a", "4") {
		t.Fatal("'4' is brand-new")
	}
	// '1' was the oldest; should have been evicted.
	if d.seen("a", "1") {
		t.Fatal("'1' should have been evicted by '4' insert")
	}
}
