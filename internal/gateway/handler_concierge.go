// handler_concierge.go — concierge__* MCP tool handlers. Today the
// only tool is concierge__record_signal; future surfaces (list signals,
// summarise frictions) live on the REST API rather than expanding the
// MCP wire-area.
package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/don-works/mcplexer/internal/concierge"
)

func (h *handler) handleConciergeRecordSignal(
	ctx context.Context, raw json.RawMessage,
) (json.RawMessage, *RPCError) {
	if h.conciergeSvc == nil {
		return marshalErrorResult("Concierge subsystem is not enabled."), nil
	}
	var args struct {
		WorkerID         string `json:"worker_id"`
		WorkspaceID      string `json:"workspace_id"`
		UserIDExternal   string `json:"user_id_external"`
		Channel          string `json:"channel"`
		PromptVersion    int    `json:"prompt_version"`
		TurnID           string `json:"turn_id"`
		UserMessage      string `json:"user_message"`
		AssistantMessage string `json:"assistant_message"`
		Label            string `json:"label"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	if strings.TrimSpace(args.WorkerID) == "" {
		return nil, &RPCError{Code: CodeInvalidParams, Message: "worker_id is required"}
	}
	if strings.TrimSpace(args.Channel) == "" {
		return nil, &RPCError{Code: CodeInvalidParams, Message: "channel is required"}
	}
	if strings.TrimSpace(args.UserMessage) == "" {
		return nil, &RPCError{Code: CodeInvalidParams, Message: "user_message is required"}
	}
	if _, ok := workerWorkspaceAccessFromContext(ctx); ok && strings.TrimSpace(args.WorkspaceID) == "" {
		args.WorkspaceID = h.currentWorkspaceID(ctx)
	}
	if strings.TrimSpace(args.WorkspaceID) != "" {
		if rpc := h.requireWorkspaceWrite(ctx, args.WorkspaceID); rpc != nil {
			return nil, rpc
		}
	}
	row, err := h.conciergeSvc.Record(ctx, concierge.RecordOptions{
		WorkerID:         args.WorkerID,
		WorkspaceID:      args.WorkspaceID,
		UserIDExternal:   args.UserIDExternal,
		Channel:          args.Channel,
		PromptVersion:    args.PromptVersion,
		TurnID:           args.TurnID,
		UserMessage:      args.UserMessage,
		AssistantMessage: args.AssistantMessage,
		SourceSessionID:  h.sessions.sessionID(),
		Label:            args.Label,
	})
	if err != nil {
		return marshalErrorResult(fmt.Sprintf("Record signal failed: %v", err)), nil
	}
	return marshalJSONResult(map[string]any{
		"signal": row,
	})
}
