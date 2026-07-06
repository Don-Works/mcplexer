package control

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/gateway"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

// seedWorkspaceNamed creates a workspace with a distinct name + root.
func seedWorkspaceNamed(t *testing.T, db *sqlite.DB, name string) *store.Workspace {
	t.Helper()
	ws := &store.Workspace{
		Name:          name,
		RootPath:      "/tmp/" + name,
		DefaultPolicy: "allow",
	}
	if err := db.CreateWorkspace(context.Background(), ws); err != nil {
		t.Fatalf("seed workspace %s: %v", name, err)
	}
	return ws
}

// seedScope creates an auth scope and returns it.
func seedScope(t *testing.T, db *sqlite.DB, name string) *store.AuthScope {
	t.Helper()
	a := &store.AuthScope{Name: name, Type: "generic"}
	if err := db.CreateAuthScope(context.Background(), a); err != nil {
		t.Fatalf("seed auth scope %s: %v", name, err)
	}
	return a
}

// seedRoute creates a route binding (workspace, server, scope).
func seedRoute(t *testing.T, db *sqlite.DB, wsID, serverID, scopeID string) *store.RouteRule {
	t.Helper()
	r := &store.RouteRule{
		WorkspaceID:        wsID,
		DownstreamServerID: serverID,
		AuthScopeID:        scopeID,
		Policy:             "allow",
	}
	if err := db.CreateRouteRule(context.Background(), r); err != nil {
		t.Fatalf("seed route: %v", err)
	}
	return r
}

func createRouteArgs(wsID, serverID, scopeID string) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(
		`{"workspace_id":%q,"downstream_server_id":%q,"auth_scope_id":%q,"policy":"allow"}`,
		wsID, serverID, scopeID))
}

// TestCreateRoute_DevEscapeCrossWorkspaceBlocked proves the incident
// shape is refused: a source-repo-trusted session may not route a
// server+scope that only another workspace references.
func TestCreateRoute_DevEscapeCrossWorkspaceBlocked(t *testing.T) {
	db := newTestDB(t)
	devWS := seedWorkspaceNamed(t, db, "dev")
	clientWS := seedWorkspaceNamed(t, db, "clients")
	srv := seedServer(t, db)
	scope := seedScope(t, db, "client-credential")
	seedRoute(t, db, clientWS.ID, srv.ID, scope.ID)

	ctx := gateway.WithAdminTrust(context.Background(), gateway.AdminTrustSourceRepo)
	_, err := handleCreateRoute(ctx, db, createRouteArgs(devWS.ID, srv.ID, scope.ID))
	if err == nil {
		t.Fatal("expected cross-workspace route to be blocked for source-repo trust")
	}
	if !strings.Contains(err.Error(), "workspace segregation") {
		t.Fatalf("unexpected error: %v", err)
	}
	// Nothing persisted.
	rules, lerr := db.ListRouteRules(ctx, devWS.ID)
	if lerr != nil {
		t.Fatalf("list routes: %v", lerr)
	}
	if len(rules) != 0 {
		t.Fatalf("blocked route was persisted: %+v", rules)
	}
}

// TestCreateRoute_DataDirTrustAllowed proves the full admin context
// keeps its authority over cross-workspace references (audited, not
// blocked), covering both the stamped data-dir level and unstamped
// trusted paths (REST, standalone control server).
func TestCreateRoute_DataDirTrustAllowed(t *testing.T) {
	for name, ctx := range map[string]context.Context{
		"datadir":   gateway.WithAdminTrust(context.Background(), gateway.AdminTrustDataDir),
		"unstamped": context.Background(),
	} {
		t.Run(name, func(t *testing.T) {
			db := newTestDB(t)
			devWS := seedWorkspaceNamed(t, db, "dev")
			clientWS := seedWorkspaceNamed(t, db, "clients")
			srv := seedServer(t, db)
			scope := seedScope(t, db, "client-credential")
			seedRoute(t, db, clientWS.ID, srv.ID, scope.ID)

			result, err := handleCreateRoute(ctx, db, createRouteArgs(devWS.ID, srv.ID, scope.ID))
			if err != nil {
				t.Fatalf("full-authority create_route failed: %v", err)
			}
			if text, isErr := parseToolResult(t, result); isErr {
				t.Fatalf("full-authority create_route errored: %s", text)
			}
		})
	}
}

// TestCreateRoute_DevEscapeFreshServerAllowed proves the provision flow
// survives: a server routed nowhere yet may be routed from the dev
// escape.
func TestCreateRoute_DevEscapeFreshServerAllowed(t *testing.T) {
	db := newTestDB(t)
	devWS := seedWorkspaceNamed(t, db, "dev")
	seedWorkspaceNamed(t, db, "clients")
	srv := seedServer(t, db) // no routes anywhere

	ctx := gateway.WithAdminTrust(context.Background(), gateway.AdminTrustSourceRepo)
	result, err := handleCreateRoute(ctx, db, createRouteArgs(devWS.ID, srv.ID, ""))
	if err != nil {
		t.Fatalf("fresh-server create_route blocked: %v", err)
	}
	if text, isErr := parseToolResult(t, result); isErr {
		t.Fatalf("fresh-server create_route errored: %s", text)
	}
}

// TestCreateRoute_DevEscapeOwnWorkspaceRefAllowed proves re-routing a
// server the target workspace already routes stays allowed (no new
// authority is granted).
func TestCreateRoute_DevEscapeOwnWorkspaceRefAllowed(t *testing.T) {
	db := newTestDB(t)
	devWS := seedWorkspaceNamed(t, db, "dev")
	clientWS := seedWorkspaceNamed(t, db, "clients")
	srv := seedServer(t, db)
	scope := seedScope(t, db, "shared-credential")
	seedRoute(t, db, clientWS.ID, srv.ID, scope.ID)
	seedRoute(t, db, devWS.ID, srv.ID, scope.ID) // dev already routes it

	ctx := gateway.WithAdminTrust(context.Background(), gateway.AdminTrustSourceRepo)
	result, err := handleCreateRoute(ctx, db, createRouteArgs(devWS.ID, srv.ID, scope.ID))
	if err != nil {
		t.Fatalf("own-workspace re-route blocked: %v", err)
	}
	if text, isErr := parseToolResult(t, result); isErr {
		t.Fatalf("own-workspace re-route errored: %s", text)
	}
}

// TestUpdateRoute_DevEscapeCrossWorkspaceBlocked proves update_route
// can't be used to swap an existing dev-workspace route onto another
// workspace's credentialed server, and that the updated rule's own
// prior references grant nothing.
func TestUpdateRoute_DevEscapeCrossWorkspaceBlocked(t *testing.T) {
	db := newTestDB(t)
	devWS := seedWorkspaceNamed(t, db, "dev")
	clientWS := seedWorkspaceNamed(t, db, "clients")
	devSrv := seedServerNamed(t, db, "dev-server", "echo", "dev")
	clientSrv := seedServerNamed(t, db, "client-server", "echo", "client")
	scope := seedScope(t, db, "client-credential")
	seedRoute(t, db, clientWS.ID, clientSrv.ID, scope.ID)
	own := seedRoute(t, db, devWS.ID, devSrv.ID, "")

	ctx := gateway.WithAdminTrust(context.Background(), gateway.AdminTrustSourceRepo)
	args := json.RawMessage(fmt.Sprintf(
		`{"id":%q,"downstream_server_id":%q,"auth_scope_id":%q}`,
		own.ID, clientSrv.ID, scope.ID))
	_, err := handleUpdateRoute(ctx, db, args)
	if err == nil {
		t.Fatal("expected cross-workspace update to be blocked for source-repo trust")
	}
	if !strings.Contains(err.Error(), "workspace segregation") {
		t.Fatalf("unexpected error: %v", err)
	}
	// The stored rule is unchanged.
	after, gerr := db.GetRouteRule(ctx, own.ID)
	if gerr != nil {
		t.Fatalf("get route: %v", gerr)
	}
	if after.DownstreamServerID != devSrv.ID {
		t.Fatalf("blocked update was persisted: %+v", after)
	}
}
