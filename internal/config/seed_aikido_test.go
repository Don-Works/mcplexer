package config

import (
	"context"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

func TestSeedDefaultAuthScopes_EnsuresAikidoScopeWhenScopesExist(t *testing.T) {
	ctx := context.Background()
	db := newSeedTestDB(t, ctx)

	if err := db.CreateAuthScope(ctx, &store.AuthScope{
		ID:     "custom-scope",
		Name:   "Custom Scope",
		Type:   "env",
		Source: "api",
	}); err != nil {
		t.Fatalf("create custom auth scope: %v", err)
	}

	if err := SeedDefaultAuthScopes(ctx, db); err != nil {
		t.Fatalf("seed default auth scopes: %v", err)
	}

	scope, err := db.GetAuthScope(ctx, aikidoAuthScopeID)
	if err != nil {
		t.Fatalf("expected aikido auth scope to exist: %v", err)
	}
	if scope.Type != "client_credentials" {
		t.Fatalf("aikido auth scope type = %q, want client_credentials", scope.Type)
	}
}

func TestSeedDefaultDownstreamServers_EnsuresAikidoServerWhenServersExist(t *testing.T) {
	ctx := context.Background()
	db := newSeedTestDB(t, ctx)

	if err := db.CreateDownstreamServer(ctx, &store.DownstreamServer{
		ID:            "custom-server",
		Name:          "Custom Server",
		Transport:     "stdio",
		Command:       "custom-mcp",
		ToolNamespace: "custom",
		Discovery:     "dynamic",
		Source:        "api",
	}); err != nil {
		t.Fatalf("create custom downstream server: %v", err)
	}

	if err := SeedDefaultDownstreamServers(ctx, db); err != nil {
		t.Fatalf("seed default downstream servers: %v", err)
	}

	aikido, err := db.GetDownstreamServer(ctx, aikidoServerID)
	if err != nil {
		t.Fatalf("expected aikido downstream server to exist: %v", err)
	}
	if aikido.Transport != "internal" {
		t.Fatalf("aikido transport = %q, want internal", aikido.Transport)
	}
	if !aikido.Disabled {
		t.Fatalf("aikido disabled = %v, want true", aikido.Disabled)
	}
}

func TestSeedDefaultRouteRules_DoesNotSeedAikidoRoutesGlobally(t *testing.T) {
	ctx := context.Background()
	db := newSeedTestDB(t, ctx)

	if err := db.CreateWorkspace(ctx, &store.Workspace{
		ID:            "global",
		Name:          "Global",
		RootPath:      "/",
		DefaultPolicy: "deny",
		Source:        "default",
	}); err != nil {
		t.Fatalf("create global workspace: %v", err)
	}

	if err := db.CreateDownstreamServer(ctx, &store.DownstreamServer{
		ID:            "mcpx-builtin",
		Name:          "MCPlexer Built-in Tools",
		Transport:     "internal",
		ToolNamespace: "mcpx",
		Discovery:     "static",
		Source:        "default",
	}); err != nil {
		t.Fatalf("create builtin server: %v", err)
	}

	if err := SeedDefaultRouteRules(ctx, db); err != nil {
		t.Fatalf("seed default route rules: %v", err)
	}

	// Aikido routes should NOT be auto-seeded — they are workspace-specific.
	_, err := db.GetRouteRule(ctx, aikidoReadRouteID)
	if err == nil {
		t.Fatal("expected aikido read route to NOT be seeded globally")
	}
	_, err = db.GetRouteRule(ctx, aikidoMutateRouteID)
	if err == nil {
		t.Fatal("expected aikido mutate route to NOT be seeded globally")
	}
}

func newSeedTestDB(t *testing.T, ctx context.Context) *sqlite.DB {
	t.Helper()

	db, err := sqlite.New(ctx, t.TempDir()+"/test.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}
