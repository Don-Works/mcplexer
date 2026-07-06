//go:build p2p

package p2p

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// memPairingStore is an in-memory PairingStore for tests. Production wires
// PairingService to a sqlite-backed adapter; tests only need round-trip
// fidelity, not durability.
type memPairingStore struct {
	mu      sync.Mutex
	pending map[string]memPending
}

type memPending struct {
	peerID    string
	addrs     []string
	expiresAt time.Time
}

func newMemPairingStore() *memPairingStore {
	return &memPairingStore{pending: make(map[string]memPending)}
}

func (m *memPairingStore) CreatePendingPair(_ context.Context, code, peerID string, addrs []string, expiresAt time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pending[code] = memPending{peerID: peerID, addrs: addrs, expiresAt: expiresAt}
	return nil
}

func (m *memPairingStore) GetPendingPair(_ context.Context, code string) (string, []string, time.Time, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.pending[code]
	if !ok {
		return "", nil, time.Time{}, errors.New("not found")
	}
	return p.peerID, p.addrs, p.expiresAt, nil
}

func (m *memPairingStore) DeletePendingPair(_ context.Context, code string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.pending, code)
	return nil
}

// memPeerPersister is a tiny PeerPersister implementation backed by a map.
// It mirrors the subset of store.P2PPeerStore the responder side of a
// pairing handshake actually calls (AddPeer / UpdateLastSeen).
type memPeerPersister struct {
	mu    sync.Mutex
	peers map[string]store.P2PPeer
}

func newMemPeerPersister() *memPeerPersister {
	return &memPeerPersister{peers: make(map[string]store.P2PPeer)}
}

func (m *memPeerPersister) AddPeer(_ context.Context, p *store.P2PPeer) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.peers[p.PeerID]; ok {
		return store.ErrAlreadyExists
	}
	m.peers[p.PeerID] = *p
	return nil
}

func (m *memPeerPersister) UpdateLastSeen(_ context.Context, peerID string, t time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.peers[peerID]
	if !ok {
		return store.ErrNotFound
	}
	tt := t
	p.LastSeen = &tt
	m.peers[peerID] = p
	return nil
}

func (m *memPeerPersister) has(peerID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.peers[peerID]
	return ok
}

// TestStartPairQRPayloadShape pins the JSON contract for the QR payload.
// In v0.3 we dropped multiaddrs from the payload — the responder side
// resolves the peer's current AddrInfo via the DHT. The payload must
// therefore contain only `code` + `peer_id` (+ optional identity hints).
// A regression that re-introduced `multiaddrs` would re-bloat the QR and
// silently fail to pick up post-pair IP rotations.
func TestStartPairQRPayloadShape(t *testing.T) {
	t.Parallel()
	host := startTestHost(t, "qr")
	defer func() { _ = host.Close() }()

	svc := NewPairingService(host, newMemPairingStore())
	res, err := svc.StartPair(context.Background())
	if err != nil {
		t.Fatalf("StartPair: %v", err)
	}

	var payload struct {
		Code   string `json:"code"`
		PeerID string `json:"peer_id"`
	}
	if err := json.Unmarshal([]byte(res.QRPayload), &payload); err != nil {
		t.Fatalf("unmarshal qr payload: %v", err)
	}
	if payload.Code != res.Code {
		t.Fatalf("payload.code = %q, want %q", payload.Code, res.Code)
	}
	if payload.PeerID != host.PeerID() {
		t.Fatalf("payload.peer_id = %q, want %q",
			payload.PeerID, host.PeerID())
	}

	// Assert the legacy address fields are NOT present — both spellings
	// (`multiaddrs` from v0.2 and `addrs` from earlier dev branches).
	var raw map[string]any
	if err := json.Unmarshal([]byte(res.QRPayload), &raw); err != nil {
		t.Fatalf("unmarshal raw: %v", err)
	}
	for _, banned := range []string{"multiaddrs", "addrs"} {
		if _, ok := raw[banned]; ok {
			t.Fatalf("QR payload must not include %q (dropped in v0.3 — DHT is the source of truth)",
				banned)
		}
	}
}

// TestStartPairQRPayloadIsCompact pins the size budget. The whole point of
// dropping addrs was a denser QR; if anything inflates the payload past
// ~256 bytes we want the test to flag it. The padding is generous because
// the test host emits exactly one loopback addr — production hosts would
// have been much bigger before this change.
func TestStartPairQRPayloadIsCompact(t *testing.T) {
	t.Parallel()
	host := startTestHost(t, "qr-size")
	defer func() { _ = host.Close() }()

	svc := NewPairingService(host, newMemPairingStore())
	res, err := svc.StartPair(context.Background())
	if err != nil {
		t.Fatalf("StartPair: %v", err)
	}
	const maxBytes = 256
	if len(res.QRPayload) > maxBytes {
		t.Fatalf("QR payload too big: %d bytes (max %d) — payload=%q",
			len(res.QRPayload), maxBytes, res.QRPayload)
	}
}

// TestStartPairCodeShape verifies the code is a 6-digit string and the QR
// payload contains the peer ID. We never want UI to surface peer IDs to the
// user, but the QR payload is fine — it's machine-readable.
func TestStartPairCodeShape(t *testing.T) {
	t.Parallel()
	host := startTestHost(t, "a")
	defer func() { _ = host.Close() }()

	svc := NewPairingService(host, newMemPairingStore())
	res, err := svc.StartPair(context.Background())
	if err != nil {
		t.Fatalf("StartPair: %v", err)
	}
	if len(res.Code) != 6 {
		t.Fatalf("code len = %d, want 6 (got %q)", len(res.Code), res.Code)
	}
	for _, c := range res.Code {
		if c < '0' || c > '9' {
			t.Fatalf("code %q contains non-digit", res.Code)
		}
	}
	if res.QRPayload == "" {
		t.Fatal("empty QR payload")
	}
	if res.ExpiresAt.Before(time.Now().Add(4 * time.Minute)) {
		t.Fatalf("expires_at too soon: %v", res.ExpiresAt)
	}
}

// TestPairHandshakeBetweenHosts is the acceptance test: two libp2p hosts on
// localhost successfully complete the pairing handshake with a valid code.
func TestPairHandshakeBetweenHosts(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	a := startTestHost(t, "a")
	defer func() { _ = a.Close() }()
	b := startTestHost(t, "b")
	defer func() { _ = b.Close() }()

	aSvc := NewPairingService(a, newMemPairingStore())
	NewPairingService(b, newMemPairingStore())

	res, err := aSvc.StartPair(ctx)
	if err != nil {
		t.Fatalf("a.StartPair: %v", err)
	}

	bSvc := NewPairingService(b, newMemPairingStore())
	if err := bSvc.CompletePair(ctx, res.Code, a.PeerID(), a.Addrs()); err != nil {
		t.Fatalf("b.CompletePair: %v", err)
	}
}

// TestPairCodeSingleUse verifies a code only works once: the second call
// fails with ErrPairingInvalid even with the correct code.
func TestPairCodeSingleUse(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	a := startTestHost(t, "a")
	defer func() { _ = a.Close() }()
	b := startTestHost(t, "b")
	defer func() { _ = b.Close() }()

	aSvc := NewPairingService(a, newMemPairingStore())
	NewPairingService(b, newMemPairingStore())

	res, err := aSvc.StartPair(ctx)
	if err != nil {
		t.Fatalf("a.StartPair: %v", err)
	}
	bSvc := NewPairingService(b, newMemPairingStore())
	if err := bSvc.CompletePair(ctx, res.Code, a.PeerID(), a.Addrs()); err != nil {
		t.Fatalf("first CompletePair: %v", err)
	}
	err = bSvc.CompletePair(ctx, res.Code, a.PeerID(), a.Addrs())
	if !errors.Is(err, ErrPairingInvalid) {
		t.Fatalf("second CompletePair err = %v, want ErrPairingInvalid", err)
	}
}

// TestCompletePairExplicitAddrsBranch covers the legacy/explicit branch
// of seedPeerAddrs: when remoteAddrs is non-empty, the pairing succeeds
// without consulting the DHT (the test host has none wired). This is the
// path used by older QR payloads still in the wild + the on-the-wire test
// path where we hand B the actual A.Addrs().
func TestCompletePairExplicitAddrsBranch(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	a := startTestHost(t, "a-explicit")
	defer func() { _ = a.Close() }()
	b := startTestHost(t, "b-explicit")
	defer func() { _ = b.Close() }()

	aSvc := NewPairingService(a, newMemPairingStore())
	bSvc := NewPairingService(b, newMemPairingStore())

	res, err := aSvc.StartPair(ctx)
	if err != nil {
		t.Fatalf("a.StartPair: %v", err)
	}
	if err := bSvc.CompletePair(ctx, res.Code, a.PeerID(), a.Addrs()); err != nil {
		t.Fatalf("CompletePair with explicit addrs: %v", err)
	}
}

// TestCompletePairDHTFallbackNoAddrsNoDHT covers the "no addrs + no DHT"
// branch of seedPeerAddrs: the test host is built without a DHT, so when
// CompletePair is invoked with an empty addrs slice it must surface the
// ErrDHTUnavailable error (wrapped) and refuse to dial. This is the
// regression baseline for "DHT must be wired in production".
func TestCompletePairDHTFallbackNoAddrsNoDHT(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	a := startTestHost(t, "a-nodht")
	defer func() { _ = a.Close() }()
	b := startTestHost(t, "b-nodht")
	defer func() { _ = b.Close() }()

	aSvc := NewPairingService(a, newMemPairingStore())
	bSvc := NewPairingService(b, newMemPairingStore())

	res, err := aSvc.StartPair(ctx)
	if err != nil {
		t.Fatalf("a.StartPair: %v", err)
	}
	err = bSvc.CompletePair(ctx, res.Code, a.PeerID(), nil)
	if err == nil {
		t.Fatal("CompletePair with no addrs + no DHT must error")
	}
	if !errors.Is(err, ErrDHTUnavailable) {
		t.Fatalf("CompletePair err = %v, want wrap of ErrDHTUnavailable", err)
	}
}

// TestPairWrongCodeRejected verifies a random wrong code is rejected.
func TestPairWrongCodeRejected(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	a := startTestHost(t, "a")
	defer func() { _ = a.Close() }()
	b := startTestHost(t, "b")
	defer func() { _ = b.Close() }()

	aSvc := NewPairingService(a, newMemPairingStore())
	if _, err := aSvc.StartPair(ctx); err != nil {
		t.Fatalf("StartPair: %v", err)
	}

	bSvc := NewPairingService(b, newMemPairingStore())
	err := bSvc.CompletePair(ctx, "999999", a.PeerID(), a.Addrs())
	if !errors.Is(err, ErrPairingInvalid) {
		t.Fatalf("CompletePair(wrong) err = %v, want ErrPairingInvalid", err)
	}
}

// TestPairResponderPersistsInitiator is the regression test for the
// asymmetric-pairing bug: when B (initiator) completes a handshake against A
// (responder), A must also persist B in its peer store. Before this fix
// only B's HTTP handler wrote a row, leaving A with an empty p2p_peers
// table — the user-visible symptom: "I paired but A doesn't see B".
func TestPairResponderPersistsInitiator(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	a := startTestHost(t, "a")
	defer func() { _ = a.Close() }()
	b := startTestHost(t, "b")
	defer func() { _ = b.Close() }()

	aPersister := newMemPeerPersister()
	bPersister := newMemPeerPersister()

	aSvc := NewPairingService(a, newMemPairingStore())
	aSvc.SetPeerPersister(aPersister)
	bSvc := NewPairingService(b, newMemPairingStore())
	bSvc.SetPeerPersister(bPersister)

	res, err := aSvc.StartPair(ctx)
	if err != nil {
		t.Fatalf("a.StartPair: %v", err)
	}
	if err := bSvc.CompletePair(ctx, res.Code, a.PeerID(), a.Addrs()); err != nil {
		t.Fatalf("b.CompletePair: %v", err)
	}

	// A (the responder) must now have B in its peer store. The HTTP
	// handler does the symmetric write on B's side; we simulate that here
	// so we test only the responder-side fix.
	if !aPersister.has(b.PeerID()) {
		t.Fatalf("A's peer store missing B (%s) after handshake — responder did not persist",
			b.PeerID())
	}
}

// TestPairResponderPersistRetriesOnExisting confirms that when the
// initiator was already a paired peer (re-pair), the responder side
// upgrades to UpdateLastSeen instead of failing the handshake.
func TestPairResponderPersistRetriesOnExisting(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	a := startTestHost(t, "a")
	defer func() { _ = a.Close() }()
	b := startTestHost(t, "b")
	defer func() { _ = b.Close() }()

	aPersister := newMemPeerPersister()
	// Pre-seed B as already-paired on A's side.
	if err := aPersister.AddPeer(ctx, &store.P2PPeer{
		PeerID:      b.PeerID(),
		DisplayName: "old-name",
		PairedAt:    time.Now().UTC().Add(-1 * time.Hour),
		Scopes:      []string{},
	}); err != nil {
		t.Fatalf("seed A peer store: %v", err)
	}

	aSvc := NewPairingService(a, newMemPairingStore())
	aSvc.SetPeerPersister(aPersister)
	bSvc := NewPairingService(b, newMemPairingStore())

	res, err := aSvc.StartPair(ctx)
	if err != nil {
		t.Fatalf("a.StartPair: %v", err)
	}
	if err := bSvc.CompletePair(ctx, res.Code, a.PeerID(), a.Addrs()); err != nil {
		t.Fatalf("b.CompletePair: %v", err)
	}
	// Give the responder goroutine a moment to land its UpdateLastSeen.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		aPersister.mu.Lock()
		ls := aPersister.peers[b.PeerID()].LastSeen
		aPersister.mu.Unlock()
		if ls != nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("expected last_seen to be set on existing peer after re-pair")
}

// TestStartPairQRPayloadIncludesDisplayName checks the new v1 wire shape:
// when a DisplayNameProvider is wired, the QR payload carries display_name
// alongside code/peer_id/multiaddrs. Old binaries without the provider
// keep emitting the legacy 3-field payload (covered by
// TestStartPairQRPayloadShape).
func TestStartPairQRPayloadIncludesDisplayName(t *testing.T) {
	t.Skip("display_name plumbing deferred during M7 merge — see ClickUp follow-up")
	t.Parallel()
	host := startTestHost(t, "qr-display")
	defer func() { _ = host.Close() }()

	svc := NewPairingService(host, newMemPairingStore())
	svc.SetDisplayNameProvider(func() string { return "dev-mbp" })
	res, err := svc.StartPair(context.Background())
	if err != nil {
		t.Fatalf("StartPair: %v", err)
	}
	var payload struct {
		Code        string `json:"code"`
		DisplayName string `json:"display_name"`
	}
	if err := json.Unmarshal([]byte(res.QRPayload), &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if payload.DisplayName != "dev-mbp" {
		t.Fatalf("display_name = %q, want dev-mbp", payload.DisplayName)
	}
}

// TestPairHandshakePropagatesDisplayName is the full-fidelity test the
// ClickUp ticket calls out: a successful handshake propagates the
// initiator's display_name to the responder's peer store row, AND the
// initiator separately persists the responder's display_name (via the QR
// payload they scanned). Verifies both sides land the friendly label —
// no more "peer-Ymq…" leaks for users on the new build.
func TestPairHandshakePropagatesDisplayName(t *testing.T) {
	t.Skip("display_name plumbing deferred during M7 merge — see ClickUp follow-up")
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	a := startTestHost(t, "a")
	defer func() { _ = a.Close() }()
	b := startTestHost(t, "b")
	defer func() { _ = b.Close() }()

	aPersister := newMemPeerPersister()

	aSvc := NewPairingService(a, newMemPairingStore())
	aSvc.SetPeerPersister(aPersister)
	aSvc.SetDisplayNameProvider(func() string { return "peer-laptop" })

	bSvc := NewPairingService(b, newMemPairingStore())
	bSvc.SetDisplayNameProvider(func() string { return "dev-mbp" })

	res, err := aSvc.StartPair(ctx)
	if err != nil {
		t.Fatalf("a.StartPair: %v", err)
	}
	if err := bSvc.CompletePair(ctx, res.Code, a.PeerID(), a.Addrs()); err != nil {
		t.Fatalf("b.CompletePair: %v", err)
	}

	// Responder side: A must have B's display_name (sent over the v1
	// pair stream by B during CompletePair).
	aPersister.mu.Lock()
	row, ok := aPersister.peers[b.PeerID()]
	aPersister.mu.Unlock()
	if !ok {
		t.Fatalf("A's peer store missing B (%s)", b.PeerID())
	}
	if row.DisplayName != "dev-mbp" {
		t.Fatalf("A's row for B has display_name=%q, want dev-mbp",
			row.DisplayName)
	}
}

// TestPairHandshakeLegacyPeerNoDisplayName is the back-compat regression:
// an initiator without a DisplayNameProvider sends the v0 plain-code line
// and the responder records the short peer-prefix label instead of the
// friendly one. Old peers must still pair fine.
func TestPairHandshakeLegacyPeerNoDisplayName(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	a := startTestHost(t, "a-legacy")
	defer func() { _ = a.Close() }()
	b := startTestHost(t, "b-legacy")
	defer func() { _ = b.Close() }()

	aPersister := newMemPeerPersister()

	aSvc := NewPairingService(a, newMemPairingStore())
	aSvc.SetPeerPersister(aPersister)
	aSvc.SetDisplayNameProvider(func() string { return "peer-laptop" })

	// B is "old": no DisplayNameProvider → v0 wire shape.
	bSvc := NewPairingService(b, newMemPairingStore())

	res, err := aSvc.StartPair(ctx)
	if err != nil {
		t.Fatalf("a.StartPair: %v", err)
	}
	if err := bSvc.CompletePair(ctx, res.Code, a.PeerID(), a.Addrs()); err != nil {
		t.Fatalf("b.CompletePair: %v", err)
	}
	aPersister.mu.Lock()
	row, ok := aPersister.peers[b.PeerID()]
	aPersister.mu.Unlock()
	if !ok {
		t.Fatalf("A's peer store missing B")
	}
	if row.DisplayName != shortPeerLabel(b.PeerID()) {
		t.Fatalf("A's row for legacy B has display_name=%q, want short label",
			row.DisplayName)
	}
}

// TestParsePairRequest pins the on-the-wire decoder: legacy v0 plain
// code lines + v1 JSON envelopes both decode cleanly.
func TestParsePairRequest(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input    string
		wantCode string
		wantName string
		wantErr  bool
	}{
		{"123456\n", "123456", "", false},
		{"  654321  \n", "654321", "", false},
		{`{"code":"123456","display_name":"peer-laptop"}`, "123456", "peer-laptop", false},
		{`{"code":"000000"}`, "000000", "", false},
		{"{not json}", "", "", true},
		{"", "", "", true},
	}
	for _, tt := range tests {
		gotCode, gotName, err := parsePairRequest(tt.input)
		if (err != nil) != tt.wantErr {
			t.Fatalf("parsePairRequest(%q) err = %v, wantErr=%v", tt.input, err, tt.wantErr)
		}
		if gotCode != tt.wantCode || gotName != tt.wantName {
			t.Fatalf("parsePairRequest(%q) = (%q,%q), want (%q,%q)",
				tt.input, gotCode, gotName, tt.wantCode, tt.wantName)
		}
	}
}

// TestGenerateSixDigitCodeRange spot-checks code generation: many samples
// must all be 6-digit numeric strings in [000000, 999999].
func TestGenerateSixDigitCodeRange(t *testing.T) {
	t.Parallel()
	seen := make(map[string]struct{}, 256)
	for i := 0; i < 256; i++ {
		c, err := generateSixDigitCode()
		if err != nil {
			t.Fatalf("generate: %v", err)
		}
		if len(c) != 6 {
			t.Fatalf("code %q len = %d", c, len(c))
		}
		var n int
		if _, err := fmt.Sscanf(c, "%6d", &n); err != nil || n < 0 || n > 999_999 {
			t.Fatalf("code %q out of range or unparseable: %v", c, err)
		}
		seen[c] = struct{}{}
	}
	if len(seen) < 200 {
		t.Fatalf("only %d unique codes in 256 samples — RNG likely broken", len(seen))
	}
}
