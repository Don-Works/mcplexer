// scope_check_handler_test.go — covers the four typed denial codes
// surfaced by POST /api/p2p/peers/{id}/scopes/check (JTAC65).
package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/scopes"
	"github.com/don-works/mcplexer/internal/store"
)

// decodeDenial pulls the typed denial out of a 403 response body so
// each table-test below stays declarative.
func decodeDenial(t *testing.T, body io.Reader) denialResponse {
	t.Helper()
	var d denialResponse
	if err := json.NewDecoder(body).Decode(&d); err != nil {
		t.Fatalf("decode denial body: %v", err)
	}
	return d
}

// runScopeCheck builds the httptest plumbing once per case.
func runScopeCheck(t *testing.T, h *p2pPairingHandler, id, scope, reason string) *httptest.ResponseRecorder {
	t.Helper()
	bodyJSON, _ := json.Marshal(scopeCheckRequest{Scope: scope})
	url := "/api/p2p/peers/" + id + "/scopes/check"
	if reason != "" {
		url += "?reason=" + reason
	}
	req := httptest.NewRequest(http.MethodPost, url, bytes.NewReader(bodyJSON))
	req.SetPathValue("id", id)
	rr := httptest.NewRecorder()
	h.checkScope(rr, req)
	return rr
}

// TestCheckScope_NoScope — peer is active but never granted the scope.
// Expected: 403 with denial.code == "no_scope".
func TestCheckScope_NoScope(t *testing.T) {
	t.Parallel()
	st := newFakePeerStore()
	st.peers["peerA"] = &store.P2PPeer{
		PeerID: "peerA", Scopes: []string{}, // no grants
	}
	h := &p2pPairingHandler{store: st}

	rr := runScopeCheck(t, h, "peerA", "mesh.memory_request", "")

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rr.Code)
	}
	d := decodeDenial(t, rr.Body)
	if d.Error != "forbidden" {
		t.Errorf("error = %q, want %q", d.Error, "forbidden")
	}
	if d.Denial.Code != scopes.DenialNoScope {
		t.Errorf("denial.code = %q, want %q", d.Denial.Code, scopes.DenialNoScope)
	}
	if d.Denial.Scope != "mesh.memory_request" {
		t.Errorf("denial.scope = %q, want %q", d.Denial.Scope, "mesh.memory_request")
	}
	if d.Denial.Peer != "peerA" {
		t.Errorf("denial.peer = %q, want %q", d.Denial.Peer, "peerA")
	}
}

// TestCheckScope_ScopeRevoked — peer row has revoked_at set; any
// scope check should return scope_revoked rather than no_scope, even
// when the peer's Scopes slice still contains the grant (revocation
// is the higher-priority signal).
func TestCheckScope_ScopeRevoked(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	st := newFakePeerStore()
	st.peers["peerB"] = &store.P2PPeer{
		PeerID: "peerB",
		// Granted before revocation — proves the revoked check
		// fires first.
		Scopes:    []string{"mesh.memory_request"},
		RevokedAt: &now,
	}
	h := &p2pPairingHandler{store: st}

	rr := runScopeCheck(t, h, "peerB", "mesh.memory_request", "")

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rr.Code)
	}
	d := decodeDenial(t, rr.Body)
	if d.Denial.Code != scopes.DenialScopeRevoked {
		t.Errorf("denial.code = %q, want %q", d.Denial.Code, scopes.DenialScopeRevoked)
	}
}

// TestCheckScope_ScopeOutOfBand — scope string isn't in the
// peerscope.Known registry. The handler should report the literal
// rather than no_scope so the caller knows it's not a grant problem.
func TestCheckScope_ScopeOutOfBand(t *testing.T) {
	t.Parallel()
	st := newFakePeerStore()
	st.peers["peerC"] = &store.P2PPeer{
		PeerID: "peerC", Scopes: []string{"mesh.memory_request"},
	}
	h := &p2pPairingHandler{store: st}

	// Made-up scope that has no peerscope.ScopeDef entry.
	rr := runScopeCheck(t, h, "peerC", "definitely.not.a.scope", "")

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rr.Code)
	}
	d := decodeDenial(t, rr.Body)
	if d.Denial.Code != scopes.DenialScopeOutOfBand {
		t.Errorf("denial.code = %q, want %q", d.Denial.Code, scopes.DenialScopeOutOfBand)
	}
	if d.Denial.Scope != "definitely.not.a.scope" {
		t.Errorf("denial.scope = %q, want %q", d.Denial.Scope, "definitely.not.a.scope")
	}
}

// TestCheckScope_CrossOrgBoundary — Tier-3 placeholder. Until the
// org-pair binding model lands, callers can force-surface the code
// via ?reason=cross_org_boundary to validate clients now. Once Tier-3
// lands the natural store-derived path will emit the same shape.
func TestCheckScope_CrossOrgBoundary(t *testing.T) {
	t.Parallel()
	st := newFakePeerStore()
	st.peers["peerD"] = &store.P2PPeer{
		PeerID: "peerD", Scopes: []string{"mesh.memory_request"},
	}
	h := &p2pPairingHandler{store: st}

	rr := runScopeCheck(t, h, "peerD", "mesh.memory_request", "cross_org_boundary")

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rr.Code)
	}
	d := decodeDenial(t, rr.Body)
	if d.Denial.Code != scopes.DenialCrossOrgBoundary {
		t.Errorf("denial.code = %q, want %q", d.Denial.Code, scopes.DenialCrossOrgBoundary)
	}
	if d.Denial.Peer != "peerD" {
		t.Errorf("denial.peer = %q, want %q", d.Denial.Peer, "peerD")
	}
}

// TestCheckScope_AllowedHappyPath — peer holds the scope; 200 with
// {allowed:true}, no denial body. Proves the helper doesn't false-
// positive when the grant is genuinely present.
func TestCheckScope_AllowedHappyPath(t *testing.T) {
	t.Parallel()
	st := newFakePeerStore()
	st.peers["peerE"] = &store.P2PPeer{
		PeerID: "peerE", Scopes: []string{"mesh.memory_request"},
	}
	h := &p2pPairingHandler{store: st}

	rr := runScopeCheck(t, h, "peerE", "mesh.memory_request", "")

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var resp map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["allowed"] != true {
		t.Errorf("allowed = %v, want true", resp["allowed"])
	}
}

// TestCheckScope_AllowedViaWildcard — a grant of "trigger_worker:*"
// should satisfy a check for "trigger_worker:audit-watcher". Mirrors
// HasPeerScope's wildcard semantics in the in-memory evaluation path.
func TestCheckScope_AllowedViaWildcard(t *testing.T) {
	t.Parallel()
	st := newFakePeerStore()
	st.peers["peerF"] = &store.P2PPeer{
		PeerID: "peerF", Scopes: []string{"trigger_worker:*"},
	}
	h := &p2pPairingHandler{store: st}

	rr := runScopeCheck(t, h, "peerF", "trigger_worker:audit-watcher", "")

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
}

// TestCheckScope_PeerNotFound — unknown peer ID returns 404, NOT a
// 403 with a denial. The denial vocabulary is reserved for scope
// failures on known peers; "this peer never existed" is a different
// failure mode entirely.
func TestCheckScope_PeerNotFound(t *testing.T) {
	t.Parallel()
	st := newFakePeerStore()
	h := &p2pPairingHandler{store: st}

	rr := runScopeCheck(t, h, "ghost", "mesh.memory_request", "")

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
}

// TestCheckScope_MissingScopeBody — caller didn't pass scope; 400.
// Sanity check that the validator runs before the store lookup.
func TestCheckScope_MissingScopeBody(t *testing.T) {
	t.Parallel()
	st := newFakePeerStore()
	st.peers["peerG"] = &store.P2PPeer{PeerID: "peerG", Scopes: []string{}}
	h := &p2pPairingHandler{store: st}

	req := httptest.NewRequest(http.MethodPost,
		"/api/p2p/peers/peerG/scopes/check", bytes.NewReader([]byte(`{}`)))
	req.SetPathValue("id", "peerG")
	rr := httptest.NewRecorder()
	h.checkScope(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

// TestCheckScope_UnknownForceReasonIgnored — bogus ?reason= falls
// through to the natural evaluation path rather than echoing the
// bogus code back. Defends against clients that try to spoof a
// specific deny code over the wire.
func TestCheckScope_UnknownForceReasonIgnored(t *testing.T) {
	t.Parallel()
	st := newFakePeerStore()
	st.peers["peerH"] = &store.P2PPeer{
		PeerID: "peerH", Scopes: []string{},
	}
	h := &p2pPairingHandler{store: st}

	rr := runScopeCheck(t, h, "peerH", "mesh.memory_request", "totally_made_up")

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rr.Code)
	}
	d := decodeDenial(t, rr.Body)
	// Falls through to the no_scope path — bogus reason ignored.
	if d.Denial.Code != scopes.DenialNoScope {
		t.Errorf("denial.code = %q, want %q (bogus reason should be ignored)",
			d.Denial.Code, scopes.DenialNoScope)
	}
}
