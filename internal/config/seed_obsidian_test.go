package config

import (
	"context"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
)

func TestSeedDefaultAuthScopes_EnsuresObsidianScopeWhenScopesExist(t *testing.T) {
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

	scope, err := db.GetAuthScope(ctx, obsidianAuthScopeID)
	if err != nil {
		t.Fatalf("expected obsidian auth scope to exist: %v", err)
	}
	if scope.Type != "header" {
		t.Fatalf("obsidian auth scope type = %q, want header", scope.Type)
	}
	if scope.Name != "Obsidian Local REST API" {
		t.Fatalf("obsidian auth scope name = %q, want Obsidian Local REST API", scope.Name)
	}
}

func TestSeedDefaultDownstreamServers_EnsuresObsidianServerWhenServersExist(t *testing.T) {
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

	srv, err := db.GetDownstreamServer(ctx, obsidianServerID)
	if err != nil {
		t.Fatalf("expected obsidian downstream server to exist: %v", err)
	}
	if srv.Transport != "http" {
		t.Fatalf("obsidian transport = %q, want http", srv.Transport)
	}
	if srv.URL == nil || *srv.URL != "http://127.0.0.1:27123/mcp/" {
		t.Fatalf("obsidian url = %v, want http://127.0.0.1:27123/mcp/", srv.URL)
	}
	if srv.ToolNamespace != "obsidian" {
		t.Fatalf("obsidian tool_namespace = %q, want obsidian", srv.ToolNamespace)
	}
	if !srv.Disabled {
		t.Fatalf("obsidian disabled = %v, want true (external server policy)", srv.Disabled)
	}
}

func TestObsidianEnvFieldsRegistered(t *testing.T) {
	fields := GetEnvFields(obsidianAuthScopeID)
	if len(fields) != 1 {
		t.Fatalf("expected 1 env field, got %d", len(fields))
	}
	if fields[0].Key != "Authorization" {
		t.Fatalf("env field key = %q, want Authorization", fields[0].Key)
	}
	if !fields[0].Secret {
		t.Fatal("expected Authorization to be marked as secret")
	}
}
