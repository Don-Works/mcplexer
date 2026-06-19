package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/don-works/mcplexer/internal/routing"
	"github.com/don-works/mcplexer/internal/store"
)

type dryRunHandler struct {
	engine          *routing.Engine
	routeStore      store.RouteRuleStore
	workspaceStore  store.WorkspaceStore
	downstreamStore store.DownstreamServerStore
	authScopeStore  store.AuthScopeStore
	flowManager     interface {
		TokenStatus(ctx context.Context, authScopeID string) (string, *time.Time, error)
	}
}

type dryRunRequest struct {
	WorkspaceID string `json:"workspace_id"`
	Subpath     string `json:"subpath"`
	ToolName    string `json:"tool_name"`
}

type dryRunAuthScope struct {
	ID          string     `json:"id"`
	Name        string     `json:"name"`
	Type        string     `json:"type"`
	OAuthStatus string     `json:"oauth_status,omitempty"` // "valid", "expired", "none", "not_applicable"
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
}

type dryRunResponse struct {
	Matched          bool                    `json:"matched"`
	Policy           string                  `json:"policy"`
	MatchedRule      *store.RouteRule        `json:"matched_rule,omitempty"`
	DownstreamServer *store.DownstreamServer `json:"downstream_server,omitempty"`
	AuthScopeID      string                  `json:"auth_scope_id,omitempty"`
	AuthScope        *dryRunAuthScope        `json:"auth_scope,omitempty"`
	CandidateRules   []store.RouteRule       `json:"candidate_rules"`
}

func (h *dryRunHandler) run(w http.ResponseWriter, r *http.Request) {
	var req dryRunRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.WorkspaceID == "" || req.ToolName == "" {
		writeError(w, http.StatusBadRequest, "workspace_id and tool_name are required")
		return
	}

	ctx := r.Context()

	rules, err := h.routeStore.ListRouteRules(ctx, req.WorkspaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list route rules")
		return
	}

	resp := dryRunResponse{CandidateRules: rules}

	rc := routing.RouteContext{
		WorkspaceID: req.WorkspaceID,
		Subpath:     req.Subpath,
		ToolName:    req.ToolName,
	}

	result, err := h.engine.Route(ctx, rc)
	switch {
	case err == nil:
		resp.Matched = true
		resp.Policy = "allow"
		resp.AuthScopeID = result.AuthScopeID

		// Find the matched rule in the candidate list.
		for i := range rules {
			if rules[i].ID == result.MatchedRuleID {
				resp.MatchedRule = &rules[i]
				break
			}
		}

		ds, dsErr := h.downstreamStore.GetDownstreamServer(ctx, result.DownstreamServerID)
		if dsErr == nil {
			resp.DownstreamServer = ds
		}

		resp.AuthScope = h.resolveAuthScope(ctx, result.AuthScopeID)

	case errors.Is(err, routing.ErrDenied):
		resp.Matched = true
		resp.Policy = "deny"

		var de *routing.DeniedError
		if errors.As(err, &de) {
			for i := range rules {
				if rules[i].ID == de.RuleID {
					resp.MatchedRule = &rules[i]
					break
				}
			}
		}

	case errors.Is(err, routing.ErrNoRoute):
		ws, wsErr := h.workspaceStore.GetWorkspace(ctx, req.WorkspaceID)
		if wsErr == nil {
			resp.Policy = ws.DefaultPolicy
		} else {
			resp.Policy = "deny"
		}

	default:
		writeError(w, http.StatusInternalServerError, "routing error")
		return
	}

	writeJSON(w, http.StatusOK, resp)
}

func (h *dryRunHandler) resolveAuthScope(ctx context.Context, id string) *dryRunAuthScope {
	if id == "" || h.authScopeStore == nil {
		return nil
	}
	scope, err := h.authScopeStore.GetAuthScope(ctx, id)
	if err != nil {
		return nil
	}
	as := &dryRunAuthScope{
		ID:   scope.ID,
		Name: scope.Name,
		Type: scope.Type,
	}
	if scope.Type != "oauth2" {
		as.OAuthStatus = "not_applicable"
		return as
	}
	if h.flowManager != nil {
		status, expiresAt, err := h.flowManager.TokenStatus(ctx, id)
		if err == nil {
			as.OAuthStatus = status
			as.ExpiresAt = expiresAt
		} else {
			as.OAuthStatus = "none"
		}
	} else {
		as.OAuthStatus = "none"
	}
	return as
}
