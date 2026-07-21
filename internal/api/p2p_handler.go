package api

import (
	"net/http"
	"time"

	"github.com/don-works/mcplexer/internal/p2p"
)

// p2pHandler exposes the embedded libp2p host's identity over the REST API.
//
// The handler is wired into the router only when a non-nil *p2p.Host is
// passed in RouterDeps. In stub builds (no `p2p` tag) the daemon still
// constructs a *p2p.Host pointer when --p2p is requested, but it will be
// nil — leaving this endpoint absent. We additionally guard at the handler
// level so a 501 is returned if the route is hit before startup completes.
type p2pHandler struct {
	host        *p2p.Host
	lookup      *p2p.SQLPeerLookup // optional; enables /api/p2p/peers/{id}/status
	reconnector *p2p.Reconnector   // optional; enables reconnect_* fields
}

// p2pIdentityResponse is the JSON shape of GET /api/p2p/identity.
type p2pIdentityResponse struct {
	PeerID     string   `json:"peer_id"`
	Multiaddrs []string `json:"multiaddrs"`
}

// identity returns the libp2p PeerID and listen multiaddrs of the embedded
// host. Returns 501 Not Implemented if the daemon was built without the p2p
// build tag (host is nil).
func (h *p2pHandler) identity(w http.ResponseWriter, _ *http.Request) {
	if h == nil || h.host == nil {
		writeError(w, http.StatusNotImplemented,
			"p2p not built in (rebuild with -tags p2p)")
		return
	}
	writeJSON(w, http.StatusOK, p2pIdentityResponse{
		PeerID:     h.host.PeerID(),
		Multiaddrs: h.host.Addrs(),
	})
}

// peerStatus implements GET /api/p2p/peers/{id}/status. Returns the persisted
// connection_mode + last_seen for a paired peer, plus the live multiaddrs
// from the libp2p peerstore. Returns 404 when the peer is not paired (and
// 501 when p2p isn't built in).
func (h *p2pHandler) peerStatus(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.host == nil {
		writeError(w, http.StatusNotImplemented,
			"p2p not built in (rebuild with -tags p2p)")
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "missing peer id")
		return
	}
	if h.lookup == nil {
		writeError(w, http.StatusServiceUnavailable, "peer store unavailable")
		return
	}
	st, err := h.lookup.GetPeerStatus(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	st.Addrs = h.host.LastSeenAddrs(id)
	if st.Addrs == nil {
		st.Addrs = []string{}
	}
	// If discovery is wired, prefer the live in-memory mode over the
	// possibly-stale DB row — the DB write is async and best-effort.
	if d := h.host.Discovery(); d != nil {
		if live := d.ModeFor(id); live != "" {
			st.ConnectionMode = string(live)
		}
	}
	mergeReconnectStatus(&st, h.reconnector, id)
	writeJSON(w, http.StatusOK, st)
}

// p2pConnectRequest is the body of POST /api/p2p/connect.
type p2pConnectRequest struct {
	Addr string `json:"addr"`
}

// connect dials an explicit multiaddr (which must include /p2p/<peerid>) and
// establishes a libp2p connection, seeding the peerstore. This is the manual
// escape hatch for peers whose working address can't be discovered via
// DHT/mDNS — e.g. a peer that only advertises a firewalled LAN addr but is
// actually reachable on the same port over a Tailscale IP. Once the live
// connection exists, pairing's NewStream + the reconnector ride it without
// needing DHT resolution. Returns 501 when p2p isn't built in.
func (h *p2pHandler) connect(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.host == nil {
		writeError(w, http.StatusNotImplemented,
			"p2p not built in (rebuild with -tags p2p)")
		return
	}
	var body p2pConnectRequest
	if err := decodeJSON(r, &body); err != nil || body.Addr == "" {
		writeError(w, http.StatusBadRequest, "addr is required")
		return
	}
	pid, err := h.host.ConnectString(r.Context(), body.Addr)
	if err != nil {
		writeErrorDetail(w, http.StatusBadGateway, "connect failed", err.Error())
		return
	}
	// Remember this direct address so it is re-dialed on the next daemon
	// startup — the reconnector otherwise only resolves peers via the DHT,
	// which can't find a peer that doesn't advertise a reachable address.
	// Best-effort; persistence failure never fails the live connection.
	_ = h.host.PersistStaticDial(body.Addr)
	writeJSON(w, http.StatusOK, map[string]any{"connected": true, "peer_id": pid})
}

// mergeReconnectStatus writes the reconnector's in-memory telemetry onto a
// PeerStatus value. Safe to call with a nil reconnector (no-op) — used by
// both peerStatus and the paired-peer list handler.
func mergeReconnectStatus(st *p2p.PeerStatus, r *p2p.Reconnector, peerID string) {
	if st == nil || r == nil || peerID == "" {
		return
	}
	rs := r.PeerStatusByID(peerID)
	if rs.State == "" && rs.LastError == "" && rs.LastAttempt.IsZero() {
		return
	}
	if !rs.LastAttempt.IsZero() {
		st.LastDialAttemptAt = rs.LastAttempt.UTC().Format(time.RFC3339)
	}
	st.LastDialError = rs.LastError
	st.ReconnectState = rs.State
}
