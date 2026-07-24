package api

import (
	"net/http"
	"strconv"

	"github.com/don-works/mcplexer/internal/store"
)

// monitoringTitleRewriteHandler exposes a one-shot operator tool that turns
// leftover "new error-class log template" incident titles into evidence
// signatures. POST only; workspace-scoped like the rest of monitoring.
type monitoringTitleRewriteHandler struct {
	store store.MonitoringTitleRewriteStore // nil = 501
}

func (h *monitoringTitleRewriteHandler) rewrite(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		writeError(w, http.StatusNotImplemented, "title rewrite not available on this daemon")
		return
	}
	wsID := workspaceIDParam(r)
	if wsID == "" {
		writeError(w, http.StatusBadRequest, "workspace_id query param required")
		return
	}
	limit := 200
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "limit must be a non-negative integer")
			return
		}
		limit = n
	}
	n, err := h.store.RewriteGenericMonitoringTitles(r.Context(), wsID, limit)
	if err != nil {
		writeMonitoringErr(w, err, "rewrite titles")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"workspace_id": wsID,
		"rewritten":    n,
		"limit":        limit,
	})
}
