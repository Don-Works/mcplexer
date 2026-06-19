package control

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/don-works/mcplexer/internal/store"
)

// -- Route rule handlers --

func handleListRoutes(
	ctx context.Context, s store.Store, args json.RawMessage,
) (json.RawMessage, error) {
	var p struct {
		WorkspaceID string `json:"workspace_id"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	var v validator
	v.requireString("workspace_id", p.WorkspaceID,
		"id of the workspace whose routes to list — call list_workspaces")
	if env, ok := v.envelope(); ok {
		return env, nil
	}
	rules, err := s.ListRouteRules(ctx, p.WorkspaceID)
	if err != nil {
		return nil, fmt.Errorf("list routes: %w", err)
	}
	return jsonResult(rules)
}

func handleCreateRoute(
	ctx context.Context, s store.Store, args json.RawMessage,
) (json.RawMessage, error) {
	var r store.RouteRule
	if err := json.Unmarshal(args, &r); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	var v validator
	v.requireString("workspace_id", r.WorkspaceID,
		"id of the workspace the route belongs to")
	v.requireString("downstream_server_id", r.DownstreamServerID,
		"id of the downstream server the route targets")
	v.requireString("policy", r.Policy,
		"\"allow\" or \"deny\" — the default match policy")
	v.requireOneOf("policy", r.Policy, "allow", "deny")
	if env, ok := v.envelope(); ok {
		return env, nil
	}
	if err := s.CreateRouteRule(ctx, &r); err != nil {
		return nil, fmt.Errorf("create route: %w", err)
	}
	return jsonResult(r)
}

func handleUpdateRoute(
	ctx context.Context, s store.Store, args json.RawMessage,
) (json.RawMessage, error) {
	id, err := requireID(args)
	if err != nil {
		return nil, err
	}
	r, err := s.GetRouteRule(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get route: %w", err)
	}
	if err := json.Unmarshal(args, r); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	r.ID = id
	if err := s.UpdateRouteRule(ctx, r); err != nil {
		return nil, fmt.Errorf("update route: %w", err)
	}
	return jsonResult(r)
}

func handleDeleteRoute(
	ctx context.Context, s store.Store, args json.RawMessage,
) (json.RawMessage, error) {
	id, err := requireID(args)
	if err != nil {
		return nil, err
	}
	if err := s.DeleteRouteRule(ctx, id); err != nil {
		return nil, fmt.Errorf("delete route: %w", err)
	}
	return textResult("deleted"), nil
}

// -- Auth scope handlers --

func handleListAuthScopes(
	ctx context.Context, s store.Store, _ json.RawMessage,
) (json.RawMessage, error) {
	scopes, err := s.ListAuthScopes(ctx)
	if err != nil {
		return nil, fmt.Errorf("list auth scopes: %w", err)
	}
	return jsonResult(scopes)
}

func handleCreateAuthScope(
	ctx context.Context, s store.Store, args json.RawMessage,
) (json.RawMessage, error) {
	var a store.AuthScope
	if err := json.Unmarshal(args, &a); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	var v validator
	v.requireString("name", a.Name,
		"short, stable scope name used for `secret://` refs (e.g. \"linear-credential\")")
	v.requireString("type", a.Type,
		"scope type — typically \"generic\" or \"oauth2\"")
	if env, ok := v.envelope(); ok {
		return env, nil
	}
	if err := s.CreateAuthScope(ctx, &a); err != nil {
		return nil, fmt.Errorf("create auth scope: %w", err)
	}
	return jsonResult(a)
}

func handleDeleteAuthScope(
	ctx context.Context, s store.Store, args json.RawMessage,
) (json.RawMessage, error) {
	id, err := requireID(args)
	if err != nil {
		return nil, err
	}
	if err := s.DeleteAuthScope(ctx, id); err != nil {
		return nil, fmt.Errorf("delete auth scope: %w", err)
	}
	return textResult("deleted"), nil
}

// handleGetAuthScope returns a single auth scope by id.
func handleGetAuthScope(
	ctx context.Context, s store.Store, args json.RawMessage,
) (json.RawMessage, error) {
	id, err := requireID(args)
	if err != nil {
		return nil, err
	}
	a, err := s.GetAuthScope(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get auth scope: %w", err)
	}
	return jsonResult(a)
}

// handleUpdateAuthScope partially updates an auth scope. Only fields provided
// in args are modified; everything else stays. The id is required.
func handleUpdateAuthScope(
	ctx context.Context, s store.Store, args json.RawMessage,
) (json.RawMessage, error) {
	var p struct {
		ID              string   `json:"id"`
		Name            *string  `json:"name,omitempty"`
		Type            *string  `json:"type,omitempty"`
		OAuthProviderID *string  `json:"oauth_provider_id,omitempty"`
		RedactionHints  []string `json:"redaction_hints,omitempty"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if p.ID == "" {
		return nil, fmt.Errorf("id is required")
	}
	existing, err := s.GetAuthScope(ctx, p.ID)
	if err != nil {
		return nil, fmt.Errorf("get auth scope: %w", err)
	}
	if p.Name != nil && *p.Name != "" {
		existing.Name = *p.Name
	}
	if p.Type != nil && *p.Type != "" {
		existing.Type = *p.Type
	}
	if p.OAuthProviderID != nil && *p.OAuthProviderID != "" {
		existing.OAuthProviderID = *p.OAuthProviderID
	}
	if p.RedactionHints != nil {
		raw, mErr := json.Marshal(p.RedactionHints)
		if mErr != nil {
			return nil, fmt.Errorf("marshal redaction_hints: %w", mErr)
		}
		existing.RedactionHints = raw
	}
	if err := s.UpdateAuthScope(ctx, existing); err != nil {
		return nil, fmt.Errorf("update auth scope: %w", err)
	}
	return jsonResult(existing)
}
