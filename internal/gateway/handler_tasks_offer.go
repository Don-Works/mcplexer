// handler_tasks_offer.go — universal task__offer / task__assign_remote /
// task__accept_offer / task__decline_offer / task__list_offers MCP
// handlers (Phase 3 cross-peer surface). Mirrors the shape of the
// memory handler's offer endpoints; share-service-not-wired and peer-
// not-found cases surface helpful messages rather than RPC errors.
package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/tasks"
)

// dispatchTaskOfferTool routes the cross-peer task__* tool names to
// their handlers. Returns (resp, err, handled). handled=false signals
// the parent dispatch should keep searching.
func (h *handler) dispatchTaskOfferTool(
	ctx context.Context, name string, raw json.RawMessage,
) (json.RawMessage, *RPCError, bool) {
	if h.tasksSvc == nil {
		switch name {
		case "task__offer", "task__assign_remote",
			"task__accept_offer", "task__decline_offer",
			"task__list_offers":
			return marshalErrorResult("Tasks subsystem is not enabled."), nil, true
		}
		return nil, nil, false
	}
	switch name {
	case "task__offer":
		resp, err := h.handleTaskOffer(ctx, raw, false)
		return resp, err, true
	case "task__assign_remote":
		resp, err := h.handleTaskOffer(ctx, raw, true)
		return resp, err, true
	case "task__accept_offer":
		resp, err := h.handleTaskAcceptOffer(ctx, raw)
		return resp, err, true
	case "task__decline_offer":
		resp, err := h.handleTaskDeclineOffer(ctx, raw)
		return resp, err, true
	case "task__list_offers":
		resp, err := h.handleTaskListOffers(ctx, raw)
		return resp, err, true
	}
	return nil, nil, false
}

// handleTaskOffer is the shared entrypoint for task__offer and
// task__assign_remote — same shape, the directAssign flag toggles
// is_direct_assign on the wire.
func (h *handler) handleTaskOffer(
	ctx context.Context, raw json.RawMessage, directAssign bool,
) (json.RawMessage, *RPCError) {
	args, err := unmarshalRawObject(raw)
	if err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	to, _ := stringField(args, "to")
	taskID, _ := stringField(args, "task_id")
	message, _ := stringField(args, "message")
	if to == "" || taskID == "" {
		return nil, &RPCError{Code: CodeInvalidParams, Message: "to and task_id are required"}
	}
	wsID := h.currentWorkspaceID(ctx)
	if wsID == "" {
		return marshalErrorResult("No workspace bound to this session — open a terminal in a project directory."), nil
	}
	if rpc := h.requireWorkspaceWrite(ctx, wsID); rpc != nil {
		return nil, rpc
	}
	peerID := h.resolvePeerAddress(ctx, to)
	if peerID == "" {
		return marshalErrorResult(fmt.Sprintf("No paired peer matches %q.", to)), nil
	}
	row, err := h.tasksSvc.Offer(ctx, tasks.OfferOptions{
		WorkspaceID:  wsID,
		TaskID:       taskID,
		ToPeerID:     peerID,
		Message:      message,
		BySessionID:  h.sessions.sessionID(),
		DirectAssign: directAssign,
	})
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return marshalErrorResult("Task not found in this workspace."), nil
		}
		// Surface the wire error to the agent but include the row id so
		// they can retry against it later.
		if row != nil {
			return marshalErrorResult(fmt.Sprintf("Offer recorded (id=%s) but wire send failed: %v", row.ID, err)), nil
		}
		return marshalErrorResult(fmt.Sprintf("Offer failed: %v", err)), nil
	}
	return marshalJSONResult(row)
}

// handleTaskAcceptOffer pulls the full payload from the offering peer
// and creates the local task. The `workspace` arg is optional unless
// no binding exists for the (peer, remote_workspace) pair.
func (h *handler) handleTaskAcceptOffer(
	ctx context.Context, raw json.RawMessage,
) (json.RawMessage, *RPCError) {
	args, err := unmarshalRawObject(raw)
	if err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	offerID, _ := stringField(args, "offer_id")
	workspace, _ := stringField(args, "workspace")
	if offerID == "" {
		return nil, &RPCError{Code: CodeInvalidParams, Message: "offer_id is required"}
	}
	if _, ok := workerWorkspaceAccessFromContext(ctx); ok && strings.TrimSpace(workspace) == "" {
		workspace = h.currentWorkspaceID(ctx)
	}
	wsID, errResult := h.resolveOfferWorkspace(ctx, workspace)
	if errResult != nil {
		return errResult, nil
	}
	if wsID != "" {
		if rpc := h.requireWorkspaceWrite(ctx, wsID); rpc != nil {
			return nil, rpc
		}
	}
	t, err := h.tasksSvc.AcceptOffer(ctx, offerID, wsID)
	if err != nil {
		if errors.Is(err, tasks.ErrBindingRequired) {
			return marshalErrorResult(
				"First offer from this peer/workspace — pass `workspace: <local-ws-name>` to bind the future offers."), nil
		}
		if errors.Is(err, store.ErrNotFound) {
			return marshalErrorResult("Offer not found."), nil
		}
		return marshalErrorResult(fmt.Sprintf("Accept failed: %v", err)), nil
	}
	return h.marshalTaskWithEnvelope(ctx, t, t.WorkspaceID, envelopeModeNone)
}

// handleTaskDeclineOffer marks the offer declined.
func (h *handler) handleTaskDeclineOffer(
	ctx context.Context, raw json.RawMessage,
) (json.RawMessage, *RPCError) {
	args, err := unmarshalRawObject(raw)
	if err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	offerID, _ := stringField(args, "offer_id")
	reason, _ := stringField(args, "reason")
	if offerID == "" {
		return nil, &RPCError{Code: CodeInvalidParams, Message: "offer_id is required"}
	}
	if _, ok := workerWorkspaceAccessFromContext(ctx); ok {
		offer, err := h.tasksSvc.GetOffer(ctx, offerID)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return marshalErrorResult("Offer not found."), nil
			}
			return marshalErrorResult(fmt.Sprintf("Decline failed: %v", err)), nil
		}
		if offer.WorkspaceID != "" {
			if rpc := h.requireWorkspaceWrite(ctx, offer.WorkspaceID); rpc != nil {
				return nil, rpc
			}
		}
	}
	if err := h.tasksSvc.DeclineOffer(ctx, offerID, reason); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return marshalErrorResult("Offer not found."), nil
		}
		return marshalErrorResult(fmt.Sprintf("Decline failed: %v", err)), nil
	}
	return marshalToolResult(fmt.Sprintf("Declined offer %s.", offerID)), nil
}

// handleTaskListOffers lists offers via the store filter shape.
// Defaults to state="pending" (the actionable set) — pass state:"any"
// (or "all") to restore the unfiltered listing. The response always
// carries expired_count so callers can see how many offers the TTL
// sweep already retired without re-querying.
func (h *handler) handleTaskListOffers(
	ctx context.Context, raw json.RawMessage,
) (json.RawMessage, *RPCError) {
	args, err := unmarshalRawObject(raw)
	if err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	direction, _ := stringField(args, "direction")
	state, _ := stringField(args, "state")
	state = normalizeOfferStateArg(state)
	peer, _ := stringField(args, "peer")
	sinceStr, _ := stringField(args, "since")
	limit, _ := intField(args, "limit")
	since, err := parseOptionalRFC3339(sinceStr)
	if err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: "since: " + err.Error()}
	}
	peerID := peer
	if peer != "" {
		if resolved := h.resolvePeerAddress(ctx, peer); resolved != "" {
			peerID = resolved
		}
	}
	f := store.TaskOfferFilter{
		Direction: direction,
		State:     state,
		PeerID:    peerID,
		Since:     since,
		Limit:     limit,
	}
	rows, err := h.tasksSvc.ListOffers(ctx, f)
	if err != nil {
		return marshalErrorResult(fmt.Sprintf("List offers failed: %v", err)), nil
	}
	if rows == nil {
		rows = []store.TaskOffer{}
	}
	rows = h.filterTaskOffersForWorker(ctx, rows)
	stateFilter := state
	if stateFilter == "" {
		stateFilter = "any"
	}
	return marshalJSONResult(map[string]any{
		"offers":        rows,
		"count":         len(rows),
		"state_filter":  stateFilter,
		"expired_count": h.countExpiredOffers(ctx, f),
	})
}

// normalizeOfferStateArg applies the pending-by-default contract:
// empty → "pending"; "any"/"all"/"*" → "" (no filter); anything else
// passes through unchanged.
func normalizeOfferStateArg(state string) string {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "":
		return store.TaskOfferPending
	case "any", "all", "*":
		return ""
	default:
		return strings.TrimSpace(state)
	}
}

// countExpiredOffers returns the number of state='expired' offers
// matching the caller's direction/peer/since filter. Best-effort: a
// query failure reads as 0 (the count is advisory).
func (h *handler) countExpiredOffers(ctx context.Context, f store.TaskOfferFilter) int {
	f.State = store.TaskOfferExpired
	f.Limit = 500
	rows, err := h.tasksSvc.ListOffers(ctx, f)
	if err != nil {
		return 0
	}
	return len(h.filterTaskOffersForWorker(ctx, rows))
}

func (h *handler) filterTaskOffersForWorker(ctx context.Context, rows []store.TaskOffer) []store.TaskOffer {
	if _, ok := workerWorkspaceAccessFromContext(ctx); !ok {
		return rows
	}
	allowed := map[string]bool{}
	for _, id := range h.readableWorkspaceIDs(ctx) {
		allowed[id] = true
	}
	out := make([]store.TaskOffer, 0, len(rows))
	for _, row := range rows {
		if row.WorkspaceID == "" || allowed[row.WorkspaceID] {
			out = append(out, row)
		}
	}
	return out
}

// resolvePeerAddress accepts either a device name or a raw peer id. We
// pass through obvious peer ids (long base58 starting with "12D3" or
// similar) and only invoke the mesh resolver for short device-name
// inputs.
func (h *handler) resolvePeerAddress(ctx context.Context, toAddr string) string {
	to := strings.TrimSpace(toAddr)
	if to == "" {
		return ""
	}
	if h.mesh == nil {
		// No mesh resolver — best we can do is pass through.
		return to
	}
	if resolved := h.mesh.ResolveDeviceName(ctx, to); resolved != "" {
		return resolved
	}
	// ResolveDeviceName falls back to "return input" semantics on its own
	// implementations, but defensively pass-through here too so the wire
	// dial gets an attempt rather than a silent empty-string return.
	return to
}

// resolveOfferWorkspace turns a `workspace` arg (name or id) into a
// local workspace id. Returns ("", nil) when the arg was omitted —
// the service layer will then consult workspace_peer_bindings.
func (h *handler) resolveOfferWorkspace(
	ctx context.Context, workspace string,
) (string, json.RawMessage) {
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		return "", nil
	}
	if h.store == nil {
		return workspace, nil
	}
	// Try id first — workspace ids look like ULID strings; if the
	// lookup misses, fall through to name lookup.
	if ws, err := h.store.GetWorkspace(ctx, workspace); err == nil && ws != nil {
		return ws.ID, nil
	}
	if ws, err := h.store.GetWorkspaceByName(ctx, workspace); err == nil && ws != nil {
		return ws.ID, nil
	}
	return "", marshalErrorResult(fmt.Sprintf("Unknown workspace %q.", workspace))
}

// silence unused-time import lint when only some code paths reference
// time — keeps the file lean across iterative additions.
var _ = time.Now
