// Package runner — audit.go owns the worker-run audit emission pipeline.
//
// The mcplexer headline claim is "every tool call, prompt, and approval
// is recorded". The runner emits the following ledger entries through
// the Auditor interface (nil-safe; all helpers no-op when auditor is
// unset):
//
//   - worker_run.started      — at run start
//   - worker_model.send       — per model adapter call (tokens + cost)
//   - worker_tool.dispatch    — per tool dispatch (write_class, allowed)
//   - worker_output.emitted   — per output channel emission (success/duration)
//   - worker_autopause.triggered — when auto-pause fires
//   - worker_run.finished     — at run finalize (final status)
//
// Approval decisions (worker_approval.decided) are emitted from
// admin/approvals.go, since that's where the operator's decision is
// recorded. Every record is stamped with worker_id + run_id in
// ParamsRedacted so cross-references are SQL-trivial.
package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/don-works/mcplexer/internal/models"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/oklog/ulid/v2"
)

// Audit tool_name prefixes — every worker-emitted record carries one of
// these in ToolName so audit dashboards can group by event class.
const (
	auditEventRunStarted       = "worker_run.started"
	auditEventRunFinished      = "worker_run.finished"
	auditEventModelSend        = "worker_model.send"
	auditEventToolDispatch     = "worker_tool.dispatch"
	auditEventOutputEmitted    = "worker_output.emitted"
	auditEventAutoPauseTrigger = "worker_autopause.triggered"
	auditEventApprovalDecided  = "worker_approval.decided"
	// auditEventMemoryConsolidatorRun is emitted at the end of a
	// successful memory-consolidator run. Carries
	// {workspace_id, consolidations_performed, run_id, started_at,
	// finished_at} so downstream agents can tell when a consolidator
	// pass completed without diffing memory snapshots. Distinct from
	// worker_run.finished — the consolidator emits BOTH (the latter is
	// the generic worker lifecycle row; this row is the domain-level
	// "memory was consolidated" signal that the dashboard, mesh
	// broadcaster, and bulletproof grader join on).
	auditEventMemoryConsolidatorRun = "memory__consolidator_run"
	// auditEventDreamRun is emitted at the end of a successful
	// dream-consolidator run (harvest recipes + memory). Same shape
	// and purpose as memory__consolidator_run.
	auditEventDreamRun = "dream__run"
)

// emitAudit is the single record-shaped emission helper. Every other
// emitAudit* wrapper builds a payload and calls this. The audit pipeline
// is best-effort: a failed Record never propagates back into the run.
func (r *Runner) emitAudit(ctx context.Context, workerID, runID, event string, payload map[string]any, status, errMsg string) {
	if r.auditor == nil {
		return
	}
	if payload == nil {
		payload = map[string]any{}
	}
	payload["worker_id"] = workerID
	payload["run_id"] = runID
	raw, _ := json.Marshal(payload)
	rec := &store.AuditRecord{
		ID:             ulid.Make().String(),
		Timestamp:      r.clock.Now().UTC(),
		ClientType:     "worker",
		SessionID:      "worker:" + workerID,
		ToolName:       event,
		ParamsRedacted: raw,
		Status:         defaultAuditStatus(status),
		ErrorMessage:   errMsg,
		CreatedAt:      r.clock.Now().UTC(),
		ActorKind:      "worker",
		ActorID:        workerID,
		CorrelationID:  runID,
	}
	if err := r.auditor.Record(ctx, rec); err != nil {
		slog.Warn("worker audit record failed",
			"worker_id", workerID, "run_id", runID,
			"event", event, "error", err)
	}
}

// defaultAuditStatus folds empty status into "ok" so dashboards that
// filter on status= can still find these rows.
func defaultAuditStatus(s string) string {
	if s == "" {
		return "ok"
	}
	return s
}

// emitAuditRunStarted records the start of a run.
func (r *Runner) emitAuditRunStarted(ctx context.Context, workerID, runID, workerName string) {
	r.emitAudit(ctx, workerID, runID, auditEventRunStarted, map[string]any{
		"worker_name": workerName,
	}, "ok", "")
}

// emitAuditRunFinished records the terminal state of a run.
func (r *Runner) emitAuditRunFinished(ctx context.Context, workerID, runID, status, errMsg string, costUSD float64, inputTokens, outputTokens, toolCalls int) {
	r.emitAudit(ctx, workerID, runID, auditEventRunFinished, map[string]any{
		"status":           status,
		"cost_usd":         costUSD,
		"input_tokens":     inputTokens,
		"output_tokens":    outputTokens,
		"tool_calls_count": toolCalls,
	}, status, errMsg)
}

// emitAuditModelSend records one model adapter call.
func (r *Runner) emitAuditModelSend(ctx context.Context, s *loopState, inputTokens, outputTokens int, costDelta float64) {
	r.emitAudit(ctx, s.worker.ID, s.runID, auditEventModelSend, map[string]any{
		"provider":      s.worker.ModelProvider,
		"model_id":      s.worker.ModelID,
		"input_tokens":  inputTokens,
		"output_tokens": outputTokens,
		"cost_usd":      costDelta,
		"iteration":     s.iteration,
	}, "ok", "")
}

// emitAuditModelSendError records a failed model adapter call so the
// audit feed sees the attempt even when no successful response came
// back. Some adapters return a partially-populated response on error
// (e.g. cached input tokens already charged); when resp is non-nil we
// fold those numbers into the ledger row so token-burn before a crash
// is visible. resp may be nil — in that case we record an error row
// with zero token counts so forensics still sees the attempt.
func (r *Runner) emitAuditModelSendError(ctx context.Context, s *loopState, resp *models.SendResponse, errMsg string) {
	payload := map[string]any{
		"provider":  s.worker.ModelProvider,
		"model_id":  s.worker.ModelID,
		"iteration": s.iteration,
	}
	if resp != nil {
		payload["input_tokens"] = resp.InputTokens
		payload["output_tokens"] = resp.OutputTokens
		payload["cost_usd"] = resp.CostUSD
	} else {
		payload["input_tokens"] = 0
		payload["output_tokens"] = 0
		payload["cost_usd"] = 0.0
	}
	r.emitAudit(ctx, s.worker.ID, s.runID, auditEventModelSend, payload, "error", errMsg)
}

// emitAuditToolDispatch records a tool dispatch attempt. The
// allowed=true case is the common one — denied dispatches are recorded
// from the dispatcher when the gateway-side allowlist guard fires.
func (r *Runner) emitAuditToolDispatch(ctx context.Context, s *loopState, toolName string, writeClass, allowed bool) {
	r.emitAudit(ctx, s.worker.ID, s.runID, auditEventToolDispatch, map[string]any{
		"tool_name":   toolName,
		"write_class": writeClass,
		"allowed":     allowed,
	}, "ok", "")
}

// emitAuditOutputEmitted records one output channel emission. duration
// is in milliseconds; success=false → status=error with the error
// message attached.
//
// destination is the channel-specific target string (webhook URL,
// Slack channel ID, ClickUp list ID, GitHub owner/repo, recipient peer
// ID). It is NEVER written to the audit row in plaintext — only a
// truncated sha256 prefix (destinationHash) lands in params so wrong-
// destination leaks are detectable WITHOUT storing the URL itself.
//
// An empty destination yields an empty destination_hash. Document
// semantics: empty hash means "no destination known at emit time"
// (e.g. a channel that doesn't address a remote target), distinct
// from "no leak detection" — downstream readers should treat empty
// hash as opaque and not derive presence/absence claims from it.
func (r *Runner) emitAuditOutputEmitted(ctx context.Context, workerID, runID, channelType, destination string, durationMS int64, success bool, errMsg string) {
	status := "ok"
	if !success {
		status = "error"
	}
	r.emitAudit(ctx, workerID, runID, auditEventOutputEmitted, map[string]any{
		"channel_type":     channelType,
		"duration_ms":      durationMS,
		"success":          success,
		"destination_hash": destinationHash(destination),
	}, status, errMsg)
}

// destinationHash returns the first 8 hex chars (4 bytes) of the
// sha256 of destination. 4 bytes is enough collision resistance to
// detect "this run emitted to a DIFFERENT destination than expected"
// — the use case is leak detection, not cryptographic identity — and
// avoids bloating every audit row with a 64-char hash. Empty input
// → empty output so callers can pass "" when no destination applies.
func destinationHash(destination string) string {
	if destination == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(destination))
	return hex.EncodeToString(sum[:4])
}

// emitAuditAutoPauseTriggered records an auto-pause event.
func (r *Runner) emitAuditAutoPauseTriggered(ctx context.Context, workerID, runID, reason, priority string) {
	r.emitAudit(ctx, workerID, runID, auditEventAutoPauseTrigger, map[string]any{
		"reason":   reason,
		"priority": priority,
	}, "ok", "")
}

// emitAuditMemoryConsolidatorRun records the domain-level "memory was
// consolidated" event at the end of a successful consolidator pass. The
// caller (finalize, for worker.Name == "memory-consolidator") supplies
// the run's workspace + tally + time bounds; we stamp them into a
// memory__consolidator_run audit row. Downstream agents query for this
// row (rather than diffing memory snapshots) to learn "machine A just
// ran the consolidator at HH:MM and changed N rows".
func (r *Runner) emitAuditMemoryConsolidatorRun(
	ctx context.Context, workerID, runID, workspaceID string,
	consolidationsPerformed int, startedAt, finishedAt time.Time,
) {
	r.emitAudit(ctx, workerID, runID, auditEventMemoryConsolidatorRun, map[string]any{
		"workspace_id":             workspaceID,
		"consolidations_performed": consolidationsPerformed,
		"started_at":               startedAt.UTC().Format(time.RFC3339),
		"finished_at":              finishedAt.UTC().Format(time.RFC3339),
	}, "ok", "")
}

// emitAuditDreamRun records the domain-level "dream pass completed"
// (recipes harvested + memory consolidated) event. Mirrors the
// memory-consolidator emit; the caller (runDreamFinalize) supplies
// the performed count.
func (r *Runner) emitAuditDreamRun(
	ctx context.Context, workerID, runID, workspaceID string,
	actionsPerformed int, startedAt, finishedAt time.Time,
) {
	r.emitAudit(ctx, workerID, runID, auditEventDreamRun, map[string]any{
		"workspace_id":      workspaceID,
		"actions_performed": actionsPerformed,
		"started_at":        startedAt.UTC().Format(time.RFC3339),
		"finished_at":       finishedAt.UTC().Format(time.RFC3339),
	}, "ok", "")
}
