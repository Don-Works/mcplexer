package gateway

import (
	"context"
	"encoding/json"
)

// handleMeshSetAgentStatus persists a free-form status string for the
// agent associated with the current MCP session. Auto-registers the
// agent if not yet known so the same call works on first turn. Status
// surfaces in mesh__list_agents and the dashboard, gives humans + peers
// a current-state read on what each agent is doing.
func (h *handler) handleMeshSetAgentStatus(ctx context.Context, args json.RawMessage) (json.RawMessage, *RPCError) {
	if h.mesh == nil {
		return marshalErrorResult("Agent mesh is not enabled."), nil
	}
	var req struct {
		Status string `json:"status"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
		}
	}
	if req.Status == "" {
		return nil, &RPCError{Code: CodeInvalidParams, Message: "status is required"}
	}
	meta := h.sessionMeshMeta(ctx)
	if err := h.mesh.SetAgentStatus(ctx, meta, req.Status); err != nil {
		return marshalErrorResult(err.Error()), nil
	}
	return marshalToolResult("Status updated: \"" + req.Status + "\""), nil
}
