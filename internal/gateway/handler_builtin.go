package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/approval"
	"github.com/don-works/mcplexer/internal/downstream"
	"github.com/don-works/mcplexer/internal/routing"
	"github.com/don-works/mcplexer/internal/store"
)

func (h *handler) handleBuiltinCall(
	ctx context.Context, req CallToolRequest,
) (json.RawMessage, *RPCError) {
	switch req.Name {
	case "mcpx__search_tools":
		var args struct {
			Queries    []string `json:"queries"`
			Limit      int      `json:"limit"`
			Detail     string   `json:"detail"`
			Namespaces []string `json:"namespaces"`
			Tool       string   `json:"tool"`
		}
		if len(req.Arguments) > 0 {
			if err := json.Unmarshal(req.Arguments, &args); err != nil {
				return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
			}
		}
		return h.handleDiscoverTools(ctx, args.Queries, args.Detail, args.Tool, args.Namespaces, args.Limit)

	case "mcpx__search_recipes":
		return h.handleSearchRecipes(ctx, req.Arguments)
	case "mcpx__recipe_stats":
		return h.handleRecipeStats(ctx, req.Arguments)

	case "mcpx__context_cost_stats":
		return h.handleContextCostStats(ctx)

	case "mcpx__delegate_worker":
		return h.handleDelegateWorker(ctx, req.Arguments)

	case "mcpx__list_delegations":
		return h.handleListDelegations(ctx, req.Arguments)

	case "mcpx__extend_delegation_budget":
		return h.handleExtendDelegationBudget(ctx, req.Arguments)

	case "mcpx__invoke_model":
		return h.handleInvokeModel(ctx, req.Arguments)

	case "mcpx__wait_for_delegation":
		return h.handleWaitForDelegation(ctx, req.Arguments)

	case "mcpx__list_delegation_model_capacity":
		return h.handleListDelegationModelCapacity(ctx, req.Arguments)

	case "mcpx__review_delegation":
		return h.handleReviewDelegation(ctx, req.Arguments)

	case "mcpx__list_pending_approvals":
		return h.handleListPendingApprovals()

	case "mcpx__approve_tool_call":
		var args struct {
			ApprovalID string `json:"approval_id"`
			Reason     string `json:"reason"`
		}
		if err := json.Unmarshal(req.Arguments, &args); err != nil {
			return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
		}
		return h.handleResolveApproval(args.ApprovalID, args.Reason, true)

	case "mcpx__deny_tool_call":
		var args struct {
			ApprovalID string `json:"approval_id"`
			Reason     string `json:"reason"`
		}
		if err := json.Unmarshal(req.Arguments, &args); err != nil {
			return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
		}
		if args.Reason == "" {
			return nil, &RPCError{Code: CodeInvalidParams, Message: "reason is required for denial"}
		}
		return h.handleResolveApproval(args.ApprovalID, args.Reason, false)

	case "mesh__send":
		return h.handleMeshSend(ctx, req.Arguments)

	case "mesh__receive":
		return h.handleMeshReceive(ctx, req.Arguments)

	case "mesh__wait_for_event":
		return h.handleMeshWaitForEvent(ctx, req.Arguments)

	case "mesh__hydrate":
		return h.handleMeshHydrate(ctx, req.Arguments)

	case "mesh__thread":
		return h.handleMeshThread(ctx, req.Arguments)

	case "mesh__offer_skill":
		return h.handleMeshOfferSkill(ctx, req.Arguments)

	case "mesh__request_skill":
		return h.handleMeshRequestSkill(ctx, req.Arguments)

	case "mesh__skill_hub_index":
		return h.handleMeshSkillHubIndex(ctx, req.Arguments)

	case "mesh__skill_hub_search":
		return h.handleMeshSkillHubSearch(ctx, req.Arguments)

	case "mesh__skill_hub_pull":
		return h.handleMeshSkillHubPull(ctx, req.Arguments)

	case "mesh__set_device_name":
		return h.handleMeshSetDeviceName(ctx, req.Arguments)

	case "mesh__send_secret":
		return h.handleMeshSendSecret(ctx, req.Arguments)
	case "mesh__list_pending_secrets":
		return h.handleMeshListPendingSecrets(ctx, req.Arguments)
	case "mesh__accept_secret":
		return h.handleMeshAcceptSecret(ctx, req.Arguments)
	case "mesh__reject_secret":
		return h.handleMeshRejectSecret(ctx, req.Arguments)

	case "mesh__push_skill":
		return h.handleMeshPushSkill(ctx, req.Arguments)
	case "mesh__list_pending_skills":
		return h.handleMeshListPendingSkills(ctx, req.Arguments)
	case "mesh__accept_skill":
		return h.handleMeshAcceptSkill(ctx, req.Arguments)
	case "mesh__reject_skill":
		return h.handleMeshRejectSkill(ctx, req.Arguments)

	case "mesh__set_agent_status":
		return h.handleMeshSetAgentStatus(ctx, req.Arguments)

	case "mesh__list_peers":
		return h.handleMeshListPeers(ctx)

	case "mesh__list_agents":
		return h.handleMeshListAgents(ctx)

	case "mesh__list_queue":
		return h.handleMeshListQueue(ctx)

	case "mesh__grant_peer_scope":
		return h.handleMeshGrantPeerScope(ctx, req.Arguments)

	case "mesh__revoke_peer_scope":
		return h.handleMeshRevokePeerScope(ctx, req.Arguments)

	case "mcpx__suggest_description":
		return h.handleSuggestDescription(ctx, req.Arguments)

	case "mcpx__skill_search":
		return h.handleSkillSearch(ctx, req.Arguments)

	case "mcpx__skill_get":
		return h.handleSkillGet(ctx, req.Arguments)

	case "mcpx__skill_export":
		return h.handleSkillExport(ctx, req.Arguments)

	case "mcpx__skill_import":
		return h.handleSkillImport(ctx, req.Arguments)

	case "mcpx__skill_publish":
		return h.handleSkillPublish(ctx, req.Arguments)

	case "mcpx__skill_install":
		return h.handleSkillInstall(ctx, req.Arguments)

	case "mcpx__skill_list":
		return h.handleSkillList(ctx, req.Arguments)

	case "mcpx__skill_inventory":
		return h.handleSkillInventory(ctx, req.Arguments)

	case "mcpx__skill_diff":
		return h.handleSkillDiff(ctx, req.Arguments)

	case "mcpx__skill_push":
		return h.handleSkillPush(ctx, req.Arguments)

	case "mcpx__skill_pull":
		return h.handleSkillPull(ctx, req.Arguments)

	case "memory__save", "memory__recall", "memory__recall_about",
		"memory__list", "memory__list_entities",
		"memory__related_entities", "memory__spreading_activation",
		"memory__co_recalled", "memory__suggestions",
		"memory__link_entity", "memory__unlink_entity",
		"memory__get", "memory__invalidate", "memory__pin", "memory__unpin",
		"memory__forget", "memory__forget_by_source",
		"memory__offer_memory", "memory__request_memory",
		"memory__import_harness", "memory__sync_status":
		resp, rpcErr, _ := h.dispatchMemoryTool(ctx, req.Name, req.Arguments)
		return resp, rpcErr

	case "data__ingest", "data__list", "data__describe",
		"data__query", "data__search", "data__drop",
		"data__harvest_harness_context":
		resp, rpcErr, _ := h.dispatchDataTool(ctx, req.Name, req.Arguments)
		return resp, rpcErr

	case "kv__set", "kv__get", "kv__list", "kv__delete":
		resp, rpcErr, _ := h.dispatchKVTool(ctx, req.Name, req.Arguments)
		return resp, rpcErr

	case "concierge__record_signal":
		return h.handleConciergeRecordSignal(ctx, req.Arguments)

	case "task__create", "task__list", "task__get",
		"task__update", "task__assign", "task__claim", "task__delete",
		"task__append_note", "task__heartbeat", "task__set_work_context",
		"task__compose", "task__decompose",
		"task__recent_activity", "task__list_milestones",
		"task__attach", "task__list_attachments", "task__get_attachment",
		"task__offer", "task__assign_remote",
		"task__accept_offer", "task__decline_offer", "task__list_offers",
		"task_status_vocabulary__upsert":
		resp, rpcErr, _ := h.dispatchTaskTool(ctx, req.Name, req.Arguments)
		return resp, rpcErr

	case "task__consolidate_statuses",
		"task__apply_status_consolidation",
		"task__rebind_peer":
		resp, rpcErr, _ := h.dispatchTaskAdminTool(ctx, req.Name, req.Arguments)
		return resp, rpcErr

	case "brain__tree", "brain__list", "brain__get",
		"brain__search", "brain__write_note",
		"brain__list_people", "brain__get_person", "brain__write_person",
		"brain__delete_person":
		resp, rpcErr, _ := h.dispatchBrainTool(ctx, req.Name, req.Arguments)
		return resp, rpcErr

	case "skill__run_start":
		return h.handleSkillRunStart(ctx, req.Arguments)
	case "skill__phase":
		return h.handleSkillPhase(ctx, req.Arguments)
	case "skill__run_complete":
		return h.handleSkillRunComplete(ctx, req.Arguments)

	case "skill__propose_refinement":
		return h.handleSkillProposeRefinement(ctx, req.Arguments)
	case "skill__adopt_refinement":
		return h.handleSkillAdoptRefinement(ctx, req.Arguments)

	case "mcpx__flush_cache":
		var args struct {
			ServerID string `json:"server_id"`
		}
		if len(req.Arguments) > 0 {
			_ = json.Unmarshal(req.Arguments, &args)
		}
		return h.handleFlushCache(args.ServerID)

	case "mcpx__reload_server":
		var args struct {
			ServerID string `json:"server_id"`
		}
		if len(req.Arguments) > 0 {
			_ = json.Unmarshal(req.Arguments, &args)
		}
		return h.handleReloadServer(ctx, args.ServerID)

	case "mcpx__create_addon":
		return h.handleCreateAddon(ctx, req.Arguments)

	case "mcpx__import_openapi":
		return h.handleImportOpenAPI(ctx, req.Arguments)

	case "mcpx__provision_mcp":
		return h.handleProvisionMCP(ctx, req.Arguments)

	case "mcpx__execute_code":
		var args struct {
			Code string `json:"code"`
		}
		if err := json.Unmarshal(req.Arguments, &args); err != nil {
			return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
		}
		if args.Code == "" {
			return nil, &RPCError{Code: CodeInvalidParams, Message: "code is required. Example: {\"code\":\"const r = mcpx.search_tools({queries:[\\\"task create\\\"]}); print(r)\"}"}
		}
		return h.handleCodeExecute(ctx, args.Code)

	case "secret__prompt":
		return h.handleSecretPrompt(ctx, req.Arguments)

	case "secret__list_refs":
		return h.handleSecretListRefs(ctx, req.Arguments)

	case "mcpx__downstream_events_since":
		return h.handleDownstreamEventsSince(ctx, req.Arguments)

	case "mcpx__downstream_events_wait":
		return h.handleDownstreamEventsWait(ctx, req.Arguments)

	case "mcpx__downstream_events_batch":
		return h.handleDownstreamEventsBatch(ctx, req.Arguments)

	default:
		return nil, &RPCError{
			Code:    CodeMethodNotFound,
			Message: fmt.Sprintf("unknown built-in: %s", req.Name),
		}
	}
}

func (h *handler) handleFlushCache(serverID string) (json.RawMessage, *RPCError) {
	cc, ok := h.manager.(CachingCaller)
	if !ok {
		return marshalErrorResult("Cache system is not enabled."), nil
	}
	tc := cc.ToolCache()
	if serverID != "" {
		tc.InvalidateServer(serverID)
		return marshalToolResult(fmt.Sprintf("Flushed cache for server %q.", serverID)), nil
	}
	tc.Flush()
	return marshalToolResult("Flushed all tool call cache entries."), nil
}

// handleReloadServer re-introspects the tool catalog for one or all downstream
// servers, bypassing and then repopulating the in-memory tools/list cache and
// the DB CapabilitiesCache. A notifications/tools/list_changed notification is
// sent to the client on completion so the sandbox description refreshes.
func (h *handler) handleReloadServer(ctx context.Context, serverID string) (json.RawMessage, *RPCError) {
	// Flush the in-memory catalog cache so the next gather gets live data.
	h.toolsListCache.Flush()
	// Clear the per-key refresh timestamps so the next gather triggers an
	// immediate live re-introspection for every server group rather than waiting
	// out backgroundRefreshInterval.
	h.bgRefreshMu.Lock()
	h.bgRefreshAt = map[string]time.Time{}
	h.bgRefreshInFlight = map[string]bool{}
	h.bgRefreshMu.Unlock()

	var serverIDs []string
	if serverID != "" {
		// Validate the server exists.
		srv, err := h.store.GetDownstreamServer(ctx, serverID)
		if err != nil || srv == nil {
			return marshalErrorResult(fmt.Sprintf("Server %q not found.", serverID)), nil
		}
		serverIDs = []string{serverID}
	} else {
		servers, err := h.store.ListDownstreamServers(ctx)
		if err != nil {
			return nil, &RPCError{Code: CodeInternalError, Message: fmt.Sprintf("list servers: %v", err)}
		}
		for _, srv := range servers {
			if srv.Transport == "internal" || srv.Disabled || downstream.IsOnDemandOnlyServer(srv) {
				continue
			}
			serverIDs = append(serverIDs, srv.ID)
		}
	}

	if len(serverIDs) == 0 {
		return marshalToolResult("No downstream servers configured."), nil
	}

	evicted := 0
	if reloader, ok := h.manager.(ServerInstanceReloader); ok {
		for _, id := range serverIDs {
			evicted += reloader.ReloadServerInstances(id)
		}
	}
	result, err := h.manager.ListToolsForServers(ctx, serverIDs)
	if err != nil {
		return nil, &RPCError{Code: CodeInternalError, Message: fmt.Sprintf("re-introspect: %v", err)}
	}

	// Update DB capabilities cache for each server.
	for id, rawResult := range result {
		if updateErr := h.store.UpdateCapabilitiesCache(ctx, id, rawResult); updateErr != nil {
			slog.Warn("reload_server: failed to update capabilities cache",
				"server", id, "error", updateErr)
		}
	}

	// Notify the connected client that tool surface has changed.
	h.sendToolsListChanged()

	var counts []string
	for id, raw := range result {
		var r struct {
			Tools []json.RawMessage `json:"tools"`
		}
		_ = json.Unmarshal(raw, &r)
		counts = append(counts, fmt.Sprintf("%s (%d tools)", id, len(r.Tools)))
	}
	// Stable output order.
	sort.Strings(counts)
	msg := fmt.Sprintf("Reloaded %d server(s): %s. Evicted %d live instance(s); tool catalog refreshed; notifications/tools/list_changed sent.",
		len(counts), strings.Join(counts, ", "), evicted)
	return marshalToolResult(msg), nil
}

func (h *handler) handleListPendingApprovals() (json.RawMessage, *RPCError) {
	if h.approvals == nil {
		return marshalToolResult("Approval system is not enabled."), nil
	}

	pending := h.approvals.ListPending(h.sessions.sessionID())
	if len(pending) == 0 {
		return marshalToolResult("No pending approvals."), nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Found %d pending approval(s):\n", len(pending))
	for _, a := range pending {
		fmt.Fprintf(&b, "\n## %s\n", a.ID)
		fmt.Fprintf(&b, "Tool: %s\n", a.ToolName)
		fmt.Fprintf(&b, "Justification: %s\n", a.Justification)
		fmt.Fprintf(&b, "Requested by: %s (%s)\n", a.RequestClientType, a.RequestModel)
		fmt.Fprintf(&b, "Arguments: %s\n", a.Arguments)
		fmt.Fprintf(&b, "Created: %s\n", a.CreatedAt.Format(time.RFC3339))
	}
	return marshalToolResult(b.String()), nil
}

func (h *handler) handleResolveApproval(
	approvalID, reason string, approved bool,
) (json.RawMessage, *RPCError) {
	if h.approvals == nil {
		return marshalErrorResult("Approval system is not enabled."), nil
	}
	if approvalID == "" {
		return nil, &RPCError{Code: CodeInvalidParams, Message: "approval_id is required"}
	}

	err := h.approvals.Resolve(
		approvalID,
		h.sessions.sessionID(),
		"mcp_agent",
		reason,
		approved,
	)
	if err != nil {
		if errors.Is(err, approval.ErrSelfApproval) {
			return marshalErrorResult("You cannot approve your own tool call request."), nil
		}
		if errors.Is(err, approval.ErrAlreadyResolved) {
			return marshalErrorResult("This approval has already been resolved."), nil
		}
		return nil, &RPCError{Code: CodeInternalError, Message: err.Error()}
	}

	action := "denied"
	if approved {
		action = "approved"
	}
	return marshalToolResult(fmt.Sprintf("Tool call %s successfully %s.", approvalID, action)), nil
}

// handleApprovalGate implements two-phase approval interception.
// Phase 1: no _justification → return error asking for it.
// Phase 2: _justification present → block until approved/denied/timeout.
// Returns (nil, nil) when approved (caller should proceed to dispatch).
func (h *handler) handleApprovalGate(
	ctx context.Context,
	req CallToolRequest,
	route *routing.RouteResult,
	start time.Time,
) (json.RawMessage, *RPCError) {
	// Parse arguments to check for _justification.
	var args map[string]json.RawMessage
	if len(req.Arguments) > 0 {
		_ = json.Unmarshal(req.Arguments, &args)
	}

	justRaw, hasJust := args["_justification"]
	var justification string
	if hasJust {
		_ = json.Unmarshal(justRaw, &justification)
	}
	justification = strings.TrimSpace(justification)

	// Phase 1: no justification provided.
	if justification == "" {
		result := marshalErrorResult(
			"This tool requires approval before execution. " +
				"Retry your call with an additional `_justification` field " +
				"explaining why you need to use this tool.",
		)
		h.recordAuditBlocked(ctx, req.Name, req.Arguments, route, result, nil, start)
		return result, nil
	}

	// Phase 2: justification present — strip it from args and block.
	delete(args, "_justification")
	cleanArgs, _ := json.Marshal(args)
	req.Arguments = cleanArgs

	timeout := route.ApprovalTimeout
	if timeout <= 0 {
		timeout = 300
	}

	rec := &store.ToolApproval{
		RequestSessionID:   h.sessions.sessionID(),
		RequestClientType:  h.sessions.clientType(),
		RequestModel:       h.sessions.modelHint(),
		WorkspaceID:        h.currentWorkspaceID(ctx),
		WorkspaceName:      h.currentWorkspaceName(ctx),
		ToolName:           req.Name,
		Arguments:          string(cleanArgs),
		Justification:      justification,
		RouteRuleID:        route.MatchedRuleID,
		DownstreamServerID: route.DownstreamServerID,
		AuthScopeID:        route.AuthScopeID,
		TimeoutSec:         timeout,
	}

	approved, err := h.approvals.RequestApproval(ctx, rec)
	if err != nil {
		rpcErr := &RPCError{
			Code:    CodeInternalError,
			Message: fmt.Sprintf("approval request failed: %v", err),
		}
		h.recordAudit(ctx, req.Name, req.Arguments, route, nil, rpcErr, start)
		return nil, rpcErr
	}

	if !approved {
		result := marshalErrorResult(
			fmt.Sprintf("Tool call denied. Reason: %s", rec.Resolution),
		)
		h.recordAuditBlocked(ctx, req.Name, req.Arguments, route, result, nil, start)
		return result, nil
	}

	// Approved — return nil to signal caller to proceed with dispatch.
	return nil, nil
}
