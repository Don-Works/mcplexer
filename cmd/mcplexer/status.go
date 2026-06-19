package main

import (
	"context"
	"fmt"

	"github.com/don-works/mcplexer/internal/store/sqlite"
)

func cmdStatus() error {
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

	workspaces, err := db.ListWorkspaces(ctx)
	if err != nil {
		return fmt.Errorf("list workspaces: %w", err)
	}

	downstreams, err := db.ListDownstreamServers(ctx)
	if err != nil {
		return fmt.Errorf("list downstreams: %w", err)
	}

	sessions, err := db.ListActiveSessions(ctx)
	if err != nil {
		return fmt.Errorf("list sessions: %w", err)
	}

	scopes, err := db.ListAuthScopes(ctx)
	if err != nil {
		return fmt.Errorf("list auth scopes: %w", err)
	}

	fmt.Printf("MCPlexer Status (db: %s)\n", cfg.DBDSN)
	fmt.Printf("  Workspaces:         %d\n", len(workspaces))
	fmt.Printf("  Downstream servers: %d\n", len(downstreams))
	fmt.Printf("  Auth scopes:        %d\n", len(scopes))
	fmt.Printf("  Active sessions:    %d\n", len(sessions))

	return nil
}
