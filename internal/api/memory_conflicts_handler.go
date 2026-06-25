// memory_conflicts_handler.go — REST surface for the memory conflict queue
// (the "conflicts to review" dashboard). A note write's neighbour scan
// persists possible duplicate/conflict pairs (migration 116); this lets the
// operator review them and record a resolution.
//
// Routes:
//
//	GET    /api/v1/memory/conflicts                 → open conflicts, newest first
//	POST   /api/v1/memory/conflicts/{id}/resolve    → close one (superseded|kept_both|dismissed)
package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/don-works/mcplexer/internal/memory"
	"github.com/don-works/mcplexer/internal/store"
)

type conflictsHandler struct {
	svc *memory.Service
}

func newConflictsHandler(svc *memory.Service) *conflictsHandler {
	return &conflictsHandler{svc: svc}
}

// handleList serves GET /api/v1/memory/conflicts?limit=N.
func (h *conflictsHandler) handleList(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	rows, err := h.svc.ListOpenConflicts(r.Context(), limit)
	if err != nil {
		writeErrorDetail(w, http.StatusInternalServerError, "list conflicts failed", err.Error())
		return
	}
	if rows == nil {
		rows = []store.MemoryConflict{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"conflicts": rows})
}

type resolveConflictRequest struct {
	Resolution string `json:"resolution"`
}

// handleResolve serves POST /api/v1/memory/conflicts/{id}/resolve.
func (h *conflictsHandler) handleResolve(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}
	var body resolveConflictRequest
	_ = decodeJSON(r, &body) // body optional; default below
	res := strings.TrimSpace(body.Resolution)
	if res == "" {
		res = "dismissed"
	}
	switch res {
	case "superseded", "kept_both", "dismissed":
	default:
		writeError(w, http.StatusBadRequest,
			"resolution must be one of: superseded, kept_both, dismissed")
		return
	}
	if err := h.svc.ResolveConflict(r.Context(), id, res); err != nil {
		writeErrorDetail(w, http.StatusInternalServerError, "resolve conflict failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "resolution": res})
}
