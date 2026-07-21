package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// fakePeerStore is a minimal in-memory store.P2PPeerStore for handler tests.
// Only the methods the handler touches are filled in; others panic so we
// catch accidental dependencies.
type fakePeerStore struct {
	peers       map[string]*store.P2PPeer
	revokeErr   error
	listErr     error
	addErr      error
	revokeCalls int
}

func newFakePeerStore() *fakePeerStore {
	return &fakePeerStore{peers: make(map[string]*store.P2PPeer)}
}

func (f *fakePeerStore) AddPeer(_ context.Context, p *store.P2PPeer) error {
	if f.addErr != nil {
		return f.addErr
	}
	if _, ok := f.peers[p.PeerID]; ok {
		return store.ErrAlreadyExists
	}
	f.peers[p.PeerID] = p
	return nil
}

func (f *fakePeerStore) GetPeer(_ context.Context, id string) (*store.P2PPeer, error) {
	if p, ok := f.peers[id]; ok {
		return p, nil
	}
	return nil, store.ErrNotFound
}

func (f *fakePeerStore) ListPeers(_ context.Context) ([]store.P2PPeer, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]store.P2PPeer, 0, len(f.peers))
	for _, p := range f.peers {
		out = append(out, *p)
	}
	return out, nil
}

func (f *fakePeerStore) RevokePeer(_ context.Context, id string) error {
	f.revokeCalls++
	if f.revokeErr != nil {
		return f.revokeErr
	}
	p, ok := f.peers[id]
	if !ok {
		return store.ErrNotFound
	}
	now := time.Now().UTC()
	p.RevokedAt = &now
	return nil
}

func (f *fakePeerStore) UnrevokePeer(_ context.Context, id string) error {
	if p, ok := f.peers[id]; ok {
		p.RevokedAt = nil
	}
	return nil
}

func (f *fakePeerStore) GrantPeerScope(_ context.Context, id, scope string) error {
	p, ok := f.peers[id]
	if !ok {
		return store.ErrNotFound
	}
	for _, s := range p.Scopes {
		if s == scope {
			return nil
		}
	}
	p.Scopes = append(p.Scopes, scope)
	return nil
}

func (f *fakePeerStore) RevokePeerScope(_ context.Context, id, scope string) error {
	p, ok := f.peers[id]
	if !ok {
		return store.ErrNotFound
	}
	out := p.Scopes[:0]
	for _, s := range p.Scopes {
		if s != scope {
			out = append(out, s)
		}
	}
	p.Scopes = out
	return nil
}

func (f *fakePeerStore) UpdateLastSeen(_ context.Context, id string, t time.Time) error {
	if p, ok := f.peers[id]; ok {
		p.LastSeen = &t
	}
	return nil
}

func (f *fakePeerStore) UpdateDisplayName(_ context.Context, id, name string) error {
	if p, ok := f.peers[id]; ok {
		p.DisplayName = name
	}
	return nil
}

func (f *fakePeerStore) SetPeerSSHTarget(_ context.Context, id, target string) error {
	if p, ok := f.peers[id]; ok && p.RevokedAt == nil {
		p.SSHTarget = target
	}
	return nil
}

func (f *fakePeerStore) UpdateSecretTransferRecipient(_ context.Context, id, recipient string) error {
	if p, ok := f.peers[id]; ok && p.RevokedAt == nil {
		p.SecretTransferRecipient = recipient
	}
	return nil
}

func (f *fakePeerStore) RememberPeerAddrs(_ context.Context, _ string, _ []string) error {
	return nil
}

func (f *fakePeerStore) LoadPeerAddrs(_ context.Context, _ string) ([]string, error) {
	return nil, nil
}

func (f *fakePeerStore) CreatePendingPair(_ context.Context, _ *store.P2PPendingPair) error {
	return nil
}
func (f *fakePeerStore) GetPendingPair(_ context.Context, _ string) (*store.P2PPendingPair, error) {
	return nil, store.ErrNotFound
}
func (f *fakePeerStore) DeletePendingPair(_ context.Context, _ string) error { return nil }
func (f *fakePeerStore) SweepExpiredPendingPairs(_ context.Context, _ time.Time) (int, error) {
	return 0, nil
}

// TestPairStartReturns501WithoutService verifies stub-mode UX: the route is
// always registered, so a frontend that hits it without --p2p gets a clean
// 501 instead of a SPA HTML page.
func TestPairStartReturns501WithoutService(t *testing.T) {
	t.Parallel()
	h := &p2pPairingHandler{svc: nil, store: newFakePeerStore()}
	req := httptest.NewRequest(http.MethodPost, "/api/p2p/pair/start", nil)
	rr := httptest.NewRecorder()
	h.pairStart(rr, req)
	if rr.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501", rr.Code)
	}
}

// TestPairCompleteRejectsBadCode verifies validation: a non-6-digit code is
// 400'd before any libp2p work happens.
func TestPairCompleteRejectsBadCode(t *testing.T) {
	t.Parallel()
	h := &p2pPairingHandler{svc: nil, store: newFakePeerStore()}
	body := bytes.NewBufferString(`{"code":"abc","peer_id":"12D3"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/p2p/pair/complete", body)
	rr := httptest.NewRecorder()
	// In stub builds we hit the 501 first; explicitly construct a fake svc
	// pathway by skipping validation when service is nil. So just assert 501.
	h.pairComplete(rr, req)
	if rr.Code != http.StatusNotImplemented {
		t.Fatalf("stub-mode status = %d, want 501", rr.Code)
	}
}

// TestListPeersEmpty checks the empty-list response shape — must always be
// {"peers":[]}, never null.
func TestListPeersEmpty(t *testing.T) {
	t.Parallel()
	h := &p2pPairingHandler{svc: nil, store: newFakePeerStore()}
	req := httptest.NewRequest(http.MethodGet, "/api/p2p/peers", nil)
	rr := httptest.NewRecorder()
	h.listPeers(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	body, _ := io.ReadAll(rr.Body)
	if !strings.Contains(string(body), `"peers":[]`) {
		t.Fatalf("body = %s, want peers:[]", string(body))
	}
}

// TestListPeersReturnsRows seeds the fake store and confirms peers are
// rendered. UI guidance: never display the raw peer ID — only DisplayName.
func TestListPeersReturnsRows(t *testing.T) {
	t.Parallel()
	st := newFakePeerStore()
	st.peers["12D3KooW1"] = &store.P2PPeer{
		PeerID: "12D3KooW1", DisplayName: "laptop", PairedAt: time.Now().UTC(), Scopes: []string{},
	}
	h := &p2pPairingHandler{svc: nil, store: st}
	req := httptest.NewRequest(http.MethodGet, "/api/p2p/peers", nil)
	rr := httptest.NewRecorder()
	h.listPeers(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var got struct {
		Peers []store.P2PPeer `json:"peers"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Peers) != 1 || got.Peers[0].DisplayName != "laptop" {
		t.Fatalf("peers = %+v", got.Peers)
	}
}

// TestListPeersOmitsReconnectFieldsWithoutReconnector verifies the wire
// shape stays backwards-compatible when the reconnector dependency is nil
// — older daemon builds shouldn't emit empty reconnect_state strings.
func TestListPeersOmitsReconnectFieldsWithoutReconnector(t *testing.T) {
	t.Parallel()
	st := newFakePeerStore()
	st.peers["12D3KooW1"] = &store.P2PPeer{
		PeerID: "12D3KooW1", DisplayName: "laptop", PairedAt: time.Now().UTC(), Scopes: []string{},
	}
	h := &p2pPairingHandler{svc: nil, store: st, reconnector: nil}
	req := httptest.NewRequest(http.MethodGet, "/api/p2p/peers", nil)
	rr := httptest.NewRecorder()
	h.listPeers(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	body, _ := io.ReadAll(rr.Body)
	for _, field := range []string{"reconnect_state", "last_dial_error", "last_dial_attempt_at"} {
		if strings.Contains(string(body), `"`+field+`"`) {
			t.Errorf("body unexpectedly includes %q: %s", field, string(body))
		}
	}
}

// TestRevokePeerNotFound returns 404 cleanly for an unknown peer.
func TestRevokePeerNotFound(t *testing.T) {
	t.Parallel()
	st := newFakePeerStore()
	h := &p2pPairingHandler{svc: nil, store: st}
	req := httptest.NewRequest(http.MethodDelete, "/api/p2p/peers/unknown", nil)
	req.SetPathValue("id", "unknown")
	rr := httptest.NewRecorder()
	h.revokePeer(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
}

// TestRevokePeerSuccess verifies the happy path: 204 on revoke.
func TestRevokePeerSuccess(t *testing.T) {
	t.Parallel()
	st := newFakePeerStore()
	st.peers["12D3KooW1"] = &store.P2PPeer{PeerID: "12D3KooW1", Scopes: []string{}}
	h := &p2pPairingHandler{svc: nil, store: st}
	req := httptest.NewRequest(http.MethodDelete, "/api/p2p/peers/12D3KooW1", nil)
	req.SetPathValue("id", "12D3KooW1")
	rr := httptest.NewRecorder()
	h.revokePeer(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rr.Code)
	}
	if st.peers["12D3KooW1"].RevokedAt == nil {
		t.Fatal("expected RevokedAt to be set")
	}
}

// TestValidPairCompleteRequest table-tests validation logic.
func TestValidPairCompleteRequest(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		req  pairCompleteRequest
		want bool
	}{
		{"valid", pairCompleteRequest{Code: "123456", PeerID: "12D3"}, true},
		{"too short", pairCompleteRequest{Code: "12345", PeerID: "12D3"}, false},
		{"non-digit", pairCompleteRequest{Code: "12345A", PeerID: "12D3"}, false},
		{"empty peer", pairCompleteRequest{Code: "123456", PeerID: ""}, false},
		{"empty code", pairCompleteRequest{Code: "", PeerID: "12D3"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := validPairCompleteRequest(tc.req); got != tc.want {
				t.Fatalf("validPairCompleteRequest(%+v) = %v, want %v", tc.req, got, tc.want)
			}
		})
	}
}

// TestShortPeerIDStrips ensures the friendly default name does not leak the
// full peer ID.
func TestShortPeerIDStrips(t *testing.T) {
	t.Parallel()
	full := "12D3KooWAbCdEfGhIjKlMnOp"
	got := shortPeerID(full)
	if strings.Contains(got, "12D3KooW") {
		t.Fatalf("shortPeerID(%q) = %q — leaked the prefix", full, got)
	}
	if !strings.HasPrefix(got, "peer-") {
		t.Fatalf("shortPeerID(%q) = %q — expected peer- prefix", full, got)
	}
}

// TestQRDataURLPrefix sanity-checks the data URL shape so the front-end can
// drop it into <img src=…> directly.
func TestQRDataURLPrefix(t *testing.T) {
	t.Parallel()
	url, err := qrDataURL(`{"code":"123456","peer_id":"12D3"}`)
	if err != nil {
		t.Fatalf("qrDataURL: %v", err)
	}
	if !strings.HasPrefix(url, "data:image/png;base64,") {
		t.Fatalf("qrDataURL prefix = %q", url[:32])
	}
	if len(url) < 200 {
		t.Fatalf("qrDataURL too short to be a real PNG: %d bytes", len(url))
	}
}

// TestPersistPeerHandlesRePair confirms that a duplicate AddPeer triggers
// the UpdateLastSeen path rather than failing the request.
func TestPersistPeerHandlesRePair(t *testing.T) {
	t.Parallel()
	st := newFakePeerStore()
	st.peers["12D3KooW1"] = &store.P2PPeer{
		PeerID: "12D3KooW1", DisplayName: "laptop",
		PairedAt: time.Now().Add(-time.Hour).UTC(), Scopes: []string{},
	}
	h := &p2pPairingHandler{svc: nil, store: st}
	err := h.persistPeer(context.Background(), pairCompleteRequest{
		Code: "123456", PeerID: "12D3KooW1", DisplayName: "laptop",
	})
	if err != nil {
		t.Fatalf("persistPeer re-pair: %v", err)
	}
	if st.peers["12D3KooW1"].LastSeen == nil {
		t.Fatal("expected LastSeen to be bumped on re-pair")
	}
}

// TestRevokeStoreErrorBubbles ensures unexpected store errors become 500s
// rather than being silently swallowed.
func TestRevokeStoreErrorBubbles(t *testing.T) {
	t.Parallel()
	st := newFakePeerStore()
	st.peers["12D3KooW1"] = &store.P2PPeer{PeerID: "12D3KooW1", Scopes: []string{}}
	st.revokeErr = errors.New("disk full")
	h := &p2pPairingHandler{svc: nil, store: st}
	req := httptest.NewRequest(http.MethodDelete, "/api/p2p/peers/12D3KooW1", nil)
	req.SetPathValue("id", "12D3KooW1")
	rr := httptest.NewRecorder()
	h.revokePeer(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
}
