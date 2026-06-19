// chat_signals_handler.go — REST surface for the concierge's chat turn
// signal log (epic 01KSGKFZMVFZRWVDSZMK8W9JN1 / migration 080).
//
// Routes:
//
//	GET    /api/v1/chat-signals       → list, filterable by workspace + label + worker + channel
//	POST   /api/v1/chat-signals/{id}/mark-promoted → stamp the refinement linkage
//
// The friction-extractor worker reads this surface to pull "new
// negative signals since the last run". The dashboard's future friction
// inbox tile reads the same surface to surface patterns to operators.
package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/don-works/mcplexer/internal/concierge"
	"github.com/don-works/mcplexer/internal/store"
)

type chatSignalsHandler struct {
	svc *concierge.Service
}

func newChatSignalsHandler(svc *concierge.Service) *chatSignalsHandler {
	return &chatSignalsHandler{svc: svc}
}

// handleList serves GET /api/v1/chat-signals.
// Query parameters (all optional):
//
//	workspace_id, worker_id, channel, user_id_external — exact match
//	label                              — comma-separated; matches ANY
//	promoted                            — "true" | "false"; false = NotPromoted=true
//	limit                               — default 100, max 1000
func (h *chatSignalsHandler) handleList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.ChatTurnSignalFilter{
		WorkerID:       strings.TrimSpace(q.Get("worker_id")),
		WorkspaceID:    strings.TrimSpace(q.Get("workspace_id")),
		UserIDExternal: strings.TrimSpace(q.Get("user_id_external")),
		Channel:        strings.TrimSpace(q.Get("channel")),
	}
	if v := strings.TrimSpace(q.Get("label")); v != "" {
		for _, lbl := range strings.Split(v, ",") {
			if t := strings.TrimSpace(lbl); t != "" {
				f.Labels = append(f.Labels, t)
			}
		}
	}
	if v := strings.TrimSpace(q.Get("promoted")); v != "" {
		// promoted=false means "only un-promoted signals" (NotPromoted=true).
		if v == "false" {
			f.NotPromoted = true
		}
	}
	if v := strings.TrimSpace(q.Get("limit")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			f.Limit = n
		}
	}

	rows, err := h.svc.List(r.Context(), f)
	if err != nil {
		writeErrorDetail(w, http.StatusInternalServerError, "list failed", err.Error())
		return
	}
	if rows == nil {
		rows = []store.ChatTurnSignal{}
	}
	writeJSON(w, http.StatusOK, rows)
}

// markPromotedRequest is the POST body for the mark-promoted endpoint.
type markPromotedRequest struct {
	RefinementID string `json:"refinement_id"`
}

// handleMarkPromoted serves POST /api/v1/chat-signals/{id}/mark-promoted.
func (h *chatSignalsHandler) handleMarkPromoted(w http.ResponseWriter, r *http.Request) {
	signalID := strings.TrimSpace(r.PathValue("id"))
	if signalID == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}
	var body markPromotedRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(body.RefinementID) == "" {
		writeError(w, http.StatusBadRequest, "refinement_id is required")
		return
	}
	if err := h.svc.MarkPromoted(r.Context(), signalID, body.RefinementID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "signal not found")
			return
		}
		writeErrorDetail(w, http.StatusInternalServerError, "mark-promoted failed", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
