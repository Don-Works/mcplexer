package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/don-works/mcplexer/internal/downstream"
	"github.com/don-works/mcplexer/internal/store"
)

// downstreamHealthHandler serves the per-server health snapshot used
// by the dashboard's "this server has been flaky" tile. Reads off the
// in-memory HealthTracker on downstream.Manager — no SQL hit on the
// hot path. Returns 404 only when the server itself is missing from
// the store; an unknown-but-valid server ID returns a zero snapshot
// (no failures recorded yet) so the dashboard can render a healthy
// state for a freshly-created server.
type downstreamHealthHandler struct {
	store   store.DownstreamServerStore
	manager *downstream.Manager
}

func (h *downstreamHealthHandler) get(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "missing downstream server id")
		return
	}

	// Confirm the server row exists. Without this the dashboard could
	// surface stale health for a server that's been deleted but whose
	// tracker entry still lingers in memory until daemon restart.
	if _, err := h.store.GetDownstreamServer(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "downstream server not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to load downstream server")
		return
	}

	if h.manager == nil {
		// No live manager — return a zero snapshot rather than 500.
		// The dashboard would otherwise mis-classify the gateway as
		// down when only the manager is decoupled (e.g. in tests).
		writeJSON(w, http.StatusOK, downstream.ServerHealth{ServerID: id})
		return
	}

	snap := h.manager.Health().Snapshot(id, time.Now())
	writeJSON(w, http.StatusOK, snap)
}
