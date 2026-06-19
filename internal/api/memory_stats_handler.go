// Package api — memory_stats_handler.go serves GET /api/v1/memory/stats,
// the aggregate "shape of the brain" snapshot powering the memory landing
// header. Returns counts, type-mix, recency histogram, 30-day write
// series, network reach, top tags, and decay pressure.
//
// Recall tracking note: there is no per-row last_recalled_at column yet,
// so the `recall_rate_7d` field requested by the dashboard spec is
// intentionally omitted from the response. DecayPressure uses an
// updated_at-based heuristic instead (older than 180d, not pinned,
// still valid). Once recall events get persisted these can be wired in
// without changing the response envelope.
package api

import (
	"net/http"
)

// handleStats serves GET /api/v1/memory/stats.
//
// Querystring: workspace_id — narrows scope to that single workspace ∪
// global. Unset = admin (IncludeAll), matching the rest of the dashboard
// memory surface.
func (h *memoryHandler) handleStats(w http.ResponseWriter, r *http.Request) {
	scope := scopeFromQuery(r)
	stats, err := h.svc.Stats(r.Context(), scope)
	if err != nil {
		writeErrorDetail(w, http.StatusInternalServerError,
			"failed to compute memory stats", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, stats)
}
