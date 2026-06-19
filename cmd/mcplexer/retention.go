package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/don-works/mcplexer/internal/config"
	"github.com/don-works/mcplexer/internal/scheduler"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

// auditPruneJobID is the stable primary key for the built-in retention
// ScheduledJob row. Stable so seeding is idempotent across daemon
// restarts.
const auditPruneJobID = "builtin-audit-prune"

// auditPruneCron is the default fire time: 03:00 UTC daily. Picked so
// the deletes land during the typical low-traffic window even on
// always-on hosts.
const auditPruneCron = "0 3 * * *"

// storePruneExecutor adapts the store.Store retention methods to the
// scheduler.PruneExecutor interface.
type storePruneExecutor struct {
	st store.Store
	// delegationSweeper is the workers admin service's retention sweep
	// (auto-disable + archive idle delegation workers). Optional —
	// wired after the admin service is constructed via
	// SetDelegationSweeper; nil skips the sweep.
	delegationSweeper delegationSweeper
}

// delegationSweeper is the slice of *workersadmin.Service the retention
// tick needs.
type delegationSweeper interface {
	SweepDelegationRetention(ctx context.Context, retention time.Duration) (int, error)
}

func newStorePruneExecutor(s store.Store) *storePruneExecutor {
	return &storePruneExecutor{st: s}
}

// SetDelegationSweeper wires the workers admin service so the nightly
// retention tick also archives idle delegation workers.
func (e *storePruneExecutor) SetDelegationSweeper(s delegationSweeper) {
	e.delegationSweeper = s
}

// Prune runs the two retention deletes and returns the row counts.
// Both deletes happen in their own statement (SQLite serialises
// writes; wrapping them in a Tx would just hold the write lock
// longer with no integrity gain — neither delete depends on the
// other's outcome).
func (e *storePruneExecutor) Prune(
	ctx context.Context, p scheduler.PrunePolicy, now time.Time,
) (int64, int64, error) {
	// Delegation retention sweep first — independent of the audit /
	// worker-run policies (delegation workers are clutter regardless of
	// how long the operator keeps audit rows). Failures are logged but
	// never abort the deletes below.
	if e.delegationSweeper != nil {
		if _, err := e.delegationSweeper.SweepDelegationRetention(ctx, 0); err != nil {
			slog.Warn("retention: delegation sweep failed", "error", err)
		}
	}
	var auditDeleted int64
	if p.AuditRetentionDays > 0 {
		cutoff := now.Add(-time.Duration(p.AuditRetentionDays) * 24 * time.Hour)
		n, err := e.st.PruneAuditRecords(ctx, cutoff)
		if err != nil {
			return 0, 0, fmt.Errorf("prune audit_records: %w", err)
		}
		auditDeleted = n
	}

	// Worker_runs always honours the per-worker floor. The age cap is
	// optional — when WorkerRunCapDays == 0 we pass a far-future cutoff
	// so only the floor's row_number filter kicks in, which by
	// construction is empty (rank > N filters out nothing once N is
	// large enough). To preserve the "keep N newest" semantic the
	// caller still gets, we pass a unix-zero cutoff so nothing gets
	// deleted by age — only the floor would, but the floor only fires
	// for rows older than cutoff. Effectively: 0 days = disabled.
	if p.WorkerRunKeepPerWorker > 0 && p.WorkerRunCapDays > 0 {
		runCutoff := now.Add(-time.Duration(p.WorkerRunCapDays) * 24 * time.Hour)
		n, err := e.st.PruneWorkerRuns(ctx, p.WorkerRunKeepPerWorker, runCutoff)
		if err != nil {
			return auditDeleted, 0, fmt.Errorf("prune worker_runs: %w", err)
		}
		return auditDeleted, n, nil
	}
	return auditDeleted, 0, nil
}

// retentionPolicyFromConfig converts the YAML retention block (or its
// default fallback) into the scheduler.PrunePolicy shape.
func retentionPolicyFromConfig(c config.AuditRetentionConfig) scheduler.PrunePolicy {
	return scheduler.PrunePolicy{
		AuditRetentionDays:     c.AuditDays,
		WorkerRunKeepPerWorker: c.WorkerRunKeepPerWorker,
		WorkerRunCapDays:       c.WorkerRunCapDays,
	}
}

// loadRetentionConfig re-parses the YAML config file purely to extract
// the retention block. The full config is also applied in cmdServe; we
// read it again here because the scheduler is constructed inside
// buildServerDeps which doesn't have the parsed config in scope.
// Returns DefaultAuditRetention when the file is missing, unparseable,
// or doesn't include a retention block.
func loadRetentionConfig(path string) config.AuditRetentionConfig {
	if path == "" {
		return config.DefaultAuditRetention()
	}
	if _, err := os.Stat(path); err != nil {
		return config.DefaultAuditRetention()
	}
	fileCfg, err := config.LoadFile(path)
	if err != nil {
		slog.Warn("retention: parse config for retention block", "error", err)
		return config.DefaultAuditRetention()
	}
	return fileCfg.ResolveRetention()
}

// ensureAuditPruneJob seeds the built-in retention ScheduledJob row if
// it isn't already present. Idempotent — a no-op once the row exists
// (the row's spec is read each fire, so live-edits via mcplexer
// admin tools still take effect). Returns nil on success, including
// the "already exists" path.
func ensureAuditPruneJob(ctx context.Context, db *sqlite.DB) error {
	if _, err := db.GetScheduledJob(ctx, auditPruneJobID); err == nil {
		return nil
	} else if err != store.ErrNotFound {
		return fmt.Errorf("lookup audit_prune job: %w", err)
	}
	next, err := scheduler.NextRun(scheduler.KindAuditPrune, auditPruneCron, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("compute next run: %w", err)
	}
	job := &store.ScheduledJob{
		ID:        auditPruneJobID,
		Name:      "audit_prune",
		Kind:      scheduler.KindAuditPrune,
		Spec:      auditPruneCron,
		Surface:   "schedule",
		Enabled:   true,
		NextRunAt: &next,
	}
	if err := db.CreateScheduledJob(ctx, job); err != nil {
		return fmt.Errorf("seed audit_prune job: %w", err)
	}
	slog.Info("seeded built-in audit_prune scheduled job",
		"id", auditPruneJobID, "spec", auditPruneCron, "next_run_at", next)
	return nil
}
