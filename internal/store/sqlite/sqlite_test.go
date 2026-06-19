package sqlite_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

func newTestDB(t *testing.T) *sqlite.DB {
	t.Helper()
	db, err := sqlite.New(context.Background(), t.TempDir()+"/test.db")
	if err != nil {
		t.Fatalf("new test db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestPing(t *testing.T) {
	db := newTestDB(t)
	if err := db.Ping(context.Background()); err != nil {
		t.Fatalf("ping: %v", err)
	}
}

func TestWorkspaceCRUD(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	w := &store.Workspace{
		Name:          "test-ws",
		RootPath:      "/tmp/test",
		Tags:          json.RawMessage(`["go","test"]`),
		DefaultPolicy: "allow",
	}

	// Create.
	if err := db.CreateWorkspace(ctx, w); err != nil {
		t.Fatalf("create: %v", err)
	}
	if w.ID == "" {
		t.Fatal("expected ID to be set")
	}

	// Get by ID.
	got, err := db.GetWorkspace(ctx, w.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Name != "test-ws" {
		t.Fatalf("name = %q, want %q", got.Name, "test-ws")
	}

	// Get by name.
	got, err = db.GetWorkspaceByName(ctx, "test-ws")
	if err != nil {
		t.Fatalf("get by name: %v", err)
	}
	if got.ID != w.ID {
		t.Fatalf("id mismatch")
	}

	// List.
	list, err := db.ListWorkspaces(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	found := false
	for _, row := range list {
		if row.ID == w.ID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("created workspace missing from list: %+v", list)
	}

	// Update.
	got.Name = "updated-ws"
	if err := db.UpdateWorkspace(ctx, got); err != nil {
		t.Fatalf("update: %v", err)
	}
	got2, _ := db.GetWorkspace(ctx, w.ID)
	if got2.Name != "updated-ws" {
		t.Fatalf("name after update = %q", got2.Name)
	}

	// Delete.
	if err := db.DeleteWorkspace(ctx, w.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	_, err = db.GetWorkspace(ctx, w.ID)
	if err != store.ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestWorkspaceDuplicate(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	w := &store.Workspace{Name: "dup", DefaultPolicy: "allow"}
	if err := db.CreateWorkspace(ctx, w); err != nil {
		t.Fatal(err)
	}
	w2 := &store.Workspace{Name: "dup", DefaultPolicy: "deny"}
	if err := db.CreateWorkspace(ctx, w2); err != store.ErrAlreadyExists {
		t.Fatalf("expected ErrAlreadyExists, got %v", err)
	}
}

func TestAuthScopeCRUD(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	a := &store.AuthScope{
		Name:          "gh-token",
		DisplayName:   "GitHub Token",
		Type:          "env",
		EncryptedData: []byte("encrypted-stuff"),
	}

	if err := db.CreateAuthScope(ctx, a); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := db.GetAuthScope(ctx, a.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if string(got.EncryptedData) != "encrypted-stuff" {
		t.Fatalf("encrypted data mismatch")
	}
	if got.DisplayName != "GitHub Token" {
		t.Fatalf("display_name = %q, want %q", got.DisplayName, "GitHub Token")
	}

	got, err = db.GetAuthScopeByName(ctx, "gh-token")
	if err != nil {
		t.Fatalf("get by name: %v", err)
	}
	if got.DisplayName != "GitHub Token" {
		t.Fatalf("get-by-name display_name = %q", got.DisplayName)
	}

	list, err := db.ListAuthScopes(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("len = %d", len(list))
	}
	if list[0].DisplayName != "GitHub Token" {
		t.Fatalf("list display_name = %q", list[0].DisplayName)
	}

	got.Name = "updated-token"
	got.DisplayName = "Renamed Token"
	if err := db.UpdateAuthScope(ctx, got); err != nil {
		t.Fatalf("update: %v", err)
	}
	reread, err := db.GetAuthScope(ctx, got.ID)
	if err != nil {
		t.Fatalf("re-read after update: %v", err)
	}
	if reread.DisplayName != "Renamed Token" {
		t.Fatalf("post-update display_name = %q", reread.DisplayName)
	}

	if err := db.DeleteAuthScope(ctx, a.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	_, err = db.GetAuthScope(ctx, a.ID)
	if err != store.ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestDownstreamServerCRUD(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	ds := &store.DownstreamServer{
		Name:           "github-mcp",
		Transport:      "stdio",
		Command:        "npx",
		Args:           json.RawMessage(`["-y","@mcp/server-github"]`),
		ToolNamespace:  "github",
		IdleTimeoutSec: 300,
		MaxInstances:   1,
		RestartPolicy:  "on-failure",
	}

	if err := db.CreateDownstreamServer(ctx, ds); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := db.GetDownstreamServer(ctx, ds.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ToolNamespace != "github" {
		t.Fatalf("namespace = %q", got.ToolNamespace)
	}

	got, err = db.GetDownstreamServerByName(ctx, "github-mcp")
	if err != nil {
		t.Fatalf("get by name: %v", err)
	}

	list, err := db.ListDownstreamServers(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("len = %d", len(list))
	}

	got.Name = "github-mcp-v2"
	if err := db.UpdateDownstreamServer(ctx, got); err != nil {
		t.Fatalf("update: %v", err)
	}

	cache := json.RawMessage(`{"tools":["create_issue"]}`)
	if err := db.UpdateCapabilitiesCache(ctx, ds.ID, cache); err != nil {
		t.Fatalf("update caps: %v", err)
	}
	got2, _ := db.GetDownstreamServer(ctx, ds.ID)
	if string(got2.CapabilitiesCache) != `{"tools":["create_issue"]}` {
		t.Fatalf("caps = %s", got2.CapabilitiesCache)
	}

	if err := db.DeleteDownstreamServer(ctx, ds.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
}

func TestRouteRuleCRUD(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	// Create prerequisites.
	ws := &store.Workspace{Name: "rule-ws", DefaultPolicy: "allow"}
	if err := db.CreateWorkspace(ctx, ws); err != nil {
		t.Fatal(err)
	}
	ds := &store.DownstreamServer{
		Name: "rule-ds", Transport: "stdio",
		ToolNamespace: "test", RestartPolicy: "on-failure",
	}
	if err := db.CreateDownstreamServer(ctx, ds); err != nil {
		t.Fatal(err)
	}

	r := &store.RouteRule{
		Priority:           100,
		WorkspaceID:        ws.ID,
		PathGlob:           "**",
		ToolMatch:          json.RawMessage(`["github__*"]`),
		ScopePolicy:        json.RawMessage(`{"org":["acme"],"repo":["acme/mcplexer"]}`),
		DownstreamServerID: ds.ID,
		Policy:             "allow",
		LogLevel:           "info",
	}

	if err := db.CreateRouteRule(ctx, r); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := db.GetRouteRule(ctx, r.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Priority != 100 {
		t.Fatalf("priority = %d", got.Priority)
	}
	if string(got.ScopePolicy) != `{"org":["acme"],"repo":["acme/mcplexer"]}` {
		t.Fatalf("scope_policy = %s", got.ScopePolicy)
	}

	list, err := db.ListRouteRules(ctx, ws.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("len = %d", len(list))
	}

	got.Priority = 200
	if err := db.UpdateRouteRule(ctx, got); err != nil {
		t.Fatalf("update: %v", err)
	}

	if err := db.DeleteRouteRule(ctx, r.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
}

func TestSessionCRUD(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	pid := 1234
	s := &store.Session{
		ClientType: "claude-code",
		ClientPID:  &pid,
		ModelHint:  "opus",
	}

	if err := db.CreateSession(ctx, s); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := db.GetSession(ctx, s.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ClientType != "claude-code" {
		t.Fatalf("type = %q", got.ClientType)
	}
	if got.DisconnectedAt != nil {
		t.Fatal("should not be disconnected yet")
	}

	active, err := db.ListActiveSessions(ctx)
	if err != nil {
		t.Fatalf("list active: %v", err)
	}
	if len(active) != 1 {
		t.Fatalf("active len = %d", len(active))
	}

	if err := db.DisconnectSession(ctx, s.ID); err != nil {
		t.Fatalf("disconnect: %v", err)
	}

	got, _ = db.GetSession(ctx, s.ID)
	if got.DisconnectedAt == nil {
		t.Fatal("should be disconnected")
	}

	active, _ = db.ListActiveSessions(ctx)
	if len(active) != 0 {
		t.Fatalf("active after disconnect = %d", len(active))
	}
}

func TestAuditCRUD(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	// Insert a few records.
	for i, name := range []string{"github__create_issue", "slack__post_message", "github__list_prs"} {
		r := &store.AuditRecord{
			Timestamp:   time.Now().UTC().Add(time.Duration(i) * time.Second),
			ToolName:    name,
			Status:      "success",
			LatencyMs:   50 + i*10,
			WorkspaceID: "ws1",
		}
		if err := db.InsertAuditRecord(ctx, r); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}

	// Query all.
	records, total, err := db.QueryAuditRecords(ctx, store.AuditFilter{Limit: 10})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if total != 3 || len(records) != 3 {
		t.Fatalf("total=%d, len=%d", total, len(records))
	}

	// Query by tool name.
	tool := "github__create_issue"
	_, total, err = db.QueryAuditRecords(ctx, store.AuditFilter{
		ToolName: &tool,
		Limit:    10,
	})
	if err != nil {
		t.Fatalf("query by tool: %v", err)
	}
	if total != 1 {
		t.Fatalf("total by tool = %d", total)
	}

	// Stats.
	stats, err := db.GetAuditStats(ctx, "ws1",
		time.Now().Add(-time.Hour), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if stats.TotalRequests != 3 {
		t.Fatalf("total requests = %d", stats.TotalRequests)
	}
}

func TestDashboardTimeSeries(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	base := time.Date(2025, 1, 15, 10, 0, 0, 0, time.UTC)

	// Insert records across 3 different minutes.
	records := []struct {
		offset  time.Duration
		session string
		server  string
		status  string
	}{
		{0 * time.Minute, "s1", "srv-a", "success"},
		{0 * time.Minute, "s2", "srv-a", "error"},
		{0 * time.Minute, "s1", "srv-b", "success"},
		{2 * time.Minute, "s3", "srv-a", "success"},
		{4 * time.Minute, "s1", "srv-b", "error"},
		{4 * time.Minute, "s1", "srv-b", "error"},
	}
	for i, rec := range records {
		r := &store.AuditRecord{
			Timestamp:          base.Add(rec.offset).Add(time.Duration(i) * time.Second),
			SessionID:          rec.session,
			DownstreamServerID: rec.server,
			ToolName:           "test__tool",
			Status:             rec.status,
			LatencyMs:          10,
		}
		if err := db.InsertAuditRecord(ctx, r); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}

	points, err := db.GetDashboardTimeSeries(ctx, base.Add(-1*time.Minute), base.Add(5*time.Minute))
	if err != nil {
		t.Fatalf("get time series: %v", err)
	}

	if len(points) != 3 {
		t.Fatalf("expected 3 buckets, got %d", len(points))
	}

	// Minute 0: 2 sessions (s1, s2), 2 servers (srv-a, srv-b), 3 total, 1 error
	p := points[0]
	if p.Sessions != 2 {
		t.Errorf("bucket 0 sessions = %d, want 2", p.Sessions)
	}
	if p.Servers != 2 {
		t.Errorf("bucket 0 servers = %d, want 2", p.Servers)
	}
	if p.Total != 3 {
		t.Errorf("bucket 0 total = %d, want 3", p.Total)
	}
	if p.Errors != 1 {
		t.Errorf("bucket 0 errors = %d, want 1", p.Errors)
	}

	// Minute 2: 1 session (s3), 1 server (srv-a), 1 total, 0 errors
	p = points[1]
	if p.Sessions != 1 || p.Servers != 1 || p.Total != 1 || p.Errors != 0 {
		t.Errorf("bucket 2: sessions=%d servers=%d total=%d errors=%d",
			p.Sessions, p.Servers, p.Total, p.Errors)
	}

	// Minute 4: 1 session (s1), 1 server (srv-b), 2 total, 2 errors
	p = points[2]
	if p.Sessions != 1 || p.Servers != 1 || p.Total != 2 || p.Errors != 2 {
		t.Errorf("bucket 4: sessions=%d servers=%d total=%d errors=%d",
			p.Sessions, p.Servers, p.Total, p.Errors)
	}
}

func TestTx(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	// Transaction commit.
	err := db.Tx(ctx, func(tx store.Store) error {
		return tx.CreateWorkspace(ctx, &store.Workspace{
			Name: "tx-ws", DefaultPolicy: "allow",
		})
	})
	if err != nil {
		t.Fatalf("tx: %v", err)
	}

	_, err = db.GetWorkspaceByName(ctx, "tx-ws")
	if err != nil {
		t.Fatalf("get after tx: %v", err)
	}
}

// --- Cascade delete tests ---

func createTestApproval(t *testing.T, db *sqlite.DB, wsID, dsID, asID, rrID string) string {
	t.Helper()
	ctx := context.Background()
	a := &store.ToolApproval{
		Status:             "pending",
		WorkspaceID:        wsID,
		DownstreamServerID: dsID,
		AuthScopeID:        asID,
		RouteRuleID:        rrID,
		ToolName:           "test__tool",
		TimeoutSec:         60,
	}
	if err := db.CreateToolApproval(ctx, a); err != nil {
		t.Fatalf("create tool approval: %v", err)
	}
	return a.ID
}

func TestDeleteWorkspaceCascade(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	ws := &store.Workspace{Name: "cascade-ws", DefaultPolicy: "allow"}
	if err := db.CreateWorkspace(ctx, ws); err != nil {
		t.Fatal(err)
	}
	ds := &store.DownstreamServer{
		Name: "cascade-ds", Transport: "stdio",
		ToolNamespace: "test", RestartPolicy: "on-failure",
	}
	if err := db.CreateDownstreamServer(ctx, ds); err != nil {
		t.Fatal(err)
	}

	r := &store.RouteRule{
		Priority: 10, WorkspaceID: ws.ID,
		DownstreamServerID: ds.ID, Policy: "allow",
		ToolMatch: json.RawMessage(`["*"]`),
	}
	if err := db.CreateRouteRule(ctx, r); err != nil {
		t.Fatal(err)
	}

	approvalID := createTestApproval(t, db, ws.ID, ds.ID, "", r.ID)

	// Delete the workspace — should cascade.
	if err := db.DeleteWorkspace(ctx, ws.ID); err != nil {
		t.Fatalf("delete workspace: %v", err)
	}

	// Route rule should be gone.
	_, err := db.GetRouteRule(ctx, r.ID)
	if err != store.ErrNotFound {
		t.Fatalf("expected route rule ErrNotFound, got %v", err)
	}

	// Approval should be cancelled.
	a, err := db.GetToolApproval(ctx, approvalID)
	if err != nil {
		t.Fatalf("get approval: %v", err)
	}
	if a.Status != "cancelled" {
		t.Fatalf("approval status = %q, want cancelled", a.Status)
	}
	if a.ResolvedAt == nil {
		t.Fatal("approval resolved_at should be set")
	}

	// Downstream should still exist.
	if _, err := db.GetDownstreamServer(ctx, ds.ID); err != nil {
		t.Fatalf("downstream should still exist: %v", err)
	}
}

func TestDeleteDownstreamServerCascade(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	ws := &store.Workspace{Name: "ds-cascade-ws", DefaultPolicy: "allow"}
	if err := db.CreateWorkspace(ctx, ws); err != nil {
		t.Fatal(err)
	}
	ds := &store.DownstreamServer{
		Name: "ds-cascade-ds", Transport: "stdio",
		ToolNamespace: "test", RestartPolicy: "on-failure",
	}
	if err := db.CreateDownstreamServer(ctx, ds); err != nil {
		t.Fatal(err)
	}

	r := &store.RouteRule{
		Priority: 10, WorkspaceID: ws.ID,
		DownstreamServerID: ds.ID, Policy: "allow",
		ToolMatch: json.RawMessage(`["*"]`),
	}
	if err := db.CreateRouteRule(ctx, r); err != nil {
		t.Fatal(err)
	}

	approvalID := createTestApproval(t, db, ws.ID, ds.ID, "", r.ID)

	if err := db.DeleteDownstreamServer(ctx, ds.ID); err != nil {
		t.Fatalf("delete downstream: %v", err)
	}

	// Route rule should be gone.
	_, err := db.GetRouteRule(ctx, r.ID)
	if err != store.ErrNotFound {
		t.Fatalf("expected route rule ErrNotFound, got %v", err)
	}

	// Approval should be cancelled.
	a, err := db.GetToolApproval(ctx, approvalID)
	if err != nil {
		t.Fatalf("get approval: %v", err)
	}
	if a.Status != "cancelled" {
		t.Fatalf("approval status = %q, want cancelled", a.Status)
	}

	// Workspace should still exist.
	if _, err := db.GetWorkspace(ctx, ws.ID); err != nil {
		t.Fatalf("workspace should still exist: %v", err)
	}
}

func TestDeleteAuthScopeCascade(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	ws := &store.Workspace{Name: "as-cascade-ws", DefaultPolicy: "allow"}
	if err := db.CreateWorkspace(ctx, ws); err != nil {
		t.Fatal(err)
	}
	ds := &store.DownstreamServer{
		Name: "as-cascade-ds", Transport: "stdio",
		ToolNamespace: "test", RestartPolicy: "on-failure",
	}
	if err := db.CreateDownstreamServer(ctx, ds); err != nil {
		t.Fatal(err)
	}
	as := &store.AuthScope{Name: "as-cascade-scope", Type: "env"}
	if err := db.CreateAuthScope(ctx, as); err != nil {
		t.Fatal(err)
	}

	r := &store.RouteRule{
		Priority: 10, WorkspaceID: ws.ID,
		DownstreamServerID: ds.ID, AuthScopeID: as.ID,
		Policy: "allow", ToolMatch: json.RawMessage(`["*"]`),
	}
	if err := db.CreateRouteRule(ctx, r); err != nil {
		t.Fatal(err)
	}

	approvalID := createTestApproval(t, db, ws.ID, ds.ID, as.ID, r.ID)

	if err := db.DeleteAuthScope(ctx, as.ID); err != nil {
		t.Fatalf("delete auth scope: %v", err)
	}

	// Route rule should still exist but with auth_scope_id cleared.
	got, err := db.GetRouteRule(ctx, r.ID)
	if err != nil {
		t.Fatalf("get route rule: %v", err)
	}
	if got.AuthScopeID != "" {
		t.Fatalf("auth_scope_id = %q, want empty", got.AuthScopeID)
	}

	// Approval should be cancelled.
	a, err := db.GetToolApproval(ctx, approvalID)
	if err != nil {
		t.Fatalf("get approval: %v", err)
	}
	if a.Status != "cancelled" {
		t.Fatalf("approval status = %q, want cancelled", a.Status)
	}
}

func TestDeleteOAuthProviderCascade(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	p := &store.OAuthProvider{
		Name: "oauth-cascade", ClientID: "cid",
		Scopes: json.RawMessage(`["read"]`),
	}
	if err := db.CreateOAuthProvider(ctx, p); err != nil {
		t.Fatal(err)
	}

	as := &store.AuthScope{
		Name: "oauth-scope", Type: "oauth",
		OAuthProviderID: p.ID,
	}
	if err := db.CreateAuthScope(ctx, as); err != nil {
		t.Fatal(err)
	}

	if err := db.DeleteOAuthProvider(ctx, p.ID); err != nil {
		t.Fatalf("delete oauth provider: %v", err)
	}

	// Auth scope should still exist but with oauth_provider_id cleared.
	got, err := db.GetAuthScope(ctx, as.ID)
	if err != nil {
		t.Fatalf("get auth scope: %v", err)
	}
	if got.OAuthProviderID != "" {
		t.Fatalf("oauth_provider_id = %q, want empty", got.OAuthProviderID)
	}
}

func TestDeleteRouteRuleCascade(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	ws := &store.Workspace{Name: "rr-cascade-ws", DefaultPolicy: "allow"}
	if err := db.CreateWorkspace(ctx, ws); err != nil {
		t.Fatal(err)
	}
	ds := &store.DownstreamServer{
		Name: "rr-cascade-ds", Transport: "stdio",
		ToolNamespace: "test", RestartPolicy: "on-failure",
	}
	if err := db.CreateDownstreamServer(ctx, ds); err != nil {
		t.Fatal(err)
	}

	r := &store.RouteRule{
		Priority: 10, WorkspaceID: ws.ID,
		DownstreamServerID: ds.ID, Policy: "allow",
		ToolMatch: json.RawMessage(`["*"]`),
	}
	if err := db.CreateRouteRule(ctx, r); err != nil {
		t.Fatal(err)
	}

	approvalID := createTestApproval(t, db, ws.ID, ds.ID, "", r.ID)

	if err := db.DeleteRouteRule(ctx, r.ID); err != nil {
		t.Fatalf("delete route rule: %v", err)
	}

	// Approval should be cancelled.
	a, err := db.GetToolApproval(ctx, approvalID)
	if err != nil {
		t.Fatalf("get approval: %v", err)
	}
	if a.Status != "cancelled" {
		t.Fatalf("approval status = %q, want cancelled", a.Status)
	}
}

func TestDeleteCascadeWithinTx(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	ws := &store.Workspace{Name: "tx-cascade-ws", DefaultPolicy: "allow"}
	if err := db.CreateWorkspace(ctx, ws); err != nil {
		t.Fatal(err)
	}
	ds := &store.DownstreamServer{
		Name: "tx-cascade-ds", Transport: "stdio",
		ToolNamespace: "test", RestartPolicy: "on-failure",
	}
	if err := db.CreateDownstreamServer(ctx, ds); err != nil {
		t.Fatal(err)
	}

	r := &store.RouteRule{
		Priority: 10, WorkspaceID: ws.ID,
		DownstreamServerID: ds.ID, Policy: "allow",
		ToolMatch: json.RawMessage(`["*"]`),
	}
	if err := db.CreateRouteRule(ctx, r); err != nil {
		t.Fatal(err)
	}

	approvalID := createTestApproval(t, db, ws.ID, ds.ID, "", r.ID)

	// Delete within a transaction — withTx should reuse the outer tx.
	err := db.Tx(ctx, func(tx store.Store) error {
		return tx.DeleteWorkspace(ctx, ws.ID)
	})
	if err != nil {
		t.Fatalf("tx delete: %v", err)
	}

	// Route rule gone.
	_, err = db.GetRouteRule(ctx, r.ID)
	if err != store.ErrNotFound {
		t.Fatalf("expected route rule ErrNotFound, got %v", err)
	}

	// Approval cancelled.
	a, err := db.GetToolApproval(ctx, approvalID)
	if err != nil {
		t.Fatalf("get approval: %v", err)
	}
	if a.Status != "cancelled" {
		t.Fatalf("approval status = %q, want cancelled", a.Status)
	}
}

func TestDeleteNoChildren(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	// Delete a workspace with no route rules or approvals.
	ws := &store.Workspace{Name: "no-children-ws", DefaultPolicy: "allow"}
	if err := db.CreateWorkspace(ctx, ws); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteWorkspace(ctx, ws.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	_, err := db.GetWorkspace(ctx, ws.ID)
	if err != store.ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestNotFound(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	tests := []struct {
		name string
		fn   func() error
	}{
		{"workspace", func() error { _, err := db.GetWorkspace(ctx, "nope"); return err }},
		{"auth_scope", func() error { _, err := db.GetAuthScope(ctx, "nope"); return err }},
		{"downstream", func() error { _, err := db.GetDownstreamServer(ctx, "nope"); return err }},
		{"route_rule", func() error { _, err := db.GetRouteRule(ctx, "nope"); return err }},
		{"session", func() error { _, err := db.GetSession(ctx, "nope"); return err }},
		{"delete_ws", func() error { return db.DeleteWorkspace(ctx, "nope") }},
		{"update_ws", func() error {
			return db.UpdateWorkspace(ctx, &store.Workspace{ID: "nope", Name: "x", DefaultPolicy: "allow"})
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.fn(); err != store.ErrNotFound {
				t.Fatalf("expected ErrNotFound, got %v", err)
			}
		})
	}
}

// --- WAL / durability / Close safety tests ---

func TestCloseNilSafe(t *testing.T) {
	var d *sqlite.DB
	if err := d.Close(); err != nil {
		t.Fatalf("nil receiver Close returned error: %v", err)
	}

	d2 := &sqlite.DB{}
	if err := d2.Close(); err != nil {
		t.Fatalf("nil db field Close returned error: %v", err)
	}
}

func TestCommittedWALRecovery(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wal-recover.db")
	ctx := context.Background()

	db1, err := sqlite.New(ctx, path)
	if err != nil {
		t.Fatalf("new1: %v", err)
	}

	ws := &store.Workspace{Name: "committed-ws", DefaultPolicy: "allow"}
	if err := db1.CreateWorkspace(ctx, ws); err != nil {
		t.Fatalf("create: %v", err)
	}
	if ws.ID == "" {
		t.Fatal("expected ID")
	}

	// Explicit Close exercises the checkpoint path.
	if err := db1.Close(); err != nil {
		t.Fatalf("close1: %v", err)
	}

	// Reopen on the exact same file path. Data must be visible (either
	// checkpointed into main DB or recovered from any residual WAL).
	db2, err := sqlite.New(ctx, path)
	if err != nil {
		t.Fatalf("new2: %v", err)
	}
	defer db2.Close()

	got, err := db2.GetWorkspaceByName(ctx, "committed-ws")
	if err != nil {
		t.Fatalf("get after reopen: %v", err)
	}
	if got.ID != ws.ID || got.Name != "committed-ws" {
		t.Fatalf("recovered workspace mismatch: %+v", got)
	}

	// Portable check: wal sidecar (if present) should not be huge after
	// orderly close+reopen. We do not assert exact deletion or zero size
	// because SQLite + open handles can legitimately leave a small or
	// zero-length -wal. We only log size for diagnostics.
	wal := path + "-wal"
	if fi, err := os.Stat(wal); err == nil {
		t.Logf("post-close wal size: %d bytes (best-effort; data durability verified via query)", fi.Size())
	}
}

func TestUncommittedTransactionRollback(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rollback.db")
	ctx := context.Background()

	db, err := sqlite.New(ctx, path)
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	// Force a rollback via the Tx helper (fn returns error -> rollback).
	err = db.Tx(ctx, func(tx store.Store) error {
		if cerr := tx.CreateWorkspace(ctx, &store.Workspace{
			Name: "rolled-back-ws", DefaultPolicy: "allow",
		}); cerr != nil {
			return cerr
		}
		return fmt.Errorf("intentional rollback for test")
	})
	if err == nil {
		t.Fatal("expected Tx to return the rollback error")
	}

	// Close to exercise checkpoint path even on the rollbacked DB.
	if cerr := db.Close(); cerr != nil {
		t.Fatalf("close: %v", cerr)
	}

	// Reopen and verify the workspace from the failed Tx is absent.
	db2, err := sqlite.New(ctx, path)
	if err != nil {
		t.Fatalf("new2: %v", err)
	}
	defer db2.Close()

	_, err = db2.GetWorkspaceByName(ctx, "rolled-back-ws")
	if err != store.ErrNotFound {
		t.Fatalf("expected rolled-back row to be absent (ErrNotFound), got: %v", err)
	}
}

func TestOrderlyCloseCheckpointBehavior(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "orderly.db")
	ctx := context.Background()

	db, err := sqlite.New(ctx, path)
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	// Write two rows in separate committed ops.
	for i, name := range []string{"orderly-1", "orderly-2"} {
		if err := db.CreateWorkspace(ctx, &store.Workspace{
			Name: name, DefaultPolicy: "allow",
		}); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}

	// Close should succeed and have attempted checkpoint.
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Fresh handle sees both.
	db2, err := sqlite.New(ctx, path)
	if err != nil {
		t.Fatalf("new2: %v", err)
	}
	defer db2.Close()

	list, err := db2.ListWorkspaces(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	names := map[string]bool{}
	for _, w := range list {
		names[w.Name] = true
	}
	if !names["orderly-1"] || !names["orderly-2"] {
		t.Fatalf("expected both workspaces after reopen+close, got: %+v", list)
	}
}

// TestKill9CrashRecovery adds the deterministic kill -9 (SIGKILL) crash-recovery
// harness. A child process (re-exec of test binary via os.Args[0]) opens a temp
// DB, commits several rows, prints READY on stdout, then blocks without Close.
// The parent delivers SIGKILL, reopens the DB, and asserts all committed rows
// are visible. This directly covers the "no torn writes on kill -9" gap.
// Uses only stdlib (bufio/exec/runtime/strings/syscall + existing). Bounded
// waits, short temp paths. Skips only where SIGKILL cannot be sent (windows).
// Does not assert physical -wal/-shm deletion (brittle). Fails if committed
// data from killed writer is missing after reopen.
func TestKill9CrashRecovery(t *testing.T) {
	if os.Getenv("CHILD_KILL9") == "1" {
		// Child mode: perform committed writes, signal, hang for kill.
		path := os.Getenv("DB_PATH")
		if path == "" {
			fmt.Fprintln(os.Stderr, "CHILD_KILL9: missing DB_PATH")
			os.Exit(2)
		}
		ctx := context.Background()
		db, err := sqlite.New(ctx, path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "CHILD_KILL9: new: %v\n", err)
			os.Exit(2)
		}
		// Commit rows (CreateWorkspace commits synchronously for this test).
		for i, name := range []string{"kill9-ws-1", "kill9-ws-2", "kill9-ws-3"} {
			if err := db.CreateWorkspace(ctx, &store.Workspace{
				Name: name, DefaultPolicy: "allow",
			}); err != nil {
				fmt.Fprintf(os.Stderr, "CHILD_KILL9: create %d: %v\n", i, err)
				_ = db.Close()
				os.Exit(2)
			}
		}
		// Signal readiness after commits; do not Close or write more.
		fmt.Fprintln(os.Stdout, "READY")
		_ = os.Stdout.Sync()
		// Block until parent sends SIGKILL (crash simulation).
		select {}
	}

	// Parent harness.
	if runtime.GOOS == "windows" {
		t.Skip("SIGKILL delivery not supported for this harness on windows")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "kill9.db")
	ctx := context.Background()

	// Re-execute *this* test binary restricted to this test, with child env.
	cmd := exec.Command(os.Args[0], "-test.run=^TestKill9CrashRecovery$")
	cmd.Env = append(os.Environ(),
		"CHILD_KILL9=1",
		"DB_PATH="+path,
	)
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}

	// Bounded wait for READY (child did commits + fsyncs).
	const readyTimeout = 5 * time.Second
	readyCh := make(chan struct{})
	go func() {
		scanner := bufio.NewScanner(stdoutPipe)
		for scanner.Scan() {
			if strings.TrimSpace(scanner.Text()) == "READY" {
				close(readyCh)
				return
			}
		}
	}()
	select {
	case <-readyCh:
		// Child committed; kill it.
	case <-time.After(readyTimeout):
		_ = cmd.Process.Kill()
		t.Fatalf("child did not signal READY within %v", readyTimeout)
	}

	// The kill -9.
	if err := cmd.Process.Signal(syscall.SIGKILL); err != nil {
		_ = cmd.Process.Kill()
	}
	_ = cmd.Wait() // expect "signal: killed", ignored

	// Tiny bounded settle for FS (100ms, justified for visibility after abrupt kill).
	time.Sleep(100 * time.Millisecond)

	// Reopen and verify durability: committed rows from killed child must appear.
	db2, err := sqlite.New(ctx, path)
	if err != nil {
		t.Fatalf("new after kill: %v", err)
	}
	defer db2.Close()

	list, err := db2.ListWorkspaces(ctx)
	if err != nil {
		t.Fatalf("list after kill+reopen: %v", err)
	}
	names := map[string]bool{}
	for _, w := range list {
		names[w.Name] = true
	}
	for _, want := range []string{"kill9-ws-1", "kill9-ws-2", "kill9-ws-3"} {
		if !names[want] {
			t.Fatalf("committed row %q missing after kill-9 + reopen; got: %+v", want, list)
		}
	}
	// Explicitly do not assert -wal/-shm sidecar state (see existing tests).
}
