package gateway

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/don-works/mcplexer/internal/index"
)

func (h *handler) handleIndexSymbols(ctx context.Context, raw json.RawMessage) (json.RawMessage, *RPCError) {
	var args struct {
		Query        string `json:"query"`
		Kind         string `json:"kind"`
		ExportedOnly bool   `json:"exported_only"`
		Limit        int    `json:"limit"`
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
	hits, err := h.codeIndex.Symbols(ctx, index.SymbolsRequest{
		WorkspaceID:  workspaceID,
		Root:         root,
		Query:        args.Query,
		Kind:         args.Kind,
		ExportedOnly: args.ExportedOnly,
		Limit:        args.Limit,
	})
	if err != nil {
		return mapIndexError("symbols", err), nil
	}
	return marshalJSONResult(map[string]any{"count": len(hits), "symbols": hits})
}

func (h *handler) handleIndexDeps(ctx context.Context, raw json.RawMessage) (json.RawMessage, *RPCError) {
	var args struct {
		File        string `json:"file"`
		Direction   string `json:"direction"`
		Limit       int    `json:"limit"`
		WorkspaceID string `json:"workspace_id"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	if strings.TrimSpace(args.File) == "" {
		return marshalErrorResult("file is required"), nil
	}
	workspaceID, root, rpc := h.indexWorkspaceRoot(ctx, args.WorkspaceID, false)
	if rpc != nil {
		return rpcResult(rpc), nil
	}
	res, err := h.codeIndex.Deps(ctx, index.DepsRequest{
		WorkspaceID: workspaceID,
		Root:        root,
		File:        args.File,
		Direction:   args.Direction,
		Limit:       args.Limit,
	})
	if err != nil {
		return mapIndexError("deps", err), nil
	}
	return marshalJSONResult(res)
}

func (h *handler) handleIndexTestsFor(ctx context.Context, raw json.RawMessage) (json.RawMessage, *RPCError) {
	var args struct {
		File        string `json:"file"`
		WorkspaceID string `json:"workspace_id"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	if strings.TrimSpace(args.File) == "" {
		return marshalErrorResult("file is required"), nil
	}
	workspaceID, root, rpc := h.indexWorkspaceRoot(ctx, args.WorkspaceID, false)
	if rpc != nil {
		return rpcResult(rpc), nil
	}
	res, err := h.codeIndex.TestsFor(ctx, workspaceID, root, args.File)
	if err != nil {
		return mapIndexError("tests_for", err), nil
	}
	return marshalJSONResult(res)
}

func (h *handler) handleIndexSummary(ctx context.Context, raw json.RawMessage) (json.RawMessage, *RPCError) {
	var args struct {
		File        string `json:"file"`
		WorkspaceID string `json:"workspace_id"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	if strings.TrimSpace(args.File) == "" {
		return marshalErrorResult("file is required"), nil
	}
	workspaceID, root, rpc := h.indexWorkspaceRoot(ctx, args.WorkspaceID, false)
	if rpc != nil {
		return rpcResult(rpc), nil
	}
	res, err := h.codeIndex.Summary(ctx, workspaceID, root, args.File)
	if err != nil {
		return mapIndexError("summary", err), nil
	}
	return marshalJSONResult(res)
}

func (h *handler) handleIndexSearch(ctx context.Context, raw json.RawMessage) (json.RawMessage, *RPCError) {
	var args struct {
		Query       string `json:"query"`
		Kind        string `json:"kind"`
		Limit       int    `json:"limit"`
		WorkspaceID string `json:"workspace_id"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	if strings.TrimSpace(args.Query) == "" {
		return marshalErrorResult("query is required"), nil
	}
	// The root is resolved from workspace authorization, never trusted from args.
	workspaceID, root, rpc := h.indexWorkspaceRoot(ctx, args.WorkspaceID, false)
	if rpc != nil {
		return rpcResult(rpc), nil
	}
	res, err := h.codeIndex.Search(ctx, index.SearchRequest{
		WorkspaceID: workspaceID,
		Root:        root,
		Query:       args.Query,
		Kind:        args.Kind,
		Limit:       args.Limit,
	})
	if err != nil {
		return mapIndexError("search", err), nil
	}
	return marshalJSONResult(res)
}
