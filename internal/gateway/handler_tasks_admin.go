// handler_tasks_admin.go — CWD-gated task admin tools (Phase 5).
// Three tools, surfaced only when the agent's session CWD is at or
// under the data directory (the AdminCWDGate enforces both the
// tools/list filter and the per-call defence-in-depth check):
//
//   - task__consolidate_statuses  — propose a status merge plan
//   - task__apply_status_consolidation — rewrite tasks + vocab from a plan
//   - task__rebind_peer           — re-pair recovery across task tables
//
// The matching `task-status-consolidator` worker template (migration
// 064) is the heavier, model-driven counterpart operators can schedule
// for cases the heuristic can't resolve.
package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/don-works/mcplexer/internal/tasks"
)

// taskAdminToolDefinitions returns the admin task tool descriptors.
// Descriptions intentionally include synonyms ("consolidate", "merge",
// "clean up statuses", "rebind", "re-pair", "rotate peer", "after
// device key rotation") so an agent searching with mcpx__search_tools
// finds them on the first try.
func taskAdminToolDefinitions() []Tool {
	return []Tool{
		{
			Name:        "task__consolidate_statuses",
			Description: "Consolidate / merge / clean up the freeform task status vocabulary in a workspace. Lists every distinct `status` currently in use (with counts), shows the workspace's existing `task_status_vocabulary` entries, and proposes a deterministic heuristic merge plan ('in-progress' → 'doing', 'finished' → 'done', etc.). Admin-only (CWD must be under ~/.mcplexer). Dry-run by default — the proposed plan is returned for review and applied via `task__apply_status_consolidation`. For richer semantic clustering, schedule the bundled `task-status-consolidator` worker template instead.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"workspace": {"type": "string", "description": "Workspace ID or name. Required."},
					"dry_run":   {"type": "boolean", "description": "Default true; reserved for future apply-after-confirm shortcut."}
				},
				"required": ["workspace"]
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "Consolidate Task Statuses",
				ReadOnlyHint:    boolPtr(true),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(true),
				OpenWorldHint:   boolPtr(false),
			}),
		},
		{
			Name:        "task__apply_status_consolidation",
			Description: "Apply / commit / execute a status consolidation plan returned by `task__consolidate_statuses` (or hand-edited from one). Rewrites every task in the workspace whose `status` matches a `from` to the canonical `to`, then upserts the canonical name into `task_status_vocabulary` with the requested `terminal` flag. Admin-only. Returns per-canonical row counts so you can audit the apply.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"workspace": {"type": "string", "description": "Workspace ID or name. Required."},
					"plan": {
						"description": "Plan object {\"merges\":[{\"from\":\"...\",\"to\":\"...\",\"terminal\":bool}]} OR a bare array of merges.",
						"type": ["object","array"]
					}
				},
				"required": ["workspace", "plan"]
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "Apply Status Consolidation",
				ReadOnlyHint:    boolPtr(false),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(true),
				OpenWorldHint:   boolPtr(false),
			}),
		},
		{
			Name:        "task__rebind_peer",
			Description: "Rebind / re-pair / rotate peer id across task tables — admin recovery tool for after a peer's device key rotates or you reissue their pairing. Atomically rewrites every reference to `old_peer_id` in `tasks` (assignee, origin, assigned_by), `task_offers` (from, to) and `workspace_peer_bindings` to `new_peer_id`. Admin-only. Returns per-table row counts. Use after `mesh__grant_peer_scope` re-establishes the trust relationship for the new id.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"old_peer_id": {"type": "string", "description": "Existing peer id that should be replaced."},
					"new_peer_id": {"type": "string", "description": "Replacement peer id."}
				},
				"required": ["old_peer_id", "new_peer_id"]
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "Rebind Peer (Task Tables)",
				ReadOnlyHint:    boolPtr(false),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(true),
				OpenWorldHint:   boolPtr(false),
			}),
		},
	}
}

// dispatchTaskAdminTool routes the three admin tools. Returns
// (response, error, handled) so the caller can short-circuit when the
// name is one of ours.
func (h *handler) dispatchTaskAdminTool(
	ctx context.Context, name string, raw json.RawMessage,
) (json.RawMessage, *RPCError, bool) {
	if !taskAdminTools[name] {
		return nil, nil, false
	}
	if h.tasksSvc == nil {
		return marshalErrorResult("Tasks subsystem is not enabled."), nil, true
	}
	if _, ok := workerWorkspaceAccessFromContext(ctx); ok && name == "task__rebind_peer" {
		return marshalErrorResult("task__rebind_peer is not available to workers; it rewrites peer bindings across workspaces."), nil, true
	}
	switch name {
	case "task__consolidate_statuses":
		resp, err := h.handleTaskConsolidateStatuses(ctx, raw)
		return resp, err, true
	case "task__apply_status_consolidation":
		resp, err := h.handleTaskApplyStatusConsolidation(ctx, raw)
		return resp, err, true
	case "task__rebind_peer":
		resp, err := h.handleTaskRebindPeer(ctx, raw)
		return resp, err, true
	}
	return nil, nil, false
}

// resolveAdminWorkspaceID accepts either a workspace ID or a workspace
// name and returns the canonical ID. Admin tools default to the
// session's CWD-resolved workspace when the arg is empty — but
// workspace-name resolution still needs the store.
func (h *handler) resolveAdminWorkspaceID(ctx context.Context, raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		if id := h.currentWorkspaceID(ctx); id != "" {
			return id, nil
		}
		return "", errors.New("workspace is required (no workspace bound to this session)")
	}
	if h.store == nil {
		return raw, nil
	}
	// Try by ID first (cheap).
	if ws, err := h.store.GetWorkspace(ctx, raw); err == nil && ws != nil {
		return ws.ID, nil
	}
	// Fall back to name lookup.
	if ws, err := h.store.GetWorkspaceByName(ctx, raw); err == nil && ws != nil {
		return ws.ID, nil
	}
	// Accept the raw string — the service layer reports cleanly if it's
	// unknown (zero counts, empty vocab).
	return raw, nil
}

func (h *handler) handleTaskConsolidateStatuses(
	ctx context.Context, raw json.RawMessage,
) (json.RawMessage, *RPCError) {
	args, err := unmarshalRawObject(raw)
	if err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	wsArg, _ := stringField(args, "workspace")
	wsID, err := h.resolveAdminWorkspaceID(ctx, wsArg)
	if err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	if rpc := h.requireWorkspaceRead(ctx, wsID); rpc != nil {
		return nil, rpc
	}
	prop, err := h.tasksSvc.ConsolidateStatusesDryRun(ctx, wsID)
	if err != nil {
		return marshalErrorResult(fmt.Sprintf("Consolidate failed: %v", err)), nil
	}
	return marshalJSONResult(prop)
}

func (h *handler) handleTaskApplyStatusConsolidation(
	ctx context.Context, raw json.RawMessage,
) (json.RawMessage, *RPCError) {
	args, err := unmarshalRawObject(raw)
	if err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	wsArg, _ := stringField(args, "workspace")
	wsID, err := h.resolveAdminWorkspaceID(ctx, wsArg)
	if err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	if rpc := h.requireWorkspaceWrite(ctx, wsID); rpc != nil {
		return nil, rpc
	}
	planRaw, ok := args["plan"]
	if !ok || len(planRaw) == 0 {
		return nil, &RPCError{Code: CodeInvalidParams, Message: "plan is required"}
	}
	merges, err := tasks.ParseConsolidationPlan(planRaw)
	if err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	res, err := h.tasksSvc.ApplyStatusConsolidation(ctx, wsID, merges, h.sessions.sessionID())
	if err != nil {
		return marshalErrorResult(fmt.Sprintf("Apply failed: %v", err)), nil
	}
	return marshalJSONResult(res)
}

func (h *handler) handleTaskRebindPeer(
	ctx context.Context, raw json.RawMessage,
) (json.RawMessage, *RPCError) {
	var args struct {
		OldPeerID string `json:"old_peer_id"`
		NewPeerID string `json:"new_peer_id"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	args.OldPeerID = strings.TrimSpace(args.OldPeerID)
	args.NewPeerID = strings.TrimSpace(args.NewPeerID)
	if args.OldPeerID == "" || args.NewPeerID == "" {
		return nil, &RPCError{Code: CodeInvalidParams, Message: "old_peer_id and new_peer_id are required"}
	}
	if args.OldPeerID == args.NewPeerID {
		return nil, &RPCError{Code: CodeInvalidParams, Message: "old_peer_id and new_peer_id must differ"}
	}
	counts, err := h.tasksSvc.RebindPeer(ctx, args.OldPeerID, args.NewPeerID)
	if err != nil {
		return marshalErrorResult(fmt.Sprintf("Rebind failed: %v", err)), nil
	}
	total := 0
	for _, n := range counts {
		total += n
	}
	return marshalJSONResult(map[string]any{
		"old_peer_id":  args.OldPeerID,
		"new_peer_id":  args.NewPeerID,
		"rows_updated": counts,
		"total":        total,
	})
}
