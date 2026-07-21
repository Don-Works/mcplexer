package admin

import (
	"context"
	"log/slog"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/workers/runner"
)

const resumeTriggerKind = "resume"

// ResumeOrphanedDelegations re-dispatches delegation workers whose runs
// were still "running" when the daemon restarted. Called once at boot
// after ReapOrphanedRunningRuns has cleared non-delegation orphans
// to interrupt status.
//
// For each orphaned delegation run:
//  1. The orphaned run row is finalised as status="interrupted" with a clear
//     error message (the in-process runner that owned it is gone, but the
//     model itself did not fail).
//  2. A 1-generation crash-loop guard skips re-dispatch when the run's
//     trigger_kind is already "resume" — this caps automatic resume at
//     exactly one generation.
//  3. Otherwise the worker is re-dispatched on a detached goroutine with
//     TriggerKind="resume".
//
// Returns the number of runs re-dispatched. Never returns a hard error
// that would abort boot — per-row failures are logged and skipped.
func (s *Service) ResumeOrphanedDelegations(ctx context.Context) (int, error) {
	runs, err := s.store.ListOrphanedDelegationRuns(ctx)
	if err != nil {
		slog.Warn("resume orphaned delegations: list failed", "error", err)
		return 0, nil
	}
	if len(runs) == 0 {
		return 0, nil
	}

	now := s.clock.Now().UTC()
	resumed := 0
	for _, run := range runs {
		if err := s.finalizeOrphanedRun(ctx, run, now); err != nil {
			slog.Warn("resume orphaned delegations: finalize failed",
				"run_id", run.ID, "error", err)
			continue
		}

		if run.TriggerKind == resumeTriggerKind {
			slog.Info("resume orphaned delegations: skipping re-resume of already-resumed run",
				"run_id", run.ID, "worker_id", run.WorkerID)
			continue
		}

		w, err := s.store.GetWorker(ctx, run.WorkerID)
		if err != nil {
			slog.Warn("resume orphaned delegations: get worker failed",
				"worker_id", run.WorkerID, "error", err)
			continue
		}
		if !w.Enabled {
			slog.Info("resume orphaned delegations: worker disabled, skipping",
				"worker_id", w.ID)
			continue
		}

		s.dispatchDelegationResume(ctx, w.ID, w.MaxWallClockSeconds)
		resumed++
	}
	return resumed, nil
}

func (s *Service) finalizeOrphanedRun(ctx context.Context, run *store.WorkerRun, now time.Time) error {
	return s.store.UpdateWorkerRunStatus(ctx, run.ID, store.WorkerRunFinalize{
		Status:     runner.StatusInterrupted,
		FinishedAt: now,
		Error:      "interrupted by daemon restart",
		// HasGitResult intentionally remains false: interrupted-state recovery
		// must preserve the initial owned branch and any trusted commit fields.
	})
}

func (s *Service) dispatchDelegationResume(parent context.Context, workerID string, wallSecs int) {
	timeout := time.Duration(wallSecs+60) * time.Second
	if wallSecs <= 0 {
		timeout = time.Duration(defaultDelegationMaxWallClockSeconds+60) * time.Second
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Warn("resume: panicked during detached dispatch",
					"worker_id", workerID, "panic", r)
			}
		}()
		ctx, cancel := context.WithTimeout(s.lifecycleContext(), timeout)
		defer cancel()
		if _, err := s.RunNowWithOpts(ctx, workerID, runner.RunOpts{
			TriggerKind: resumeTriggerKind,
		}); err != nil {
			slog.Warn("resume: detached run failed",
				"worker_id", workerID, "error", err)
		}
	}()
	_ = parent
}
