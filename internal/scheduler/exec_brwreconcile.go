package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/don-works/mcplexer/internal/store"
)

// BrwReconcileCommand is the sentinel ScheduledJob.Command that marks a job
// as the in-process brw browser-profile auto-discovery reconcile rather than
// an external binary to exec.
//
// Unlike the audit-prune job — which gets its own KindAuditPrune kind because
// it is purely heap/cron-driven — brw auto-discovery needs TWO trigger
// sources sharing ONE executor: a periodic interval fallback (heap) and a
// file_watch on the policy file (FileWatcher). The heap selects jobs by
// NextRun(kind) and the FileWatcher selects jobs by kind==file_watch, so a
// single kind cannot serve both. Reusing the already-wired KindInterval and
// KindFileWatch kinds and keying dispatch on this command keeps both firing
// paths unchanged while still routing to the executor — the "reuse the
// interval/file_watch kinds with a wired executor" option.
const BrwReconcileCommand = "__brw_reconcile__"

// brwReconcileRunTimeout caps a single reconcile tick: a brwctl exec plus a
// handful of sqlite writes. 60s is generous headroom that still guarantees
// forward progress on the heap if brwctl hangs.
const brwReconcileRunTimeout = 60 * time.Second

// BrwReconcileResult is the per-run summary the executor returns so the
// scheduler can emit a traceable brw.reconciled audit row + slog line.
type BrwReconcileResult struct {
	Daemons   int
	Created   int
	Updated   int
	Adopted   int
	Pruned    int
	Unchanged int
	Skipped   int
}

// BrwReconcileExecutor loads the live brwd roster, reconciles the gateway's
// source="brw" downstream servers + routes with it, and applies the
// make-it-live side effects (route invalidation + downstream instance
// reload). The daemon wires a concrete implementation backed by the config
// service + routing engine + downstream manager in cmd/; tests pass a fake to
// assert dispatch wiring. Mirrors the PruneExecutor seam exactly.
type BrwReconcileExecutor interface {
	Reconcile(ctx context.Context, now time.Time) (BrwReconcileResult, error)
}

// SetBrwReconcileExecutor wires the brw auto-discovery runner. Safe to call
// before or after Start; subsequent brw-reconcile fires use the new value.
// Pass nil to disable (the row surfaces as last_status="failure" / "brw
// reconcile executor not wired").
func (s *Scheduler) SetBrwReconcileExecutor(exec BrwReconcileExecutor) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.brwExec = exec
}

func (s *Scheduler) brwReconcileExecutor() BrwReconcileExecutor {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.brwExec
}

// executeBrwReconcileJob runs one reconcile synchronously, bounded by
// brwReconcileRunTimeout. On success it emits a dedicated brw.reconciled
// audit row carrying the counts so the reconcile itself is traceable
// (mirrors executePruneJob's audit.pruned emission).
func (s *Scheduler) executeBrwReconcileJob(ctx context.Context, j store.ScheduledJob) (string, error) {
	exec := s.brwReconcileExecutor()
	if exec == nil {
		return statusFailure, errors.New("brw reconcile executor not wired")
	}
	now := s.clock.Now()
	runCtx, cancel := context.WithTimeout(ctx, brwReconcileRunTimeout)
	defer cancel()
	res, err := exec.Reconcile(runCtx, now)
	if err != nil {
		return statusFailure, fmt.Errorf("brw reconcile: %w", err)
	}
	s.emitBrwReconciledAudit(ctx, j, now, res)
	slog.Info("scheduler: brw reconcile complete",
		"job_id", j.ID,
		"daemons", res.Daemons,
		"created", res.Created,
		"updated", res.Updated,
		"adopted", res.Adopted,
		"pruned", res.Pruned,
		"unchanged", res.Unchanged,
		"skipped", res.Skipped,
	)
	return "success", nil
}

// emitBrwReconciledAudit writes one audit_records row capturing the reconcile
// outcome under the stable tool_name "brw.reconciled". Best-effort: a dropped
// audit row must never fail the reconcile.
func (s *Scheduler) emitBrwReconciledAudit(
	ctx context.Context, j store.ScheduledJob, now time.Time, res BrwReconcileResult,
) {
	if s.auditor == nil {
		return
	}
	payload := map[string]any{
		"job_id":    j.ID,
		"daemons":   res.Daemons,
		"created":   res.Created,
		"updated":   res.Updated,
		"adopted":   res.Adopted,
		"pruned":    res.Pruned,
		"unchanged": res.Unchanged,
		"skipped":   res.Skipped,
	}
	raw, _ := json.Marshal(payload)
	rec := &store.AuditRecord{
		ID:             ulid.Make().String(),
		Timestamp:      now,
		ClientType:     "scheduler",
		ToolName:       "brw.reconciled",
		ParamsRedacted: raw,
		Status:         "success",
		CreatedAt:      now,
		ActorKind:      "scheduler",
		ActorID:        j.ID,
	}
	if err := s.auditor.Record(ctx, rec); err != nil {
		// Audit is best-effort — never fail a reconcile because audit didn't.
		_ = err
	}
}
