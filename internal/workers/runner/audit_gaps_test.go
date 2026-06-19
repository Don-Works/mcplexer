package runner_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/models"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/workers/runner"
)

// These tests close the three audit gaps identified in the 2026-05-21
// audit pass. Each gap is its own t.Run so a CI regression names the
// exact gap that re-opened.

// TestAuditGap_ProposeGateEmitsDeniedDispatch verifies that
// preDispatchGate emits a worker_tool.dispatch{allowed:false} audit
// row BEFORE short-circuiting to awaiting_approval. Without this row
// the awaiting_approval fast path is forensics-blind — incident
// reconstruction can't see WHICH tool got denied.
func TestAuditGap_ProposeGateEmitsDeniedDispatch(t *testing.T) {
	db := newTestStore(t)
	wsID, scopeID := setupFKs(t, db)
	w := sampleWorker(wsID, scopeID)
	w.ExecMode = runner.ExecModePropose
	createWorker(t, db, w)

	adapter := &fakeAdapter{responses: []models.SendResponse{
		{ToolCalls: []models.ToolCall{{ID: "t1", Name: "post_message"}}, StopReason: models.StopToolUse},
	}}
	// writeTools triggers the pre-dispatch gate; results sets WriteClass
	// so the post-dispatch gate WOULD also trigger if pre-dispatch
	// somehow let it through (it shouldn't).
	disp := &fakeDispatcher{
		writeTools: map[string]bool{"post_message": true},
		results:    map[string]runner.ToolCallResult{"post_message": {WriteClass: true}},
	}
	auditor := &fakeAuditor{}
	r := runner.New(runner.Deps{
		Store:      db,
		Dispatcher: disp,
		Mesh:       &fakeMesh{},
		Secrets:    &fakeSecrets{},
		Auditor:    auditor,
		Adapter:    func(_ models.Config) (models.ModelAdapter, error) { return adapter, nil },
	})
	if _, err := r.Run(context.Background(), w.ID); err != nil {
		t.Fatalf("run: %v", err)
	}
	// Find the worker_tool.dispatch audit row and assert allowed=false.
	denied := findToolDispatchAudits(t, auditor, "post_message", false)
	if len(denied) == 0 {
		t.Fatalf("expected worker_tool.dispatch{allowed:false} for post_message; got names=%v", auditor.toolNames())
	}
	// SECURITY contract: the deny row must come from the pre-dispatch
	// gate (no DispatchTool call happened), so disp.dispatched stays
	// empty. If pre-dispatch let it through and the post-dispatch gate
	// fired instead, disp.dispatched would have one entry — the test
	// would still record allowed=false but the SECURITY claim is broken.
	if len(disp.dispatched) != 0 {
		t.Fatalf("pre-dispatch gate failed: DispatchTool was called %d times; want 0", len(disp.dispatched))
	}
}

// TestAuditGap_AutoPauseAuditCarriesRunID verifies the auto-pause
// audit row's run_id field is set to the triggering run, not empty.
// Without this, joining autopause back to the run that caused it
// requires timestamp guessing.
func TestAuditGap_AutoPauseAuditCarriesRunID(t *testing.T) {
	db := newTestStore(t)
	wsID, scopeID := setupFKs(t, db)
	w := sampleWorker(wsID, scopeID)
	w.MaxMonthlyCostUSD = 0.0001 // microscopic cap → trips on first run
	createWorker(t, db, w)

	adapter := &fakeAdapter{responses: []models.SendResponse{
		{
			Text:         "ok",
			InputTokens:  1000,
			OutputTokens: 500,
			StopReason:   models.StopEndTurn,
		},
	}}
	auditor := &fakeAuditor{}
	r := runner.New(runner.Deps{
		Store:      db,
		Dispatcher: &fakeDispatcher{},
		Mesh:       &fakeMesh{},
		Secrets:    &fakeSecrets{},
		Auditor:    auditor,
		Adapter:    func(_ models.Config) (models.ModelAdapter, error) { return adapter, nil },
	})
	runID, err := r.Run(context.Background(), w.ID)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	rec := findAuditByEvent(t, auditor, "worker_autopause.triggered")
	if rec == nil {
		t.Fatalf("no worker_autopause.triggered audit emitted; got names=%v", auditor.toolNames())
	}
	gotRunID := auditPayloadField(t, rec, "run_id")
	if gotRunID == "" {
		t.Fatalf("worker_autopause.triggered audit has empty run_id; want %q", runID)
	}
	if gotRunID != runID {
		t.Fatalf("worker_autopause.triggered run_id = %q; want %q", gotRunID, runID)
	}
	// Actor / correlation columns (migration 053) must be populated so
	// cross-actor incident queries can find this row without grepping
	// ParamsRedacted.
	if rec.ActorKind != "worker" {
		t.Errorf("ActorKind = %q, want worker", rec.ActorKind)
	}
	if rec.ActorID != w.ID {
		t.Errorf("ActorID = %q, want %q", rec.ActorID, w.ID)
	}
	if rec.CorrelationID != runID {
		t.Errorf("CorrelationID = %q, want %q", rec.CorrelationID, runID)
	}
}

// TestAuditGap_AdapterErrorEmitsModelSendError verifies that an
// adapter Send error produces a worker_model.send{status:error} audit
// row BEFORE the run terminates as failure. Without this, token-burn
// before a crash is invisible.
func TestAuditGap_AdapterErrorEmitsModelSendError(t *testing.T) {
	db := newTestStore(t)
	wsID, scopeID := setupFKs(t, db)
	w := sampleWorker(wsID, scopeID)
	createWorker(t, db, w)

	adapter := &fakeAdapter{err: errSentinelBoom()}
	auditor := &fakeAuditor{}
	r := runner.New(runner.Deps{
		Store:      db,
		Dispatcher: &fakeDispatcher{},
		Mesh:       &fakeMesh{},
		Secrets:    &fakeSecrets{},
		Auditor:    auditor,
		Adapter:    func(_ models.Config) (models.ModelAdapter, error) { return adapter, nil },
	})
	runID, err := r.Run(context.Background(), w.ID)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	// Confirm the run itself ended as failure.
	run, _ := db.GetWorkerRun(context.Background(), runID)
	if run.Status != runner.StatusFailure {
		t.Fatalf("run status = %q, want failure", run.Status)
	}
	// Find the worker_model.send error row.
	var found *store.AuditRecord
	for _, rec := range auditor.records {
		if rec.ToolName != "worker_model.send" {
			continue
		}
		if rec.Status == "error" {
			found = rec
			break
		}
	}
	if found == nil {
		t.Fatalf("no worker_model.send{status:error} audit row; got names+statuses=%v",
			auditNamesWithStatuses(auditor))
	}
	if !strings.Contains(found.ErrorMessage, "boom") {
		t.Fatalf("audit ErrorMessage = %q; want substring %q", found.ErrorMessage, "boom")
	}
	if got := auditPayloadField(t, found, "run_id"); got != runID {
		t.Fatalf("audit run_id = %q; want %q", got, runID)
	}
}

// --- helpers ---

// findToolDispatchAudits returns every worker_tool.dispatch audit row
// for the given tool name with the requested allowed flag.
func findToolDispatchAudits(t *testing.T, a *fakeAuditor, toolName string, allowed bool) []*store.AuditRecord {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []*store.AuditRecord
	for _, rec := range a.records {
		if rec.ToolName != "worker_tool.dispatch" {
			continue
		}
		payload := decodeAuditPayload(t, rec)
		if payload["tool_name"] != toolName {
			continue
		}
		gotAllowed, _ := payload["allowed"].(bool)
		if gotAllowed == allowed {
			out = append(out, rec)
		}
	}
	return out
}

// findAuditByEvent returns the first audit record matching event, or
// nil when none exists.
func findAuditByEvent(t *testing.T, a *fakeAuditor, event string) *store.AuditRecord {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, rec := range a.records {
		if rec.ToolName == event {
			return rec
		}
	}
	return nil
}

// auditPayloadField pulls a string field out of an audit row's
// ParamsRedacted JSON. Empty when missing or non-string.
func auditPayloadField(t *testing.T, rec *store.AuditRecord, key string) string {
	t.Helper()
	payload := decodeAuditPayload(t, rec)
	v, _ := payload[key].(string)
	return v
}

func decodeAuditPayload(t *testing.T, rec *store.AuditRecord) map[string]any {
	t.Helper()
	if len(rec.ParamsRedacted) == 0 {
		return map[string]any{}
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.ParamsRedacted, &payload); err != nil {
		t.Fatalf("decode audit payload: %v", err)
	}
	return payload
}

// auditNamesWithStatuses returns a "name=status" list for every audit
// row — used in failure messages so the diagnostic shows what WAS
// emitted when the expected row is missing.
func auditNamesWithStatuses(a *fakeAuditor) []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]string, 0, len(a.records))
	for _, rec := range a.records {
		out = append(out, rec.ToolName+"="+rec.Status)
	}
	return out
}
