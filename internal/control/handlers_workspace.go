package control

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/don-works/mcplexer/internal/store"
)

func handleListWorkspaces(
	ctx context.Context, s store.Store, _ json.RawMessage,
) (json.RawMessage, error) {
	workspaces, err := s.ListWorkspaces(ctx)
	if err != nil {
		return nil, fmt.Errorf("list workspaces: %w", err)
	}
	return jsonResult(workspaces)
}

func handleGetWorkspace(
	ctx context.Context, s store.Store, args json.RawMessage,
) (json.RawMessage, error) {
	id, err := requireID(args)
	if err != nil {
		return nil, err
	}
	ws, err := s.GetWorkspace(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get workspace: %w", err)
	}
	return jsonResult(ws)
}

func handleCreateWorkspace(
	ctx context.Context, s store.Store, args json.RawMessage,
) (json.RawMessage, error) {
	var ws store.Workspace
	if err := json.Unmarshal(args, &ws); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	var v validator
	v.requireString("name", ws.Name,
		"human-readable workspace name (e.g. \"my-project\")")
	if env, ok := v.envelope(); ok {
		return env, nil
	}
	if ws.DefaultPolicy == "" {
		ws.DefaultPolicy = "deny"
	}
	if err := s.CreateWorkspace(ctx, &ws); err != nil {
		return nil, fmt.Errorf("create workspace: %w", err)
	}
	return jsonResult(ws)
}

func handleUpdateWorkspace(
	ctx context.Context, s store.Store, args json.RawMessage,
) (json.RawMessage, error) {
	id, err := requireID(args)
	if err != nil {
		return nil, err
	}
	ws, err := s.GetWorkspace(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get workspace: %w", err)
	}
	if err := json.Unmarshal(args, ws); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	ws.ID = id
	if err := s.UpdateWorkspace(ctx, ws); err != nil {
		return nil, fmt.Errorf("update workspace: %w", err)
	}
	return jsonResult(ws)
}

func handleDeleteWorkspace(
	ctx context.Context, s store.Store, args json.RawMessage,
) (json.RawMessage, error) {
	id, err := requireID(args)
	if err != nil {
		return nil, err
	}
	if err := s.DeleteWorkspace(ctx, id); err != nil {
		return nil, fmt.Errorf("delete workspace: %w", err)
	}
	return textResult("deleted"), nil
}
