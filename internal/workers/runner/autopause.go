// Package runner — autopause.go owns the inline auto-pause checks that
// run at the end of every WorkerRun (M1). Two distinct triggers, both
// derived from the just-persisted ledger state:
//
//  1. Monthly cost cap — total cost_usd this calendar month > worker's
//     MaxMonthlyCostUSD → pause + critical mesh alert.
//  2. Consecutive failures — last N WorkerRuns (N = worker's
//     MaxConsecutiveFailures) are all status="failure" → pause + high
//     priority mesh alert.
//
// Both checks rely on store-side methods (SumCostThisMonth,
// LastFailureStatuses) so the SQL stays out of the runner. Pausing
// sets Worker.Enabled=false and Worker.AutoPausedReason so the operator
// can see WHY in the dashboard.
//
// Failures inside these checks are logged but never fail the run —
// the run itself already terminated.
package runner

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/don-works/mcplexer/internal/store"
)

// runAutoPauseChecks is the entry point called from runner.finalize.
// It re-reads the Worker (so we observe edits made by other code paths
// between dispatch and finalize) and runs both pause triggers in
// sequence. The first trigger to fire short-circuits — once a worker
// is paused, the second alert would be redundant noise.
//
// runID is the WorkerRun that just terminated and prompted the check.
// It's stamped into the worker_autopause.triggered audit row so
// incident reconstruction can join the pause back to the triggering
// run. An empty runID is tolerated (older callers / direct test
// invocations) but means audit forensics lose that link.
func (r *Runner) runAutoPauseChecks(ctx context.Context, workerID, runID string) {
	if workerID == "" {
		return
	}
	worker, err := r.store.GetWorker(ctx, workerID)
	if err != nil {
		slog.Warn("auto-pause: failed to load worker", "worker_id", workerID, "error", err)
		return
	}
	if !worker.Enabled {
		// Already paused — nothing to do.
		return
	}
	if r.checkMonthlyCostCap(ctx, worker, runID) {
		return
	}
	r.checkConsecutiveFailureCap(ctx, worker, runID)
}

// checkMonthlyCostCap pauses worker when this month's spend exceeds
// MaxMonthlyCostUSD. Returns true when the cap fired (so the caller
// can skip the second check).
func (r *Runner) checkMonthlyCostCap(ctx context.Context, worker *store.Worker, runID string) bool {
	if worker.MaxMonthlyCostUSD <= 0 {
		return false
	}
	now := r.clock.Now().UTC()
	sum, err := r.store.SumCostThisMonth(ctx, worker.ID, now)
	if err != nil {
		slog.Warn("auto-pause: failed to sum monthly cost",
			"worker_id", worker.ID, "error", err)
		return false
	}
	if sum <= worker.MaxMonthlyCostUSD {
		return false
	}
	reason := fmt.Sprintf(
		"monthly budget exceeded ($%.4f / $%.4f)",
		sum, worker.MaxMonthlyCostUSD,
	)
	r.pauseWorker(ctx, worker, runID, reason, "critical")
	return true
}

// checkConsecutiveFailureCap pauses worker when the last N runs are
// all failures (N = MaxConsecutiveFailures). A streak of fewer than N
// failures, or a success interleaved, leaves the worker enabled.
func (r *Runner) checkConsecutiveFailureCap(ctx context.Context, worker *store.Worker, runID string) {
	n := worker.MaxConsecutiveFailures
	if n <= 0 {
		return
	}
	statuses, err := r.store.LastFailureStatuses(ctx, worker.ID, n)
	if err != nil {
		slog.Warn("auto-pause: failed to load last statuses",
			"worker_id", worker.ID, "error", err)
		return
	}
	if len(statuses) < n {
		return
	}
	for _, s := range statuses {
		if s != StatusFailure {
			return
		}
	}
	reason := fmt.Sprintf("consecutive failures (%d in a row)", n)
	r.pauseWorker(ctx, worker, runID, reason, "high")
}

// pauseWorker sets Enabled=false + AutoPausedReason and emits a mesh
// alert with the requested priority. Errors are logged but not
// propagated — the run that triggered the check has already
// terminated.
func (r *Runner) pauseWorker(
	ctx context.Context, worker *store.Worker, runID, reason, priority string,
) {
	worker.Enabled = false
	worker.AutoPausedReason = reason
	if err := r.store.UpdateWorker(ctx, worker); err != nil {
		slog.Error("auto-pause: failed to disable worker",
			"worker_id", worker.ID, "error", err)
		return
	}
	r.emitAuditAutoPauseTriggered(ctx, worker.ID, runID, reason, priority)
	if r.mesh == nil {
		return
	}
	content := fmt.Sprintf("Worker %q auto-paused: %s", worker.Name, reason)
	id, err := r.mesh.Send(ctx, MeshOutbound{
		Kind:     "alert",
		Priority: priority,
		Content:  content,
		Tags:     "worker,auto_paused",
		WorkerID: worker.ID,
	})
	if err != nil {
		slog.Warn("auto-pause: mesh alert failed",
			"worker_id", worker.ID, "error", err)
		return
	}
	slog.Info("auto-pause: worker paused",
		"worker_id", worker.ID,
		"reason", reason,
		"mesh_msg_id", id,
	)
}
