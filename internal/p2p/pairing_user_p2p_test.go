//go:build p2p

package p2p

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"
)

// memUserLinker is an in-memory UserLinker for tests. Tracks every upsert
// + link so assertions can confirm the per-human identity rows landed.
type memUserLinker struct {
	mu    sync.Mutex
	users map[string]string   // userID -> displayName
	links map[string][]string // userID -> []peerID
}

func newMemUserLinker() *memUserLinker {
	return &memUserLinker{
		users: make(map[string]string),
		links: make(map[string][]string),
	}
}

func (m *memUserLinker) UpsertUser(_ context.Context, userID, displayName string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.users[userID] = displayName
	return nil
}

func (m *memUserLinker) LinkPeerToUser(_ context.Context, peerID, userID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range m.links[userID] {
		if p == peerID {
			return nil
		}
	}
	m.links[userID] = append(m.links[userID], peerID)
	return nil
}

func (m *memUserLinker) hasUser(userID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.users[userID]
	return ok
}

func (m *memUserLinker) peersFor(userID string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := append([]string(nil), m.links[userID]...)
	return out
}

// TestStartPairQRPayloadIncludesSelfIdentity confirms the QR payload now
// surfaces user_id + display_name when the responder has been bootstrapped.
func TestStartPairQRPayloadIncludesSelfIdentity(t *testing.T) {
	t.Parallel()
	host := startTestHost(t, "qr-id")
	defer func() { _ = host.Close() }()

	svc := NewPairingService(host, newMemPairingStore())
	svc.SetSelfIdentity("u-user", "User")

	res, err := svc.StartPair(context.Background())
	if err != nil {
		t.Fatalf("StartPair: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(res.QRPayload), &raw); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if raw["user_id"] != "u-user" {
		t.Fatalf("user_id = %v, want u-user", raw["user_id"])
	}
	if raw["display_name"] != "User" {
		t.Fatalf("display_name = %v, want User", raw["display_name"])
	}
}

// TestPairHandshakeLinksRemoteUser is the M7.1 acceptance test: B (the
// initiator) sends its user_id over the stream → A's responder side
// upserts the user row + links B's peer to it.
func TestPairHandshakeLinksRemoteUser(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	a := startTestHost(t, "a-user")
	defer func() { _ = a.Close() }()
	b := startTestHost(t, "b-user")
	defer func() { _ = b.Close() }()

	aLinker := newMemUserLinker()
	aSvc := NewPairingService(a, newMemPairingStore())
	aSvc.SetPeerPersister(newMemPeerPersister())
	aSvc.SetUserLinker(aLinker)
	aSvc.SetSelfIdentity("u-alice", "Alice")

	bSvc := NewPairingService(b, newMemPairingStore())
	bSvc.SetSelfIdentity("u-bob", "Bob")

	res, err := aSvc.StartPair(ctx)
	if err != nil {
		t.Fatalf("a.StartPair: %v", err)
	}
	if err := bSvc.CompletePair(ctx, res.Code, a.PeerID(), a.Addrs()); err != nil {
		t.Fatalf("b.CompletePair: %v", err)
	}

	// A's responder should have upserted Bob's user row + linked B's peer.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if aLinker.hasUser("u-bob") && len(aLinker.peersFor("u-bob")) == 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !aLinker.hasUser("u-bob") {
		t.Fatalf("A's user store missing u-bob after handshake")
	}
	peers := aLinker.peersFor("u-bob")
	if len(peers) != 1 || peers[0] != b.PeerID() {
		t.Fatalf("u-bob peers = %v, want [%s]", peers, b.PeerID())
	}
}

// TestPairHandshakeLegacyInitiatorSynthesizesUser covers the backward-
// compat path: when B (initiator) doesn't send a user_id (older binary
// or not yet bootstrapped), A's responder must still produce a user row
// using SyntheticUserIDForPeer so peers without identity get a stable
// home in the new schema.
func TestPairHandshakeLegacyInitiatorSynthesizesUser(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	a := startTestHost(t, "a-legacy")
	defer func() { _ = a.Close() }()
	b := startTestHost(t, "b-legacy")
	defer func() { _ = b.Close() }()

	aLinker := newMemUserLinker()
	aSvc := NewPairingService(a, newMemPairingStore())
	aSvc.SetPeerPersister(newMemPeerPersister())
	aSvc.SetUserLinker(aLinker)
	aSvc.SetSelfIdentity("u-alice", "Alice")

	// B has NO self identity — emulates an older binary.
	bSvc := NewPairingService(b, newMemPairingStore())

	res, err := aSvc.StartPair(ctx)
	if err != nil {
		t.Fatalf("a.StartPair: %v", err)
	}
	if err := bSvc.CompletePair(ctx, res.Code, a.PeerID(), a.Addrs()); err != nil {
		t.Fatalf("b.CompletePair: %v", err)
	}

	// A should still have produced a user row, keyed by the synthetic ID.
	expected := SyntheticUserIDForPeer(b.PeerID())
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if aLinker.hasUser(expected) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !aLinker.hasUser(expected) {
		t.Fatalf("A's user store missing synthetic user %q after legacy handshake", expected)
	}
	peers := aLinker.peersFor(expected)
	if len(peers) != 1 || peers[0] != b.PeerID() {
		t.Fatalf("synthetic-user peers = %v, want [%s]", peers, b.PeerID())
	}
}
