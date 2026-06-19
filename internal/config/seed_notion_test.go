package config

import (
	"context"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
)

func TestSeedDefaultAuthScopes_EnsuresNotionScopeWhenScopesExist(t *testing.T) {
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

	scope, err := db.GetAuthScope(ctx, notionAuthScopeID)
	if err != nil {
		t.Fatalf("expected notion auth scope to exist: %v", err)
	}
	if scope.Type != "env" {
		t.Fatalf("notion auth scope type = %q, want env", scope.Type)
	}
	if scope.Name != "Notion Integration Token" {
		t.Fatalf("notion auth scope name = %q, want Notion Integration Token", scope.Name)
	}
}

func TestNotionEnvFieldsRegistered(t *testing.T) {
	fields := GetEnvFields(notionAuthScopeID)
	if len(fields) != 1 {
		t.Fatalf("expected 1 env field, got %d", len(fields))
	}
	if fields[0].Key != "NOTION_TOKEN" {
		t.Fatalf("env field key = %q, want NOTION_TOKEN", fields[0].Key)
	}
	if !fields[0].Secret {
		t.Fatal("expected NOTION_TOKEN to be marked as secret")
	}
}

func TestSeedDefaultDownstreamServers_EnsuresNotionServerWhenServersExist(t *testing.T) {
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

	srv, err := db.GetDownstreamServer(ctx, "notion")
	if err != nil {
		t.Fatalf("expected notion downstream server to exist: %v", err)
	}
	if srv.Transport != "http" {
		t.Fatalf("notion transport = %q, want http", srv.Transport)
	}
	if srv.URL == nil || *srv.URL != "https://mcp.notion.com/mcp" {
		t.Fatalf("notion url = %v, want https://mcp.notion.com/mcp", srv.URL)
	}
	if srv.ToolNamespace != "notion" {
		t.Fatalf("notion tool_namespace = %q, want notion", srv.ToolNamespace)
	}
	if !srv.Disabled {
		t.Fatalf("notion disabled = %v, want true (external server policy)", srv.Disabled)
	}
}
