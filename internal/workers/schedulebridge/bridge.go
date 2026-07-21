// Package schedulebridge wires the workers/admin Service to the
// scheduler's ScheduledJob catalog. Every enabled Worker is mirrored as
// a kind="worker" ScheduledJob row so the scheduler heap fires it on
// the configured ScheduleSpec; the row is removed (or its enabled flag
// flipped) when the Worker is paused or deleted.
//
// This package exists separately from internal/workers/admin so the
// admin package can stay scheduler-agnostic and tests can swap in fake
// bridges without spinning up the real heap.
package schedulebridge

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/don-works/mcplexer/internal/scheduler"
	"github.com/don-works/mcplexer/internal/store"
)

// SchedulerKicker is the optional handle the bridge calls after
// mutating scheduled_jobs so the running scheduler reloads its heap.
// Implementations may be nil — the bridge then writes the row and
// relies on the next scheduler.Reload to pick it up.
type SchedulerKicker interface {
	Reload(ctx context.Context) error
}

// Bridge is the schedulebridge.ScheduleBridge implementation. Construct
// via New. Safe for concurrent use; underlying store + scheduler calls
// already serialize at their own layer.
type Bridge struct {
	jobs    store.ScheduledJobStore
	kicker  SchedulerKicker
	nowFunc func() time.Time
	nextRun func(kind, spec string, after time.Time) (time.Time, error)
}

// New constructs a Bridge.
//
//   - jobs is required.
//   - kicker is optional (nil means "don't proactively reload").
//   - in production both nowFunc and nextRun are nil so the bridge
//     falls back to time.Now().UTC() and scheduler.NextRun. Tests
//     inject deterministic clocks.
func New(jobs store.ScheduledJobStore, kicker SchedulerKicker) *Bridge {
	return &Bridge{
		jobs:    jobs,
		kicker:  kicker,
		nowFunc: func() time.Time { return time.Now().UTC() },
		nextRun: scheduler.NextRun,
	}
}

// EnsureForWorker creates a kind="worker" scheduled_jobs row for w when
// one doesn't already exist, refreshes spec/enabled/next_run_at on the
// existing row when the worker config has drifted, or deletes the row
// when w is disabled or archived. Idempotent.
//
// When ScheduleSpec is the manual sentinel (scheduler.SpecManual), the
// worker has no scheduler-driven firing — only mesh triggers and
// RunNow can dispatch it. We skip row creation entirely; an existing
// row from a prior non-manual spec is deleted so the heap stops
// picking it up.
func (b *Bridge) EnsureForWorker(ctx context.Context, w *store.Worker) error {
	if w == nil {
		return errors.New("schedulebridge: nil worker")
	}
	if b == nil || b.jobs == nil {
		return errors.New("schedulebridge: no store wired")
	}
	existing, err := b.findJobForWorker(ctx, w.ID)
	if err != nil {
		return err
	}
	if !w.Enabled || w.ArchivedAt != nil {
		if existing == nil {
			return nil
		}
		if err := b.jobs.DeleteScheduledJob(ctx, existing.ID); err != nil {
			return fmt.Errorf("delete scheduled job (disabled/archived): %w", err)
		}
		b.kick(ctx)
		return nil
	}
	if scheduler.IsManualSpec(w.ScheduleSpec) {
		if existing == nil {
			return nil
		}
		if err := b.jobs.DeleteScheduledJob(ctx, existing.ID); err != nil {
			return fmt.Errorf("delete scheduled job (manual): %w", err)
		}
		b.kick(ctx)
		return nil
	}
	if existing == nil {
		return b.createJobForWorker(ctx, w)
	}
	return b.updateJobForWorker(ctx, existing, w)
}

// RemoveForWorker deletes the kind="worker" scheduled_jobs row for the
// given worker id. Missing rows are not an error — the bridge is
// designed to be safely re-invoked.
func (b *Bridge) RemoveForWorker(ctx context.Context, workerID string) error {
	if b == nil || b.jobs == nil {
		return errors.New("schedulebridge: no store wired")
	}
	existing, err := b.findJobForWorker(ctx, workerID)
	if err != nil {
		return err
	}
	if existing == nil {
		return nil
	}
	if err := b.jobs.DeleteScheduledJob(ctx, existing.ID); err != nil {
		return err
	}
	b.kick(ctx)
	return nil
}

// findJobForWorker walks the scheduled_jobs catalog looking for the
// kind="worker" row tied to workerID. Returns (nil, nil) when none is
// found.
func (b *Bridge) findJobForWorker(
	ctx context.Context, workerID string,
) (*store.ScheduledJob, error) {
	rows, err := b.jobs.ListScheduledJobs(ctx)
	if err != nil {
		return nil, fmt.Errorf("list scheduled jobs: %w", err)
	}
	for i := range rows {
		j := rows[i]
		if j.Kind == scheduler.KindWorker && j.WorkerID == workerID {
			return &j, nil
		}
	}
	return nil, nil
}

// createJobForWorker inserts a fresh row for w. NextRunAt is computed
// from the worker's ScheduleSpec; an unparseable spec is logged and the
// row is created with next_run_at=NULL (scheduler heap skips it until
// the operator fixes the spec).
func (b *Bridge) createJobForWorker(
	ctx context.Context, w *store.Worker,
) error {
	job := store.ScheduledJob{
		ID:       "sj-" + ulid.Make().String(),
		Name:     "worker:" + w.Name,
		Kind:     scheduler.KindWorker,
		Spec:     w.ScheduleSpec,
		Command:  "", // unused for kind=worker
		ArgsJSON: "[]",
		EnvJSON:  "{}",
		Surface:  "worker",
		Enabled:  w.Enabled,
		WorkerID: w.ID,
	}
	if next, nerr := b.computeNextRun(w); nerr == nil {
		t := next
		job.NextRunAt = &t
	}
	if err := b.jobs.CreateScheduledJob(ctx, &job); err != nil {
		return fmt.Errorf("create scheduled job: %w", err)
	}
	b.kick(ctx)
	return nil
}

// updateJobForWorker re-syncs an existing row when spec/enabled changed.
// Always recomputes NextRunAt so a row that lost its next-run during a
// failed parse can heal once the operator fixes the spec.
func (b *Bridge) updateJobForWorker(
	ctx context.Context, existing *store.ScheduledJob, w *store.Worker,
) error {
	changed := false
	if existing.Spec != w.ScheduleSpec {
		existing.Spec = w.ScheduleSpec
		changed = true
	}
	if existing.Enabled != w.Enabled {
		existing.Enabled = w.Enabled
		changed = true
	}
	if existing.Surface != "worker" {
		existing.Surface = "worker"
		changed = true
	}
	if next, nerr := b.computeNextRun(w); nerr == nil {
		t := next
		if existing.NextRunAt == nil || !existing.NextRunAt.Equal(t) {
			existing.NextRunAt = &t
			changed = true
		}
	} else {
		if existing.NextRunAt != nil {
			existing.NextRunAt = nil
			changed = true
		}
	}
	if !changed {
		return nil
	}
	if err := b.jobs.UpdateScheduledJob(ctx, existing); err != nil {
		return fmt.Errorf("update scheduled job: %w", err)
	}
	b.kick(ctx)
	return nil
}

// computeNextRun resolves the worker's next-run timestamp. Logs +
// returns the error to the caller so it can decide whether to null out
// NextRunAt; we never crash the bridge on a bad spec.
func (b *Bridge) computeNextRun(w *store.Worker) (time.Time, error) {
	next, err := b.nextRun(scheduler.KindCron, w.ScheduleSpec, b.nowFunc())
	if err == nil {
		return next, nil
	}
	next, ierr := b.nextRun(scheduler.KindInterval, w.ScheduleSpec, b.nowFunc())
	if ierr == nil {
		return next, nil
	}
	slog.Warn("schedulebridge: worker schedule_spec unparseable",
		"worker_id", w.ID, "spec", w.ScheduleSpec,
		"cron_err", err, "interval_err", ierr)
	return time.Time{}, err
}

// kick tells the scheduler (if wired) to reload its heap so the next
// fire timestamp is honoured without waiting for the polling tick.
// Failures are logged — the row is already on disk and Reload will
// catch up on next call.
func (b *Bridge) kick(ctx context.Context) {
	if b.kicker == nil {
		return
	}
	if err := b.kicker.Reload(ctx); err != nil {
		slog.Warn("schedulebridge: scheduler reload failed", "error", err)
	}
}
