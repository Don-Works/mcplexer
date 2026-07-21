// workers_handler_test.go — HTTP smoke tests for the M0.6 workers
// surface. Spins up a real sqlite-backed admin.Service so the tests
// exercise the same code path the PWA hits in production.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
	workersadmin "github.com/don-works/mcplexer/internal/workers/admin"
)

func newWorkersTestServer(t *testing.T) (*httptest.Server, *sqlite.DB, string, string) {
	t.Helper()
	ctx := context.Background()
	db, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "workers.db"))
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ws := &store.Workspace{Name: "workers", DefaultPolicy: "allow"}
	if err := db.CreateWorkspace(ctx, ws); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	scope := &store.AuthScope{Name: "anthropic-key", Type: "env"}
	if err := db.CreateAuthScope(ctx, scope); err != nil {
		t.Fatalf("create auth scope: %v", err)
	}

	svc := workersadmin.New(db, workersadmin.Options{Workspaces: db})
	r := NewRouter(RouterDeps{
		APIToken:    "",
		Store:       db,
		WorkerAdmin: svc,
	})
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)
	return srv, db, ws.ID, scope.ID
}

func TestWorkersHandlerCRUDLifecycle(t *testing.T) {
	srv, _, wsID, scopeID := newWorkersTestServer(t)

	// Empty list returns []
	rows := mustGetWorkers(t, srv.URL+"/api/v1/workers")
	if len(rows) != 0 {
		t.Fatalf("expected 0 workers, got %d", len(rows))
	}

	// Create
	create := map[string]any{
		"name":            "digest",
		"model_provider":  "anthropic",
		"model_id":        "claude-opus-4-7",
		"secret_scope_id": scopeID,
		"prompt_template": "Summarise {x}.",
		"schedule_spec":   "0 9 * * *",
		"workspace_id":    wsID,
	}
	created := postJSON(t, srv.URL+"/api/v1/workers", create, http.StatusCreated)
	id, _ := created["id"].(string)
	if id == "" {
		t.Fatalf("created worker has no id: %+v", created)
	}

	// List now has 1 row
	rows = mustGetWorkers(t, srv.URL+"/api/v1/workers")
	if len(rows) != 1 {
		t.Fatalf("expected 1 worker, got %d", len(rows))
	}

	// Pause
	postJSON(t, srv.URL+"/api/v1/workers/"+id+"/pause", nil, http.StatusOK)
	one := getJSON(t, srv.URL+"/api/v1/workers/"+id, http.StatusOK)
	w, _ := one["worker"].(map[string]any)
	if w == nil {
		t.Fatalf("get returned no worker field: %+v", one)
	}
	if enabled, _ := w["enabled"].(bool); enabled {
		t.Fatalf("expected paused worker, got enabled=true")
	}

	// Resume
	postJSON(t, srv.URL+"/api/v1/workers/"+id+"/resume", nil, http.StatusOK)

	// Run now — handler returns 202 immediately and the run row is
	// created on a background goroutine (the dispatch was detached
	// from the request context so HTTP-client timeouts can't SIGKILL
	// long-running model subprocesses). Poll for the row instead of
	// asserting after the next tick.
	postJSON(t, srv.URL+"/api/v1/workers/"+id+"/run-now", nil, http.StatusAccepted)
	var runs []map[string]any
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		runs = mustGetSlice(t, srv.URL+"/api/v1/workers/"+id+"/runs")
		if len(runs) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(runs) == 0 {
		t.Fatalf("expected at least one run within 3s of run-now")
	}

	// Update name via PATCH
	newName := "digest-renamed"
	patch := map[string]any{"name": newName}
	postJSONMethod(t, http.MethodPatch, srv.URL+"/api/v1/workers/"+id, patch, http.StatusOK)
	one = getJSON(t, srv.URL+"/api/v1/workers/"+id, http.StatusOK)
	w, _ = one["worker"].(map[string]any)
	if got, _ := w["name"].(string); got != newName {
		t.Fatalf("expected name=%q, got %q", newName, got)
	}

	// Delete
	deleteReq(t, srv.URL+"/api/v1/workers/"+id, http.StatusNoContent)
	rows = mustGetWorkers(t, srv.URL+"/api/v1/workers")
	if len(rows) != 0 {
		t.Fatalf("expected 0 workers after delete, got %d", len(rows))
	}
}

func TestWorkersHandlerGet404(t *testing.T) {
	srv, _, _, _ := newWorkersTestServer(t)
	resp, err := http.Get(srv.URL + "/api/v1/workers/wkr-does-not-exist")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", resp.StatusCode)
	}
}

func TestWorkersHandlerCancelRun(t *testing.T) {
	srv, db, wsID, scopeID := newWorkersTestServer(t)
	ctx := context.Background()
	w := &store.Worker{
		Name:               "cancel-http",
		ModelProvider:      "anthropic",
		ModelID:            "claude-opus-4-7",
		SecretScopeID:      scopeID,
		PromptTemplate:     "hi",
		ScheduleSpec:       "manual",
		ToolAllowlistJSON:  "[]",
		OutputChannelsJSON: "[]",
		ExecMode:           "propose",
		ConcurrencyPolicy:  "skip",
		Enabled:            true,
		WorkspaceID:        wsID,
	}
	if err := db.CreateWorker(ctx, w); err != nil {
		t.Fatalf("create worker: %v", err)
	}
	run := &store.WorkerRun{
		WorkerID:  w.ID,
		Status:    "running",
		StartedAt: time.Now().UTC().Add(-time.Minute),
	}
	if err := db.CreateWorkerRun(ctx, run); err != nil {
		t.Fatalf("create run: %v", err)
	}
	// No runner is wired in this harness, so the run is an orphan/stub
	// 'running' row → the direct-flip path writes the distinct
	// 'cancelled' status (not 'failure') and returns 200.
	out := postJSON(t, srv.URL+"/api/v1/worker-runs/"+run.ID+"/cancel",
		map[string]any{"reason": "stale row"}, http.StatusOK)
	if out["status"] != "cancelled" {
		t.Fatalf("cancel response = %+v", out)
	}
	got, err := db.GetWorkerRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if got.Status != "cancelled" {
		t.Fatalf("status = %q, want cancelled", got.Status)
	}
	// Second cancel on the now-terminal row → 409 (already finished).
	postJSON(t, srv.URL+"/api/v1/worker-runs/"+run.ID+"/cancel", nil, http.StatusConflict)

	req, err := http.NewRequest(
		http.MethodPost,
		srv.URL+"/api/v1/worker-runs/"+run.ID+"/cancel",
		strings.NewReader("{"),
	)
	if err != nil {
		t.Fatalf("new malformed cancel request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("malformed cancel request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("malformed cancel status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

// Helpers ---------------------------------------------------------------

func mustGetWorkers(t *testing.T, url string) []map[string]any {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("get %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get %s: status=%d", url, resp.StatusCode)
	}
	var rows []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return rows
}

func mustGetSlice(t *testing.T, url string) []map[string]any {
	return mustGetWorkers(t, url)
}

func getJSON(t *testing.T, url string, wantStatus int) map[string]any {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("get %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != wantStatus {
		t.Fatalf("get %s: status=%d want=%d", url, resp.StatusCode, wantStatus)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

func postJSON(t *testing.T, url string, body any, wantStatus int) map[string]any {
	return postJSONMethod(t, http.MethodPost, url, body, wantStatus)
}

func postJSONMethod(t *testing.T, method, url string, body any, wantStatus int) map[string]any {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode body: %v", err)
		}
	}
	req, err := http.NewRequest(method, url, &buf)
	if err != nil {
		t.Fatalf("new req: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != wantStatus {
		t.Fatalf("%s %s: status=%d want=%d", method, url, resp.StatusCode, wantStatus)
	}
	if resp.StatusCode == http.StatusNoContent {
		return nil
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

func deleteReq(t *testing.T, url string, wantStatus int) {
	t.Helper()
	req, err := http.NewRequest(http.MethodDelete, url, nil)
	if err != nil {
		t.Fatalf("new req: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("delete %s: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != wantStatus {
		t.Fatalf("delete %s: status=%d want=%d", url, resp.StatusCode, wantStatus)
	}
}
