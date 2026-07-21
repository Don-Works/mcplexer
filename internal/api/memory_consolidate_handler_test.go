// memory_consolidate_handler_test.go — HTTP coverage for the consolidator
// surface (GET status / POST enable / POST run / POST disable). Spins up
// a real sqlite-backed admin.Service + memory.Service + the embedded
// worker-template registry so the test exercises the same publish-then-
// install path the dashboard hits.
package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/don-works/mcplexer/internal/memory"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
	workersadmin "github.com/don-works/mcplexer/internal/workers/admin"
	"github.com/don-works/mcplexer/internal/workertemplates"
)

func newConsolidatorTestServer(t *testing.T) (url string, wsID, scopeID string) {
	t.Helper()
	ctx := context.Background()
	db, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "consolidator.db"))
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ws := &store.Workspace{Name: "consolidator-test", DefaultPolicy: "allow"}
	if err := db.CreateWorkspace(ctx, ws); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	scope := &store.AuthScope{Name: "anthropic-key", Type: "api_key"}
	if err := db.CreateAuthScope(ctx, scope); err != nil {
		t.Fatalf("create auth scope: %v", err)
	}
	// Permissive schedule validator so the cron spec doesn't need the
	// scheduler package wired in for tests.
	svc := workersadmin.New(db, workersadmin.Options{
		Workspaces:        db,
		ScheduleValidator: func(string) error { return nil },
	})
	reg := workertemplates.New(db)
	if err := workertemplates.Seed(ctx, reg); err != nil {
		t.Fatalf("seed worker templates: %v", err)
	}
	svc.SetTemplatePublisher(reg)

	memSvc := memory.NewService(db, nil, nil)
	r := NewRouter(RouterDeps{
		Store:                  db,
		MemorySvc:              memSvc,
		WorkerAdmin:            svc,
		WorkerTemplateRegistry: reg,
	})
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv.URL, ws.ID, scope.ID
}

// TestConsolidatorRoundTrip exercises status → enable → run-now → disable.
// The run-now invocation uses the stub runner path (no real runner wired)
// which still returns a run id — sufficient to confirm the endpoint
// dispatches correctly.
//
// MCPLEXER_ALLOW_CLAUDE_CLI=1 is required because the seeded
// memory-consolidator template now defaults to model_provider=claude_cli
// (so the recurring worker bills against the host's Claude subscription
// instead of per-token API spend). The admin validator H4 gate rejects
// claude_cli without the env opt-in, which would 400 the /enable call.
func TestConsolidatorRoundTrip(t *testing.T) {
	t.Setenv("MCPLEXER_ALLOW_CLAUDE_CLI", "1")
	url, wsID, _ := newConsolidatorTestServer(t)

	// 1. Status BEFORE enable → installed=false, enabled=false.
	pre := getJSON(t, url+"/api/v1/memory/consolidate/status?workspace_id="+wsID, http.StatusOK)
	if pre["installed"] != false {
		t.Fatalf("pre-enable: expected installed=false, got %v", pre["installed"])
	}
	if pre["enabled"] != false {
		t.Fatalf("pre-enable: expected enabled=false, got %v", pre["enabled"])
	}

	// 2. Enable (picks the auth scope we seeded above).
	postJSON(t, url+"/api/v1/memory/consolidate/enable",
		map[string]any{"workspace_id": wsID},
		http.StatusOK)

	// 3. Status AFTER enable → installed=true, enabled=true, with a
	//    worker_id + schedule.
	post := getJSON(t, url+"/api/v1/memory/consolidate/status?workspace_id="+wsID, http.StatusOK)
	if post["installed"] != true {
		t.Errorf("post-enable: expected installed=true, got %v", post["installed"])
	}
	if post["enabled"] != true {
		t.Errorf("post-enable: expected enabled=true, got %v", post["enabled"])
	}
	if post["worker_id"] == nil || post["worker_id"] == "" {
		t.Errorf("post-enable: missing worker_id: %+v", post)
	}
	if post["schedule_spec"] != defaultConsolidatorSchedule {
		t.Errorf("post-enable: expected schedule=%q, got %v",
			defaultConsolidatorSchedule, post["schedule_spec"])
	}

	// 4. Run now → returns a run_id (stub path is fine).
	runResp := postJSON(t, url+"/api/v1/memory/consolidate/run",
		map[string]any{"workspace_id": wsID}, http.StatusOK)
	if runResp["run_id"] == nil || runResp["run_id"] == "" {
		t.Errorf("run-now: missing run_id: %+v", runResp)
	}

	// 5. Disable → enabled flips false.
	postJSON(t, url+"/api/v1/memory/consolidate/disable",
		map[string]any{"workspace_id": wsID}, http.StatusOK)
	paused := getJSON(t, url+"/api/v1/memory/consolidate/status?workspace_id="+wsID, http.StatusOK)
	if paused["enabled"] != false {
		t.Errorf("post-disable: expected enabled=false, got %v", paused["enabled"])
	}
	if paused["installed"] != true {
		t.Errorf("post-disable: row should still exist, got installed=%v", paused["installed"])
	}

	// 6. Re-enable should idempotently flip the existing worker.
	postJSON(t, url+"/api/v1/memory/consolidate/enable",
		map[string]any{"workspace_id": wsID}, http.StatusOK)
	resumed := getJSON(t, url+"/api/v1/memory/consolidate/status?workspace_id="+wsID, http.StatusOK)
	if resumed["enabled"] != true {
		t.Errorf("post-resume: expected enabled=true, got %v", resumed["enabled"])
	}
}

func TestConsolidatorMissingWorkspaceID(t *testing.T) {
	url, _, _ := newConsolidatorTestServer(t)

	resp, err := http.Get(url + "/api/v1/memory/consolidate/status")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

func TestConsolidatorRunBeforeEnableReturns404(t *testing.T) {
	url, wsID, _ := newConsolidatorTestServer(t)

	postJSON(t, url+"/api/v1/memory/consolidate/run",
		map[string]any{"workspace_id": wsID}, http.StatusNotFound)
}
