package config

import (
	"context"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
)

func TestSeedDefaultDownstreamServers_EnsuresLMStudioServerWhenServersExist(t *testing.T) {
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

	srv, err := db.GetDownstreamServer(ctx, "lmstudio")
	if err != nil {
		t.Fatalf("expected lmstudio downstream server to exist: %v", err)
	}
	if srv.Transport != "internal" {
		t.Fatalf("lmstudio transport = %q, want internal", srv.Transport)
	}
	if srv.ToolNamespace != "lmstudio" {
		t.Fatalf("lmstudio namespace = %q, want lmstudio", srv.ToolNamespace)
	}
}

func TestSeedDefaultRouteRules_EnsuresLMStudioRouteWhenRulesExist(t *testing.T) {
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
	if err := db.CreateDownstreamServer(ctx, &store.DownstreamServer{
		ID:            "lmstudio",
		Name:          "LM Studio (local models)",
		Transport:     "internal",
		ToolNamespace: "lmstudio",
		Discovery:     "static",
		Source:        "default",
	}); err != nil {
		t.Fatalf("create lmstudio downstream server: %v", err)
	}
	if err := db.CreateRouteRule(ctx, &store.RouteRule{
		ID:                 "custom-route",
		Name:               "Custom Route",
		Priority:           1,
		WorkspaceID:        "global",
		PathGlob:           "**",
		ToolMatch:          []byte(`["custom__*"]`),
		DownstreamServerID: "custom-server",
		Policy:             "allow",
		Source:             "api",
	}); err != nil {
		t.Fatalf("create custom route: %v", err)
	}

	if err := SeedDefaultRouteRules(ctx, db); err != nil {
		t.Fatalf("seed default route rules: %v", err)
	}

	route, err := db.GetRouteRule(ctx, "lmstudio-allow")
	if err != nil {
		t.Fatalf("expected lmstudio route to exist: %v", err)
	}
	if route.DownstreamServerID != "lmstudio" {
		t.Fatalf("route downstream = %q, want lmstudio", route.DownstreamServerID)
	}
}
