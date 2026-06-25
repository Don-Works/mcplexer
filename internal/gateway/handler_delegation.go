package gateway

import (
	"context"
	"encoding/json"

	workersadmin "github.com/don-works/mcplexer/internal/workers/admin"
)

func clientModelFromHint(hint string) string {
	if hint == "" {
		return ""
	}
	return "client:" + hint
}

func (h *handler) currentDelegationDisabled() map[string]bool {
	if h.settingsSvc == nil {
		return map[string]bool{}
	}
	s := h.settingsSvc.Load(context.Background())
	if s.DelegationDisabledProviders == nil {
		return map[string]bool{}
	}
	out := make(map[string]bool, len(s.DelegationDisabledProviders))
	for k, v := range s.DelegationDisabledProviders {
		out[k] = v
	}
	return out
}

func (h *handler) handleDelegateWorker(ctx context.Context, raw json.RawMessage) (json.RawMessage, *RPCError) {
	if h.workerAdmin == nil {
		return marshalErrorResult("worker delegation is not enabled"), nil
	}
	var in workersadmin.DelegationInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	if in.WorkspaceID == "" {
		in.WorkspaceID = h.currentWorkspaceID(ctx)
	}
	if in.WorkspaceID == "" {
		return nil, &RPCError{Code: CodeInvalidParams, Message: "workspace_id required (session is not bound to a workspace)"}
	}
	if rpc := h.requireWorkspaceWrite(ctx, in.WorkspaceID); rpc != nil {
		return nil, rpc
	}
	if in.ParentSessionID == "" {
		in.ParentSessionID = h.sessions.sessionID()
	}
	if in.ParentContextID == "" {
		in.ParentContextID = in.ParentSessionID
	}
	if in.ParentModel == "" {
		in.ParentModel = clientModelFromHint(h.sessions.modelHint())
	}
	in.DisabledProviders = h.currentDelegationDisabled()
	out, err := h.workerAdmin.Delegate(ctx, in)
	if err != nil {
		return marshalErrorResult(err.Error()), nil
	}
	return marshalJSONResult(out)
}

func (h *handler) handleListDelegations(ctx context.Context, raw json.RawMessage) (json.RawMessage, *RPCError) {
	if h.workerAdmin == nil {
		return marshalErrorResult("worker delegation is not enabled"), nil
	}
	var in workersadmin.DelegationListInput
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
		}
	}
	if in.WorkspaceID == "" {
		in.WorkspaceID = h.currentWorkspaceID(ctx)
	}
	if in.WorkspaceID == "" {
		return nil, &RPCError{Code: CodeInvalidParams, Message: "workspace_id required (session is not bound to a workspace)"}
	}
	if rpc := h.requireWorkspaceRead(ctx, in.WorkspaceID); rpc != nil {
		return nil, rpc
	}
	out, err := h.workerAdmin.ListDelegations(ctx, in)
	if err != nil {
		return marshalErrorResult(err.Error()), nil
	}
	return marshalJSONResult(map[string]any{"delegations": out, "count": len(out)})
}

func (h *handler) handleExtendDelegationBudget(ctx context.Context, raw json.RawMessage) (json.RawMessage, *RPCError) {
	if h.workerAdmin == nil {
		return marshalErrorResult("worker delegation is not enabled"), nil
	}
	var in workersadmin.DelegationBudgetInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	if in.WorkspaceID == "" {
		in.WorkspaceID = h.currentWorkspaceID(ctx)
	}
	if in.WorkspaceID == "" {
		return nil, &RPCError{Code: CodeInvalidParams, Message: "workspace_id required (session is not bound to a workspace)"}
	}
	if rpc := h.requireWorkspaceWrite(ctx, in.WorkspaceID); rpc != nil {
		return nil, rpc
	}
	out, err := h.workerAdmin.ExtendDelegationBudget(ctx, in)
	if err != nil {
		return marshalErrorResult(err.Error()), nil
	}
	return marshalJSONResult(out)
}

func (h *handler) handleInvokeModel(ctx context.Context, raw json.RawMessage) (json.RawMessage, *RPCError) {
	if h.workerAdmin == nil {
		return marshalErrorResult("worker delegation is not enabled"), nil
	}
	var in workersadmin.InvokeModelInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	if in.WorkspaceID == "" {
		in.WorkspaceID = h.currentWorkspaceID(ctx)
	}
	if in.WorkspaceID == "" {
		return nil, &RPCError{Code: CodeInvalidParams, Message: "workspace_id required (session is not bound to a workspace)"}
	}
	if rpc := h.requireWorkspaceWrite(ctx, in.WorkspaceID); rpc != nil {
		return nil, rpc
	}
	in.DisabledProviders = h.currentDelegationDisabled()
	out, err := h.workerAdmin.InvokeModel(ctx, in)
	if err != nil {
		return marshalErrorResult(err.Error()), nil
	}
	return marshalJSONResult(out)
}

func (h *handler) handleWaitForDelegation(ctx context.Context, raw json.RawMessage) (json.RawMessage, *RPCError) {
	if h.workerAdmin == nil {
		return marshalErrorResult("worker delegation is not enabled"), nil
	}
	var in workersadmin.WaitForDelegationInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	if in.WorkspaceID == "" {
		in.WorkspaceID = h.currentWorkspaceID(ctx)
	}
	if in.WorkspaceID == "" {
		return nil, &RPCError{Code: CodeInvalidParams, Message: "workspace_id required (session is not bound to a workspace)"}
	}
	if rpc := h.requireWorkspaceRead(ctx, in.WorkspaceID); rpc != nil {
		return nil, rpc
	}
	out, err := h.workerAdmin.WaitForDelegation(ctx, in)
	if err != nil {
		return marshalErrorResult(err.Error()), nil
	}
	return marshalJSONResult(out)
}

func (h *handler) handleListDelegationModelCapacity(ctx context.Context, raw json.RawMessage) (json.RawMessage, *RPCError) {
	if h.workerAdmin == nil {
		return marshalErrorResult("worker delegation is not enabled"), nil
	}
	var in workersadmin.DelegationCapacityListInput
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
		}
	}
	if in.WorkspaceID == "" {
		in.WorkspaceID = h.currentWorkspaceID(ctx)
	}
	if in.WorkspaceID == "" {
		return nil, &RPCError{Code: CodeInvalidParams, Message: "workspace_id required (session is not bound to a workspace)"}
	}
	if rpc := h.requireWorkspaceRead(ctx, in.WorkspaceID); rpc != nil {
		return nil, rpc
	}
	in.DisabledProviders = h.currentDelegationDisabled()
	out, err := h.workerAdmin.ListDelegationModelCapacity(ctx, in)
	if err != nil {
		return marshalErrorResult(err.Error()), nil
	}
	return marshalJSONResult(map[string]any{"capacity": out, "count": len(out)})
}

func (h *handler) handleReviewDelegation(ctx context.Context, raw json.RawMessage) (json.RawMessage, *RPCError) {
	if h.workerAdmin == nil {
		return marshalErrorResult("worker delegation is not enabled"), nil
	}
	var in workersadmin.DelegationReviewInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	if in.WorkspaceID == "" {
		in.WorkspaceID = h.currentWorkspaceID(ctx)
	}
	if in.WorkspaceID == "" {
		return nil, &RPCError{Code: CodeInvalidParams, Message: "workspace_id required (session is not bound to a workspace)"}
	}
	if rpc := h.requireWorkspaceWrite(ctx, in.WorkspaceID); rpc != nil {
		return nil, rpc
	}
	if in.ReviewerContextID == "" {
		in.ReviewerContextID = h.sessions.sessionID()
	}
	if in.ReviewerModel == "" {
		in.ReviewerModel = clientModelFromHint(h.sessions.modelHint())
	}
	out, err := h.workerAdmin.ReviewDelegation(ctx, in)
	if err != nil {
		return marshalErrorResult(err.Error()), nil
	}
	return marshalJSONResult(out)
}
