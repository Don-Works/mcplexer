package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/don-works/mcplexer/internal/mesh"
)

type chatSendHandler struct {
	mgr *mesh.Manager
}

type chatSendRequest struct {
	Message     string `json:"message"`
	WorkspaceID string `json:"workspace_id,omitempty"`
	Priority    string `json:"priority,omitempty"`
}

type chatSendResponse struct {
	MessageID string `json:"message_id"`
}

func (h *chatSendHandler) send(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.mgr == nil {
		writeError(w, http.StatusServiceUnavailable, "mesh disabled")
		return
	}
	var req chatSendRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	defer func() { _ = r.Body.Close() }()
	req.Message = strings.TrimSpace(req.Message)
	if req.Message == "" {
		writeError(w, http.StatusBadRequest, "message is required")
		return
	}
	priority := strings.TrimSpace(req.Priority)
	if priority == "" {
		priority = "normal"
	}
	wsID := strings.TrimSpace(req.WorkspaceID)
	if wsID == "" {
		wsID = "global"
	}
	sreq := mesh.SendRequest{
		Kind:       "chat",
		Content:    req.Message,
		Priority:   priority,
		Audience:   "*",
		NotifyUser: true,
	}
	meta := mesh.SessionMeta{
		SessionID:    "chat:" + r.RemoteAddr,
		WorkspaceIDs: []string{wsID},
		ClientType:   "chat",
	}
	msg, err := h.mgr.Send(r.Context(), meta, sreq)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(chatSendResponse{MessageID: msg.ID})
}
