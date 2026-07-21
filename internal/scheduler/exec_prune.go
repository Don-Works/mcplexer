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

// PrunePolicy carries the retention knobs the prune dispatch reads
// each tick. Defaults are populated by the daemon at startup from the
// YAML config; flipping the YAML and reloading propagates via the
// pointer the scheduler holds onto. All fields are positive — zero on
// AuditRetentionDays disables audit pruning; zero on WorkerRunCapDays
// disables the worker_runs age cap (per-worker keep-count still
// applies).
type PrunePolicy struct {
	AuditRetentionDays     int
	WorkerRunKeepPerWorker int
	WorkerRunCapDays       int
}

// DefaultPrunePolicy is the out-of-the-box retention shape. 90d audit,
// 1000/worker worker_runs with a 180d hard cap so a runaway worker
// can't keep ten thousand rows just because it fires hourly.
func DefaultPrunePolicy() PrunePolicy {
	return PrunePolicy{
		AuditRetentionDays:     90,
		WorkerRunKeepPerWorker: 1000,
		WorkerRunCapDays:       180,
	}
}

// PruneExecutor is the narrow surface scheduler needs to delete old
// audit + worker_run rows. The daemon wires a concrete implementation
// backed by store.Store; tests pass a fake to assert dispatch wiring.
// Returns the (audit, worker_runs) row counts so the scheduler can
// emit the audit.pruned trace.
type PruneExecutor interface {
	Prune(ctx context.Context, policy PrunePolicy, now time.Time) (auditDeleted, workerRunsDeleted int64, err error)
}

// SetPruneExecutor wires the retention runner. Safe to call before or
// after Start; subsequent audit_prune-kind fires use the new value.
// Pass nil to disable retention (the row will surface as
// last_status="failure" / "prune executor not wired").
func (s *Scheduler) SetPruneExecutor(exec PruneExecutor) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneExec = exec
}

func (s *Scheduler) pruneExecutor() PruneExecutor {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pruneExec
}

// executePruneJob runs one retention sweep synchronously. The actual
// deletes are bounded by `pruneRunTimeout` so a multi-million-row
// purge can't block the heap forever. On success it emits a separate
// `audit.pruned` audit row carrying the deleted counts + cutoff so
// the pruning itself is traceable (meta-but-important per the design
// brief).
func (s *Scheduler) executePruneJob(
	ctx context.Context, j store.ScheduledJob,
) (string, error) {
	exec := s.pruneExecutor()
	if exec == nil {
		return statusFailure, errors.New("prune executor not wired")
	}
	policy := DefaultPrunePolicy()
	if s.prunePolicy != nil {
		policy = *s.prunePolicy
	}
	// Mint one correlation ID per prune run so the slog lines and the
	// audit.pruned row can be joined post-hoc.
	correlationID := ulid.Make().String()
	now := s.clock.Now()
	runCtx, cancel := context.WithTimeout(ctx, pruneRunTimeout)
	defer cancel()
	auditDeleted, runsDeleted, err := exec.Prune(runCtx, policy, now)
	if err != nil {
		return statusFailure, fmt.Errorf("prune: %w", err)
	}
	s.emitPrunedAudit(ctx, j, policy, now, auditDeleted, runsDeleted, correlationID)
	slog.Info("scheduler: retention prune complete",
		"job_id", j.ID,
		"correlation_id", correlationID,
		"audit_deleted", auditDeleted,
		"worker_runs_deleted", runsDeleted,
		"audit_retention_days", policy.AuditRetentionDays,
		"worker_run_keep_per_worker", policy.WorkerRunKeepPerWorker,
		"worker_run_cap_days", policy.WorkerRunCapDays,
	)
	return "success", nil
}

// pruneRunTimeout caps a single retention tick. Deleting tens of
// thousands of rows on a busy SQLite database can take a few seconds
// while WAL is contended; 60s is generous headroom that still
// guarantees forward progress on the heap.
const pruneRunTimeout = 60 * time.Second

// SetPrunePolicy hot-swaps the retention knobs read each fire. The
// scheduler holds the pointer (not a copy) so a runtime YAML reload
// can update policy without restarting the daemon. Pass nil to revert
// to DefaultPrunePolicy.
func (s *Scheduler) SetPrunePolicy(p *PrunePolicy) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prunePolicy = p
}

// emitPrunedAudit writes one audit_records row capturing the prune
// outcome — independent of the generic schedule:<name> audit fire()
// already records. This gives operators a stable tool_name
// ("audit.pruned") to query when they want the retention history
// alone.
//
// ActorKind="scheduler" + ActorID=j.ID land here so the 053 actor
// index catches "what did the scheduler do" queries. CorrelationID
// joins this row to the slog lines emitted by executePruneJob.
func (s *Scheduler) emitPrunedAudit(
	ctx context.Context, j store.ScheduledJob, policy PrunePolicy,
	now time.Time, auditDeleted, runsDeleted int64, correlationID string,
) {
	if s.auditor == nil {
		return
	}
	beforeCutoff := now.Add(-time.Duration(policy.AuditRetentionDays) * 24 * time.Hour)
	payload := map[string]any{
		"job_id":                     j.ID,
		"count_audit":                auditDeleted,
		"count_worker_runs":          runsDeleted,
		"before_cutoff":              beforeCutoff.UTC().Format(time.RFC3339),
		"audit_retention_days":       policy.AuditRetentionDays,
		"worker_run_keep_per_worker": policy.WorkerRunKeepPerWorker,
		"worker_run_cap_days":        policy.WorkerRunCapDays,
	}
	raw, _ := json.Marshal(payload)
	rec := &store.AuditRecord{
		ID:             ulid.Make().String(),
		Timestamp:      now,
		ClientType:     "scheduler",
		ToolName:       "audit.pruned",
		ParamsRedacted: raw,
		Status:         "success",
		CreatedAt:      now,
		ActorKind:      "scheduler",
		ActorID:        j.ID,
		CorrelationID:  correlationID,
	}
	if err := s.auditor.Record(ctx, rec); err != nil {
		// Audit is best-effort — never fail a prune because audit didn't.
		_ = err
	}
}
