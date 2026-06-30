package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// Approver is the narrow surface scheduler needs from approval.Manager.
type Approver interface {
	RequestApproval(ctx context.Context, a *store.ToolApproval) (bool, error)
}

// Auditor is the narrow surface scheduler needs from audit.Logger.
// Implementations may be nil — scheduler must handle that.
type Auditor interface {
	Record(ctx context.Context, r *store.AuditRecord) error
}

// approvalTimeoutSec is the default per-fire approval window. Scheduled
// jobs can't block forever waiting for a human — timeout = deny.
const approvalTimeoutSec = 10

// maxOutputBytes caps captured stdout+stderr per fire. M3 hardcodes the
// limit; later milestones can lift it into settings.
const maxOutputBytes = 256 * 1024

// stopGrace is the max time Stop() waits for the loop goroutine.
const stopGrace = 5 * time.Second

// Scheduler is the in-process job runner.
type Scheduler struct {
	store       store.ScheduledJobStore
	approver    Approver
	auditor     Auditor
	clock       Clock
	workerExec  WorkerExecutor
	workerStor  WorkerLookup
	pruneExec   PruneExecutor
	prunePolicy *PrunePolicy
	brwExec     BrwReconcileExecutor

	// workerRunTimeout, when > 0, overrides the derived worker
	// goroutine's outer wall-clock cap. Zero means "use the per-worker
	// MaxWallClockSeconds or the package fallback". Guarded by mu;
	// primarily a test seam for exercising the context-expiry finalise
	// path deterministically.
	workerRunTimeout time.Duration

	mu   sync.Mutex
	jobs *jobHeap

	wakeCh chan struct{}
	doneCh chan struct{}
	stopMu sync.Mutex
	stopFn context.CancelFunc
	exec   commandExecutor
}

// New constructs a Scheduler. Pass RealClock{} in production.
func New(
	s store.ScheduledJobStore,
	approver Approver,
	auditor Auditor,
	clock Clock,
) *Scheduler {
	h := &jobHeap{}
	return &Scheduler{
		store:    s,
		approver: approver,
		auditor:  auditor,
		clock:    clock,
		jobs:     h,
		wakeCh:   make(chan struct{}, 1),
		exec:     osCommandExecutor{},
	}
}

// SetWorkerExecutor wires the M0.3 runner so ScheduledJob rows with
// Kind="worker" dispatch in-process instead of execing a shell command.
// Safe to call before or after Start; subsequent worker-kind fires use
// the new value. Pass nil to disable worker dispatch (the row will
// surface as last_status="failure"/"executor unwired").
func (s *Scheduler) SetWorkerExecutor(exec WorkerExecutor) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.workerExec = exec
}

// SetWorkerStore wires the WorkerStore surface used by the worker
// dispatch branch (lookup + concurrency counter + finished-run lookup).
// Safe to call independent of SetWorkerExecutor.
func (s *Scheduler) SetWorkerStore(ws WorkerLookup) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.workerStor = ws
}

// Start loads enabled jobs from the store and kicks off the dispatch
// goroutine. Returns immediately; the loop runs until ctx is cancelled
// or Stop is called.
func (s *Scheduler) Start(ctx context.Context) error {
	if err := s.Reload(ctx); err != nil {
		return fmt.Errorf("scheduler initial load: %w", err)
	}
	s.catchUp(ctx)
	loopCtx, cancel := context.WithCancel(ctx)
	s.stopMu.Lock()
	s.stopFn = cancel
	s.doneCh = make(chan struct{})
	s.stopMu.Unlock()
	go s.run(loopCtx)
	return nil
}

// Stop signals the loop goroutine and waits up to timeout for it to
// finish. A zero or negative timeout uses stopGrace.
func (s *Scheduler) Stop(timeout time.Duration) error {
	s.stopMu.Lock()
	cancel := s.stopFn
	done := s.doneCh
	s.stopFn = nil
	s.stopMu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	if timeout <= 0 {
		timeout = stopGrace
	}
	select {
	case <-done:
		return nil
	case <-time.After(timeout):
		return errors.New("scheduler: stop timed out")
	}
}

// Reload reads every enabled job from the store and re-seeds the heap.
// Jobs without a NextRunAt are skipped — they're added once admin code
// stamps one via Create.
func (s *Scheduler) Reload(ctx context.Context) error {
	jobs, err := s.store.ListScheduledJobs(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	*s.jobs = (*s.jobs)[:0]
	for _, j := range jobs {
		if !j.Enabled || j.NextRunAt == nil {
			continue
		}
		s.jobs.upsertByID(j, *j.NextRunAt)
	}
	s.kick()
	return nil
}

// RunOnce executes a single job synchronously by id, bypassing the
// heap. Used by admin tools + the run-job CLI path.
func (s *Scheduler) RunOnce(ctx context.Context, jobID string) error {
	j, err := s.store.GetScheduledJob(ctx, jobID)
	if err != nil {
		return err
	}
	s.fire(ctx, *j)
	return nil
}

// kick wakes the dispatch loop. Non-blocking — the channel has cap 1.
func (s *Scheduler) kick() {
	select {
	case s.wakeCh <- struct{}{}:
	default:
	}
}

// run is the dispatch goroutine. Sleeps until the next due job or until
// woken via kick / context cancellation.
func (s *Scheduler) run(ctx context.Context) {
	defer close(s.doneCh)
	for {
		next, waitFor := s.nextWait()
		if next == nil {
			// Idle — block until kicked or cancelled.
			select {
			case <-ctx.Done():
				return
			case <-s.wakeCh:
				continue
			}
		}
		if waitFor <= 0 {
			s.popAndFire(ctx)
			continue
		}
		t := s.clock.NewTimer(waitFor)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-s.wakeCh:
			t.Stop()
			continue
		case <-t.C():
			s.popAndFire(ctx)
		}
	}
}

// nextWait returns the peek of the heap plus how long until it's due,
// or (nil, 0) when the heap is empty.
func (s *Scheduler) nextWait() (*pendingJob, time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	pj := s.jobs.peek()
	if pj == nil {
		return nil, 0
	}
	return pj, pj.nextRun.Sub(s.clock.Now())
}

// popAndFire removes the head of the heap and fires it in the
// scheduler's context. Re-schedules via NextRun on success or
// disables (next_run_at cleared) when the spec is unrecognised.
func (s *Scheduler) popAndFire(ctx context.Context) {
	s.mu.Lock()
	pj := s.jobs.peek()
	if pj == nil {
		s.mu.Unlock()
		return
	}
	// Pop via container/heap to maintain invariants.
	_ = s.jobs.removeByID(pj.job.ID)
	s.mu.Unlock()
	s.fire(ctx, pj.job)
}

// fire executes a single job: persist running state, dispatch by kind,
// update DB, emit audit, and (if still enabled) compute the next run
// and push back into the heap. fire never panics out — every branch
// updates the row's last_status / last_error.
func (s *Scheduler) fire(ctx context.Context, j store.ScheduledJob) {
	current, err := s.store.GetScheduledJob(ctx, j.ID)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			slog.Warn("scheduler: fire lookup failed", "id", j.ID, "err", err)
		}
		return
	}
	j = *current
	now := s.clock.Now()
	j.LastRunAt = ptrTime(now)
	j.LastStatus = "running"
	j.LastError = ""
	if err := s.store.UpdateScheduledJob(ctx, &j); err != nil {
		slog.Warn("scheduler: persist running failed", "id", j.ID, "err", err)
		return
	}

	status, runErr := s.dispatch(ctx, j)
	current, err = s.store.GetScheduledJob(ctx, j.ID)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			slog.Warn("scheduler: post-dispatch lookup failed", "id", j.ID, "err", err)
		}
		return
	}
	j = *current
	j.LastStatus = status
	if runErr != nil {
		j.LastError = truncateErr(runErr.Error())
	} else {
		j.LastError = ""
	}

	if next, nerr := NextRun(j.Kind, j.Spec, s.clock.Now()); nerr == nil {
		j.NextRunAt = ptrTime(next)
	} else {
		j.NextRunAt = nil
	}
	// For async worker dispatches the goroutine owns the entire
	// post-run write — both LastStatus/LastError (which it learns
	// after the runner returns) AND NextRunAt (so fire doesn't race
	// the goroutine's read-modify-write on the same row). The
	// scheduler's heap is still re-armed below using the value we
	// computed here.
	asyncWorker := j.Kind == KindWorker && status == "running" && runErr == nil
	if !asyncWorker {
		if err := s.store.UpdateScheduledJob(ctx, &j); err != nil {
			slog.Warn("scheduler: persist post-run failed", "id", j.ID, "err", err)
		}
	}

	s.recordAudit(ctx, j, status, runErr)

	if j.Enabled && j.NextRunAt != nil {
		s.mu.Lock()
		s.jobs.upsertByID(j, *j.NextRunAt)
		s.mu.Unlock()
		s.kick()
	}
}

// dispatch routes a fire to the kind-specific executor. Shell-style
// kinds (cron, interval, file_watch, git_hook) flow through the
// approval + os/exec path; worker-kind jobs are handed off to the
// in-process WorkerExecutor synchronously and the terminal WorkerRun
// status is mirrored back onto the ScheduledJob row.
func (s *Scheduler) dispatch(ctx context.Context, j store.ScheduledJob) (string, error) {
	switch j.Kind {
	case KindWorker:
		return s.executeWorkerJob(ctx, j)
	case KindAuditPrune:
		return s.executePruneJob(ctx, j)
	}
	// brw auto-discovery reuses the existing interval + file_watch firing
	// machinery rather than a dedicated kind: a single sentinel Command on
	// otherwise-ordinary KindInterval / KindFileWatch rows routes BOTH the
	// heap fire and the FileWatcher fire to the wired BrwReconcileExecutor.
	// This is the only discriminator the dual-trigger design needs — see
	// BrwReconcileCommand in exec_brwreconcile.go.
	if j.Command == BrwReconcileCommand {
		return s.executeBrwReconcileJob(ctx, j)
	}
	return s.executeAndApprove(ctx, j)
}

func ptrTime(t time.Time) *time.Time {
	t = t.UTC()
	return &t
}

// maxCatchUpPerJob caps the number of missed fires that will be run
// for a single job during the boot catch-up pass. Bounded to prevent
// an absurdly-frequent interval job (e.g. "1s") from running thousands
// of catch-up iterations when the daemon has been down for hours.
const maxCatchUpPerJob = 1

// catchUp fires enabled jobs whose NextRunAt is in the past — the
// daemon was down when they should have fired. Each job gets at most
// maxCatchUpPerJob fires; the remaining overdue jobs are re-armed
// with a fresh next_run computed from now so they resume normal
// scheduling. Fires synchronously before the loop goroutine starts
// so there is no concurrency with normal scheduling.
func (s *Scheduler) catchUp(ctx context.Context) {
	now := s.clock.Now()
	s.mu.Lock()
	var overdue []store.ScheduledJob
	remaining := (*s.jobs)[:0]
	for _, pj := range *s.jobs {
		if pj.nextRun.Before(now) {
			overdue = append(overdue, pj.job)
		} else {
			remaining = append(remaining, pj)
		}
	}
	*s.jobs = remaining
	s.mu.Unlock()

	if len(overdue) == 0 {
		return
	}

	fired := 0
	for i, j := range overdue {
		if ctx.Err() != nil {
			overdue = overdue[i:]
			break
		}
		s.fire(ctx, j)
		fired++
		if fired >= maxCatchUpPerJob {
			overdue = overdue[i+1:]
			break
		}
		overdue = nil
	}

	for _, j := range overdue {
		if next, nerr := NextRun(j.Kind, j.Spec, now); nerr == nil {
			j.NextRunAt = ptrTime(next)
			if err := s.store.UpdateScheduledJob(ctx, &j); err != nil {
				slog.Warn("scheduler: catch-up re-arm failed", "id", j.ID, "err", err)
				continue
			}
			s.mu.Lock()
			s.jobs.upsertByID(j, *j.NextRunAt)
			s.mu.Unlock()
		}
	}
	s.kick()

	if fired > 0 {
		slog.Info("scheduler: catch-up pass completed", "overdue", len(overdue)+fired, "fired", fired)
	}
}

func truncateErr(s string) string {
	const max = 1024
	if len(s) <= max {
		return s
	}
	return s[:max]
}
