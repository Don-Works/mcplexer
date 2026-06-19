package api

import (
	"net/http"

	"github.com/don-works/mcplexer/internal/cache"
	"github.com/don-works/mcplexer/internal/routing"
)

type cacheHandler struct {
	toolCache *cache.ToolCache
	engine    *routing.Engine
}

type cacheStatsResponse struct {
	ToolCall        cache.Stats `json:"tool_call"`
	RouteResolution cache.Stats `json:"route_resolution"`
}

func (h *cacheHandler) stats(w http.ResponseWriter, _ *http.Request) {
	resp := cacheStatsResponse{
		RouteResolution: h.engine.RouteStats(),
	}
	if h.toolCache != nil {
		resp.ToolCall = h.toolCache.Stats()
	}
	writeJSON(w, http.StatusOK, resp)
}

type flushRequest struct {
	Layer    string `json:"layer"`     // "tool_call", "route", "all" (default)
	ServerID string `json:"server_id"` // optional: flush specific server only
}

func (h *cacheHandler) flush(w http.ResponseWriter, r *http.Request) {
	var req flushRequest
	if r.ContentLength > 0 {
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
	}
	if req.Layer == "" {
		req.Layer = "all"
	}

	switch req.Layer {
	case "tool_call":
		if h.toolCache != nil {
			if req.ServerID != "" {
				h.toolCache.InvalidateServer(req.ServerID)
			} else {
				h.toolCache.Flush()
			}
		}
	case "route":
		h.engine.InvalidateAllRoutes()
	case "all":
		if h.toolCache != nil {
			h.toolCache.Flush()
		}
		h.engine.InvalidateAllRoutes()
	default:
		writeError(w, http.StatusBadRequest, "invalid layer: use tool_call, route, or all")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "flushed"})
}
