package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/don-works/mcplexer/internal/config"
	"github.com/don-works/mcplexer/internal/downstream"
	"github.com/don-works/mcplexer/internal/routing"
	"github.com/don-works/mcplexer/internal/scheduler"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

// Stable primary keys for the two built-in brw auto-discovery jobs. Stable so
// seeding is idempotent across daemon restarts.
const (
	brwReconcileIntervalJobID = "builtin-brw-reconcile-interval"
	brwReconcileWatchJobID    = "builtin-brw-reconcile-watch"
)

// brwReconcileExecutor implements scheduler.BrwReconcileExecutor. It owns the
// dependencies the in-process reconcile needs but the internal/config layer
// must not (routing engine + downstream manager), keeping config's deps
// one-way. A single reconcile: load the live brwd roster, apply it to the
// store via config.ReconcileBrwProfiles, then make the change live by
// invalidating routes and reloading the changed downstream instances.
type brwReconcileExecutor struct {
	store      store.Store
	svc        *config.Service
	engine     *routing.Engine
	manager    *downstream.Manager
	workspaces []string
	brwctlPath string
	policyPath string
	prune      bool

	// mu serialises reconciles. The interval fire (heap goroutine) and the
	// file_watch fire (FileWatcher's debounce timer goroutine) can overlap;
	// the SyncBrwProfiles re-list-each-iteration logic is idempotent but a
	// create/create race would surface a duplicate-namespace error, so we
	// hold one reconcile at a time.
	mu sync.Mutex
}

type brwReconcileDeps struct {
	store      store.Store
	svc        *config.Service
	engine     *routing.Engine
	manager    *downstream.Manager
	workspaces []string
	brwctlPath string
	policyPath string
	prune      bool
}

func newBrwReconcileExecutor(d brwReconcileDeps) *brwReconcileExecutor {
	return &brwReconcileExecutor{
		store:      d.store,
		svc:        d.svc,
		engine:     d.engine,
		manager:    d.manager,
		workspaces: d.workspaces,
		brwctlPath: d.brwctlPath,
		policyPath: d.policyPath,
		prune:      d.prune,
	}
}

// Reconcile loads the roster, applies it, makes it live, and returns the
// counts. Errors are returned to the scheduler (which logs + surfaces a
// failure status) rather than crashing the daemon.
func (e *brwReconcileExecutor) Reconcile(ctx context.Context, _ time.Time) (scheduler.BrwReconcileResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	roster, err := config.LoadBrwRoster(ctx, e.brwctlPath)
	if err != nil {
		return scheduler.BrwReconcileResult{}, fmt.Errorf("load brw roster: %w", err)
	}

	plan, err := config.ReconcileBrwProfiles(ctx, e.svc, e.store, roster, config.SyncOptions{
		Workspaces: e.workspaces,
		PolicyPath: e.policyPath,
		Prune:      e.prune,
	})
	if err != nil {
		return scheduler.BrwReconcileResult{}, fmt.Errorf("reconcile brw profiles: %w", err)
	}

	e.applyLive(plan)

	res := scheduler.BrwReconcileResult{Daemons: len(roster)}
	for _, a := range plan.Actions {
		switch a.Action {
		case config.ActionCreated:
			res.Created++
		case config.ActionUpdated:
			res.Updated++
		case config.ActionAdopted:
			res.Adopted++
		case config.ActionPruned:
			res.Pruned++
		case config.ActionUnchanged:
			res.Unchanged++
		case config.ActionSkipped:
			res.Skipped++
		}
	}
	return res, nil
}

// applyLive drives the make-it-live side effects the config layer can't: a
// changed server set means routes must be re-resolved (InvalidateAllRoutes)
// and any running instances of the changed servers evicted so the next call
// lazy-starts from the new config. Created/updated/pruned all qualify —
// pruned so a deleted server's stale child process is torn down. Adopted /
// unchanged servers are intentionally left running.
func (e *brwReconcileExecutor) applyLive(plan config.SyncPlan) {
	var changedServers []string
	dirty := false
	for _, a := range plan.Actions {
		switch a.Action {
		case config.ActionCreated, config.ActionUpdated, config.ActionPruned:
			dirty = true
			if a.Kind == "server" && a.ID != "" {
				changedServers = append(changedServers, a.ID)
			}
		}
	}
	if !dirty {
		return
	}
	if e.engine != nil {
		e.engine.InvalidateAllRoutes()
	}
	if e.manager != nil {
		for _, id := range changedServers {
			e.manager.ReloadServerInstances(id)
		}
		e.manager.NotifyToolsChanged()
	}
}

// ensureBrwReconcileJobs upserts the two built-in brw auto-discovery
// ScheduledJob rows: an interval-fallback job (heap-driven) and, when a
// policy path is resolvable, a file_watch job. Both carry the
// scheduler.BrwReconcileCommand sentinel so dispatch routes them to the wired
// executor. Idempotent + env-responsive: an existing row's mutable fields
// (spec/command/enabled) are refreshed so changing MCPLEXER_BRW_INTERVAL /
// MCPLEXER_BRW_POLICY takes effect on restart.
//
// Must run BEFORE scheduler.Start (so its initial Reload sees the interval
// job) and BEFORE the FileWatcher starts (so its initial Reload sees the
// watch job) — both hold in serve.go's wiring order.
func ensureBrwReconcileJobs(ctx context.Context, db *sqlite.DB, interval time.Duration, policyPath string) error {
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	if err := upsertBrwReconcileJob(ctx, db, &store.ScheduledJob{
		ID:      brwReconcileIntervalJobID,
		Name:    "brw_reconcile_interval",
		Kind:    scheduler.KindInterval,
		Spec:    interval.String(),
		Command: scheduler.BrwReconcileCommand,
		Surface: "schedule",
		Enabled: true,
	}); err != nil {
		return fmt.Errorf("seed brw interval job: %w", err)
	}

	// Resolve the file to watch: explicit env wins, else the canonical brw
	// policy path. An empty result (neither set) means interval-only.
	watchPath := strings.TrimSpace(policyPath)
	if watchPath == "" {
		watchPath = config.DefaultBrwPolicyPath
	}
	if strings.TrimSpace(watchPath) == "" {
		return nil
	}
	if err := upsertBrwReconcileJob(ctx, db, &store.ScheduledJob{
		ID:      brwReconcileWatchJobID,
		Name:    "brw_reconcile_watch",
		Kind:    scheduler.KindFileWatch,
		Spec:    watchPath,
		Command: scheduler.BrwReconcileCommand,
		Surface: "schedule",
		Enabled: true,
	}); err != nil {
		return fmt.Errorf("seed brw watch job: %w", err)
	}
	return nil
}

// upsertBrwReconcileJob creates the row or refreshes its mutable fields. For
// interval-kind jobs it (re)stamps NextRunAt so the heap arms it; file_watch
// jobs keep NextRunAt nil (event-driven, fired by the FileWatcher).
func upsertBrwReconcileJob(ctx context.Context, db *sqlite.DB, want *store.ScheduledJob) error {
	if want.Kind == scheduler.KindInterval {
		next, err := scheduler.NextRun(want.Kind, want.Spec, time.Now().UTC())
		if err != nil {
			return fmt.Errorf("compute next run: %w", err)
		}
		want.NextRunAt = &next
	}

	existing, err := db.GetScheduledJob(ctx, want.ID)
	if err == store.ErrNotFound {
		if cerr := db.CreateScheduledJob(ctx, want); cerr != nil {
			return cerr
		}
		slog.Info("seeded brw auto-discovery job", "id", want.ID, "kind", want.Kind, "spec", want.Spec)
		return nil
	}
	if err != nil {
		return fmt.Errorf("lookup %s: %w", want.ID, err)
	}

	existing.Name = want.Name
	existing.Kind = want.Kind
	existing.Spec = want.Spec
	existing.Command = want.Command
	existing.Surface = want.Surface
	existing.Enabled = want.Enabled
	existing.NextRunAt = want.NextRunAt
	return db.UpdateScheduledJob(ctx, existing)
}
