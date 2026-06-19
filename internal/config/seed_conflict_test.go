package config

import (
	"context"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
)

func TestSeedDefaultOAuthProviders_MigratesNameConflict(t *testing.T) {
	ctx := context.Background()
	db := newSeedTestDB(t, ctx)

	if err := db.CreateOAuthProvider(ctx, &store.OAuthProvider{
		ID:                    "legacy-github",
		Name:                  "GitHub",
		ClientID:              "keep-client",
		EncryptedClientSecret: []byte("keep-secret"),
		Source:                "api",
	}); err != nil {
		t.Fatalf("create legacy OAuth provider: %v", err)
	}

	if err := SeedDefaultOAuthProviders(ctx, db); err != nil {
		t.Fatalf("seed default OAuth providers: %v", err)
	}

	provider, err := db.GetOAuthProviderByName(ctx, "GitHub")
	if err != nil {
		t.Fatalf("get migrated provider: %v", err)
	}
	if provider.ID != "legacy-github" {
		t.Fatalf("provider id = %q, want legacy-github", provider.ID)
	}
	if provider.TemplateID != "github" {
		t.Fatalf("template_id = %q, want github", provider.TemplateID)
	}
	if provider.ClientID != "keep-client" {
		t.Fatalf("client_id = %q, want preserved client id", provider.ClientID)
	}
	if string(provider.EncryptedClientSecret) != "keep-secret" {
		t.Fatal("encrypted client secret was not preserved")
	}
}

func TestSeedDefaultAuthScopes_SkipsNameConflict(t *testing.T) {
	ctx := context.Background()
	db := newSeedTestDB(t, ctx)

	if err := db.CreateAuthScope(ctx, &store.AuthScope{
		ID:     "legacy-hammerspoon",
		Name:   "Hammerspoon Bridge",
		Type:   "env",
		Source: "api",
	}); err != nil {
		t.Fatalf("create legacy auth scope: %v", err)
	}

	if err := SeedDefaultAuthScopes(ctx, db); err != nil {
		t.Fatalf("seed default auth scopes: %v", err)
	}

	scope, err := db.GetAuthScopeByName(ctx, "Hammerspoon Bridge")
	if err != nil {
		t.Fatalf("get legacy auth scope: %v", err)
	}
	if scope.ID != "legacy-hammerspoon" {
		t.Fatalf("scope id = %q, want legacy-hammerspoon", scope.ID)
	}
}

func TestSeedDefaultDownstreamServers_SkipsNameConflict(t *testing.T) {
	ctx := context.Background()
	db := newSeedTestDB(t, ctx)

	if err := db.CreateDownstreamServer(ctx, &store.DownstreamServer{
		ID:            "legacy-hammerspoon",
		Name:          "Hammerspoon (macOS automation)",
		Transport:     "internal",
		ToolNamespace: "hammerspoon",
		Discovery:     "static",
		Source:        "api",
	}); err != nil {
		t.Fatalf("create legacy downstream server: %v", err)
	}

	if err := SeedDefaultDownstreamServers(ctx, db); err != nil {
		t.Fatalf("seed default downstream servers: %v", err)
	}

	server, err := db.GetDownstreamServerByName(ctx, "Hammerspoon (macOS automation)")
	if err != nil {
		t.Fatalf("get legacy downstream server: %v", err)
	}
	if server.ID != "legacy-hammerspoon" {
		t.Fatalf("server id = %q, want legacy-hammerspoon", server.ID)
	}
}
