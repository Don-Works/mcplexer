package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/don-works/mcplexer/internal/audit"
	"github.com/don-works/mcplexer/internal/store"
)

// workerRunTimeout is the fallback outer cap when the worker's own
// MaxWallClockSeconds is zero or unavailable. The scheduler-tick context
// is too short-lived to span an LLM call, so the worker goroutine uses a
// derived context. When the worker has MaxWallClockSeconds set, the
// scheduler uses that value (plus 5 min headroom) so long-running
// autonomous workers (multi-hour code delivery) are not prematurely
// killed. The runner enforces its own internal wall-clock cap; this
// outer cap exists as a safety net.
const workerRunTimeoutFallback = 30 * time.Minute

// terminalFinaliseTimeout bounds the fresh context used to write a
// terminal ScheduledJob row when the worker's derived context has
// already expired. Short on purpose — it only covers a single
// GetScheduledJob + UpdateScheduledJob round-trip against the store.
const terminalFinaliseTimeout = 10 * time.Second

// WorkerExecutor is the narrow surface scheduler needs from the M0.3
// worker runner. Implementations dispatch a Worker by id, persist the
// initial WorkerRun row synchronously, and return the run id so the
// scheduler can poll terminal status. *runner.Runner satisfies this
// natively via its Run method.
type WorkerExecutor interface {
	Run(ctx context.Context, workerID string) (runID string, err error)
}

// WorkerLookup is the narrow store surface the scheduler needs to
// resolve a Worker row before dispatch and to read back its terminal
// run status. Implementations may be nil — worker kind jobs then fail
// closed with status="failure" and a clear error.
type WorkerLookup interface {
	GetWorker(ctx context.Context, id string) (*store.Worker, error)
	GetWorkerRun(ctx context.Context, id string) (*store.WorkerRun, error)
	CountRunningWorkerRuns(ctx context.Context, workerID string) (int, error)
}

// Worker dispatch status strings (sentinel values written to
// ScheduledJob.LastStatus). Mirrored from the runner where applicable
// so the schedule UI can present a single vocabulary.
const (
	// statusSkipped marks a worker fire that was deliberately not run
	// (worker disabled, missing, or concurrency policy blocked the
	// tick). The next NextRunAt is still recomputed so the schedule
	// keeps ticking.
	statusSkipped = "skipped"
	// statusFailure marks an exec-time error before the runner
	// produced a WorkerRun. Used when the executor itself is unwired
	// or returns an error.
	statusFailure = "failure"
)

// executeWorkerJob is the kind="worker" dispatch path. Skip decisions
// (worker missing / disabled / concurrency policy) run synchronously
// so the ScheduledJob row reflects the outcome before fire() advances
// the heap. The actual runner invocation is dispatched in a goroutine
// — a long LLM call must NOT block the scheduler heap. The goroutine
// finalises the row with the terminal WorkerRun status when it
// completes.
//
// Returns ("running", nil) on async-dispatch so fire() writes
// LastStatus="running" and recomputes NextRunAt without waiting on the
// runner. The goroutine's writeback later overrides LastStatus +
// LastError via a read-modify-write so fire's NextRunAt is preserved.
func (s *Scheduler) executeWorkerJob(
	ctx context.Context, j store.ScheduledJob,
) (string, error) {
	if !j.Enabled {
		return "disabled", nil
	}
	if reason := s.workerSkipReason(ctx, j); reason != "" {
		return statusSkipped, errors.New(reason)
	}
	exec := s.workerExecutor()
	if exec == nil {
		return statusFailure, errors.New("worker executor not wired")
	}
	go s.runWorkerAsync(j, exec)
	return "running", nil
}

// runWorkerAsync invokes the runner on its own goroutine + ctx so a
// long-running LLM call does not stall the scheduler's heap. After the
// runner returns, the goroutine reads the WorkerRun's terminal status
// and writes it back to the ScheduledJob row. Owns the whole post-run
// write for the async path (fire skips its own UpdateScheduledJob for
// kind=worker + status="running") so the two paths don't race on the
// same row.
func (s *Scheduler) runWorkerAsync(j store.ScheduledJob, exec WorkerExecutor) {
	timeout := s.resolveWorkerTimeout(j)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	// Seed a scheduler-level correlation_id so any slog/audit emitted
	// BEFORE the runner picks up its own run.ID (worker missing,
	// store read failure, etc.) still joins back to this tick. The
	// runner then overrides with run.ID via audit.WithCorrelation in
	// RunWithOpts — the run is the more specific correlation and that
	// override is intentional.
	tickID := fmt.Sprintf("sched:%s:%d", j.ID, time.Now().UTC().UnixNano())
	ctx = audit.WithCorrelation(ctx, tickID)
	runID, runErr := exec.Run(ctx, j.WorkerID)
	status, statusErr := s.terminalStatus(ctx, runID, runErr)
	// If the derived worker context expired (the runner blew past its
	// wall-clock cap), neither the row writeback nor the terminal audit
	// can run on the dead context — and fire() already wrote
	// LastStatus="running" optimistically while skipping its own
	// post-run write for the async path. So recover here: rewrite the
	// outcome to a failure that explains the abort and swap in a fresh,
	// correlation-preserving context, then drive BOTH the row writeback
	// AND the terminal audit from it. Without this the row is stuck at
	// "running" forever and the audit trail never sees the true outcome.
	finCtx := ctx
	if cerr := ctx.Err(); cerr != nil {
		slog.Warn("scheduler: worker async ctx expired; finalising as failure",
			"job_id", j.ID, "err", cerr)
		status = statusFailure
		if statusErr == nil {
			statusErr = fmt.Errorf("worker run aborted: %w", cerr)
		} else {
			statusErr = fmt.Errorf("worker run aborted (%v): %w", cerr, statusErr)
		}
		// WithoutCancel keeps the correlation_id (tickID) so the
		// terminal audit still joins this tick, while dropping the
		// expired deadline. Bound it so a wedged store can't hang the
		// goroutine.
		base := context.WithoutCancel(ctx)
		var finCancel context.CancelFunc
		finCtx, finCancel = context.WithTimeout(base, terminalFinaliseTimeout)
		defer finCancel()
	}
	// persistTerminal owns the ScheduledJob row writeback; we then emit a
	// single TERMINAL audit row from this goroutine — fire() only ever
	// wrote the optimistic "running" status audit for the async path, so
	// the true outcome must be recorded here for the audit trail to
	// reflect reality.
	s.persistTerminal(finCtx, j.ID, status, statusErr)
	s.recordAudit(finCtx, j, status, statusErr)
}

// resolveWorkerTimeout decides the outer wall-clock cap for the worker
// goroutine. Precedence: an explicit test/override (workerRunTimeout >
// 0) wins, then the worker's own MaxWallClockSeconds (+5 min headroom),
// then the package fallback. The override field exists so tests can
// drive the derived-context-expiry path deterministically without
// waiting out the multi-minute production caps.
func (s *Scheduler) resolveWorkerTimeout(j store.ScheduledJob) time.Duration {
	s.mu.Lock()
	override := s.workerRunTimeout
	s.mu.Unlock()
	if override > 0 {
		return override
	}
	if lookup := s.workerLookup(); lookup != nil {
		if w, err := lookup.GetWorker(context.Background(), j.WorkerID); err == nil && w != nil && w.MaxWallClockSeconds > 0 {
			return time.Duration(w.MaxWallClockSeconds+300) * time.Second
		}
	}
	return workerRunTimeoutFallback
}

// terminalStatus turns (runID, runErr) into the final (status, err)
// pair the scheduler should record. Mirrors the previous synchronous
// flow's branching so existing test expectations (failure/success
// reflection) keep working.
func (s *Scheduler) terminalStatus(
	ctx context.Context, runID string, runErr error,
) (string, error) {
	if runErr != nil {
		return statusFailure, fmt.Errorf("run worker: %w", runErr)
	}
	return s.reflectRunStatus(ctx, runID)
}

// persistTerminal applies a status/error transition to the
// ScheduledJob row. Owns the whole post-run write for the async path:
// LastStatus, LastError, AND NextRunAt (since fire deliberately skipped
// its own UpdateScheduledJob so it doesn't race the goroutine). The
// NextRunAt recompute mirrors fire's own logic verbatim — workers
// reschedule via the ScheduleSpec, not a per-fire override.
func (s *Scheduler) persistTerminal(
	ctx context.Context, jobID, status string, statusErr error,
) {
	// NOTE: this MUST NOT early-return on ctx.Err() — fire() already wrote
	// LastStatus="running" optimistically and skips its own post-run
	// write for the async path, so bailing here leaves the row stuck at
	// "running" forever. runWorkerAsync guarantees the context passed in
	// is live (it swaps in a fresh, correlation-preserving context when
	// the derived worker context has expired), so we always attempt the
	// terminal write.
	current, err := s.store.GetScheduledJob(ctx, jobID)
	if err != nil || current == nil {
		slog.Warn("scheduler: worker async finalise lookup failed",
			"job_id", jobID, "err", err)
		return
	}
	current.LastStatus = status
	if statusErr != nil {
		current.LastError = truncateErr(statusErr.Error())
	} else {
		current.LastError = ""
	}
	if !current.Enabled {
		current.NextRunAt = nil
	} else if next, nerr := NextRun(current.Kind, current.Spec, s.clock.Now()); nerr == nil {
		current.NextRunAt = ptrTime(next)
	} else {
		current.NextRunAt = nil
	}
	if err := s.store.UpdateScheduledJob(ctx, current); err != nil {
		slog.Warn("scheduler: worker async finalise write failed",
			"job_id", jobID, "err", err)
	}
}

// workerSkipReason returns a non-empty reason string when the worker
// fire must be skipped (disabled, missing, concurrency block). Empty
// string means "proceed to dispatch".
//
// Note: a nil WorkerLookup is *not* a skip — the caller falls through
// to executor dispatch where the runner's own store will catch missing
// workers and surface a typed error. The lookup is only required for
// the concurrency check; without it we let the run start (best-effort)
// and rely on the worker's own concurrency_policy enforcement.
func (s *Scheduler) workerSkipReason(
	ctx context.Context, j store.ScheduledJob,
) string {
	if j.WorkerID == "" {
		return "worker_id missing"
	}
	lookup := s.workerLookup()
	if lookup == nil {
		return ""
	}
	w, err := lookup.GetWorker(ctx, j.WorkerID)
	if err != nil {
		return fmt.Sprintf("worker missing: %v", err)
	}
	if !w.Enabled {
		return "worker disabled"
	}
	if w.ConcurrencyPolicy == "" || w.ConcurrencyPolicy == "skip" {
		count, cerr := lookup.CountRunningWorkerRuns(ctx, w.ID)
		if cerr == nil && count > 0 {
			return "previous run still running"
		}
	}
	return ""
}

// reflectRunStatus reads the WorkerRun row the executor created and
// maps its terminal status onto the ScheduledJob.LastStatus column. A
// missing lookup or a missing run row both fall back to the optimistic
// "running" state — the runner will catch up the row asynchronously
// via its own finalize path.
func (s *Scheduler) reflectRunStatus(
	ctx context.Context, runID string,
) (string, error) {
	lookup := s.workerLookup()
	if lookup == nil || runID == "" {
		return "running", nil
	}
	run, err := lookup.GetWorkerRun(ctx, runID)
	if err != nil || run == nil {
		// Best-effort: we know the runner accepted the dispatch, so
		// keep the row in "running" rather than surfacing a spurious
		// failure when the lookup races the runner's writeback.
		return "running", nil
	}
	if run.Error != "" {
		return run.Status, errors.New(run.Error)
	}
	return run.Status, nil
}

// workerExecutor returns the currently-wired executor under lock.
func (s *Scheduler) workerExecutor() WorkerExecutor {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.workerExec
}

// workerLookup returns the currently-wired lookup under lock.
func (s *Scheduler) workerLookup() WorkerLookup {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.workerStor
}
