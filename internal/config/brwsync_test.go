package config

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

// newBrwTestStore opens a hermetic temp sqlite store with the "global"
// workspace seeded so route creation can resolve its workspace FK.
func newBrwTestStore(t *testing.T) *sqlite.DB {
	t.Helper()
	ctx := context.Background()
	db, err := sqlite.New(ctx, t.TempDir()+"/test.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.CreateWorkspace(ctx, &store.Workspace{
		ID: "global", Name: "Global", RootPath: "/", DefaultPolicy: "deny",
		Source: "default", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("seed global workspace: %v", err)
	}
	return db
}

func sampleBrwDaemons() []BrwDaemon {
	return []BrwDaemon{
		{
			Name: "work-profile", Workspace: "brw", Profile: "work-profile",
			HTTPAddr: "http://127.0.0.1:17310", WSAddr: "127.0.0.1:17311",
			ExtensionID: "amocjcgddnoakjijfggdpnefdnboilpe", Reachable: true,
			Identity: BrwIdentity{Workspace: "brw", Profile: "work-profile", Mode: "bridge"},
		},
		{
			Name: "chromium", Workspace: "brw-chromium", Profile: "default",
			HTTPAddr: "http://127.0.0.1:17410", WSAddr: "127.0.0.1:17411",
			Reachable: true,
			Identity:  BrwIdentity{Workspace: "brw-chromium", Profile: "default", Mode: "bridge"},
		},
	}
}

func countActions(plan SyncPlan, action string) int {
	n := 0
	for _, a := range plan.Actions {
		if a.Action == action {
			n++
		}
	}
	return n
}

// countBrwServers / countBrwRoutes count only source="brw" rows, ignoring the
// baseline rows (e.g. the migration-seeded global-deny route) a fresh store
// ships with.
func countBrwServers(t *testing.T, db *sqlite.DB) int {
	t.Helper()
	servers, err := db.ListDownstreamServers(context.Background())
	if err != nil {
		t.Fatalf("list servers: %v", err)
	}
	n := 0
	for _, s := range servers {
		if s.Source == "brw" {
			n++
		}
	}
	return n
}

func countBrwRoutes(t *testing.T, db *sqlite.DB) int {
	t.Helper()
	routes, err := db.ListRouteRules(context.Background(), "")
	if err != nil {
		t.Fatalf("list routes: %v", err)
	}
	n := 0
	for _, r := range routes {
		if r.Source == "brw" {
			n++
		}
	}
	return n
}

// (a) A fresh sync creates exactly one stdio server + one route per daemon.
func TestSyncBrwProfiles_FreshCreatesServerAndRoute(t *testing.T) {
	ctx := context.Background()
	db := newBrwTestStore(t)
	svc := NewService(db)

	plan, err := SyncBrwProfiles(ctx, svc, db, sampleBrwDaemons(), SyncOptions{
		Workspaces: []string{"global"},
	})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if got := countActions(plan, ActionCreated); got != 4 {
		t.Fatalf("created actions = %d, want 4 (2 servers + 2 routes)", got)
	}

	if got := countBrwServers(t, db); got != 2 {
		t.Fatalf("brw server count = %d, want 2", got)
	}

	srv, err := db.GetDownstreamServer(ctx, "brw-brw")
	if err != nil {
		t.Fatalf("get brw-brw: %v", err)
	}
	if srv.Transport != "stdio" {
		t.Errorf("transport = %q, want stdio", srv.Transport)
	}
	if srv.Command != DefaultBrwdPath {
		t.Errorf("command = %q, want %q", srv.Command, DefaultBrwdPath)
	}
	if srv.ToolNamespace != "brw" {
		t.Errorf("namespace = %q, want brw", srv.ToolNamespace)
	}
	if srv.MaxInstances != 1 || srv.IdleTimeoutSec != 300 || srv.Disabled {
		t.Errorf("server knobs wrong: max=%d idle=%d disabled=%v", srv.MaxInstances, srv.IdleTimeoutSec, srv.Disabled)
	}
	if srv.Source != "brw" {
		t.Errorf("source = %q, want brw", srv.Source)
	}
	var args []string
	if err := json.Unmarshal(srv.Args, &args); err != nil {
		t.Fatalf("unmarshal args: %v", err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"--workspace brw",
		"--upstream-http http://127.0.0.1:17310",
		"--profile-policy " + DefaultBrwPolicyPath,
		"--mcp",
		"--http off",
		"--mcp-tools all",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("args %q missing %q", joined, want)
		}
	}

	// The hyphenated workspace must sanitize to brw_chromium.
	if _, err := db.GetDownstreamServer(ctx, "brw-brw_chromium"); err != nil {
		t.Fatalf("get brw-brw_chromium: %v", err)
	}

	rt, err := db.GetRouteRule(ctx, "brw-route-global-brw")
	if err != nil {
		t.Fatalf("get route: %v", err)
	}
	if rt.DownstreamServerID != "brw-brw" {
		t.Errorf("route server id = %q, want brw-brw", rt.DownstreamServerID)
	}
	if rt.WorkspaceID != "global" || rt.Policy != "allow" || rt.Priority != 50 {
		t.Errorf("route wiring wrong: ws=%q policy=%q prio=%d", rt.WorkspaceID, rt.Policy, rt.Priority)
	}
	if string(rt.ToolMatch) != `["brw__*"]` {
		t.Errorf("route tool_match = %s, want [\"brw__*\"]", rt.ToolMatch)
	}
	if got := countBrwRoutes(t, db); got != 2 {
		t.Fatalf("brw route count = %d, want 2", got)
	}
}

// (b) Re-running is idempotent: no duplicates, every action is unchanged.
func TestSyncBrwProfiles_RerunIdempotent(t *testing.T) {
	ctx := context.Background()
	db := newBrwTestStore(t)
	svc := NewService(db)
	daemons := sampleBrwDaemons()
	opts := SyncOptions{Workspaces: []string{"global"}}

	if _, err := SyncBrwProfiles(ctx, svc, db, daemons, opts); err != nil {
		t.Fatalf("first sync: %v", err)
	}
	plan2, err := SyncBrwProfiles(ctx, svc, db, daemons, opts)
	if err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if c := countActions(plan2, ActionCreated); c != 0 {
		t.Errorf("re-run created %d, want 0", c)
	}
	if c := countActions(plan2, ActionUpdated); c != 0 {
		t.Errorf("re-run updated %d, want 0", c)
	}
	if c := countActions(plan2, ActionUnchanged); c != 4 {
		t.Errorf("re-run unchanged %d, want 4", c)
	}

	if got := countBrwServers(t, db); got != 2 {
		t.Errorf("brw server count after re-run = %d, want 2", got)
	}
	if got := countBrwRoutes(t, db); got != 2 {
		t.Errorf("brw route count after re-run = %d, want 2", got)
	}
}

// (c) A pre-existing source!="brw" server holding the namespace is adopted,
// never overwritten.
func TestSyncBrwProfiles_DoesNotOverwriteNonBrwServer(t *testing.T) {
	ctx := context.Background()
	db := newBrwTestStore(t)
	svc := NewService(db)

	manual := &store.DownstreamServer{
		ID: "manual-brw", Name: "manual-brw", Transport: "stdio",
		Command: "/custom/bin/other", Args: json.RawMessage(`["--flag"]`),
		ToolNamespace: "brw", Source: "api",
	}
	if err := svc.CreateDownstreamServer(ctx, manual); err != nil {
		t.Fatalf("seed manual server: %v", err)
	}

	plan, err := SyncBrwProfiles(ctx, svc, db, []BrwDaemon{{
		Name: "work-profile", Workspace: "brw", HTTPAddr: "http://127.0.0.1:17310",
		Identity: BrwIdentity{Workspace: "brw"},
	}}, SyncOptions{Workspaces: []string{"global"}})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if c := countActions(plan, ActionAdopted); c != 1 {
		t.Errorf("adopted actions = %d, want 1", c)
	}

	// The manual server is untouched.
	got, err := db.GetDownstreamServer(ctx, "manual-brw")
	if err != nil {
		t.Fatalf("get manual: %v", err)
	}
	if got.Command != "/custom/bin/other" || got.Source != "api" || string(got.Args) != `["--flag"]` {
		t.Errorf("manual server mutated: command=%q source=%q args=%s", got.Command, got.Source, got.Args)
	}
	// No deterministic brw-brw server was created.
	if _, err := db.GetDownstreamServer(ctx, "brw-brw"); err == nil {
		t.Errorf("brw-brw should not have been created")
	}
	// The route targets the adopted server.
	rt, err := db.GetRouteRule(ctx, "brw-route-global-brw")
	if err != nil {
		t.Fatalf("get route: %v", err)
	}
	if rt.DownstreamServerID != "manual-brw" {
		t.Errorf("route server id = %q, want manual-brw", rt.DownstreamServerID)
	}
}

// (d) DryRun computes a plan but writes nothing.
func TestSyncBrwProfiles_DryRunWritesNothing(t *testing.T) {
	ctx := context.Background()
	db := newBrwTestStore(t)
	svc := NewService(db)

	plan, err := SyncBrwProfiles(ctx, svc, db, sampleBrwDaemons(), SyncOptions{
		DryRun: true, Workspaces: []string{"global"},
	})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if !plan.DryRun {
		t.Errorf("plan.DryRun = false, want true")
	}
	if c := countActions(plan, ActionCreated); c != 4 {
		t.Errorf("planned created = %d, want 4", c)
	}

	if got := countBrwServers(t, db); got != 0 {
		t.Errorf("dry-run wrote %d brw servers, want 0", got)
	}
	if got := countBrwRoutes(t, db); got != 0 {
		t.Errorf("dry-run wrote %d brw routes, want 0", got)
	}
}

// (e) Prune removes a source="brw" server absent from the new input while
// leaving a non-brw server alone.
func TestSyncBrwProfiles_PruneRemovesStaleBrwServer(t *testing.T) {
	ctx := context.Background()
	db := newBrwTestStore(t)
	svc := NewService(db)

	// Seed via a sync so brw-brw + its route exist.
	if _, err := SyncBrwProfiles(ctx, svc, db, []BrwDaemon{{
		Name: "work-profile", Workspace: "brw", HTTPAddr: "http://127.0.0.1:17310",
		Identity: BrwIdentity{Workspace: "brw"},
	}}, SyncOptions{Workspaces: []string{"global"}}); err != nil {
		t.Fatalf("seed sync: %v", err)
	}
	// A non-brw server that must survive prune.
	if err := svc.CreateDownstreamServer(ctx, &store.DownstreamServer{
		ID: "keep-api", Name: "keep-api", Transport: "stdio",
		Args: json.RawMessage(`[]`), ToolNamespace: "keepns", Source: "api",
	}); err != nil {
		t.Fatalf("seed keep-api: %v", err)
	}

	// Re-sync with EMPTY input + prune.
	plan, err := SyncBrwProfiles(ctx, svc, db, nil, SyncOptions{
		Workspaces: []string{"global"}, Prune: true,
	})
	if err != nil {
		t.Fatalf("prune sync: %v", err)
	}
	if c := countActions(plan, ActionPruned); c != 2 {
		t.Errorf("pruned actions = %d, want 2 (server + route)", c)
	}

	if _, err := db.GetDownstreamServer(ctx, "brw-brw"); err == nil {
		t.Errorf("brw-brw should have been pruned")
	}
	if _, err := db.GetRouteRule(ctx, "brw-route-global-brw"); err == nil {
		t.Errorf("brw route should have been pruned")
	}
	if _, err := db.GetDownstreamServer(ctx, "keep-api"); err != nil {
		t.Errorf("non-brw server was pruned: %v", err)
	}
}
