// handler_brain.go — implements the agent-facing brain__* MCP tools.
// Reads project the derived SQLite index (tree, list, get, search); the single
// write tool persists a free-form note through the canonical outbound
// Serializer. When the brain subsystem is disabled (nil Editor) every tool
// returns a clear tool-level error rather than panicking.
package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/don-works/mcplexer/internal/brain"
	"github.com/don-works/mcplexer/internal/store"
)

// dispatchBrainTool routes brain__* tool names to their handlers. It returns
// (response, rpcError, handled) so the main built-in dispatcher mirrors the
// task/memory shape. When the brain is disabled it still handles the call,
// returning a graceful tool-level error.
func (h *handler) dispatchBrainTool(
	ctx context.Context, name string, raw json.RawMessage,
) (json.RawMessage, *RPCError, bool) {
	if h.brainEditor == nil {
		return marshalErrorResult("Brain subsystem is not enabled."), nil, true
	}
	switch name {
	case "brain__tree":
		resp, err := h.handleBrainTree(ctx)
		return resp, err, true
	case "brain__list":
		resp, err := h.handleBrainList(ctx, raw)
		return resp, err, true
	case "brain__get":
		resp, err := h.handleBrainGet(ctx, raw)
		return resp, err, true
	case "brain__search":
		resp, err := h.handleBrainSearch(ctx, raw)
		return resp, err, true
	case "brain__write_note":
		resp, err := h.handleBrainWriteNote(ctx, raw)
		return resp, err, true
	case "brain__list_people":
		resp, err := h.handleBrainListPeople(ctx, raw)
		return resp, err, true
	case "brain__get_person":
		resp, err := h.handleBrainGetPerson(ctx, raw)
		return resp, err, true
	case "brain__write_person":
		resp, err := h.handleBrainWritePerson(ctx, raw)
		return resp, err, true
	case "brain__delete_person":
		resp, err := h.handleBrainDeletePerson(ctx, raw)
		return resp, err, true
	default:
		return nil, nil, false
	}
}

// handleBrainTree returns the workspace browser tree with live counts.
func (h *handler) handleBrainTree(ctx context.Context) (json.RawMessage, *RPCError) {
	nodes, err := h.brainEditor.Tree(ctx)
	if err != nil {
		return nil, &RPCError{Code: CodeInternalError, Message: fmt.Sprintf("brain tree: %v", err)}
	}
	nodes = h.filterBrainTreeForSession(ctx, nodes)
	return marshalBrainJSON(map[string]any{"workspaces": nodes})
}

// handleBrainList lists tasks and/or memories for one workspace. An empty
// kind lists BOTH kinds rather than erroring — a bare brain__list call is a
// legitimate "show me everything here" probe, not a mistake.
func (h *handler) handleBrainList(ctx context.Context, raw json.RawMessage) (json.RawMessage, *RPCError) {
	var args struct {
		Kind      string `json:"kind"`
		Workspace string `json:"workspace"`
	}
	if rpcErr := unmarshalBrainArgs(raw, &args); rpcErr != nil {
		return nil, rpcErr
	}
	if strings.TrimSpace(args.Workspace) == "" {
		args.Workspace = h.currentWorkspaceID(ctx)
	}
	if rpc := h.requireBrainWorkspace(ctx, args.Workspace, false); rpc != nil {
		return nil, rpc
	}
	out := map[string]any{}
	kind := strings.TrimSpace(args.Kind)
	if kind == brain.EntityKindTask || kind == "" {
		rows, err := h.brainEditor.ListTasks(ctx, args.Workspace)
		if err != nil {
			return nil, &RPCError{Code: CodeInternalError, Message: fmt.Sprintf("brain list tasks: %v", err)}
		}
		out["tasks"] = rows
	}
	if kind == brain.EntityKindMemory || kind == "" {
		rows, err := h.brainEditor.ListMemories(ctx, args.Workspace)
		if err != nil {
			return nil, &RPCError{Code: CodeInternalError, Message: fmt.Sprintf("brain list memories: %v", err)}
		}
		out["memories"] = rows
	}
	if len(out) == 0 {
		return marshalErrorResult("kind must be 'task' or 'memory' (omit kind to list both)."), nil
	}
	return marshalBrainJSON(out)
}

// handleBrainGet fetches one task or memory by id, including its raw .md.
func (h *handler) handleBrainGet(ctx context.Context, raw json.RawMessage) (json.RawMessage, *RPCError) {
	var args struct {
		Kind string `json:"kind"`
		ID   string `json:"id"`
	}
	if rpcErr := unmarshalBrainArgs(raw, &args); rpcErr != nil {
		return nil, rpcErr
	}
	if args.ID == "" {
		return marshalErrorResult("id is required."), nil
	}
	switch args.Kind {
	case brain.EntityKindTask:
		rec, err := h.brainEditor.GetTaskDetail(ctx, args.ID)
		if err == nil && rec != nil {
			if rpc := h.requireBrainWorkspace(ctx, rec.Workspace, false); rpc != nil {
				return nil, rpc
			}
		}
		return brainRecordResult(rec, err)
	case brain.EntityKindMemory:
		rec, err := h.brainEditor.GetMemoryDetail(ctx, args.ID)
		if err == nil && rec != nil {
			if rpc := h.requireBrainWorkspace(ctx, rec.Workspace, false); rpc != nil {
				return nil, rpc
			}
		}
		return brainRecordResult(rec, err)
	default:
		return marshalErrorResult("kind must be 'task' or 'memory'."), nil
	}
}

// handleBrainSearch runs the three-tier frecency intellisense search.
func (h *handler) handleBrainSearch(ctx context.Context, raw json.RawMessage) (json.RawMessage, *RPCError) {
	var args struct {
		Q         string `json:"q"`
		Kind      string `json:"kind"`
		Workspace string `json:"workspace"`
		Limit     int    `json:"limit"`
	}
	if rpcErr := unmarshalBrainArgs(raw, &args); rpcErr != nil {
		return nil, rpcErr
	}
	if strings.TrimSpace(args.Q) == "" {
		// Loud, not silent: an empty q used to return an empty result set,
		// which burned a live debugging session when the caller passed
		// `query` instead of `q`.
		return marshalErrorResult("q is required (did you pass 'query' instead of 'q'?)."), nil
	}
	if args.Limit <= 0 {
		args.Limit = 20
	}
	if args.Limit > 100 {
		args.Limit = 100
	}
	if strings.TrimSpace(args.Workspace) == "" {
		args.Workspace = h.currentWorkspaceID(ctx)
	}
	if rpc := h.requireBrainWorkspace(ctx, args.Workspace, false); rpc != nil {
		return nil, rpc
	}
	res, err := h.brainEditor.Search(ctx, args.Q, args.Kind, args.Workspace, args.Limit)
	if err != nil {
		return nil, &RPCError{Code: CodeInternalError, Message: fmt.Sprintf("brain search: %v", err)}
	}
	return marshalBrainJSON(res)
}

// handleBrainWriteNote persists a free-form Markdown note via the Editor.
func (h *handler) handleBrainWriteNote(ctx context.Context, raw json.RawMessage) (json.RawMessage, *RPCError) {
	var args struct {
		Name      string   `json:"name"`
		Content   string   `json:"content"`
		Workspace string   `json:"workspace"`
		Tags      []string `json:"tags"`
		Pinned    bool     `json:"pinned"`
	}
	if rpcErr := unmarshalBrainArgs(raw, &args); rpcErr != nil {
		return nil, rpcErr
	}
	if args.Name == "" || args.Content == "" {
		return marshalErrorResult("name and content are required."), nil
	}
	if args.Workspace == "" {
		if h.currentWorkspaceID(ctx) != "" {
			args.Workspace = h.currentWorkspaceID(ctx)
		} else {
			args.Workspace = "global"
		}
	}
	if rpc := h.requireBrainWorkspace(ctx, args.Workspace, true); rpc != nil {
		return nil, rpc
	}
	saved, err := h.brainEditor.SaveMemory(ctx, brain.MemoryRecord{
		Kind:      brain.MemoryKindNote,
		Name:      args.Name,
		Content:   args.Content,
		Workspace: args.Workspace,
		Tags:      args.Tags,
		Pinned:    args.Pinned,
	})
	if rpcErr := brainWriteError(err); rpcErr != nil {
		return rpcErr, nil
	}
	return marshalBrainJSON(map[string]any{
		"id":        saved.ID,
		"path":      saved.Path,
		"workspace": saved.Workspace,
		"saved":     true,
	})
}

// handleBrainListPeople lists CRM people records, newest first.
func (h *handler) handleBrainListPeople(ctx context.Context, raw json.RawMessage) (json.RawMessage, *RPCError) {
	var args struct {
		Workspace string `json:"workspace"`
		Limit     int    `json:"limit"`
	}
	if rpcErr := unmarshalBrainArgs(raw, &args); rpcErr != nil {
		return nil, rpcErr
	}
	if args.Limit <= 0 {
		args.Limit = 100
	}
	if args.Limit > 1000 {
		args.Limit = 1000
	}
	workspaceID, rpcErr := h.resolveBrainPersonWorkspace(ctx, args.Workspace, false)
	if rpcErr != nil {
		return nil, rpcErr
	}
	rows, err := h.brainEditor.ListPeople(ctx, workspaceID, args.Limit)
	if err != nil {
		return nil, &RPCError{Code: CodeInternalError, Message: fmt.Sprintf("brain list people: %v", err)}
	}
	return marshalBrainJSON(map[string]any{"workspace": workspaceID, "people": rows})
}

// handleBrainGetPerson fetches one CRM person by id, including its raw .md.
func (h *handler) handleBrainGetPerson(ctx context.Context, raw json.RawMessage) (json.RawMessage, *RPCError) {
	var args struct {
		ID string `json:"id"`
	}
	if rpcErr := unmarshalBrainArgs(raw, &args); rpcErr != nil {
		return nil, rpcErr
	}
	if args.ID == "" {
		return marshalErrorResult("id is required."), nil
	}
	rec, err := h.brainEditor.GetPersonDetail(ctx, args.ID)
	if err == nil && rec != nil {
		if rpcErr := h.requireWorkerWorkspaceAccess(ctx, rec.Workspace, false); rpcErr != nil {
			return nil, rpcErr
		}
	}
	return brainRecordResult(rec, err)
}

// handleBrainWritePerson creates or updates a CRM person via the Editor.
func (h *handler) handleBrainWritePerson(ctx context.Context, raw json.RawMessage) (json.RawMessage, *RPCError) {
	var args struct {
		Name      string               `json:"name"`
		Email     string               `json:"email"`
		Phone     string               `json:"phone"`
		Company   string               `json:"company"`
		Role      string               `json:"role"`
		Notes     string               `json:"notes"`
		Tags      []string             `json:"tags"`
		Pinned    bool                 `json:"pinned"`
		Entities  []brain.EntityLinkFM `json:"entities"`
		Workspace string               `json:"workspace"`
		ID        string               `json:"id"`
		IfHash    string               `json:"if_hash"`
	}
	if rpcErr := unmarshalBrainArgs(raw, &args); rpcErr != nil {
		return nil, rpcErr
	}
	if args.Name == "" {
		return marshalErrorResult("name is required."), nil
	}
	targetWorkspace := args.Workspace
	if args.ID != "" {
		if existing, err := h.brainEditor.GetPerson(ctx, args.ID); err == nil && existing != nil {
			if rpcErr := h.requireWorkerWorkspaceAccess(ctx, existing.Workspace, true); rpcErr != nil {
				return nil, rpcErr
			}
			if strings.TrimSpace(targetWorkspace) == "" {
				targetWorkspace = existing.Workspace
			}
		} else if err != nil && !errors.Is(err, store.ErrNotFound) {
			return nil, &RPCError{Code: CodeInternalError, Message: fmt.Sprintf("brain load person: %v", err)}
		}
	}
	workspaceID, rpcErr := h.resolveBrainPersonWorkspace(ctx, targetWorkspace, true)
	if rpcErr != nil {
		return nil, rpcErr
	}
	saved, err := h.brainEditor.SavePerson(ctx, brain.PersonRecord{
		ID:        args.ID,
		Workspace: workspaceID,
		Name:      args.Name,
		Email:     args.Email,
		Phone:     args.Phone,
		Company:   args.Company,
		Role:      args.Role,
		Notes:     args.Notes,
		Tags:      args.Tags,
		Pinned:    args.Pinned,
		Entities:  args.Entities,
		IfHash:    args.IfHash,
	})
	if rpcErr := brainWriteError(err); rpcErr != nil {
		return rpcErr, nil
	}
	return marshalBrainJSON(map[string]any{
		"id":        saved.ID,
		"path":      saved.Path,
		"workspace": saved.Workspace,
		"saved":     true,
	})
}

// handleBrainDeletePerson soft-deletes a CRM person and removes its .md file.
func (h *handler) handleBrainDeletePerson(ctx context.Context, raw json.RawMessage) (json.RawMessage, *RPCError) {
	var args struct {
		ID string `json:"id"`
	}
	if rpcErr := unmarshalBrainArgs(raw, &args); rpcErr != nil {
		return nil, rpcErr
	}
	if args.ID == "" {
		return marshalErrorResult("id is required."), nil
	}
	if rec, err := h.brainEditor.GetPerson(ctx, args.ID); err == nil && rec != nil {
		if rpcErr := h.requireWorkerWorkspaceAccess(ctx, rec.Workspace, true); rpcErr != nil {
			return nil, rpcErr
		}
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		return nil, &RPCError{Code: CodeInternalError, Message: fmt.Sprintf("brain load person: %v", err)}
	}
	if err := h.brainEditor.DeletePerson(ctx, args.ID); err != nil {
		return nil, &RPCError{Code: CodeInternalError, Message: fmt.Sprintf("brain delete person: %v", err)}
	}
	return marshalBrainJSON(map[string]any{"id": args.ID, "deleted": true})
}

func (h *handler) filterBrainTreeForSession(ctx context.Context, nodes []brain.TreeNode) []brain.TreeNode {
	allowed := map[string]bool{}
	for _, id := range h.readableWorkspaceIDs(ctx) {
		allowed[id] = true
	}
	if len(allowed) == 0 {
		return []brain.TreeNode{}
	}
	out := make([]brain.TreeNode, 0, len(nodes))
	for _, n := range nodes {
		if allowed[n.Workspace] {
			out = append(out, n)
		}
	}
	return out
}

func (h *handler) resolveBrainPersonWorkspace(ctx context.Context, raw string, write bool) (string, *RPCError) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "global" {
		raw = store.PersonDefaultWorkspaceID
	}
	workspaceID := raw
	if h.store != nil {
		if ws, err := h.store.GetWorkspace(ctx, raw); err == nil && ws != nil {
			workspaceID = ws.ID
		} else if ws, err := h.store.GetWorkspaceByName(ctx, raw); err == nil && ws != nil {
			workspaceID = ws.ID
		} else {
			return "", &RPCError{
				Code:    CodeInvalidRequest,
				Message: fmt.Sprintf("Unknown workspace %q.", raw),
			}
		}
	}
	if rpcErr := h.requireWorkerWorkspaceAccess(ctx, workspaceID, write); rpcErr != nil {
		return "", rpcErr
	}
	return workspaceID, nil
}

func (h *handler) requireBrainWorkspace(ctx context.Context, workspace string, write bool) *RPCError {
	workspace = strings.TrimSpace(workspace)
	if workspace == "" || workspace == "global" {
		return nil
	}
	if write {
		return h.requireWorkspaceWrite(ctx, workspace)
	}
	return h.requireWorkspaceRead(ctx, workspace)
}

// brainRecordResult marshals a detail record, mapping a not-found to a clear
// tool-level error rather than a 500.
func brainRecordResult(rec any, err error) (json.RawMessage, *RPCError) {
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return marshalErrorResult("Record not found."), nil
		}
		return nil, &RPCError{Code: CodeInternalError, Message: fmt.Sprintf("brain get: %v", err)}
	}
	return marshalBrainJSON(rec)
}

// brainWriteError maps the Editor's write-path errors onto tool-level results.
// A validation failure or a conflict is a user-actionable condition surfaced as
// an isError result; anything else is an internal RPC error.
func brainWriteError(err error) json.RawMessage {
	if err == nil {
		return nil
	}
	if errors.Is(err, brain.ErrConflict) {
		return marshalErrorResult(
			"Write conflicted with a concurrent on-disk edit; diverted to a .conflict sidecar. " +
				"Reconcile the file and retry.")
	}
	if errors.Is(err, brain.ErrValidation) {
		return marshalErrorResult(fmt.Sprintf("Validation failed: %v", err))
	}
	return marshalErrorResult(fmt.Sprintf("Brain write failed: %v", err))
}

// unmarshalBrainArgs decodes tool arguments, tolerating an empty body.
func unmarshalBrainArgs(raw json.RawMessage, v any) *RPCError {
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	return nil
}

// marshalBrainJSON serializes a value to a pretty JSON tool result.
func marshalBrainJSON(v any) (json.RawMessage, *RPCError) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, &RPCError{Code: CodeInternalError, Message: fmt.Sprintf("marshal: %v", err)}
	}
	return marshalToolResult(string(data)), nil
}
