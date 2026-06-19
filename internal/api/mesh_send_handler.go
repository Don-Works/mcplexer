package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/mesh"
)

// defaultRESTAgentName is used as the ClientType for REST callers that
// don't supply an agent_name. agentDisplayName turns this into a friendly
// label at the display boundary.
const defaultRESTAgentName = "rest"

// meshSendHandler exposes mesh.Manager.Send over HTTP so curl/CI can drive
// the cross-machine flow without an stdio MCP client. Auth is gated by the
// existing browserOriginProtection middleware (localhost-only).
type meshSendHandler struct {
	mgr *mesh.Manager
}

// recipient mirrors the on-the-wire shape used by the libp2p envelope so
// API callers can target a peer / role / audience uniformly.
type meshSendRecipient struct {
	Kind  string `json:"kind"` // "peer" | "role" | "audience"
	Value string `json:"value"`
}

type meshSendRequest struct {
	Recipient  meshSendRecipient `json:"recipient"`
	Kind       string            `json:"kind"`
	Content    string            `json:"content"`
	Priority   string            `json:"priority,omitempty"`
	Tags       string            `json:"tags,omitempty"`
	ReplyTo    string            `json:"reply_to,omitempty"`
	NotifyUser bool              `json:"notify_user,omitempty"`
	// AgentName is the caller-supplied display label that surfaces in the
	// active-agents UI. Empty falls back to defaultRESTAgentName, which
	// agentDisplayName pretty-prints.
	AgentName string `json:"agent_name,omitempty"`
	// WorkspaceID lets the caller stamp a specific workspace onto the
	// outgoing message — required for cross-peer trigger flows where the
	// receiving daemon's dispatcher gates by workspace_id (G2 isolation).
	// Empty falls back to "global".
	WorkspaceID string `json:"workspace_id,omitempty"`
}

type meshSendResponse struct {
	MessageID string    `json:"message_id"`
	ExpiresAt time.Time `json:"expires_at"`
}

// send accepts a JSON body, dispatches it through mesh.Manager.Send (so
// local + libp2p paths stay in sync), and returns the persisted message
// ID. Returns 503 when mesh is disabled.
func (h *meshSendHandler) send(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.mgr == nil {
		writeError(w, http.StatusServiceUnavailable, "mesh disabled")
		return
	}
	var req meshSendRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	defer func() { _ = r.Body.Close() }()
	if req.Kind == "" || req.Content == "" {
		writeError(w, http.StatusBadRequest, "kind + content are required")
		return
	}

	sreq := mesh.SendRequest{
		Kind:       req.Kind,
		Content:    req.Content,
		Priority:   req.Priority,
		Tags:       req.Tags,
		ReplyTo:    req.ReplyTo,
		NotifyUser: req.NotifyUser,
	}
	switch req.Recipient.Kind {
	case "peer":
		sreq.ToPeer = req.Recipient.Value
	case "role":
		sreq.Audience = req.Recipient.Value
	case "audience", "":
		if req.Recipient.Value == "" {
			sreq.Audience = "*"
		} else {
			sreq.Audience = req.Recipient.Value
		}
	default:
		writeError(w, http.StatusBadRequest,
			`recipient.kind must be "peer" | "role" | "audience"`)
		return
	}

	clientType := strings.TrimSpace(req.AgentName)
	if clientType == "" {
		clientType = defaultRESTAgentName
	}
	wsID := strings.TrimSpace(req.WorkspaceID)
	if wsID == "" {
		wsID = "global"
	}
	meta := mesh.SessionMeta{
		SessionID:    "rest:" + r.RemoteAddr,
		WorkspaceIDs: []string{wsID},
		ClientType:   clientType,
	}
	msg, err := h.mgr.Send(r.Context(), meta, sreq)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(meshSendResponse{
		MessageID: msg.ID,
		ExpiresAt: msg.ExpiresAt,
	})
}
