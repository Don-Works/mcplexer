// delegation_dispatch_reap.go — reaper for delegation workers stuck in
// the "dispatched but never ran" state.
//
// A delegation can wedge in status="dispatched" forever when the worker
// process dies (or the daemon restarts) in the window between DISPATCH
// (dispatchDelegationRun's goroutine start) and the creation of its run
// row (runner.prepareRun → CreateWorkerRun). recordDelegationDispatchFailure
// only fires when RunNowWithOpts returns an IN-PROCESS error, so a process
// death / restart before that skips it — no failure is ever recorded. Such a
// worker is then invisible to every existing recovery path:
//
//   - ReapOrphanedRunningRuns only touches EXISTING status='running'/'dispatched'
//     run ROWS, and explicitly excludes delegate-* workers anyway;
//   - ResumeOrphanedDelegations (ListOrphanedDelegationRuns) requires a
//     status='running' run row, which never got created;
//   - the retention sweep silently ARCHIVES the worker (delegationWorkerExpired
//     falls back to worker.UpdatedAt) rather than surfacing a failure.
//
// This sweep closes that gap deterministically — no model is invoked.
package admin

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// DefaultDispatchOrphanGrace is how long a delegation worker may sit in
// the "dispatched" state — created + enabled, but with NO run row and no
// recorded dispatch failure — before SweepOrphanedDispatches resolves it
// as a dispatch failure.
//
// A healthy dispatch creates its run row within seconds: dispatchDelegationRun
// runs RunNowWithOpts synchronously on its goroutine, and prepareRun persists
// the run row (CreateWorkerRun) right after prompt build + adapter construction
// — sub-second in the common case, a few seconds at worst for a large-repo
// worktree prepare. 10 minutes is ~100x that latency, so a slow-but-legitimate
// dispatch is never falsely reaped; meanwhile a dispatch orphaned by a
// worker-process death or a daemon restart between DISPATCH and CreateWorkerRun
// (which leaves NO run row, invisible to ReapOrphanedRunningRuns and
// ResumeOrphanedDelegations) resolves to a terminal failure within one sweep
// interval instead of lingering forever.
const DefaultDispatchOrphanGrace = 10 * time.Minute

// SweepOrphanedDispatches resolves delegation workers stuck in the
// "dispatched but never ran" state (created + enabled, past the grace
// window, no run row, no recorded dispatch failure) by marking each a
// dispatch FAILURE — the exact terminal shape recordDelegationDispatchFailure
// produces: DispatchFailed=true + DispatchError, no run row.
//
// That shape is classified OPERATIONAL (an adapter/launch reliability event,
// not a model-quality one) everywhere it is read, so a reaped orphan never
// corrupts per-model capacity ranking:
//   - aggregateDelegation counts it as agg.Failure with zero token/cost/
//     duration attribution (the model never ran);
//   - delegationIsOperationalOnly short-circuits to operational on
//     DispatchFailed, keeping the delegation out of the needs_review gate;
//   - modelStatsForDelegation records it as an OperationalFailure +
//     DispatchFailure and excludes it from the per-model quality average.
//
// grace <= 0 uses DefaultDispatchOrphanGrace. Idempotent: a worker already
// carrying DispatchFailed, already archived, disabled, or holding ANY run
// row is skipped — so a genuinely running worker is left to the existing
// ResumeOrphanedDelegations path and re-running the sweep is a no-op.
// Per-worker stamp failures are logged and skipped so one bad row never
// aborts the sweep. Returns the number of workers reaped.
func (s *Service) SweepOrphanedDispatches(ctx context.Context, grace time.Duration) (int, error) {
	if grace <= 0 {
		grace = DefaultDispatchOrphanGrace
	}
	cutoff := s.clock.Now().UTC().Add(-grace)
	rows, err := s.listDelegationWorkers(ctx, "")
	if err != nil {
		return 0, err
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.worker.ID)
	}
	runsByWorker, err := s.recentRunsByWorker(ctx, ids)
	if err != nil {
		return 0, err
	}
	reaped := 0
	for _, row := range rows {
		if !dispatchOrphaned(row, runsByWorker[row.worker.ID], cutoff) {
			continue
		}
		reason := fmt.Errorf("dispatched but never ran; reaped after %s", grace)
		if err := s.stampDispatchFailure(ctx, row.worker.ID, reason); err != nil {
			slog.Warn("dispatch orphan sweep: stamp failed",
				"worker_id", row.worker.ID, "error", err)
			continue
		}
		reaped++
	}
	if reaped > 0 {
		slog.Info("dispatch orphan sweep: reaped dispatched-but-never-ran delegations",
			"reaped", reaped, "grace", grace.String())
	}
	return reaped, nil
}

// dispatchOrphaned reports whether one delegation worker is a
// dispatched-but-never-ran orphan as of cutoff. Pure — the caller
// supplies the worker's run rows so the predicate is unit-testable.
func dispatchOrphaned(row delegationWorkerRow, runs []*store.WorkerRun, cutoff time.Time) bool {
	// Already resolved (dispatch failure recorded, or retention-archived) →
	// idempotent no-op.
	if row.meta.DispatchFailed || row.meta.ArchivedAt != nil {
		return false
	}
	// A disabled worker was paused/cancelled by an operator or a prior
	// sweep, not stuck mid-dispatch — leave it alone.
	if !row.worker.Enabled {
		return false
	}
	// ANY run row means the worker reached the runner: it is either running
	// (owned by ResumeOrphanedDelegations) or terminal. Only a worker with
	// NO run row is "dispatched but never ran", so we never double-reap a
	// live run.
	if len(runs) > 0 {
		return false
	}
	// Grace: give a legitimate dispatch time to create its run row. The
	// delegation create stamp is the dispatch time (dispatch fires in the
	// same Delegate call); fall back to the worker row's CreatedAt when the
	// metadata stamp is absent.
	created := row.meta.CreatedAt
	if created.IsZero() {
		created = row.worker.CreatedAt
	}
	if created.IsZero() {
		return false
	}
	return created.Before(cutoff)
}

// stampDispatchFailure marks a delegation worker's metadata with
// DispatchFailed=true + DispatchError. Idempotent — a worker already
// flagged (or one whose parameters carry no delegation metadata) is a
// no-op. Shared by recordDelegationDispatchFailure (detached-dispatch
// error path) and SweepOrphanedDispatches (orphan reaper); the fresh
// GetWorker read avoids clobbering a concurrent metadata write.
func (s *Service) stampDispatchFailure(ctx context.Context, workerID string, dispatchErr error) error {
	w, err := s.store.GetWorker(ctx, workerID)
	if err != nil {
		return fmt.Errorf("get worker: %w", err)
	}
	meta, ok := parseDelegationMetadata(w.ParametersJSON)
	if !ok {
		return nil
	}
	if meta.DispatchFailed {
		return nil
	}
	meta.DispatchFailed = true
	meta.DispatchError = dispatchErr.Error()
	params, err := updateDelegationMetadataJSON(w.ParametersJSON, meta)
	if err != nil {
		return fmt.Errorf("marshal metadata: %w", err)
	}
	if _, err := s.Update(ctx, UpdateInput{ID: workerID, ParametersJSON: &params}); err != nil {
		return fmt.Errorf("update worker: %w", err)
	}
	return nil
}
