// workers_handler_m1_test.go — HTTP integration tests for M1: per-
// worker cap persistence via Create + the worker-approvals surface
// (list/approve/reject). Reuses the test scaffolding in
// workers_handler_test.go (newWorkersTestServer + postJSON helpers).
package api

import (
	"context"
	"net/http"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
)

// TestWorkers_CreateWithCaps_PersistsAndReadsBack ensures the 6 cap
// fields on CreateWorkerInput round-trip through the HTTP CREATE +
// GET cycle. This is the contract the PWA editor relies on.
func TestWorkers_CreateWithCaps_PersistsAndReadsBack(t *testing.T) {
	srv, _, wsID, scopeID := newWorkersTestServer(t)
	body := map[string]any{
		"name":                     "capped",
		"model_provider":           "anthropic",
		"model_id":                 "claude-opus-4-7",
		"secret_scope_id":          scopeID,
		"prompt_template":          "x",
		"schedule_spec":            "0 * * * *",
		"workspace_id":             wsID,
		"max_input_tokens":         5000,
		"max_output_tokens":        1024,
		"max_tool_calls":           20,
		"max_wall_clock_seconds":   45,
		"max_monthly_cost_usd":     0.75,
		"max_consecutive_failures": 4,
	}
	created := postJSON(t, srv.URL+"/api/v1/workers", body, http.StatusCreated)
	id, _ := created["id"].(string)
	if id == "" {
		t.Fatal("no id in response")
	}

	got := getJSON(t, srv.URL+"/api/v1/workers/"+id, http.StatusOK)
	w, _ := got["worker"].(map[string]any)
	if w == nil {
		t.Fatalf("missing worker in get response: %+v", got)
	}
	if v, _ := w["max_input_tokens"].(float64); int(v) != 5000 {
		t.Fatalf("max_input_tokens = %v", w["max_input_tokens"])
	}
	if v, _ := w["max_output_tokens"].(float64); int(v) != 1024 {
		t.Fatalf("max_output_tokens = %v", w["max_output_tokens"])
	}
	if v, _ := w["max_tool_calls"].(float64); int(v) != 20 {
		t.Fatalf("max_tool_calls = %v", w["max_tool_calls"])
	}
	if v, _ := w["max_wall_clock_seconds"].(float64); int(v) != 45 {
		t.Fatalf("max_wall_clock_seconds = %v", w["max_wall_clock_seconds"])
	}
	if v, _ := w["max_monthly_cost_usd"].(float64); v != 0.75 {
		t.Fatalf("max_monthly_cost_usd = %v", w["max_monthly_cost_usd"])
	}
	if v, _ := w["max_consecutive_failures"].(float64); int(v) != 4 {
		t.Fatalf("max_consecutive_failures = %v", w["max_consecutive_failures"])
	}
}

// TestWorkers_Approvals_RejectFlow drives the full reject lifecycle:
// seed a pending approval directly via the store, then verify the list
// endpoint returns it AND POST /reject transitions the row.
func TestWorkers_Approvals_RejectFlow(t *testing.T) {
	srv, db, wsID, scopeID := newWorkersTestServer(t)
	ctx := context.Background()

	w := &store.Worker{
		Name:           "needs-approval",
		ModelProvider:  "anthropic",
		ModelID:        "claude-opus-4-7",
		SecretScopeID:  scopeID,
		PromptTemplate: "x",
		ScheduleSpec:   "0 * * * *",
		WorkspaceID:    wsID,
		Enabled:        true,
	}
	if err := db.CreateWorker(ctx, w); err != nil {
		t.Fatalf("create worker: %v", err)
	}
	run := &store.WorkerRun{WorkerID: w.ID, Status: "awaiting_approval"}
	if err := db.CreateWorkerRun(ctx, run); err != nil {
		t.Fatalf("create run: %v", err)
	}
	app := &store.WorkerApproval{
		WorkerID: w.ID, RunID: run.ID,
		ToolName: "post_message", ToolInput: `{}`,
	}
	if err := db.CreateWorkerApproval(ctx, app); err != nil {
		t.Fatalf("create approval: %v", err)
	}

	pending := mustGetSlice(t, srv.URL+"/api/v1/worker-approvals?status=pending")
	if len(pending) != 1 {
		t.Fatalf("pending list len = %d, want 1", len(pending))
	}
	if pending[0]["id"] != app.ID {
		t.Fatalf("pending[0].id = %v, want %v", pending[0]["id"], app.ID)
	}

	resp := postJSON(t,
		srv.URL+"/api/v1/worker-approvals/"+app.ID+"/reject",
		map[string]any{"decided_by": "tester"}, http.StatusOK,
	)
	if resp["status"] != "rejected" {
		t.Fatalf("reject status = %v", resp["status"])
	}

	// Pending list now empty.
	pending = mustGetSlice(t, srv.URL+"/api/v1/worker-approvals?status=pending")
	if len(pending) != 0 {
		t.Fatalf("after reject pending = %d, want 0", len(pending))
	}

	// Second reject must 404 because the row was decided.
	resp2 := postJSONMethod(t, http.MethodPost,
		srv.URL+"/api/v1/worker-approvals/"+app.ID+"/reject",
		map[string]any{"decided_by": "tester"}, http.StatusBadRequest,
	)
	if resp2 == nil {
		t.Fatalf("expected error body on re-reject, got nil")
	}
}
