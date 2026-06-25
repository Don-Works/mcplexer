// consolidator_autoinstall_test.go — unit coverage for the startup wiring
// that auto-installs memory-consolidator workers.
package main

import (
	"context"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
	workersadmin "github.com/don-works/mcplexer/internal/workers/admin"
	"github.com/don-works/mcplexer/internal/workertemplates"
)

// allowClaudeCLI sets MCPLEXER_ALLOW_CLAUDE_CLI=1 for the duration of a
// test (and t.Cleanup resets it). The memory-consolidator seed template uses
// model_provider_hint="claude_cli" which is blocked by the H4 validator
// unless the env is set — this helper gates the install-path tests that
// require the full round-trip.
func allowClaudeCLI(t *testing.T) {
	t.Helper()
	t.Setenv("MCPLEXER_ALLOW_CLAUDE_CLI", "1")
}

// newAutoinstallDB spins up an in-memory SQLite DB with optional workspaces
// and auth scopes pre-seeded. Returns (db).
func newAutoinstallDB(t *testing.T) *sqlite.DB {
	t.Helper()
	db, err := sqlite.New(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// seedAPIKeyScope creates an api_key auth scope in db and returns its ID.
func seedAPIKeyScope(t *testing.T, db *sqlite.DB) string {
	t.Helper()
	scope := &store.AuthScope{
		Name: "anthropic-key",
		Type: "api_key",
	}
	if err := db.CreateAuthScope(context.Background(), scope); err != nil {
		t.Fatalf("CreateAuthScope: %v", err)
	}
	return scope.ID
}

// seedWorkspace creates a workspace in db and returns its ID.
func seedWorkspace(t *testing.T, db *sqlite.DB, name string) string {
	t.Helper()
	ws := &store.Workspace{Name: name, DefaultPolicy: "allow"}
	if err := db.CreateWorkspace(context.Background(), ws); err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	return ws.ID
}

// newWorkerAdminWithTemplates builds a workersadmin.Service wired with the
// real workertemplates registry and the seeded "memory-consolidator" template.
func newWorkerAdminWithTemplates(t *testing.T, db *sqlite.DB) *workersadmin.Service {
	t.Helper()
	svc := workersadmin.New(db, workersadmin.Options{Workspaces: db})
	reg := workertemplates.New(db)
	svc.SetTemplatePublisher(reg)
	if err := workertemplates.Seed(context.Background(), reg); err != nil {
		t.Fatalf("workertemplates.Seed: %v", err)
	}
	return svc
}

// TestPickConsolidatorScope_FallsBackToAnyScope: with no api_key scope but
// SOME other scope present, the picker now falls back to that scope instead of
// refusing to install. The consolidator's default claude_cli provider ignores
// the bound scope at runtime, so any scope satisfies the NOT NULL placeholder —
// this is the fix for "consolidation never installs for subscription users".
func TestPickConsolidatorScope_FallsBackToAnyScope(t *testing.T) {
	ctx := context.Background()
	db := newAutoinstallDB(t)

	envScope := &store.AuthScope{Name: "some-env", Type: "env"}
	if err := db.CreateAuthScope(ctx, envScope); err != nil {
		t.Fatalf("CreateAuthScope: %v", err)
	}

	id, ok := pickConsolidatorScope(ctx, db)
	if !ok {
		t.Fatal("expected ok=true falling back to a non-api_key scope")
	}
	if id != envScope.ID {
		t.Fatalf("expected fallback to the env scope %q, got %q", envScope.ID, id)
	}
}

// TestPickConsolidatorScope_NoScopesAtAll returns ok=false ONLY when there are
// zero auth scopes (nothing to satisfy the placeholder); the next boot retries.
func TestPickConsolidatorScope_NoScopesAtAll(t *testing.T) {
	ctx := context.Background()
	db := newAutoinstallDB(t)
	if _, ok := pickConsolidatorScope(ctx, db); ok {
		t.Error("expected ok=false when no auth scopes exist at all")
	}
}

// TestPickConsolidatorScope_ReturnsApiKey returns the api_key scope id when
// one is present.
func TestPickConsolidatorScope_ReturnsApiKey(t *testing.T) {
	ctx := context.Background()
	db := newAutoinstallDB(t)
	scopeID := seedAPIKeyScope(t, db)

	got, ok := pickConsolidatorScope(ctx, db)
	if !ok {
		t.Fatal("expected ok=true when api_key scope exists, got false")
	}
	if got != scopeID {
		t.Errorf("pickConsolidatorScope returned %q, want %q", got, scopeID)
	}
}

// TestAutoInstallConsolidator_NoOpWhenNoApiKey confirms the function is a
// no-op when no api_key auth scope is configured, leaving zero workers behind.
func TestAutoInstallConsolidator_NoOpWhenNoApiKey(t *testing.T) {
	ctx := context.Background()
	db := newAutoinstallDB(t)
	wsID := seedWorkspace(t, db, "my-workspace")
	workers := newWorkerAdminWithTemplates(t, db)

	autoInstallConsolidator(ctx, db, workers)

	existing, err := workers.List(ctx, workersadmin.ListInput{WorkspaceID: wsID})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(existing) != 0 {
		t.Errorf("expected 0 workers when no api_key scope, got %d", len(existing))
	}
}

// TestAutoInstallConsolidator_InstallsWhenApiKeyPresent confirms that a
// workspace with an api_key scope gets a memory-consolidator worker.
func TestAutoInstallConsolidator_InstallsWhenApiKeyPresent(t *testing.T) {
	allowClaudeCLI(t)
	ctx := context.Background()
	db := newAutoinstallDB(t)
	wsID := seedWorkspace(t, db, "workspace-a")
	seedAPIKeyScope(t, db)
	workers := newWorkerAdminWithTemplates(t, db)

	autoInstallConsolidator(ctx, db, workers)

	got, err := workers.Get(ctx, workersadmin.GetInput{
		Name:        autoConsolidatorName,
		WorkspaceID: wsID,
	})
	if err != nil {
		t.Fatalf("Get after autoinstall: %v", err)
	}
	if got.Worker == nil {
		t.Fatal("expected worker to be installed, got nil")
	}
	if got.Worker.Name != autoConsolidatorName {
		t.Errorf("worker name = %q, want %q", got.Worker.Name, autoConsolidatorName)
	}
	if !got.Worker.Enabled {
		t.Error("expected worker to be enabled after autoinstall")
	}
}

// TestAutoInstallConsolidator_Idempotent confirms that calling
// autoInstallConsolidator twice does NOT install a second worker — the
// second call silently skips the workspace.
func TestAutoInstallConsolidator_Idempotent(t *testing.T) {
	allowClaudeCLI(t)
	ctx := context.Background()
	db := newAutoinstallDB(t)
	wsID := seedWorkspace(t, db, "workspace-idempotent")
	seedAPIKeyScope(t, db)
	workers := newWorkerAdminWithTemplates(t, db)

	autoInstallConsolidator(ctx, db, workers)
	autoInstallConsolidator(ctx, db, workers) // second call — must be a no-op

	all, err := workers.List(ctx, workersadmin.ListInput{WorkspaceID: wsID})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	count := 0
	for _, w := range all {
		if w.Name == autoConsolidatorName {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected exactly 1 consolidator after 2 calls, got %d", count)
	}
}

// TestAutoInstallConsolidator_MultiWorkspace confirms that every workspace
// gets its own consolidator worker when an api_key scope is present.
func TestAutoInstallConsolidator_MultiWorkspace(t *testing.T) {
	allowClaudeCLI(t)
	ctx := context.Background()
	db := newAutoinstallDB(t)
	ws1 := seedWorkspace(t, db, "ws-multi-1")
	ws2 := seedWorkspace(t, db, "ws-multi-2")
	seedAPIKeyScope(t, db)
	workers := newWorkerAdminWithTemplates(t, db)

	autoInstallConsolidator(ctx, db, workers)

	for _, wsID := range []string{ws1, ws2} {
		got, err := workers.Get(ctx, workersadmin.GetInput{
			Name: autoConsolidatorName, WorkspaceID: wsID,
		})
		if err != nil {
			t.Errorf("workspace %s: Get: %v", wsID, err)
			continue
		}
		if got.Worker == nil {
			t.Errorf("workspace %s: expected worker installed, got nil", wsID)
		}
	}
}
