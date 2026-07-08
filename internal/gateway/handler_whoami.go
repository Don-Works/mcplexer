package gateway

import (
	"context"
	"encoding/json"
	"fmt"
)

// whoamiWorkspace is one workspace in the session's resolved ancestor
// chain (nearest first).
type whoamiWorkspace struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	RootPath string `json:"root_path,omitempty"`
}

// whoamiResult is the mcpx__whoami payload: the session's workspace
// binding, which is distinct from the filesystem CWD an agent might
// assume it is operating in.
type whoamiResult struct {
	WorkspaceID    string            `json:"workspace_id"`
	WorkspaceName  string            `json:"workspace_name"`
	WorkspaceBound bool              `json:"workspace_bound"`
	ClientRoot     string            `json:"client_root,omitempty"`
	WorkspaceChain []whoamiWorkspace `json:"workspace_chain"`
	Summary        string            `json:"summary"`
}

// handleWhoami answers "which workspace is this session bound to?" — the
// question no other tool covers: mcpx status reports global counts, not
// the caller's own binding, and the binding is resolved by the gateway
// from the routed workspace chain, NOT from the filesystem working
// directory an agent sees. An empty workspace_id means the session is
// unscoped (global) with no workspace bound.
func (h *handler) handleWhoami(ctx context.Context) (json.RawMessage, *RPCError) {
	wsID := h.currentWorkspaceID(ctx)
	wsName := h.currentWorkspaceName(ctx)

	ancestors := h.routingWorkspaceAncestors(ctx)
	chain := make([]whoamiWorkspace, 0, len(ancestors))
	for _, a := range ancestors {
		chain = append(chain, whoamiWorkspace{ID: a.ID, Name: a.Name, RootPath: a.RootPath})
	}

	res := whoamiResult{
		WorkspaceID:    wsID,
		WorkspaceName:  wsName,
		WorkspaceBound: wsID != "",
		ClientRoot:     h.routingClientRoot(ctx),
		WorkspaceChain: chain,
	}
	if res.WorkspaceBound {
		res.Summary = fmt.Sprintf("Bound to workspace %q (%s).", wsName, wsID)
	} else {
		res.Summary = "No workspace bound to this session — running unscoped (global)."
	}

	data, err := json.Marshal(res)
	if err != nil {
		return nil, &RPCError{Code: CodeInternalError, Message: err.Error()}
	}
	return marshalToolResult(string(data)), nil
}
