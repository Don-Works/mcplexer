package config

import (
	"context"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

func TestSeedDefaultWorkspaces_EnsuresGlobalWhenWorkspaceExists(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.New(ctx, t.TempDir()+"/test.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer func() { _ = db.Close() }()

	if err := db.CreateWorkspace(ctx, &store.Workspace{
		ID:            "personal",
		Name:          "Personal",
		RootPath:      "/tmp/test/personal",
		DefaultPolicy: "deny",
		Source:        "api",
	}); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}

	if err := SeedDefaultWorkspaces(ctx, db); err != nil {
		t.Fatalf("seed default workspaces: %v", err)
	}

	global, err := db.GetWorkspace(ctx, "global")
	if err != nil {
		t.Fatalf("expected global workspace to exist: %v", err)
	}
	if global.RootPath != "/" {
		t.Fatalf("global root_path = %q, want /", global.RootPath)
	}
}
