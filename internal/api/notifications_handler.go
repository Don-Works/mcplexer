package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/don-works/mcplexer/internal/notify"
)

// notificationsHandler serves the persistent history backing the Signal
// tray. The SSE stream (notify_sse_handler) still pushes live events;
// this handler covers backfill on tray open + read-state writes.
type notificationsHandler struct {
	store notify.Store
}

// list — GET /api/v1/notifications
//
// Query params (all optional):
//
//	source=mesh|approval|system|secret|memory
//	kind=mesh|approval|system|secret
//	priority=critical|high|normal|low
//	unread=true
//	before=<id>     pagination cursor (return rows with id < before)
//	limit=N         hard-capped to 200 server-side
func (h *notificationsHandler) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := notify.ListFilter{
		Source:     q.Get("source"),
		Kind:       q.Get("kind"),
		Priority:   q.Get("priority"),
		UnreadOnly: q.Get("unread") == "true" || q.Get("unread") == "1",
	}
	if before := q.Get("before"); before != "" {
		if v, err := strconv.ParseInt(before, 10, 64); err == nil {
			f.BeforeID = v
		}
	}
	if limit := q.Get("limit"); limit != "" {
		if v, err := strconv.Atoi(limit); err == nil {
			f.Limit = v
		}
	}
	events, err := h.store.List(r.Context(), f)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list notifications: "+err.Error())
		return
	}
	unread, err := h.store.UnreadCount(r.Context())
	if err != nil {
		// Non-fatal — we can still return the page.
		unread = -1
	}
	if events == nil {
		events = []notify.StoredEvent{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"notifications": events,
		"unread_count":  unread,
	})
}

// markRead — POST /api/v1/notifications/{id}/read
func (h *notificationsHandler) markRead(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := h.store.MarkRead(r.Context(), []int64{id}); err != nil {
		writeError(w, http.StatusInternalServerError, "mark read: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// markReadBulk — POST /api/v1/notifications/read
// Body: {"ids": [1, 2, 3]} OR {"all": true}.
func (h *notificationsHandler) markReadBulk(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []int64 `json:"ids"`
		All bool    `json:"all"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !strings.Contains(err.Error(), "EOF") {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.All {
		if err := h.store.MarkAllRead(r.Context()); err != nil {
			writeError(w, http.StatusInternalServerError, "mark all read: "+err.Error())
			return
		}
	} else if len(req.IDs) > 0 {
		if err := h.store.MarkRead(r.Context(), req.IDs); err != nil {
			writeError(w, http.StatusInternalServerError, "mark read: "+err.Error())
			return
		}
	}
	unread, _ := h.store.UnreadCount(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "unread_count": unread})
}

// unreadCount — GET /api/v1/notifications/unread-count
//
// Lightweight endpoint for the sidebar counter to poll on a short
// interval without dragging the full list down the wire.
func (h *notificationsHandler) unreadCount(w http.ResponseWriter, r *http.Request) {
	n, err := h.store.UnreadCount(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "unread count: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"unread_count": n})
}
