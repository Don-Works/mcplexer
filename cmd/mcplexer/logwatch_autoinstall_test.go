// logwatch_autoinstall_test.go — unit coverage for the log-watch
// worker autoinstall, mirroring consolidator_autoinstall_test.go.
package main

import (
	"context"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
	workersadmin "github.com/don-works/mcplexer/internal/workers/admin"
)

func enableLogWatchAutoinstall(t *testing.T) {
	t.Helper()
	t.Setenv("MCPLEXER_AUTO_INSTALL_LOG_WATCH", "1")
}

// seedLogSource gives a workspace one enabled docker source so the
// autoinstall considers it watchable.
func seedLogSource(t *testing.T, db *sqlite.DB, wsID, scopeID string) {
	t.Helper()
	ctx := context.Background()
	host := &store.RemoteHost{
		WorkspaceID: wsID, Name: "box-1", SSHUser: "logwatch",
		SSHHost: "192.0.2.9", AuthScopeID: scopeID, Enabled: true,
	}
	if err := db.CreateRemoteHost(ctx, host); err != nil {
		t.Fatalf("CreateRemoteHost: %v", err)
	}
	src := &store.LogSource{
		WorkspaceID: wsID, RemoteHostID: host.ID, Name: "api",
		Selector: "api", Enabled: true,
	}
	if err := db.CreateLogSource(ctx, src); err != nil {
		t.Fatalf("CreateLogSource: %v", err)
	}
}

// TestAutoInstallLogWatch covers the full contract: installs only in
// workspaces with enabled sources, stamps the zero-spend gate + caps +
// wake trigger, and is idempotent.
func TestAutoInstallLogWatch(t *testing.T) {
	allowClaudeCLI(t)
	enableLogWatchAutoinstall(t)
	ctx := context.Background()
	db := newAutoinstallDB(t)
	scopeID := seedAPIKeyScope(t, db)
	watched := seedWorkspace(t, db, "watched")
	unwatched := seedWorkspace(t, db, "unwatched")
	seedLogSource(t, db, watched, scopeID)
	workers := newWorkerAdminWithTemplates(t, db)
	workers.SetMeshTriggerStore(db)

	autoInstallLogWatch(ctx, db, workers)

	// Watched workspace gets the worker, fully configured.
	got, err := workers.Get(ctx, workersadmin.GetInput{Name: autoLogWatchName, WorkspaceID: watched})
	if err != nil || got == nil || got.Worker == nil {
		t.Fatalf("expected worker in watched workspace: %v", err)
	}
	w := got.Worker
	if w.ExecMode != "autonomous" || w.ScheduleSpec != autoLogWatchSchedule {
		t.Fatalf("worker config: exec=%s schedule=%s", w.ExecMode, w.ScheduleSpec)
	}
	if !strings.Contains(w.PreExecuteScript, `abort("quiet")`) {
		t.Fatalf("zero-spend gate missing: %q", w.PreExecuteScript)
	}
	if w.MaxToolCalls != 15 || w.MaxWallClockSeconds != 120 || w.MaxConsecutiveFailures != 5 {
		t.Fatalf("caps not stamped: %+v", w)
	}
	if strings.Contains(w.ToolAllowlistJSON, "telegram") || strings.Contains(w.ToolAllowlistJSON, "openwa") {
		t.Fatalf("worker must hold NO channel tools: %s", w.ToolAllowlistJSON)
	}
	triggers, err := db.ListWorkerMeshTriggers(ctx, w.ID)
	if err != nil || len(triggers) != 1 || triggers[0].TagMatch != "logwatch" {
		t.Fatalf("wake trigger: %v %+v", err, triggers)
	}

	// No sources → no worker.
	if got, err := workers.Get(ctx, workersadmin.GetInput{Name: autoLogWatchName, WorkspaceID: unwatched}); err == nil && got != nil && got.Worker != nil {
		t.Fatal("workspace without sources must not get a worker")
	}

	// Idempotent: second run leaves the single worker + trigger alone.
	autoInstallLogWatch(ctx, db, workers)
	triggers, _ = db.ListWorkerMeshTriggers(ctx, w.ID)
	if len(triggers) != 1 {
		t.Fatalf("second run must not duplicate triggers: %d", len(triggers))
	}
}
