package control

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/don-works/mcplexer/internal/gateway"
	"github.com/don-works/mcplexer/internal/store"
)

// guardCrossWorkspaceRouteRefs enforces workspace segregation on route
// mutations arriving from the dev-mode source-repo admin escape.
//
// The rule: a session that qualified for admin ONLY by sitting in a
// mcplexer source tree (gateway.AdminTrustSourceRepo) may not create or
// update a route whose downstream server or auth scope is routed in
// some OTHER workspace but not in the route's target workspace. That
// exact shape is the segregation bypass from the 2026-07-06 incident:
// borrowing another workspace's credentialed server from a workspace
// whose routing policy deliberately excludes it.
//
// What stays allowed from the dev escape:
//   - routes referencing servers/scopes the target workspace already
//     routes (no new authority), and
//   - routes referencing servers/scopes routed NOWHERE yet (the
//     provision_mcp flow: create server, then route it).
//
// Full-authority contexts (data-dir CWD, REST API, standalone control
// server, in-process workers) never reach the restriction —
// AdminTrustFromContext returns something other than SourceRepo for
// all of them.
func guardCrossWorkspaceRouteRefs(
	ctx context.Context, s store.Store, r *store.RouteRule,
) error {
	if gateway.AdminTrustFromContext(ctx) != gateway.AdminTrustSourceRepo {
		return nil
	}
	serverForeign, scopeForeign, foreignWS, err := crossWorkspaceRefs(ctx, s, r)
	if err != nil {
		return fmt.Errorf("cross-workspace reference check: %w", err)
	}
	if !serverForeign && !scopeForeign {
		return nil
	}
	slog.Warn("blocked cross-workspace route reference from dev-mode admin escape",
		"workspace_id", r.WorkspaceID,
		"downstream_server_id", r.DownstreamServerID,
		"auth_scope_id", r.AuthScopeID,
		"referenced_in_workspace", foreignWS)
	what := "downstream server " + r.DownstreamServerID
	if scopeForeign {
		what = "auth scope " + r.AuthScopeID
		if serverForeign {
			what = "downstream server " + r.DownstreamServerID + " and auth scope " + r.AuthScopeID
		}
	}
	return fmt.Errorf(
		"workspace segregation: %s is routed in workspace %s but not in target workspace %s, "+
			"and this session's admin access comes from the dev-mode source-repo escape. "+
			"Borrowing another workspace's servers/credentials requires the full admin context: "+
			"re-run from ~/.mcplexer or use the dashboard",
		what, foreignWS, r.WorkspaceID)
}

// crossWorkspaceRefs reports whether the rule's downstream server /
// auth scope are referenced by another workspace's routes while absent
// from the target workspace's own routes, plus one foreign workspace id
// for the error message. References already present in the target
// workspace clear the flag regardless of other workspaces.
func crossWorkspaceRefs(
	ctx context.Context, s store.Store, r *store.RouteRule,
) (serverForeign, scopeForeign bool, foreignWS string, err error) {
	own, err := s.ListRouteRules(ctx, r.WorkspaceID)
	if err != nil {
		return false, false, "", fmt.Errorf("list target workspace routes: %w", err)
	}
	serverNeeded := r.DownstreamServerID != ""
	scopeNeeded := r.AuthScopeID != ""
	for _, rule := range own {
		if rule.ID == r.ID {
			continue // updating this rule: its old refs grant nothing
		}
		if rule.DownstreamServerID == r.DownstreamServerID {
			serverNeeded = false
		}
		if scopeNeeded && rule.AuthScopeID == r.AuthScopeID {
			scopeNeeded = false
		}
	}
	if !serverNeeded && !scopeNeeded {
		return false, false, "", nil
	}
	workspaces, err := s.ListWorkspaces(ctx)
	if err != nil {
		return false, false, "", fmt.Errorf("list workspaces: %w", err)
	}
	for _, ws := range workspaces {
		if ws.ID == r.WorkspaceID {
			continue
		}
		rules, lerr := s.ListRouteRules(ctx, ws.ID)
		if lerr != nil {
			return false, false, "", fmt.Errorf("list routes for workspace %s: %w", ws.ID, lerr)
		}
		for _, rule := range rules {
			if serverNeeded && rule.DownstreamServerID == r.DownstreamServerID {
				serverForeign, foreignWS = true, ws.ID
			}
			if scopeNeeded && r.AuthScopeID != "" && rule.AuthScopeID == r.AuthScopeID {
				scopeForeign, foreignWS = true, ws.ID
			}
		}
	}
	return serverForeign, scopeForeign, foreignWS, nil
}
