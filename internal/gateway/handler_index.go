package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/don-works/mcplexer/internal/index"
)

// dispatchIndexTool routes the ten builtin index__* tools to their handlers.
// It returns handled=false for any non-index name so handleBuiltinCall can fall
// through. A nil index service (indexer not wired into this build) short-circuits
// every index tool with a clean, agent-readable "unavailable" result instead of
// panicking on the nil pointer.
func (h *handler) dispatchIndexTool(
	ctx context.Context, name string, raw json.RawMessage,
) (json.RawMessage, *RPCError, bool) {
	var fn func(context.Context, json.RawMessage) (json.RawMessage, *RPCError)
	switch name {
	case "index__build":
		fn = h.handleIndexBuild
	case "index__status":
		fn = h.handleIndexStatus
	case "index__symbols":
		fn = h.handleIndexSymbols
	case "index__deps":
		fn = h.handleIndexDeps
	case "index__tests_for":
		fn = h.handleIndexTestsFor
	case "index__summary":
		fn = h.handleIndexSummary
	case "index__recent_changes":
		fn = h.handleIndexRecentChanges
	case "index__map_failure":
		fn = h.handleIndexMapFailure
	case "index__context":
		fn = h.handleIndexContext
	case "index__search":
		fn = h.handleIndexSearch
	default:
		return nil, nil, false
	}
	if h.codeIndex == nil {
		return indexUnavailable(), nil, true
	}
	resp, rpcErr := fn(ctx, raw)
	return resp, rpcErr, true
}

// indexUnavailable is returned when no index.Service is wired (e.g. a slim build
// or a test handler without the indexer). The tools are still advertised, so the
// agent needs a clear reason rather than a silent failure.
func indexUnavailable() json.RawMessage {
	return marshalErrorResult("code index is unavailable on this build (indexer service not wired)")
}

// mapIndexError converts the index package sentinels into agent-friendly tool
// results. ErrBuildInProgress carries a retry hint; ErrNotBuilt tells the agent
// to build first; ErrRootUnsafe already reads as guidance ("run from a project
// workspace"); anything else surfaces as "<op> failed: <err>".
func mapIndexError(op string, err error) json.RawMessage {
	switch {
	case errors.Is(err, index.ErrBuildInProgress):
		return marshalErrorResult("code index build already in progress for this workspace — retry in a moment")
	case errors.Is(err, index.ErrNotBuilt):
		return marshalErrorResult("code index not built yet for this workspace — run index__build first")
	case errors.Is(err, index.ErrRootUnsafe):
		return marshalErrorResult(err.Error())
	default:
		return marshalErrorResult(fmt.Sprintf("%s failed: %v", op, err))
	}
}

// indexWorkspaceRoot resolves the workspace id and repo root for an index tool
// call. Access is gated exactly like dataWorkspace (requireWorkspaceRead/Write
// via the shared helper), then the resolved root is checked against the D8
// safety gate: an empty root, "/", or a missing directory is rejected with a
// structured message telling the agent to run from a project workspace.
func (h *handler) indexWorkspaceRoot(
	ctx context.Context, override string, write bool,
) (workspaceID, root string, rpc *RPCError) {
	workspaceID, rpc = h.dataWorkspace(ctx, override, write)
	if rpc != nil {
		return "", "", rpc
	}
	root = h.indexRootFor(ctx, workspaceID)
	if msg := indexRootError(root); msg != "" {
		return "", "", &RPCError{Code: CodeInvalidRequest, Message: msg}
	}
	return workspaceID, root, nil
}

// indexRootFor resolves a workspace's on-disk root: prefer the routing ancestor
// chain (covers the current session and worker grants, which carry RootPath),
// falling back to a direct store lookup for an override outside that chain.
func (h *handler) indexRootFor(ctx context.Context, workspaceID string) string {
	for _, a := range h.routingWorkspaceAncestors(ctx) {
		if a.ID == workspaceID && strings.TrimSpace(a.RootPath) != "" {
			return a.RootPath
		}
	}
	if ws, err := h.store.GetWorkspace(ctx, workspaceID); err == nil && ws != nil {
		return ws.RootPath
	}
	return ""
}

// indexRootError enforces the D8 root-safety gate. Returns "" when the root is a
// usable project directory, else an agent-facing explanation.
func indexRootError(root string) string {
	trimmed := strings.TrimSpace(root)
	if trimmed == "" || trimmed == "/" {
		return "code index needs a project workspace: this session's workspace root is empty or \"/\". " +
			"Open your agent in a project directory (a git repo), or pass workspace_id for a project workspace."
	}
	info, err := os.Stat(trimmed)
	if err != nil || !info.IsDir() {
		return fmt.Sprintf("code index root %q is not an existing directory — run from a project workspace", trimmed)
	}
	return ""
}

func (h *handler) handleIndexBuild(ctx context.Context, raw json.RawMessage) (json.RawMessage, *RPCError) {
	var args struct {
		Paths       []string `json:"paths"`
		Force       bool     `json:"force"`
		WorkspaceID string   `json:"workspace_id"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	workspaceID, root, rpc := h.indexWorkspaceRoot(ctx, args.WorkspaceID, true)
	if rpc != nil {
		return rpcResult(rpc), nil
	}
	res, err := h.codeIndex.Build(ctx, index.BuildRequest{
		WorkspaceID: workspaceID, Root: root, Paths: args.Paths, Force: args.Force,
	})
	if err != nil {
		return mapIndexError("build", err), nil
	}
	return marshalJSONResult(res)
}

func (h *handler) handleIndexStatus(ctx context.Context, raw json.RawMessage) (json.RawMessage, *RPCError) {
	var args struct {
		WorkspaceID string `json:"workspace_id"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	workspaceID, root, rpc := h.indexWorkspaceRoot(ctx, args.WorkspaceID, false)
	if rpc != nil {
		return rpcResult(rpc), nil
	}
	res, err := h.codeIndex.Status(ctx, workspaceID, root)
	if err != nil {
		return mapIndexError("status", err), nil
	}
	return marshalJSONResult(res)
}
