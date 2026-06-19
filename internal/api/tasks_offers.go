package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/tasks"
)

func (h *tasksHandler) handleListOffers(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.TaskOfferFilter{
		Direction: q.Get("direction"),
		State:     q.Get("state"),
		PeerID:    q.Get("peer"),
	}
	if limit, err := strconv.Atoi(q.Get("limit")); err == nil {
		f.Limit = limit
	}
	if since := q.Get("since"); since != "" {
		if t, err := time.Parse(time.RFC3339, since); err == nil {
			f.Since = &t
		}
	}
	rows, err := h.svc.ListOffers(r.Context(), f)
	if err != nil {
		writeErrorDetail(w, http.StatusInternalServerError, "list offers failed", err.Error())
		return
	}
	if rows == nil {
		rows = []store.TaskOffer{}
	}
	writeJSON(w, http.StatusOK, rows)
}

// createOfferRequest is the body of POST /api/v1/tasks/offers.
type createOfferRequest struct {
	WorkspaceID  string `json:"workspace_id"`
	TaskID       string `json:"task_id"`
	ToPeerID     string `json:"to_peer_id"`
	Message      string `json:"message"`
	DirectAssign bool   `json:"direct_assign"`
}

// POST /api/v1/tasks/offers — outgoing offer (dashboard calls when the
// user clicks "share with peer"). The taskShare wire layer + scope
// checks live on the receiving daemon; this endpoint is best-effort
// dispatch.
func (h *tasksHandler) handleCreateOffer(w http.ResponseWriter, r *http.Request) {
	var body createOfferRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(body.TaskID) == "" || strings.TrimSpace(body.ToPeerID) == "" {
		writeError(w, http.StatusBadRequest, "task_id and to_peer_id are required")
		return
	}
	row, err := h.svc.Offer(r.Context(), tasks.OfferOptions{
		WorkspaceID:  body.WorkspaceID,
		TaskID:       body.TaskID,
		ToPeerID:     body.ToPeerID,
		Message:      body.Message,
		DirectAssign: body.DirectAssign,
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "task not found")
			return
		}
		// Return 200 + body anyway when the row was persisted but the
		// wire send failed — the dashboard can retry from the row id.
		if row != nil {
			writeJSON(w, http.StatusAccepted, map[string]any{
				"offer":   row,
				"warning": err.Error(),
			})
			return
		}
		writeErrorDetail(w, http.StatusInternalServerError, "offer failed", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, row)
}

// taskAcceptOfferRequest is the body of POST /api/v1/tasks/offers/{id}/accept.
type taskAcceptOfferRequest struct {
	WorkspaceID string `json:"workspace_id"`
}

// POST /api/v1/tasks/offers/{id}/accept
func (h *tasksHandler) handleAcceptOffer(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}
	var body taskAcceptOfferRequest
	_ = decodeJSON(r, &body) // body is optional
	t, err := h.svc.AcceptOffer(r.Context(), id, body.WorkspaceID)
	if errors.Is(err, tasks.ErrBindingRequired) {
		writeError(w, http.StatusPreconditionRequired, "workspace_id required for first offer from this peer/workspace")
		return
	}
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "offer not found")
		return
	}
	if err != nil {
		writeErrorDetail(w, http.StatusInternalServerError, "accept failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// taskDeclineOfferRequest is the body of POST /api/v1/tasks/offers/{id}/decline.
type taskDeclineOfferRequest struct {
	Reason string `json:"reason"`
}

// POST /api/v1/tasks/offers/{id}/decline
func (h *tasksHandler) handleDeclineOffer(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}
	var body taskDeclineOfferRequest
	_ = decodeJSON(r, &body)
	if err := h.svc.DeclineOffer(r.Context(), id, body.Reason); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "offer not found")
			return
		}
		writeErrorDetail(w, http.StatusInternalServerError, "decline failed", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
