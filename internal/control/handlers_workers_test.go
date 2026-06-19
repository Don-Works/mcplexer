package control

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
	"github.com/don-works/mcplexer/internal/workers/admin"
)

// waitForWorkerRun polls until the detached run_worker_now goroutine has
// materialised a run row for the worker, returning its id. run_worker_now
// now dispatches asynchronously, so the row appears shortly after the call
// acks; waiting also ensures the detached goroutine has finished its DB
// writes before the test's in-memory DB tears down (otherwise the goroutine
// races teardown with a "database is closed" write).
func waitForWorkerRun(t *testing.T, db *sqlite.DB, workerID string) string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		runs, err := db.ListWorkerRuns(context.Background(), workerID, 10)
		if err == nil && len(runs) > 0 {
			return runs[0].ID
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no worker run materialised for %s within deadline", workerID)
	return ""
}

// newWorkerBackend wires an InternalBackend with the worker admin
// service plugged in but no runner (so RunNow exercises the stub path).
// Returns (backend, db, workspaceID, scopeID) for tests to seed inputs.
func newWorkerBackend(t *testing.T) (*InternalBackend, *sqlite.DB, string, string) {
	t.Helper()
	db := newTestDB(t)
	ws := seedWorkspace(t, db)
	scope := &store.AuthScope{Name: "anthropic-key", Type: "env"}
	if err := db.CreateAuthScope(context.Background(), scope); err != nil {
		t.Fatalf("seed auth scope: %v", err)
	}
	b := NewInternalBackend(db, nil)
	b.SetWorkerAdmin(admin.New(db, admin.Options{Workspaces: db}))
	return b, db, ws.ID, scope.ID
}

// callWorkerTool drives one InternalBackend.Call through the dispatch
// path and returns the parsed result.
func callWorkerTool(t *testing.T, b *InternalBackend, name string, args any) (string, bool) {
	t.Helper()
	var raw json.RawMessage
	if args != nil {
		var err error
		raw, err = json.Marshal(args)
		if err != nil {
			t.Fatalf("marshal args: %v", err)
		}
	}
	out, err := b.Call(context.Background(), name, raw)
	if err != nil {
		t.Fatalf("backend.Call(%q): %v", name, err)
	}
	return parseToolResult(t, out)
}

func TestWorkerToolsListedInToolsList(t *testing.T) {
	// The InternalBackend's ListTools is what the downstream manager
	// surfaces to the gateway. The 10 worker tools must all appear.
	b := NewInternalBackend(newTestDB(t), nil)
	raw, err := b.ListTools(context.Background())
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	var listing struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &listing); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got := map[string]bool{}
	for _, tool := range listing.Tools {
		got[tool.Name] = true
	}
	for _, want := range []string{
		"list_workers", "get_worker", "create_worker", "update_worker",
		"delete_worker", "pause_worker", "resume_worker", "run_worker_now",
		"list_worker_runs", "get_worker_run", "cancel_worker_run",
	} {
		if !got[want] {
			t.Errorf("ListTools missing %q", want)
		}
	}
}

func TestWorkerCreateEndToEnd(t *testing.T) {
	b, _, wsID, scopeID := newWorkerBackend(t)
	args := map[string]any{
		"name":            "digest",
		"model_provider":  "anthropic",
		"model_id":        "claude-opus-4-7",
		"secret_scope_id": scopeID,
		"prompt_template": "Summarise.",
		"schedule_spec":   "0 9 * * *",
		"workspace_id":    wsID,
	}
	text, isErr := callWorkerTool(t, b, "create_worker", args)
	if isErr {
		t.Fatalf("create error: %s", text)
	}
	var w store.Worker
	if err := json.Unmarshal([]byte(text), &w); err != nil {
		t.Fatalf("unmarshal worker: %v", err)
	}
	if w.ID == "" || w.ExecMode != "propose" || w.ConcurrencyPolicy != "skip" {
		t.Errorf("defaults not applied: %+v", w)
	}
}

func TestWorkerCreateValidationError(t *testing.T) {
	b, _, _, scopeID := newWorkerBackend(t)
	// Missing workspace_id — must fail validation, not write a row.
	text, isErr := callWorkerTool(t, b, "create_worker", map[string]any{
		"name":            "x",
		"model_provider":  "anthropic",
		"model_id":        "claude-opus-4-7",
		"secret_scope_id": scopeID,
		"prompt_template": "x",
		"schedule_spec":   "5m",
	})
	if !isErr {
		t.Fatalf("expected error result, got success: %s", text)
	}
	if !strings.Contains(text, "workspace_id required") {
		t.Errorf("error %q missing workspace_id hint", text)
	}
}

func TestWorkerPauseResumeFlow(t *testing.T) {
	b, _, wsID, scopeID := newWorkerBackend(t)
	created := createWorker(t, b, wsID, scopeID)
	text, isErr := callWorkerTool(t, b, "pause_worker", map[string]string{"id": created.ID})
	if isErr {
		t.Fatalf("pause error: %s", text)
	}
	var paused store.Worker
	_ = json.Unmarshal([]byte(text), &paused)
	if paused.Enabled {
		t.Error("pause did not disable worker")
	}

	text, isErr = callWorkerTool(t, b, "resume_worker", map[string]string{"id": created.ID})
	if isErr {
		t.Fatalf("resume error: %s", text)
	}
	var resumed store.Worker
	_ = json.Unmarshal([]byte(text), &resumed)
	if !resumed.Enabled {
		t.Error("resume did not re-enable worker")
	}
}

func TestWorkerRunNowStub(t *testing.T) {
	b, db, wsID, scopeID := newWorkerBackend(t)
	created := createWorker(t, b, wsID, scopeID)
	text, isErr := callWorkerTool(t, b, "run_worker_now", map[string]string{"id": created.ID})
	if isErr {
		t.Fatalf("run_now: %s", text)
	}
	// run_worker_now dispatches on a detached goroutine and acks
	// immediately with {worker_id, status:"dispatched"} so the run can
	// outlive the MCP call's context (long autonomous workers).
	var ack struct {
		WorkerID string `json:"worker_id"`
		Status   string `json:"status"`
	}
	if err := json.Unmarshal([]byte(text), &ack); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if ack.WorkerID != created.ID || ack.Status != "dispatched" {
		t.Errorf("unexpected dispatch ack: %+v", ack)
	}
	// The detached run still materialises a row, visible via get_worker_run.
	runID := waitForWorkerRun(t, db, created.ID)
	if _, isErr := callWorkerTool(t, b, "get_worker_run", map[string]string{"run_id": runID}); isErr {
		t.Fatalf("get_worker_run: run %s not retrievable", runID)
	}
}

func TestWorkerToolsMissingService(t *testing.T) {
	// Without SetWorkerAdmin, every worker tool must return a clean
	// error rather than panicking.
	b := NewInternalBackend(newTestDB(t), nil)
	text, isErr := callWorkerTool(t, b, "list_workers", nil)
	if !isErr {
		t.Fatalf("expected error result, got success: %s", text)
	}
	if !strings.Contains(text, "worker admin service") {
		t.Errorf("error %q missing service-not-wired hint", text)
	}
}

func TestWorkerDeletePreservesRuns(t *testing.T) {
	b, db, wsID, scopeID := newWorkerBackend(t)
	created := createWorker(t, b, wsID, scopeID)
	// Fire a stub run so there's a row to verify outlives the delete.
	// Dispatch is async now, so wait for the row to materialise before deleting.
	if _, isErr := callWorkerTool(t, b, "run_worker_now", map[string]string{"id": created.ID}); isErr {
		t.Fatal("run_worker_now returned error")
	}
	runID := waitForWorkerRun(t, db, created.ID)
	// Delete.
	if _, isErr := callWorkerTool(t, b, "delete_worker", map[string]string{"id": created.ID}); isErr {
		t.Fatal("delete returned error")
	}
	// Worker is gone, but the run row is still there.
	if _, err := db.GetWorker(context.Background(), created.ID); err == nil {
		t.Error("worker still present after delete")
	}
	if _, err := db.GetWorkerRun(context.Background(), runID); err != nil {
		t.Errorf("run row was cascade-deleted (should have survived): %v", err)
	}
}

func TestWorkerCancelRunTool(t *testing.T) {
	b, db, wsID, scopeID := newWorkerBackend(t)
	created := createWorker(t, b, wsID, scopeID)
	if _, isErr := callWorkerTool(t, b, "run_worker_now", map[string]string{"id": created.ID}); isErr {
		t.Fatal("run_worker_now returned error")
	}
	runID := waitForWorkerRun(t, db, created.ID)
	text, isErr := callWorkerTool(t, b, "cancel_worker_run", map[string]string{
		"run_id": runID,
		"reason": "test cleanup",
	})
	if isErr {
		t.Fatalf("cancel_worker_run returned error: %s", text)
	}
	var out struct {
		RunID  string `json:"run_id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("unmarshal cancel output: %v", err)
	}
	if out.RunID != runID || out.Status != "cancelled" {
		t.Fatalf("cancel output = %+v", out)
	}
}

// createWorker is a small helper for tests that need a pre-existing
// Worker; goes through the public dispatch surface so the test stays
// integration-shaped.
func createWorker(t *testing.T, b *InternalBackend, wsID, scopeID string) store.Worker {
	t.Helper()
	args := map[string]any{
		"name":            "tw-" + strings.ToLower(strings.ReplaceAll(t.Name(), "/", "-")),
		"model_provider":  "anthropic",
		"model_id":        "claude-opus-4-7",
		"secret_scope_id": scopeID,
		"prompt_template": "hi",
		"schedule_spec":   "5m",
		"workspace_id":    wsID,
	}
	text, isErr := callWorkerTool(t, b, "create_worker", args)
	if isErr {
		t.Fatalf("create_worker fixture error: %s", text)
	}
	var w store.Worker
	if err := json.Unmarshal([]byte(text), &w); err != nil {
		t.Fatalf("unmarshal worker: %v", err)
	}
	return w
}
