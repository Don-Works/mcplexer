// handler_monitoring_resolution_test.go — the filing side of the feedback
// loop: one class must produce ONE task no matter how many runs or how much
// the model rewords itself, and a recurrence after resolution must surface.
package gateway

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/tasks"
	"github.com/don-works/mcplexer/internal/workers/runner"
)

// Normalisation is the duplicate-filing fix the DB constraint cannot make:
// migration 143 guarantees one live task per class_key, but that is worthless
// when every run computes a different class_key from reworded free text.
func TestNormalizeMonitoringCorrelationKey(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{"counts differ", "source discontinuity (6 restarts)", "source discontinuity restarts"},
		{"same class reworded", "Source Discontinuity — 7 restarts!", "source discontinuity restarts"},
		{"case and punctuation", "  API  container: RESTARTED  ", "api container restarted"},
		{"pure count is dropped", "restarts (x12)", "restarts"},
		{"punctuation splits, numeric tokens drop", "api-A|ordersync.go:42", "api a ordersync go"},
		{"empty stays empty", "   ", ""},
		{"numeric-only collapses to empty", "12 34", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeMonitoringCorrelationKey(tc.in); got != tc.want {
				t.Fatalf("normalize(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// Two reworded triages of the same operational class must converge on one
// incident and one task, not two siblings.
func TestRewordedCorrelationKeysShareOneTask(t *testing.T) {
	h, db, _ := newMonitoringOwnershipHandler(t)
	ctx := runner.WithWorkerRunCtx(context.Background(), runner.WorkerRunCtx{
		RunID: "run-1", WorkerID: "log-watch", WorkspaceID: "ws-A",
	})
	first := commitTriageResult(t, ctx, h, resolutionCommitArgs(t, "source discontinuity (6 restarts)", store.SeverityWarn))
	second := commitTriageResult(t, ctx, h, resolutionCommitArgs(t, "Source Discontinuity — 7 restarts!", store.SeverityWarn))

	if first["task_id"] != second["task_id"] {
		t.Fatalf("reworded correlation keys filed separate tasks: %v vs %v", first["task_id"], second["task_id"])
	}
	if second["task_created"] == true {
		t.Fatalf("second commit must reuse the canonical task, not create one")
	}
	rows, err := db.ListTasks(ctx, store.TaskFilter{WorkspaceID: "ws-A", Tags: []string{"logwatch"}})
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("logwatch tasks = %d, want 1", len(rows))
	}
}

// Concurrent triage of the same class must produce ONE task. The partial
// unique index on tasks(workspace_id, meta.$.logwatch_class) elects a winner
// and the loser recovers on ErrAlreadyExists rather than creating a sibling.
func TestConcurrentTriageOfOneClassProducesOneTask(t *testing.T) {
	h, db, _ := newMonitoringOwnershipHandler(t)
	args := resolutionCommitArgs(t, "concurrent restart storm", store.SeverityWarn)

	const runners = 6
	var wg sync.WaitGroup
	results := make([]string, runners)
	errs := make([]string, runners)
	for i := 0; i < runners; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx := runner.WithWorkerRunCtx(context.Background(), runner.WorkerRunCtx{
				WorkerID: "log-watch", WorkspaceID: "ws-A",
			})
			raw, _, handled := h.dispatchMonitoringTool(ctx, "monitoring__commit_triage", json.RawMessage(args))
			if !handled {
				errs[i] = "not handled"
				return
			}
			var parsed CallToolResult
			if err := json.Unmarshal(raw, &parsed); err != nil || len(parsed.Content) == 0 {
				errs[i] = "bad envelope"
				return
			}
			if parsed.IsError {
				errs[i] = parsed.Content[0].Text
				return
			}
			var decoded map[string]any
			if err := json.Unmarshal([]byte(parsed.Content[0].Text), &decoded); err != nil {
				errs[i] = err.Error()
				return
			}
			results[i] = decoded["task_id"].(string)
		}(i)
	}
	wg.Wait()

	// The invariant under test is task count, not per-call success: a losing
	// racer may legitimately fail on a busy DB, but it must never file a
	// second task for the class.
	rows, err := db.ListTasks(context.Background(), store.TaskFilter{
		WorkspaceID: "ws-A", Tags: []string{"logwatch"},
	})
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("concurrent triage produced %d tasks, want 1 (errors: %v)", len(rows), errs)
	}
	for i, id := range results {
		if id != "" && id != rows[0].ID {
			t.Fatalf("runner %d got task %s, want the single canonical %s", i, id, rows[0].ID)
		}
	}
}

// A recurrence after the task was resolved must REOPEN it. The old behaviour
// patched meta and priority on a closed row and never touched status, so a
// regression after "fixed" was invisible by construction.
func TestRecurrenceAfterResolutionReopensCanonicalTask(t *testing.T) {
	h, db, _ := newMonitoringOwnershipHandler(t)
	ctx := runner.WithWorkerRunCtx(context.Background(), runner.WorkerRunCtx{
		RunID: "run-first", WorkerID: "log-watch", WorkspaceID: "ws-A",
	})
	args := resolutionCommitArgs(t, "recurring restart", store.SeverityWarn)
	first := commitTriageResult(t, ctx, h, args)
	taskID := first["task_id"].(string)

	// Operator resolves it as genuinely fixed.
	done := "done"
	if _, err := h.tasksSvc.Update(ctx, "ws-A", taskID, tasks.UpdatePatch{
		Status: &done, UpdatedBySessionID: "sess-op", ActorKind: "user",
	}); err != nil {
		t.Fatalf("close task: %v", err)
	}
	closed, err := db.GetTask(ctx, taskID)
	if err != nil || closed.ClosedAt == nil {
		t.Fatalf("task should be closed: %+v err=%v", closed, err)
	}

	// It comes back. Escalated severity is what re-queues an already-triaged
	// template, which is the only way a resolved class legitimately reaches
	// commit_triage again.
	recurCtx := runner.WithWorkerRunCtx(context.Background(), runner.WorkerRunCtx{
		RunID: "run-recur", WorkerID: "log-watch", WorkspaceID: "ws-A",
	})
	again := commitTriageResult(t, recurCtx, h, resolutionCommitArgs(t, "recurring restart", store.SeverityError))
	if again["task_id"] != taskID {
		t.Fatalf("recurrence filed a new task %v instead of reopening %s", again["task_id"], taskID)
	}
	reopened, err := db.GetTask(ctx, taskID)
	if err != nil {
		t.Fatalf("re-read task: %v", err)
	}
	if reopened.ClosedAt != nil {
		t.Fatalf("recurrence must reopen the canonical task, got closed_at=%v", reopened.ClosedAt)
	}
	if reopened.Status != "open" {
		t.Fatalf("reopened task status = %q, want open", reopened.Status)
	}
	rows, err := db.ListTasks(ctx, store.TaskFilter{WorkspaceID: "ws-A", Tags: []string{"logwatch"}})
	if err != nil || len(rows) != 1 {
		t.Fatalf("recurrence must not create a sibling: len=%d err=%v", len(rows), err)
	}
}

// The operator read path: suppressions are enumerable and reversible through
// the tool surface, not just the store.
func TestSuppressionToolsListAndReverse(t *testing.T) {
	h, db, _ := newMonitoringOwnershipHandler(t)
	ctx := runner.WithWorkerRunCtx(context.Background(), runner.WorkerRunCtx{
		RunID: "run-suppress", WorkerID: "log-watch", WorkspaceID: "ws-A",
	})
	filed := commitTriageResult(t, ctx, h, resolutionCommitArgs(t, "noisy but harmless", store.SeverityWarn))
	taskID, incidentID := filed["task_id"].(string), filed["incident_id"].(string)

	cancelled := "cancelled"
	if _, err := h.tasksSvc.Update(ctx, "ws-A", taskID, tasks.UpdatePatch{
		Status: &cancelled, UpdatedBySessionID: "sess-op", ActorKind: "user",
	}); err != nil {
		t.Fatalf("close task as cancelled: %v", err)
	}

	text, isErr := monitoringToolTextWithContext(t, ctx, h, "monitoring__suppressions", `{"workspace_id":"ws-A"}`)
	if isErr {
		t.Fatalf("suppressions failed: %s", text)
	}
	var listed map[string]any
	if err := json.Unmarshal([]byte(text), &listed); err != nil {
		t.Fatalf("decode suppressions: %v (%s)", err, text)
	}
	if listed["suppressing"].(float64) != 1 {
		t.Fatalf("expected exactly one live suppression: %s", text)
	}
	entries := listed["resolutions"].([]any)
	entry := entries[0].(map[string]any)
	if entry["incident_id"] != incidentID || entry["resolved_as"] != "cancelled" {
		t.Fatalf("suppression must be attributable to its task resolution: %#v", entry)
	}

	clearArgs, _ := json.Marshal(map[string]any{
		"incident_id": incidentID, "reason": "operator disagreed", "workspace_id": "ws-A",
	})
	text, isErr = monitoringToolTextWithContext(t, ctx, h, "monitoring__unsuppress", string(clearArgs))
	if isErr {
		t.Fatalf("unsuppress failed: %s", text)
	}
	var cleared map[string]any
	if err := json.Unmarshal([]byte(text), &cleared); err != nil {
		t.Fatalf("decode unsuppress: %v (%s)", err, text)
	}
	if cleared["cleared"] != true {
		t.Fatalf("unsuppress must reverse the suppression: %s", text)
	}
	incident, err := db.GetMonitoringIncidentByClass(ctx, "ws-A", "correlation:noisy but harmless")
	if err != nil {
		t.Fatalf("read incident: %v", err)
	}
	if incident.Disposition == store.MonitoringDispositionBenign {
		t.Fatalf("unsuppress must lift the mute, disposition still %q", incident.Disposition)
	}
	// Idempotent.
	text, isErr = monitoringToolTextWithContext(t, ctx, h, "monitoring__unsuppress", string(clearArgs))
	if isErr {
		t.Fatalf("second unsuppress failed: %s", text)
	}
}

func resolutionCommitArgs(t *testing.T, correlationKey, severity string) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"disposition": "actionable", "severity": severity,
		"title": "Container restarting repeatedly",
		"body": "Observed evidence\n- repeated restart lines\n\n" +
			"Verified facts\n- the container comes back each time\n\n" +
			"Hypotheses / unknowns\n- OOM or a failing health check",
		"template_ids":    []string{"tpl-A"},
		"correlation_key": correlationKey,
		"workspace_id":    "ws-A",
	})
	if err != nil {
		t.Fatalf("marshal commit args: %v", err)
	}
	return string(raw)
}

func commitTriageResult(t *testing.T, ctx context.Context, h *handler, args string) map[string]any {
	t.Helper()
	text, isErr := monitoringToolTextWithContext(t, ctx, h, "monitoring__commit_triage", args)
	if isErr {
		t.Fatalf("commit_triage failed: %s", text)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(text), &decoded); err != nil {
		t.Fatalf("decode commit_triage: %v (%s)", err, text)
	}
	return decoded
}
