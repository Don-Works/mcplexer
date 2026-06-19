package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/don-works/mcplexer/internal/routing"
	"github.com/don-works/mcplexer/internal/store"
)

type workerWorkspaceAccessKey struct{}

// WorkerWorkspaceGrant is the gateway-facing shape of one worker
// workspace grant. RootPath/Name are optional routing aids; Access is
// "read" or "write" (write implies read).
type WorkerWorkspaceGrant struct {
	WorkspaceID   string
	WorkspaceName string
	RootPath      string
	Access        string
}

type workerWorkspaceAccessContext struct {
	PreferredWorkspaceID string
	PreferredRootPath    string
	Grants               []WorkerWorkspaceGrant
}

// WithWorkerWorkspaceAccess attaches a worker's preferred workspace and
// explicit grants to ctx. The gateway uses this to route in-process worker
// tool calls and to deny cross-workspace reads/writes outside the grant set.
func WithWorkerWorkspaceAccess(
	ctx context.Context,
	preferredWorkspaceID string,
	grants []WorkerWorkspaceGrant,
) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	preferredWorkspaceID = strings.TrimSpace(preferredWorkspaceID)
	cp := make([]WorkerWorkspaceGrant, 0, len(grants))
	preferredRoot := ""
	seen := map[string]bool{}
	for _, g := range grants {
		g.WorkspaceID = strings.TrimSpace(g.WorkspaceID)
		g.Access = strings.TrimSpace(g.Access)
		if g.WorkspaceID == "" || seen[g.WorkspaceID] {
			continue
		}
		seen[g.WorkspaceID] = true
		if g.WorkspaceID == preferredWorkspaceID {
			preferredRoot = g.RootPath
		}
		cp = append(cp, g)
	}
	for i, g := range cp {
		if g.WorkspaceID != preferredWorkspaceID {
			continue
		}
		if i > 0 {
			cp[0], cp[i] = cp[i], cp[0]
		}
		break
	}
	return context.WithValue(ctx, workerWorkspaceAccessKey{}, workerWorkspaceAccessContext{
		PreferredWorkspaceID: preferredWorkspaceID,
		PreferredRootPath:    preferredRoot,
		Grants:               cp,
	})
}

func workerWorkspaceAccessFromContext(ctx context.Context) (workerWorkspaceAccessContext, bool) {
	if ctx == nil {
		return workerWorkspaceAccessContext{}, false
	}
	c, ok := ctx.Value(workerWorkspaceAccessKey{}).(workerWorkspaceAccessContext)
	return c, ok
}

func (h *handler) currentWorkspaceID(ctx context.Context) string {
	if c, ok := workerWorkspaceAccessFromContext(ctx); ok && c.PreferredWorkspaceID != "" {
		return c.PreferredWorkspaceID
	}
	return h.sessions.workspaceID()
}

func (h *handler) currentWorkspaceName(ctx context.Context) string {
	if c, ok := workerWorkspaceAccessFromContext(ctx); ok && c.PreferredWorkspaceID != "" {
		for _, g := range c.Grants {
			if g.WorkspaceID == c.PreferredWorkspaceID {
				return g.WorkspaceName
			}
		}
	}
	return h.sessions.workspaceName()
}

func (h *handler) routingClientRoot(ctx context.Context) string {
	if c, ok := workerWorkspaceAccessFromContext(ctx); ok && c.PreferredRootPath != "" {
		return c.PreferredRootPath
	}
	return h.sessions.clientRoot()
}

func (h *handler) currentSubpath(ctx context.Context) string {
	ancestors := h.routingWorkspaceAncestors(ctx)
	if len(ancestors) == 0 {
		return ""
	}
	return routing.ComputeSubpath(h.routingClientRoot(ctx), ancestors[0].RootPath)
}

func (h *handler) routingWorkspaceAncestors(ctx context.Context) []routing.WorkspaceAncestor {
	if c, ok := workerWorkspaceAccessFromContext(ctx); ok {
		out := make([]routing.WorkspaceAncestor, 0, len(c.Grants))
		for _, g := range c.Grants {
			if !grantCanRead(g.Access) {
				continue
			}
			out = append(out, routing.WorkspaceAncestor{
				ID:       g.WorkspaceID,
				Name:     g.WorkspaceName,
				RootPath: g.RootPath,
			})
		}
		return out
	}
	return h.sessions.workspaceAncestors(ctx)
}

func (h *handler) readableWorkspaceIDs(ctx context.Context) []string {
	if c, ok := workerWorkspaceAccessFromContext(ctx); ok {
		out := make([]string, 0, len(c.Grants))
		for _, g := range c.Grants {
			if grantCanRead(g.Access) && g.WorkspaceID != "" {
				out = append(out, g.WorkspaceID)
			}
		}
		return out
	}
	ancestors := h.sessions.workspaceAncestors(ctx)
	out := make([]string, 0, len(ancestors))
	for _, a := range ancestors {
		if a.ID != "" {
			out = append(out, a.ID)
		}
	}
	return out
}

func (h *handler) requireWorkerWorkspaceAccess(ctx context.Context, workspaceID string, write bool) *RPCError {
	c, ok := workerWorkspaceAccessFromContext(ctx)
	if !ok || strings.TrimSpace(workspaceID) == "" {
		return nil
	}
	for _, g := range c.Grants {
		if g.WorkspaceID != workspaceID {
			continue
		}
		if write && !grantCanWrite(g.Access) {
			return &RPCError{
				Code: CodeInvalidRequest,
				Message: fmt.Sprintf(
					"worker has read-only access to workspace %q; write access is required",
					workspaceID,
				),
			}
		}
		if grantCanRead(g.Access) {
			return nil
		}
	}
	need := "read"
	if write {
		need = "write"
	}
	return &RPCError{
		Code: CodeInvalidRequest,
		Message: fmt.Sprintf(
			"worker is not granted %s access to workspace %q",
			need, workspaceID,
		),
	}
}

func (h *handler) enforceWorkerRouteAccess(
	ctx context.Context,
	toolName string,
	originalTool string,
	routeResult *routing.RouteResult,
) *RPCError {
	if _, ok := workerWorkspaceAccessFromContext(ctx); !ok || routeResult == nil {
		return nil
	}
	wsID := routeResult.MatchedWorkspaceID
	if wsID == "" {
		return nil
	}
	readOnly := false
	if _, ok := builtinDownstreamIDs[routeResult.DownstreamServerID]; ok {
		readOnly = h.isGatewayReadOnlyTool(ctx, toolName)
	} else {
		readOnly = h.isReadOnlyTool(ctx, routeResult.DownstreamServerID, originalTool)
	}
	return h.requireWorkerWorkspaceAccess(ctx, wsID, !readOnly)
}

func grantCanRead(access string) bool {
	switch strings.TrimSpace(access) {
	case store.WorkerWorkspaceAccessRead, store.WorkerWorkspaceAccessWrite:
		return true
	default:
		return false
	}
}

func grantCanWrite(access string) bool {
	return strings.TrimSpace(access) == store.WorkerWorkspaceAccessWrite
}

func toolReadOnlyFromAnnotations(t Tool) bool {
	if t.Extras == nil {
		return false
	}
	raw, ok := t.Extras["annotations"]
	if !ok {
		return false
	}
	var ann ToolAnnotations
	if err := json.Unmarshal(raw, &ann); err != nil || ann.ReadOnlyHint == nil {
		return false
	}
	return *ann.ReadOnlyHint
}

func (h *handler) isGatewayReadOnlyTool(ctx context.Context, toolName string) bool {
	for _, t := range h.buildAllBuiltinTools(ctx) {
		if t.Name == toolName {
			return toolReadOnlyFromAnnotations(t)
		}
	}
	for _, t := range h.searchableBuiltins(ctx) {
		if t.Name == toolName {
			return toolReadOnlyFromAnnotations(t)
		}
	}
	return false
}
