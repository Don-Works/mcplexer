package api

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

func TestWorkersRunSSE_NotFound(t *testing.T) {
	srv, _, _, _ := newWorkersTestServer(t)
	resp, err := http.Get(srv.URL + "/api/v1/workers/x/runs/missing/events")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestWorkersRunSSE_TerminalImmediate(t *testing.T) {
	srv, db, wsID, scopeID := newWorkersTestServer(t)
	// Create a worker + a finalized run so the SSE handler closes on
	// first poll (terminal status).
	create := map[string]any{
		"name":            "sse-worker",
		"model_provider":  "anthropic",
		"model_id":        "claude-opus-4-7",
		"secret_scope_id": scopeID,
		"prompt_template": "x.",
		"schedule_spec":   "0 9 * * *",
		"workspace_id":    wsID,
	}
	w := postJSON(t, srv.URL+"/api/v1/workers", create, http.StatusCreated)
	workerID, _ := w["id"].(string)

	ctx := context.Background()
	run := &store.WorkerRun{
		WorkerID:      workerID,
		StartedAt:     time.Now().UTC().Add(-time.Minute),
		Status:        "running",
		ModelProvider: "anthropic",
		ModelID:       "claude-opus-4-7",
	}
	if err := db.CreateWorkerRun(ctx, run); err != nil {
		t.Fatalf("create run: %v", err)
	}
	fin := store.WorkerRunFinalize{
		Status:     "success",
		FinishedAt: time.Now().UTC(),
		OutputText: "done",
	}
	if err := db.UpdateWorkerRunStatus(ctx, run.ID, fin); err != nil {
		t.Fatalf("finalize: %v", err)
	}

	// Subscribe; expect a single `event: status` frame then EOF.
	req, _ := http.NewRequest(
		http.MethodGet,
		srv.URL+"/api/v1/workers/"+workerID+"/runs/"+run.ID+"/events",
		nil,
	)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	got := string(body)
	if !strings.Contains(got, "event: status") {
		t.Fatalf("missing status event: %q", got)
	}
	if !strings.Contains(got, `"status":"success"`) {
		t.Fatalf("missing success status in payload: %q", got)
	}
}
