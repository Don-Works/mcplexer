package api

import (
	"net/http"
	"testing"
)

func TestWorkersHandlerCostAggregate(t *testing.T) {
	srv, _, wsID, scopeID := newWorkersTestServer(t)

	// Create one worker so the aggregator has a row to project.
	create := map[string]any{
		"name":            "cost-worker",
		"model_provider":  "anthropic",
		"model_id":        "claude-opus-4-7",
		"secret_scope_id": scopeID,
		"prompt_template": "x.",
		"schedule_spec":   "0 9 * * *",
		"workspace_id":    wsID,
	}
	postJSON(t, srv.URL+"/api/v1/workers", create, http.StatusCreated)

	got := getJSON(t,
		srv.URL+"/api/v1/workers/cost-aggregate?days=30&workspace_id="+wsID,
		http.StatusOK,
	)
	if got["days"] != float64(30) {
		t.Fatalf("days = %v, want 30", got["days"])
	}
	workers, ok := got["workers"].([]any)
	if !ok {
		t.Fatalf("workers not a slice: %T", got["workers"])
	}
	if len(workers) != 1 {
		t.Fatalf("workers = %d, want 1", len(workers))
	}
	w0 := workers[0].(map[string]any)
	if w0["worker_name"] != "cost-worker" {
		t.Fatalf("worker_name = %v", w0["worker_name"])
	}
	if w0["month_to_date_usd"] != float64(0) {
		t.Fatalf("MTD = %v, want 0", w0["month_to_date_usd"])
	}
	daily, ok := w0["daily_costs"].([]any)
	if !ok {
		t.Fatalf("daily_costs not a slice: %T", w0["daily_costs"])
	}
	if len(daily) != 30 {
		t.Fatalf("daily_costs len = %d, want 30", len(daily))
	}
}

func TestWorkersHandlerCostAggregate_DefaultsDays(t *testing.T) {
	srv, _, _, _ := newWorkersTestServer(t)
	got := getJSON(t, srv.URL+"/api/v1/workers/cost-aggregate", http.StatusOK)
	if got["days"] != float64(30) {
		t.Fatalf("default days = %v, want 30", got["days"])
	}
}
