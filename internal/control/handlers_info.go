package control

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/don-works/mcplexer/internal/store"
)

func handleStatus(
	ctx context.Context, s store.Store, _ json.RawMessage,
) (json.RawMessage, error) {
	servers, err := s.ListDownstreamServers(ctx)
	if err != nil {
		return nil, fmt.Errorf("list servers: %w", err)
	}
	workspaces, err := s.ListWorkspaces(ctx)
	if err != nil {
		return nil, fmt.Errorf("list workspaces: %w", err)
	}
	sessions, err := s.ListActiveSessions(ctx)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	scopes, err := s.ListAuthScopes(ctx)
	if err != nil {
		return nil, fmt.Errorf("list auth scopes: %w", err)
	}

	status := map[string]int{
		"downstream_servers": len(servers),
		"workspaces":         len(workspaces),
		"active_sessions":    len(sessions),
		"auth_scopes":        len(scopes),
	}
	return jsonResult(status)
}

func handleQueryAudit(
	ctx context.Context, s store.Store, args json.RawMessage,
) (json.RawMessage, error) {
	var p struct {
		ToolName           *string `json:"tool_name"`
		Status             *string `json:"status"`
		WorkspaceID        *string `json:"workspace_id"`
		ActorKind          *string `json:"actor_kind"`
		ActorID            *string `json:"actor_id"`
		DownstreamServerID *string `json:"downstream_server_id"`
		RouteRuleID        *string `json:"route_rule_id"`
		ClientType         *string `json:"client_type"`
		ErrorCode          *string `json:"error_code"`
		Tier               *string `json:"tier"`
		Q                  string  `json:"q"`
		Sort               string  `json:"sort"`
		Limit              int     `json:"limit"`
		Offset             int     `json:"offset"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
	}
	if p.Limit == 0 {
		p.Limit = 50
	}

	filter := store.AuditFilter{
		ToolName:           p.ToolName,
		Status:             p.Status,
		WorkspaceID:        p.WorkspaceID,
		ActorKind:          p.ActorKind,
		ActorID:            p.ActorID,
		DownstreamServerID: p.DownstreamServerID,
		RouteRuleID:        p.RouteRuleID,
		ClientType:         p.ClientType,
		ErrorCode:          p.ErrorCode,
		Tier:               p.Tier,
		Q:                  p.Q,
		Sort:               p.Sort,
		Limit:              p.Limit,
		Offset:             p.Offset,
	}
	records, total, err := s.QueryAuditRecords(ctx, filter)
	if err != nil {
		return nil, fmt.Errorf("query audit: %w", err)
	}

	result := map[string]any{
		"records": records,
		"total":   total,
	}
	return jsonResult(result)
}
