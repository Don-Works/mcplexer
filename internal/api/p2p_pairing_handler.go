package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/don-works/mcplexer/internal/config"
	"github.com/don-works/mcplexer/internal/p2p"
	"github.com/don-works/mcplexer/internal/peerscope"
	"github.com/don-works/mcplexer/internal/scopes"
	"github.com/don-works/mcplexer/internal/store"
)

// p2pPairingHandler exposes pairing + paired-peer management over the REST
// API. The handler is wired into the router only when both a non-nil pairing
// service and a non-nil store are passed in RouterDeps. We always register
// the route in stub builds so callers see a 501 instead of an HTML page.
type p2pPairingHandler struct {
	svc         *p2p.PairingService
	store       store.P2PPeerStore
	users       store.UserStore  // optional: M7.1 link of remote peer → user
	reconnector *p2p.Reconnector // optional; enables reconnect_* fields on list
	host        *p2p.Host        // optional; used to stop redialing a revoked peer
}

// peerListRow embeds a paired-peer row with optional reconnector telemetry.
// The embedded P2PPeer keeps the existing wire fields stable (peer_id,
// display_name, paired_at, last_seen, …); the new fields ride alongside as
// omitempty so older clients see no diff.
type peerListRow struct {
	store.P2PPeer
	LastDialAttemptAt string `json:"last_dial_attempt_at,omitempty"`
	LastDialError     string `json:"last_dial_error,omitempty"`
	ReconnectState    string `json:"reconnect_state,omitempty"`
}

// pairStartResponse is the JSON shape of POST /api/p2p/pair/start.
//
// QRDataURL is a `data:image/png;base64,...` URL the front-end can drop into
// an <img> tag without computing the QR client-side. (We render the QR with
// a tiny pure-Go encoder vendored as part of the API package — see qr.go.)
type pairStartResponse struct {
	Code      string    `json:"code"`
	QRPayload string    `json:"qr_payload"`
	QRDataURL string    `json:"qr_data_url"`
	ExpiresAt time.Time `json:"expires_at"`
}

// pairCompleteRequest takes the QR/URL-encoded payload from the other
// device + the typed 6-digit code. The other device's peer ID is the only
// required address-routing field; multiaddrs are no longer baked into the
// payload — the daemon resolves the peer's current AddrInfo via the DHT
// when CompletePair runs.
//
// M7.1 fields (UserID, RemoteDisplayName) are optional: a peer paired from
// an older binary won't include them, in which case the initiator side
// synthesizes a stable user_id from the peer ID (see
// config.SyntheticUserIDForPeer) and falls back to a peer-based display name.
type pairCompleteRequest struct {
	Code              string `json:"code"`
	PeerID            string `json:"peer_id"`
	DisplayName       string `json:"display_name,omitempty"`
	UserID            string `json:"user_id,omitempty"`
	RemoteDisplayName string `json:"remote_display_name,omitempty"`
}

// pairStart generates a code + QR payload. Returns 501 in stub builds.
func (h *p2pPairingHandler) pairStart(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.svc == nil {
		writeError(w, http.StatusNotImplemented,
			"p2p not built in (rebuild with -tags p2p)")
		return
	}
	res, err := h.svc.StartPair(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "start pair: "+err.Error())
		return
	}
	dataURL, err := qrDataURL(res.QRPayload)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "render qr: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, pairStartResponse{
		Code:      res.Code,
		QRPayload: res.QRPayload,
		QRDataURL: dataURL,
		ExpiresAt: res.ExpiresAt,
	})
}

// pairComplete accepts a QR payload + typed code from another device, runs
// the libp2p handshake against the remote peer, and persists the trust on
// success.
func (h *p2pPairingHandler) pairComplete(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.svc == nil {
		writeError(w, http.StatusNotImplemented,
			"p2p not built in (rebuild with -tags p2p)")
		return
	}
	var req pairCompleteRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "decode: "+err.Error())
		return
	}
	if !validPairCompleteRequest(req) {
		writeError(w, http.StatusBadRequest,
			"code (6 digits) and peer_id are required")
		return
	}
	if err := h.svc.CompletePair(r.Context(), req.Code, req.PeerID, nil); err != nil {
		if errors.Is(err, p2p.ErrPairingInvalid) {
			writeError(w, http.StatusUnauthorized, "code invalid or expired")
			return
		}
		writeError(w, http.StatusInternalServerError, "complete pair: "+err.Error())
		return
	}
	if err := h.persistPeer(r.Context(), req); err != nil {
		writeError(w, http.StatusInternalServerError, "persist peer: "+err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// persistPeer inserts a paired peer; if it already exists (re-pair), bump
// last_seen instead of failing. We deliberately don't surface that to the
// caller as a different status — re-pair is a normal flow.
//
// M7.1: also link the peer to a per-human user row. Multi-machine support:
// if the remote sent a user_id we already know about (with a different
// peer), we just add the new peer to that existing user. A missing
// user_id falls back to a synthetic value derived from the peer ID so
// legacy peers still get a row.
func (h *p2pPairingHandler) persistPeer(ctx context.Context, req pairCompleteRequest) error {
	display := req.DisplayName
	if display == "" {
		display = shortPeerID(req.PeerID)
	}
	now := time.Now().UTC()
	peer := &store.P2PPeer{
		PeerID:      req.PeerID,
		DisplayName: display,
		PairedAt:    now,
		TrustLevel:  0,
		Scopes:      []string{},
	}
	err := h.store.AddPeer(ctx, peer)
	if errors.Is(err, store.ErrAlreadyExists) {
		// Re-pair: clear any prior revocation, refresh last_seen, and
		// update the display_name in case the remote renamed itself.
		if uerr := h.store.UnrevokePeer(ctx, req.PeerID); uerr != nil {
			return uerr
		}
		if uerr := h.store.UpdateLastSeen(ctx, req.PeerID, now); uerr != nil {
			return uerr
		}
		if display != "" {
			if uerr := h.store.UpdateDisplayName(ctx, req.PeerID, display); uerr != nil {
				return uerr
			}
		}
	} else if err != nil {
		return err
	}
	return h.linkPeerUser(ctx, req)
}

// linkPeerUser writes the per-human user row + peer_users join for the
// remote peer. Errors are returned so the API caller sees a 500 — the row
// is part of the pairing contract for M7.1, not a best-effort side effect.
func (h *p2pPairingHandler) linkPeerUser(ctx context.Context, req pairCompleteRequest) error {
	if h.users == nil {
		return nil
	}
	userID := req.UserID
	if userID == "" {
		userID = config.SyntheticUserIDForPeer(req.PeerID)
	}
	display := req.RemoteDisplayName
	if display == "" {
		display = shortPeerID(req.PeerID)
	}
	if err := h.users.UpsertUser(ctx, userID, display); err != nil {
		return fmt.Errorf("upsert remote user: %w", err)
	}
	return h.users.LinkPeerToUser(ctx, req.PeerID, userID)
}

// listPeers returns every paired peer (active + revoked). UI filters as it
// pleases. When a reconnector is wired, each row is augmented with the
// last_dial_attempt_at / last_dial_error / reconnect_state fields.
func (h *p2pPairingHandler) listPeers(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.store == nil {
		writeError(w, http.StatusNotImplemented, "p2p store not configured")
		return
	}
	peers, err := h.store.ListPeers(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list peers: "+err.Error())
		return
	}
	rows := make([]peerListRow, 0, len(peers))
	statuses := allReconnectStatuses(h.reconnector)
	for _, p := range peers {
		row := peerListRow{P2PPeer: p}
		if rs, ok := statuses[p.PeerID]; ok {
			if !rs.LastAttempt.IsZero() {
				row.LastDialAttemptAt = rs.LastAttempt.UTC().Format(time.RFC3339)
			}
			row.LastDialError = rs.LastError
			row.ReconnectState = rs.State
		}
		rows = append(rows, row)
	}
	writeJSON(w, http.StatusOK, map[string]any{"peers": rows})
}

// allReconnectStatuses is a nil-safe wrapper around (*p2p.Reconnector).
// AllPeerStatus(). Returns an empty map (never nil) so callers can range
// over the result unconditionally.
func allReconnectStatuses(r *p2p.Reconnector) map[string]p2p.ReconnectStatus {
	if r == nil {
		return map[string]p2p.ReconnectStatus{}
	}
	return r.AllPeerStatus()
}

// setSSHTarget records the SSH user@host (or ssh-config alias) the
// dashboard uses when the user clicks "Focus" on a peer-origin agent.
// PATCH /api/p2p/peers/{id}/ssh-target with body { "ssh_target": "..." }.
// Empty string clears. 404 when the peer is unknown or revoked.
func (h *p2pPairingHandler) setSSHTarget(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.store == nil {
		writeError(w, http.StatusNotImplemented, "p2p store not configured")
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "missing peer id")
		return
	}
	var req struct {
		SSHTarget string `json:"ssh_target"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	// Existence/revocation check first so we can return 404 instead of a
	// silent no-op when the user typed the wrong peer.
	peer, err := h.store.GetPeer(r.Context(), id)
	if err != nil || peer == nil || peer.RevokedAt != nil {
		writeError(w, http.StatusNotFound, "peer not found")
		return
	}
	if err := h.store.SetPeerSSHTarget(r.Context(), id, req.SSHTarget); err != nil {
		writeError(w, http.StatusInternalServerError, "set ssh_target: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "ssh_target": req.SSHTarget})
}

// scopeCheckRequest asks "would this peer be allowed to use `scope`
// right now?". The handler resolves to a 200 (allowed) OR a 403 with
// a structured `denial` block — the whole point of the JTAC65 bug
// fix: callers can tell why they were rejected.
type scopeCheckRequest struct {
	Scope string `json:"scope"`
}

// checkScope is POST /api/p2p/peers/{id}/scopes/check. Returns 200
// {allowed:true} when the peer holds the scope, or 403 with a typed
// scopes.Denial block when it doesn't. The four denial codes map to
// the four distinguishable failure modes:
//
//   - scope_revoked       — peer row exists, revoked_at != nil
//   - no_scope            — peer active, scope absent from p.Scopes
//   - scope_out_of_band   — scope literal isn't in the peerscope.Known
//     registry (typo, stale scope id, etc.)
//   - cross_org_boundary  — reserved for Tier-3 (no org model yet;
//     emitted only when the caller passes
//     ?reason=cross_org for forward-compat
//     tests of the wire shape).
//
// 404 (writeError) is reserved for "peer doesn't exist at all" so
// the deny vocabulary stays focused on the four scope-failure modes.
func (h *p2pPairingHandler) checkScope(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.store == nil {
		writeError(w, http.StatusNotImplemented, "p2p store not configured")
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "missing peer id")
		return
	}
	var req scopeCheckRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "decode: "+err.Error())
		return
	}
	if req.Scope == "" {
		writeError(w, http.StatusBadRequest, "scope is required")
		return
	}
	peer, err := h.store.GetPeer(r.Context(), id)
	if err != nil || peer == nil {
		writeError(w, http.StatusNotFound, "peer not found")
		return
	}
	denial, ok := evaluatePeerScope(peer, req.Scope, r.URL.Query().Get("reason"))
	if ok {
		writeJSON(w, http.StatusOK, map[string]any{
			"allowed": true,
			"scope":   req.Scope,
			"peer":    peer.PeerID,
		})
		return
	}
	writeDenial(w, denial)
}

// evaluatePeerScope is the pure decision function the checkScope
// handler delegates to. Split out so the unit tests don't need an
// HTTP roundtrip to exercise each of the four code paths.
//
// The `forceReason` argument lets callers (and tests) force a
// specific code path — used today only for cross_org_boundary
// since the org model is a Tier-3 follow-up. When non-empty AND
// it maps to a canonical code, that code wins over the natural
// store-derived decision.
func evaluatePeerScope(peer *store.P2PPeer, scope, forceReason string) (scopes.Denial, bool) {
	// Forward-compat fast path: callers can ask the handler to
	// surface a specific code (e.g. cross_org_boundary) before the
	// underlying enforcement layer lands. Only canonical codes are
	// honoured; unknown reasons fall through to the natural path.
	if forceReason != "" {
		c := scopes.DenialCode(forceReason)
		if c.Valid() {
			return scopes.New(c, scope, peer.PeerID), false
		}
	}
	// Revoked peers always lose, regardless of whether the scope is
	// still in p.Scopes (the revoke flag is the higher-priority
	// signal). This is the literal RevokePeer follow-up the bug
	// description names.
	if peer.RevokedAt != nil {
		return scopes.New(scopes.DenialScopeRevoked, scope, peer.PeerID), false
	}
	// Out-of-band guard before any membership check: if the scope
	// string itself doesn't match the canonical registry the answer
	// is "this isn't a valid grant target for ANY peer" rather than
	// "this peer doesn't hold it".
	if peerscope.FindByPrefix(scope) == nil {
		return scopes.New(scopes.DenialScopeOutOfBand, scope, peer.PeerID), false
	}
	// Happy path: the scope (or its wildcard form) appears in p.Scopes.
	if peerHoldsScope(peer, scope) {
		return scopes.Denial{}, true
	}
	return scopes.New(scopes.DenialNoScope, scope, peer.PeerID), false
}

// peerHoldsScope is the membership check that mirrors the store's
// HasPeerScope behaviour but without a DB roundtrip — operates on
// the already-loaded P2PPeer.Scopes slice. Accepts an exact match
// OR the colon-prefix wildcard form ("trigger_worker:*" satisfies
// any "trigger_worker:foo" check).
func peerHoldsScope(peer *store.P2PPeer, scope string) bool {
	for _, s := range peer.Scopes {
		if s == scope {
			return true
		}
		// Wildcard form: an entry like "trigger_worker:*" matches
		// any "trigger_worker:<name>" request.
		if len(s) > 0 && s[len(s)-1] == '*' {
			prefix := s[:len(s)-1]
			if len(scope) >= len(prefix) && scope[:len(prefix)] == prefix {
				return true
			}
		}
	}
	return false
}

// revokePeer flips revoked_at on a paired peer. 404 if unknown or already
// revoked.
func (h *p2pPairingHandler) revokePeer(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.store == nil {
		writeError(w, http.StatusNotImplemented, "p2p store not configured")
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "missing peer id")
		return
	}
	if err := h.store.RevokePeer(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "peer not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "revoke: "+err.Error())
		return
	}
	// Stop re-dialing the revoked peer's persisted static address every 60s.
	// Authorization is already denied post-revoke (IsPaired excludes revoked
	// rows); this just stops the wasteful redial loop. Best-effort.
	if h.host != nil {
		if err := h.host.PruneStaticDial(id); err != nil {
			slog.Warn("p2p revoke: prune static dial", "peer", id, "error", err)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// validPairCompleteRequest checks code + peer ID surface validation.
func validPairCompleteRequest(req pairCompleteRequest) bool {
	if len(req.Code) != 6 {
		return false
	}
	for _, c := range req.Code {
		if c < '0' || c > '9' {
			return false
		}
	}
	return req.PeerID != ""
}

// shortPeerID returns a friendly default display name (last 8 chars of the
// peer ID). We prepend "peer-" so it doesn't render as an opaque hash to the
// user; UX guidance says NEVER show full peer IDs.
func shortPeerID(id string) string {
	if len(id) <= 8 {
		return "peer-" + id
	}
	return "peer-" + id[len(id)-8:]
}

// qrDataURL renders a QR code containing the pairing payload and returns it
// as a data: URL. The PNG body is base64-encoded for direct <img src=...>
// consumption.
func qrDataURL(payload string) (string, error) {
	png, err := encodeQRPNG(payload)
	if err != nil {
		return "", fmt.Errorf("encode qr: %w", err)
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(png), nil
}
