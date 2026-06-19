// Package admin — audit.go owns the worker-admin CRUD audit emission
// pipeline. It mirrors internal/workers/runner/audit.go in shape: every
// CRUD mutation lands as one AuditRecord with ClientType="worker_admin"
// and ToolName=worker_admin.<verb>.
//
// Why it matters: an AI tampering with update_worker(prompt_template=…)
// or flipping exec_mode → autonomous needs to leave a permanent trail.
// Without this every CRUD call was invisible to query_audit.
//
// Field-handling rules:
//   - Short fields (name, model_id, schedule_spec, exec_mode, …) are
//     logged verbatim.
//   - Long opaque fields (prompt_template, parameters_json,
//     tool_allowlist_json, output_channels_json) are fingerprinted as
//     {sha256: <8-byte hex prefix>, len: <bytes>}. The body never lands
//     in the audit ledger — prompts can be huge and may contain
//     operator-sensitive context the redaction layer doesn't catch.
//
// The emission helper is nil-safe — when s.auditor is nil every helper
// no-ops so non-daemon paths (CLI, tests) don't need to wire the audit
// pipeline. Record failures NEVER propagate back into the CRUD call;
// they're logged via slog and dropped.
package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"

	"github.com/don-works/mcplexer/internal/audit"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/oklog/ulid/v2"
)

// Audit event constants — every worker-admin CRUD emission carries one
// of these in ToolName. Dashboards group by event class for triage.
const (
	auditEventAdminCreate     = "worker_admin.create"
	auditEventAdminUpdate     = "worker_admin.update"
	auditEventAdminDelete     = "worker_admin.delete"
	auditEventAdminSetEnabled = "worker_admin.set_enabled"
	auditEventAdminPause      = "worker_admin.pause"
	auditEventAdminResume     = "worker_admin.resume"
	auditEventAdminRunNow     = "worker_admin.run_now"
	auditEventAdminCancelRun  = "worker_admin.cancel_run"
)

// auditClientType is stamped on every worker-admin audit record so
// query_audit can isolate CRUD-class events from runner-class events
// (which use ClientType="worker"). Lives separately from the approval
// path's "worker" ClientType because the operator's surface area is
// distinct from the runtime's.
const auditClientType = "worker_admin"

// emitAudit is the single record-shaped emission helper. Every emitAudit*
// wrapper builds a payload and calls this. Nil-safe — when s.auditor is
// nil this is a no-op. A failed Record never propagates back to the
// caller: the CRUD write already landed, audit is best-effort.
func (s *Service) emitAudit(
	ctx context.Context, workerID, event string,
	payload map[string]any, status, errMsg string,
) {
	if s.auditor == nil {
		return
	}
	if payload == nil {
		payload = map[string]any{}
	}
	payload["worker_id"] = workerID
	raw, _ := json.Marshal(payload)
	rec := &store.AuditRecord{
		ID:             ulid.Make().String(),
		Timestamp:      s.clock.Now().UTC(),
		ClientType:     auditClientType,
		SessionID:      "worker:" + workerID,
		ToolName:       event,
		ParamsRedacted: raw,
		Status:         defaultAdminAuditStatus(status),
		ErrorMessage:   errMsg,
		CorrelationID:  audit.FromCtx(ctx),
		CreatedAt:      s.clock.Now().UTC(),
		ActorKind:      "worker_admin",
		ActorID:        workerID,
	}
	if err := s.auditor.Record(ctx, rec); err != nil {
		slog.Warn("worker admin audit record failed",
			"worker_id", workerID, "event", event, "error", err)
	}
}

// defaultAdminAuditStatus folds empty status into "ok" so dashboards
// that filter on status= can still find these rows.
func defaultAdminAuditStatus(s string) string {
	if s == "" {
		return "ok"
	}
	return s
}

// fingerprint returns a stable summary of a long opaque field — sha256
// (8-byte hex prefix) plus byte length. Empty input returns {empty:true}
// so callers can still distinguish "field cleared" from "field unchanged".
// The 8-byte prefix is enough to detect tampering (collision-resistant
// for the cardinalities of prompts we'll ever see) without bloating the
// audit row.
func fingerprint(s string) map[string]any {
	if s == "" {
		return map[string]any{"empty": true}
	}
	sum := sha256.Sum256([]byte(s))
	return map[string]any{
		"sha256": hex.EncodeToString(sum[:8]),
		"len":    len(s),
	}
}

// emitAuditCreate records a successful worker create. Long fields are
// fingerprinted; short fields are logged verbatim. Errors carry the
// payload too so investigators see what was attempted even on failure.
func (s *Service) emitAuditCreate(
	ctx context.Context, w *store.Worker, status, errMsg string,
) {
	id := ""
	payload := map[string]any{}
	if w != nil {
		id = w.ID
		payload["name"] = w.Name
		payload["model_provider"] = w.ModelProvider
		payload["model_id"] = w.ModelID
		payload["schedule_spec"] = w.ScheduleSpec
		payload["exec_mode"] = w.ExecMode
		payload["workspace_id"] = w.WorkspaceID
		payload["workspace_access_count"] = len(w.WorkspaceAccess)
		payload["prompt_template"] = fingerprint(w.PromptTemplate)
		payload["parameters_json"] = fingerprint(w.ParametersJSON)
		payload["tool_allowlist_json"] = fingerprint(w.ToolAllowlistJSON)
		payload["capability_profile_json"] = fingerprint(w.CapabilityProfileJSON)
		payload["output_channels_json"] = fingerprint(w.OutputChannelsJSON)
		// model_endpoint_url is fingerprinted (not bodied) because
		// openai_compat URLs can embed credentials in path/query (e.g.
		// "https://api.example.com/v1?key=…"). Same redaction rationale
		// as buildUpdateDiff's long-fingerprint set.
		payload["model_endpoint_url"] = fingerprint(w.ModelEndpointURL)
	}
	s.emitAudit(ctx, id, auditEventAdminCreate, payload, status, errMsg)
}

// emitAuditUpdate records a worker update with per-field diffs. Only the
// fields actually mutated (non-nil UpdateInput fields) appear under
// "changes" so reviewers see what the operator (or AI) touched without
// noise. Long fields render as fingerprint old/new pairs.
func (s *Service) emitAuditUpdate(
	ctx context.Context, workerID string, changes map[string]any,
	status, errMsg string,
) {
	payload := map[string]any{"changes": changes}
	s.emitAudit(ctx, workerID, auditEventAdminUpdate, payload, status, errMsg)
}

// emitAuditDelete records a hard delete. Carries the worker name so the
// audit row remains meaningful after the worker row is gone.
func (s *Service) emitAuditDelete(
	ctx context.Context, workerID, name, status, errMsg string,
) {
	s.emitAudit(ctx, workerID, auditEventAdminDelete, map[string]any{
		"name": name,
	}, status, errMsg)
}

// emitAuditSetEnabled records a SetEnabled (or its Pause/Resume aliases)
// call. previousEnabled is the value BEFORE the flip so reviewers can
// see the transition.
func (s *Service) emitAuditSetEnabled(
	ctx context.Context, workerID, event string, enabled, previousEnabled bool,
	status, errMsg string,
) {
	s.emitAudit(ctx, workerID, event, map[string]any{
		"enabled":          enabled,
		"previous_enabled": previousEnabled,
	}, status, errMsg)
}

// emitAuditRunNow records an ad-hoc run_now. runID is "" when the call
// failed before producing one. paramsOverride is fingerprinted; empty
// override renders as {empty:true}.
func (s *Service) emitAuditRunNow(
	ctx context.Context, workerID, runID, paramsOverride, status, errMsg string,
) {
	s.emitAudit(ctx, workerID, auditEventAdminRunNow, map[string]any{
		"run_id":              runID,
		"parameters_override": fingerprint(paramsOverride),
	}, status, errMsg)
}

// emitAuditCancelRun records an operator recovery action against a WorkerRun.
func (s *Service) emitAuditCancelRun(
	ctx context.Context, runID, workerID, reason, status, errMsg string,
) {
	s.emitAudit(ctx, workerID, auditEventAdminCancelRun, map[string]any{
		"run_id": runID,
		"reason": reason,
	}, status, errMsg)
}

// buildUpdateDiff + the per-axis diff helpers live in audit_diff.go to
// honour the 300-line file budget.
