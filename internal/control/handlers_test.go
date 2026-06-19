package control

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
)

func TestHandleListServers(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	// Empty list.
	result, err := handleListServers(ctx, db, nil)
	if err != nil {
		t.Fatal(err)
	}
	text, isErr := parseToolResult(t, result)
	if isErr {
		t.Fatal("unexpected error result")
	}
	// Empty slice marshals to "null" in Go; verify parseable.
	var servers []store.DownstreamServer
	if err := json.Unmarshal([]byte(text), &servers); err != nil {
		t.Fatalf("unmarshal empty list: %v", err)
	}
	if len(servers) != 0 {
		t.Fatalf("expected 0 servers, got %d", len(servers))
	}

	// With one server.
	seedServer(t, db)
	result, err = handleListServers(ctx, db, nil)
	if err != nil {
		t.Fatal(err)
	}
	text, _ = parseToolResult(t, result)

	if err := json.Unmarshal([]byte(text), &servers); err != nil {
		t.Fatal(err)
	}
	if len(servers) != 1 {
		t.Fatalf("got %d servers, want 1", len(servers))
	}
	if servers[0].Name != "test-server" {
		t.Fatalf("name = %q", servers[0].Name)
	}
}

func TestHandleCreateServer(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	args := json.RawMessage(`{
		"name": "new-server",
		"command": "node",
		"args": ["server.js"],
		"tool_namespace": "myns"
	}`)

	result, err := handleCreateServer(ctx, db, args)
	if err != nil {
		t.Fatal(err)
	}
	text, isErr := parseToolResult(t, result)
	if isErr {
		t.Fatal("unexpected error result")
	}

	var srv store.DownstreamServer
	if err := json.Unmarshal([]byte(text), &srv); err != nil {
		t.Fatal(err)
	}
	if srv.ID == "" {
		t.Fatal("expected ID to be generated")
	}
	if srv.Name != "new-server" {
		t.Fatalf("name = %q", srv.Name)
	}
	if srv.Transport != "stdio" {
		t.Fatalf("transport = %q, expected default stdio", srv.Transport)
	}

	// Verify in DB.
	got, err := db.GetDownstreamServer(ctx, srv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ToolNamespace != "myns" {
		t.Fatalf("namespace = %q", got.ToolNamespace)
	}
}

func TestHandleUpdateServer(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	srv := seedServer(t, db)

	args, _ := json.Marshal(map[string]any{
		"id":   srv.ID,
		"name": "updated-name",
	})

	result, err := handleUpdateServer(ctx, db, args)
	if err != nil {
		t.Fatal(err)
	}
	text, isErr := parseToolResult(t, result)
	if isErr {
		t.Fatal("unexpected error result")
	}

	var updated store.DownstreamServer
	if err := json.Unmarshal([]byte(text), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Name != "updated-name" {
		t.Fatalf("name = %q", updated.Name)
	}
	// Verify unchanged fields preserved.
	if updated.Command != "echo" {
		t.Fatalf("command = %q, should be preserved", updated.Command)
	}
}

func TestHandleDeleteServer(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	srv := seedServer(t, db)

	args, _ := json.Marshal(map[string]string{"id": srv.ID})
	result, err := handleDeleteServer(ctx, db, args)
	if err != nil {
		t.Fatal(err)
	}
	text, _ := parseToolResult(t, result)
	if text != "deleted" {
		t.Fatalf("result = %q", text)
	}

	// Verify gone.
	_, err = db.GetDownstreamServer(ctx, srv.ID)
	if err != store.ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestHandleGetServerNotFound(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	args := json.RawMessage(`{"id": "nonexistent"}`)
	_, err := handleGetServer(ctx, db, args)
	if err == nil {
		t.Fatal("expected error for nonexistent server")
	}
}

func TestHandleStatus(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	// Empty DB.
	result, err := handleStatus(ctx, db, nil)
	if err != nil {
		t.Fatal(err)
	}
	text, isErr := parseToolResult(t, result)
	if isErr {
		t.Fatal("unexpected error result")
	}

	var status map[string]int
	if err := json.Unmarshal([]byte(text), &status); err != nil {
		t.Fatal(err)
	}
	if status["downstream_servers"] != 0 {
		t.Fatalf("servers = %d", status["downstream_servers"])
	}
	if status["workspaces"] != 1 {
		t.Fatalf("workspaces = %d, want 1", status["workspaces"])
	}

	// Add some data.
	seedServer(t, db)
	seedWorkspace(t, db)

	result, err = handleStatus(ctx, db, nil)
	if err != nil {
		t.Fatal(err)
	}
	text, _ = parseToolResult(t, result)
	if err := json.Unmarshal([]byte(text), &status); err != nil {
		t.Fatal(err)
	}
	if status["downstream_servers"] != 1 {
		t.Fatalf("servers = %d, want 1", status["downstream_servers"])
	}
	if status["workspaces"] != 2 {
		t.Fatalf("workspaces = %d, want 2", status["workspaces"])
	}
}

func TestHandleCreateWorkspace(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	args := json.RawMessage(`{"name": "my-workspace", "root_path": "/home/user/project"}`)
	result, err := handleCreateWorkspace(ctx, db, args)
	if err != nil {
		t.Fatal(err)
	}
	text, isErr := parseToolResult(t, result)
	if isErr {
		t.Fatal("unexpected error result")
	}

	var ws store.Workspace
	if err := json.Unmarshal([]byte(text), &ws); err != nil {
		t.Fatal(err)
	}
	if ws.ID == "" {
		t.Fatal("expected ID")
	}
	if ws.DefaultPolicy != "deny" {
		t.Fatalf("default_policy = %q, expected default deny", ws.DefaultPolicy)
	}
}

func TestHandleCreateAndListRoutes(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	ws := seedWorkspace(t, db)
	srv := seedServer(t, db)

	// Create route.
	args, _ := json.Marshal(map[string]any{
		"workspace_id":         ws.ID,
		"downstream_server_id": srv.ID,
		"policy":               "allow",
		"priority":             10,
		"path_glob":            "**",
	})
	result, err := handleCreateRoute(ctx, db, args)
	if err != nil {
		t.Fatal(err)
	}
	_, isErr := parseToolResult(t, result)
	if isErr {
		t.Fatal("unexpected error result")
	}

	// List routes.
	listArgs, _ := json.Marshal(map[string]string{"workspace_id": ws.ID})
	result, err = handleListRoutes(ctx, db, listArgs)
	if err != nil {
		t.Fatal(err)
	}
	text, _ := parseToolResult(t, result)

	var routes []store.RouteRule
	if err := json.Unmarshal([]byte(text), &routes); err != nil {
		t.Fatal(err)
	}
	if len(routes) != 1 {
		t.Fatalf("got %d routes, want 1", len(routes))
	}
}

func TestHandleCreateAuthScope(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	args := json.RawMessage(`{"name": "gh-token", "type": "env"}`)
	result, err := handleCreateAuthScope(ctx, db, args)
	if err != nil {
		t.Fatal(err)
	}
	text, isErr := parseToolResult(t, result)
	if isErr {
		t.Fatal("unexpected error result")
	}

	var scope store.AuthScope
	if err := json.Unmarshal([]byte(text), &scope); err != nil {
		t.Fatal(err)
	}
	if scope.ID == "" {
		t.Fatal("expected ID")
	}
	if scope.Name != "gh-token" {
		t.Fatalf("name = %q", scope.Name)
	}
}

func TestHandleRequiredFieldValidation(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	tests := []struct {
		name    string
		handler handlerFunc
		args    string
	}{
		{"create_server_no_name", handleCreateServer, `{"command":"x","tool_namespace":"ns"}`},
		{"get_server_no_id", handleGetServer, `{}`},
		{"create_workspace_no_name", handleCreateWorkspace, `{}`},
		{"create_route_no_workspace", handleCreateRoute, `{"downstream_server_id":"x","policy":"allow"}`},
		{"create_route_no_server", handleCreateRoute, `{"workspace_id":"x","policy":"allow"}`},
		{"create_route_no_policy", handleCreateRoute, `{"workspace_id":"x","downstream_server_id":"y"}`},
		{"create_auth_no_name", handleCreateAuthScope, `{"type":"env"}`},
		{"create_auth_no_type", handleCreateAuthScope, `{"name":"x"}`},
		{"list_routes_no_ws", handleListRoutes, `{}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := tt.handler(ctx, db, json.RawMessage(tt.args))
			if err != nil {
				return
			}
			text, isErr := parseToolResult(t, result)
			if !isErr {
				t.Fatalf("expected validation error result, got %s", text)
			}
		})
	}
}
