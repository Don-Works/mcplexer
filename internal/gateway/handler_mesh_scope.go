package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/mesh"
	"github.com/don-works/mcplexer/internal/store"
)

// approvalKindMeshGrantConsent is the value emitted by the grant path so
// the recipient's dashboard can render an audit trail of which peer
// scopes they accepted (vs. the silent-grant pre-fix behaviour). Mirrors
// the JSON-side vocabulary documented on store.ToolApproval.Kind.
const approvalKindMeshGrantConsent = "mesh_grant_consent"

// recordMeshGrantConsent writes an already-resolved (status=approved)
// tool_approvals row of kind=mesh_grant_consent immediately after a
// successful peer-scope grant. The row is durable evidence that this
// user explicitly accepted the grant — distinct from the mesh-audit
// row (mesh__grant_peer_scope) which lives in a separate audit table
// and is not surfaced on the approval queue.
//
// Failures are non-fatal: the grant has already succeeded, so we log
// and move on rather than rolling back. The audit record (emitted by
// the caller) is the primary write; this is a UI convenience.
func (h *handler) recordMeshGrantConsent(ctx context.Context, peerID, scope string) {
	now := time.Now().UTC()
	resolvedAt := now
	rec := &store.ToolApproval{
		Status:               "approved",
		Kind:                 approvalKindMeshGrantConsent,
		Surface:              "mesh",
		RequestSessionID:     h.sessions.sessionID(),
		RequestClientType:    h.sessions.clientType(),
		RequestModel:         h.sessions.modelHint(),
		WorkspaceID:          h.currentWorkspaceID(ctx),
		WorkspaceName:        h.currentWorkspaceName(ctx),
		OriginatingWorkspace: h.currentWorkspaceID(ctx),
		ToolName:             "mesh__grant_peer_scope",
		Arguments:            fmt.Sprintf(`{"peer":%q,"scope":%q}`, peerID, scope),
		Summary:              fmt.Sprintf("Granted %s to peer %s", scope, peerID),
		Justification:        "consent recorded for peer-scope grant",
		ApproverSessionID:    h.sessions.sessionID(),
		ApproverType:         "system",
		Resolution:           "consent recorded",
		TimeoutSec:           0,
		CreatedAt:            now,
		ResolvedAt:           &resolvedAt,
	}
	if err := h.store.CreateToolApproval(ctx, rec); err != nil {
		slog.Warn("mesh_grant_consent: failed to record consent row",
			"peer", peerID, "scope", scope, "err", err)
		return
	}
	if h.approvals != nil {
		// Surface to the live approvals SSE stream so the dashboard can
		// fold the consent record into the Recent History pane without
		// a refetch. The Manager exposes a publish hook for already-
		// resolved rows that bypass the request/resolve cycle.
		h.approvals.PublishExternal(rec)
	}
}

// handleMeshGrantPeerScope grants a scope on a paired peer so the
// peer is authorized for actions like cross-peer skill share
// (mesh.skill_request). The remote peer must ALSO grant the same
// scope on their side — the protocol checks both directions before
// allowing the libp2p stream open.
//
// Args: { peer: "<peer_id-or-display-name>", scope: "<scope_name>" }
func (h *handler) handleMeshGrantPeerScope(ctx context.Context, args json.RawMessage) (json.RawMessage, *RPCError) {
	if h.mesh == nil {
		return marshalErrorResult("Agent mesh is not enabled."), nil
	}
	var req struct {
		Peer  string `json:"peer"`
		Scope string `json:"scope"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
		}
	}
	req.Peer = strings.TrimSpace(req.Peer)
	req.Scope = strings.TrimSpace(req.Scope)
	v := newValidator()
	v.requireStringWithHint("peer", req.Peer,
		"peer libp2p ID or display name — call mesh__list_peers")
	v.requireStringWithHint("scope", req.Scope,
		"scope name to grant (e.g. mesh.skill_request, mesh.memory_request)")
	if env, ok := v.envelope(); ok {
		return env, nil
	}
	if rpc := h.requireWorkerPeerScopeAccess(ctx, req.Scope); rpc != nil {
		return nil, rpc
	}
	peerID, resolveErr := h.resolveMeshPeer(ctx, req.Peer)
	if resolveErr != nil {
		return marshalErrorResult(mesh.FormatPeerNotPairedError(req.Peer, resolveErr)), nil
	}
	meta := h.sessionMeshMeta(ctx)
	// mesh.auth_sync is high-trust: granting it immediately mirrors ALL local
	// auth scopes, OAuth provider secrets, OAuth tokens, route rules, and
	// downstream server command/args to the peer, and authorizes inbound
	// snapshots that can register downstream servers (local code execution).
	// Require a REAL interactive approval before the grant takes effect — a
	// system auto-approved consent row is not a sufficient gate here.
	if req.Scope == mesh.AuthSyncScopeName {
		approved, rpc := h.approveAuthSyncGrant(ctx, peerID)
		if rpc != nil {
			h.mesh.RecordGrantPeerScope(ctx, meta, peerID, req.Scope, "error", rpc.Message)
			return nil, rpc
		}
		if !approved {
			h.mesh.RecordGrantPeerScope(ctx, meta, peerID, req.Scope, "denied", "user declined mesh.auth_sync grant")
			return marshalErrorResult(fmt.Sprintf("Grant of %q to peer %s was declined. No credentials or config were shared.", req.Scope, peerID)), nil
		}
	}
	if err := h.store.GrantPeerScope(ctx, peerID, req.Scope); err != nil {
		h.mesh.RecordGrantPeerScope(ctx, meta, peerID, req.Scope, "error", err.Error())
		if errors.Is(err, store.ErrNotFound) {
			return marshalErrorResult(fmt.Sprintf("Peer %q not found (or revoked). Call mesh__list_peers to see paired peers.", req.Peer)), nil
		}
		return marshalErrorResult(err.Error()), nil
	}
	h.mesh.RecordGrantPeerScope(ctx, meta, peerID, req.Scope, "success", "")
	if req.Scope == mesh.AuthSyncScopeName {
		// The interactive RequestApproval above already wrote a durable,
		// user-resolved approval row — no auto-system consent row needed.
		go func() {
			syncCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			defer cancel()
			if err := h.mesh.SendAllAuthScopesToPeer(syncCtx, peerID); err != nil {
				slog.Debug("mesh.auth_sync initial send failed", "peer", peerID, "err", err)
			}
			if err := h.mesh.BroadcastPeerIdentity(syncCtx); err != nil {
				slog.Debug("mesh.auth_sync identity broadcast failed", "peer", peerID, "err", err)
			}
		}()
	} else {
		// Emit a kind=mesh_grant_consent row on the approvals queue so the
		// user has a UI-visible audit trail of grants they explicitly
		// accepted, not just an entry buried in the mesh audit log.
		h.recordMeshGrantConsent(ctx, peerID, req.Scope)
	}
	return marshalToolResult(fmt.Sprintf("Granted %q on peer %s. Remote side must grant the same scope on their device for the gate to clear.", req.Scope, peerID)), nil
}

// approveAuthSyncGrant blocks on a real interactive approval before a
// mesh.auth_sync grant is allowed to take effect. It fails closed: when no
// approval subsystem is wired (e.g. stdio mode) there is no way to obtain
// genuine consent, so the grant is refused rather than silently auto-approved.
func (h *handler) approveAuthSyncGrant(ctx context.Context, peerID string) (bool, *RPCError) {
	if h.approvals == nil {
		return false, &RPCError{
			Code:    CodeInvalidRequest,
			Message: "mesh.auth_sync grants require interactive approval, which is unavailable in this mode",
		}
	}
	rec := &store.ToolApproval{
		RequestSessionID:     h.sessions.sessionID(),
		RequestClientType:    h.sessions.clientType(),
		RequestModel:         h.sessions.modelHint(),
		WorkspaceID:          h.currentWorkspaceID(ctx),
		WorkspaceName:        h.currentWorkspaceName(ctx),
		OriginatingWorkspace: h.currentWorkspaceID(ctx),
		Surface:              "mesh",
		Kind:                 approvalKindMeshGrantConsent,
		ToolName:             "mesh__grant_peer_scope",
		Arguments:            fmt.Sprintf(`{"peer":%q,"scope":%q}`, peerID, mesh.AuthSyncScopeName),
		Summary:              fmt.Sprintf("Grant mesh.auth_sync to peer %s", peerID),
		Justification: "mesh.auth_sync mirrors ALL local auth scopes, OAuth provider secrets, " +
			"OAuth tokens, route rules, and downstream server command/args to this peer, and " +
			"authorizes inbound imports that can register downstream servers (local code " +
			"execution). Approve only for a machine you fully trust as the same user.",
		TimeoutSec: 300,
	}
	approved, err := h.approvals.RequestApproval(ctx, rec)
	if err != nil {
		return false, &RPCError{Code: CodeInternalError, Message: fmt.Sprintf("auth_sync approval failed: %v", err)}
	}
	return approved, nil
}

// handleMeshRevokePeerScope is the inverse — strip a scope from a
// paired peer so they lose authorization for the gated action.
//
// Args: { peer: "<peer_id-or-display-name>", scope: "<scope_name>" }
func (h *handler) handleMeshRevokePeerScope(ctx context.Context, args json.RawMessage) (json.RawMessage, *RPCError) {
	if h.mesh == nil {
		return marshalErrorResult("Agent mesh is not enabled."), nil
	}
	var req struct {
		Peer  string `json:"peer"`
		Scope string `json:"scope"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
		}
	}
	req.Peer = strings.TrimSpace(req.Peer)
	req.Scope = strings.TrimSpace(req.Scope)
	v := newValidator()
	v.requireStringWithHint("peer", req.Peer,
		"peer libp2p ID or display name — call mesh__list_peers")
	v.requireStringWithHint("scope", req.Scope,
		"scope name to revoke (e.g. mesh.skill_request)")
	if env, ok := v.envelope(); ok {
		return env, nil
	}
	if rpc := h.requireWorkerPeerScopeAccess(ctx, req.Scope); rpc != nil {
		return nil, rpc
	}
	peerID, resolveErr := h.resolveMeshPeer(ctx, req.Peer)
	if resolveErr != nil {
		return marshalErrorResult(mesh.FormatPeerNotPairedError(req.Peer, resolveErr)), nil
	}
	meta := h.sessionMeshMeta(ctx)
	if err := h.store.RevokePeerScope(ctx, peerID, req.Scope); err != nil {
		h.mesh.RecordRevokePeerScope(ctx, meta, peerID, req.Scope, "error", err.Error())
		if errors.Is(err, store.ErrNotFound) {
			return marshalErrorResult(fmt.Sprintf("Peer %q not found.", req.Peer)), nil
		}
		return marshalErrorResult(err.Error()), nil
	}
	h.mesh.RecordRevokePeerScope(ctx, meta, peerID, req.Scope, "success", "")
	return marshalToolResult(fmt.Sprintf("Revoked %q on peer %s.", req.Scope, peerID)), nil
}

func (h *handler) requireWorkerPeerScopeAccess(ctx context.Context, scope string) *RPCError {
	if _, ok := workerWorkspaceAccessFromContext(ctx); !ok {
		return nil
	}
	const (
		offerPrefix  = "task_offer:"
		assignPrefix = "task_assign:"
	)
	var resource string
	switch {
	case strings.HasPrefix(scope, offerPrefix):
		resource = strings.TrimPrefix(scope, offerPrefix)
	case strings.HasPrefix(scope, assignPrefix):
		resource = strings.TrimPrefix(scope, assignPrefix)
	default:
		return nil
	}
	resource = strings.TrimSpace(resource)
	if resource == "" || resource == "*" {
		return &RPCError{
			Code:    CodeInvalidRequest,
			Message: fmt.Sprintf("workers cannot grant or revoke wildcard peer task scope %q", scope),
		}
	}
	workspaceID := resource
	if h.store != nil {
		if ws, err := h.store.GetWorkspace(ctx, resource); err == nil && ws != nil {
			workspaceID = ws.ID
		} else if ws, err := h.store.GetWorkspaceByName(ctx, resource); err == nil && ws != nil {
			workspaceID = ws.ID
		}
	}
	return h.requireWorkerWorkspaceAccess(ctx, workspaceID, true)
}
