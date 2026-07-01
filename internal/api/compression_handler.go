package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/don-works/mcplexer/internal/compression"
	"github.com/don-works/mcplexer/internal/config"
	"github.com/don-works/mcplexer/internal/store"
)

// compressionHandler serves the token-compression dashboard: the durable
// savings ledger plus the current global mode. Reads straight from the store
// (the gateway persists per tool result), so it needs no reference to the
// per-connection gateway handler.
type compressionHandler struct {
	store    store.Store
	settings *config.SettingsService
}

// stats serves GET /api/v1/compression/stats?days=30&workspace_id=...
// Returns the current global mode plus the per-transform + daily savings
// rollup that backs the settings page's observed-savings-next-to-each-toggle.
func (h *compressionHandler) stats(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	days := 30
	if v := q.Get("days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			days = n
		}
	}
	agg, err := h.store.CompressionAggregate(r.Context(), q.Get("workspace_id"), days, time.Now())
	if err != nil {
		writeErrorDetail(w, http.StatusInternalServerError, "failed to aggregate compression stats", err.Error())
		return
	}
	mode := string(compression.ModeShadow)
	if h.settings != nil {
		mode = string(compression.ParseMode(h.settings.Load(r.Context()).CompressionMode))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"mode":      mode,
		"aggregate": agg,
	})
}
