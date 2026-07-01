package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/don-works/mcplexer/internal/compression"
	"github.com/don-works/mcplexer/internal/config"
	"github.com/don-works/mcplexer/internal/store"
)

// compressionSSEHandler live-streams the compression stats payload (same shape
// as GET /compression/stats) on a ticker, so the dashboard updates without
// polling. The stats are an aggregate (not a per-event bus), so we re-query and
// push on a fixed cadence.
type compressionSSEHandler struct {
	store    store.Store
	settings *config.SettingsService
}

func (h *compressionSSEHandler) stream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}
	// Disable the server WriteTimeout for this long-lived connection.
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{})

	q := r.URL.Query()
	days := 30
	if v := q.Get("days"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			days = n
		}
	}
	workspaceID := q.Get("workspace_id")

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher.Flush()

	ctx := r.Context()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	send := func() bool {
		agg, err := h.store.CompressionAggregate(ctx, workspaceID, days, time.Now())
		if err != nil {
			return true // transient — keep the stream open
		}
		mode := string(compression.ModeShadow)
		disabled := []string{}
		if h.settings != nil {
			s := h.settings.Load(ctx)
			mode = string(compression.ParseMode(s.CompressionMode))
			if len(s.CompressionDisabledTransforms) > 0 {
				disabled = s.CompressionDisabledTransforms
			}
		}
		data, err := json.Marshal(map[string]any{
			"mode":       mode,
			"transforms": compression.DefaultTransformInfo(),
			"disabled":   disabled,
			"aggregate":  agg,
		})
		if err != nil {
			return true
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	if !send() {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !send() {
				return
			}
		}
	}
}
