package gateway

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/don-works/mcplexer/internal/index"
)

func (h *handler) handleIndexRecentChanges(ctx context.Context, raw json.RawMessage) (json.RawMessage, *RPCError) {
	var args struct {
		Path        string `json:"path"`
		Days        int    `json:"days"`
		Limit       int    `json:"limit"`
		WorkspaceID string `json:"workspace_id"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	workspaceID, root, rpc := h.indexWorkspaceRoot(ctx, args.WorkspaceID, false)
	if rpc != nil {
		return rpcResult(rpc), nil
	}
	res, err := h.codeIndex.RecentChanges(ctx, index.RecentChangesRequest{
		WorkspaceID: workspaceID,
		Root:        root,
		Path:        args.Path,
		Days:        args.Days,
		Limit:       args.Limit,
	})
	if err != nil {
		return mapIndexError("recent_changes", err), nil
	}
	return marshalJSONResult(res)
}

func (h *handler) handleIndexMapFailure(ctx context.Context, raw json.RawMessage) (json.RawMessage, *RPCError) {
	var args struct {
		Text        string `json:"text"`
		Limit       int    `json:"limit"`
		WorkspaceID string `json:"workspace_id"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	if strings.TrimSpace(args.Text) == "" {
		return marshalErrorResult("text is required"), nil
	}
	workspaceID, root, rpc := h.indexWorkspaceRoot(ctx, args.WorkspaceID, false)
	if rpc != nil {
		return rpcResult(rpc), nil
	}
	candidates, err := h.codeIndex.MapFailure(ctx, workspaceID, root, args.Text, args.Limit)
	if err != nil {
		return mapIndexError("map_failure", err), nil
	}
	return marshalJSONResult(map[string]any{"count": len(candidates), "candidates": candidates})
}

func (h *handler) handleIndexContext(ctx context.Context, raw json.RawMessage) (json.RawMessage, *RPCError) {
	var args struct {
		Query        string `json:"query"`
		BudgetTokens int    `json:"budget_tokens"`
		WorkspaceID  string `json:"workspace_id"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	if strings.TrimSpace(args.Query) == "" {
		return marshalErrorResult("query is required"), nil
	}
	workspaceID, root, rpc := h.indexWorkspaceRoot(ctx, args.WorkspaceID, false)
	if rpc != nil {
		return rpcResult(rpc), nil
	}
	pack, err := h.codeIndex.ContextPack(ctx, index.ContextRequest{
		WorkspaceID:  workspaceID,
		Root:         root,
		Query:        args.Query,
		BudgetTokens: args.BudgetTokens,
	})
	if err != nil {
		return mapIndexError("context", err), nil
	}
	return marshalJSONResult(pack)
}
