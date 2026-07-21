package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/don-works/mcplexer/internal/routing"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

func cmdDryRun(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("usage: mcplexer dry-run <workspace-id> <tool-name>")
	}
	workspaceID := args[0]
	toolName := args[1]

	ctx := context.Background()
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	db, err := sqlite.New(ctx, cfg.DBDSN)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer func() { _ = db.Close() }()

	ws, err := db.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return fmt.Errorf("workspace %q not found: %w", workspaceID, err)
	}

	fmt.Printf("Dry-run: workspace=%s tool=%s\n", workspaceID, toolName)
	fmt.Printf("  Workspace: %s (root=%s, default_policy=%s)\n\n", ws.Name, ws.RootPath, ws.DefaultPolicy)

	engine := routing.NewEngine(db)
	rc := routing.RouteContext{
		WorkspaceID: workspaceID,
		ToolName:    toolName,
	}
	result, err := engine.Route(ctx, rc)
	if err != nil {
		if errors.Is(err, routing.ErrDenied) {
			fmt.Printf("  DENIED: %v\n", err)
		} else if errors.Is(err, routing.ErrNoRoute) {
			fmt.Printf("  NO ROUTE: no matching rule found for tool %q\n", toolName)
		} else {
			fmt.Printf("  ERROR: %v\n", err)
		}
		return nil
	}

	fmt.Printf("  ALLOWED\n")
	fmt.Printf("    matched_rule:  %s\n", result.MatchedRuleID)
	fmt.Printf("    downstream:    %s\n", result.DownstreamServerID)
	fmt.Printf("    auth_scope:    %s\n", result.AuthScopeID)
	if result.ApprovalMode != "" && result.ApprovalMode != "none" {
		fmt.Printf("    approval:      %s (timeout=%ds)\n", result.ApprovalMode, result.ApprovalTimeout)
	}
	return nil
}
