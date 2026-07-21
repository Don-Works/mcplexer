package control

import (
	"context"
	"encoding/json"
	"testing"
)

// TestCallInvalidatesRouteCacheOnMutation guards the parity between the MCP
// admin surface and the REST API: route/workspace/server mutations through
// InternalBackend.Call must fire the route invalidator so the routing rules
// cache picks up the change immediately, not after the 30s TTL. Regression
// found 2026-07-06: a route created via mcplexer__create_route stayed
// unroutable ("no matching route") until the cache expired.
func TestCallInvalidatesRouteCacheOnMutation(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	ws := seedWorkspace(t, db)
	srv := seedServer(t, db)

	b := NewInternalBackend(db, nil)
	invalidations := 0
	b.SetRouteInvalidator(func() { invalidations++ })

	createArgs, _ := json.Marshal(map[string]any{
		"workspace_id":         ws.ID,
		"downstream_server_id": srv.ID,
		"policy":               "allow",
		"path_glob":            "**",
	})
	result, err := b.Call(ctx, "create_route", createArgs)
	if err != nil {
		t.Fatal(err)
	}
	if _, isErr := parseToolResult(t, result); isErr {
		t.Fatal("unexpected error result from create_route")
	}
	if invalidations != 1 {
		t.Fatalf("invalidations after create_route = %d, want 1", invalidations)
	}

	// Read-only calls must NOT invalidate.
	listArgs, _ := json.Marshal(map[string]string{"workspace_id": ws.ID})
	if _, err := b.Call(ctx, "list_routes", listArgs); err != nil {
		t.Fatal(err)
	}
	if invalidations != 1 {
		t.Fatalf("invalidations after list_routes = %d, want 1 (reads must not invalidate)", invalidations)
	}

	// A failed mutation (validation error) must NOT invalidate.
	if _, err := b.Call(ctx, "delete_route", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if invalidations != 1 {
		t.Fatalf("invalidations after failed delete_route = %d, want 1", invalidations)
	}

	// Workspace mutations are part of the same set (they change the
	// ancestor chain routing resolves against).
	wsArgs := json.RawMessage(`{"name": "inval-ws", "root_path": "/tmp/inval-ws"}`)
	result, err = b.Call(ctx, "create_workspace", wsArgs)
	if err != nil {
		t.Fatal(err)
	}
	if _, isErr := parseToolResult(t, result); isErr {
		t.Fatal("unexpected error result from create_workspace")
	}
	if invalidations != 2 {
		t.Fatalf("invalidations after create_workspace = %d, want 2", invalidations)
	}

	// Nil invalidator must not panic.
	nb := NewInternalBackend(db, nil)
	if _, err := nb.Call(ctx, "create_route", createArgs); err != nil {
		t.Fatal(err)
	}
}
