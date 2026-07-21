package sqlite_test

import (
	"context"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

func TestWorkerCostAggregate(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	wsID, scopeID := seedWorkspaceAndScope(t, db, ctx)

	// Two workers. Worker A has costs in multiple days; worker B has none
	// (verifying the zero-run inclusion behaviour).
	wa := newWorker(wsID, scopeID, "alpha")
	wb := newWorker(wsID, scopeID, "beta")
	if err := db.CreateWorker(ctx, wa); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateWorker(ctx, wb); err != nil {
		t.Fatal(err)
	}

	// Anchor "now" at the 21st mid-day so we can place runs on the 19th
	// (in-window) and the 20th (in-window) without skew worries.
	now := time.Date(2026, 5, 21, 12, 0, 0, 0, time.UTC)
	mustInsertRun(t, db, ctx, wa.ID, now.AddDate(0, 0, -2), 0.10, "success")
	mustInsertRun(t, db, ctx, wa.ID, now.AddDate(0, 0, -2), 0.05, "success")
	mustInsertRun(t, db, ctx, wa.ID, now.AddDate(0, 0, -1), 0.20, "failure")
	// One run BEFORE the window (should not contribute to the daily
	// series — days=5 → window starts 2026-05-17, this row is 2026-05-15).
	mustInsertRun(t, db, ctx, wa.ID, now.AddDate(0, 0, -6), 0.99, "success")

	got, err := db.WorkerCostAggregate(ctx, wsID, 5, now)
	if err != nil {
		t.Fatalf("WorkerCostAggregate: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("rows = %d, want 2", len(got))
	}
	// Aggregates ordered by name ASC → alpha, beta.
	alpha := got[0]
	if alpha.WorkerName != "alpha" {
		t.Fatalf("first row = %q, want alpha", alpha.WorkerName)
	}
	if len(alpha.DailyCosts) != 5 {
		t.Fatalf("daily series len = %d, want 5", len(alpha.DailyCosts))
	}
	// 5-day window with anchor "now" 5/21 → days are 5/17..5/21 inclusive.
	if alpha.DailyCosts[0].Date != "2026-05-17" {
		t.Fatalf("first day = %q, want 2026-05-17", alpha.DailyCosts[0].Date)
	}
	if alpha.DailyCosts[4].Date != "2026-05-21" {
		t.Fatalf("last day = %q, want 2026-05-21", alpha.DailyCosts[4].Date)
	}
	// Day 5/19 → 0.10 + 0.05 = 0.15. Day 5/20 → 0.20. Out-of-window 0.99 ignored.
	wantByDay := map[string]float64{
		"2026-05-17": 0,
		"2026-05-18": 0,
		"2026-05-19": 0.15,
		"2026-05-20": 0.20,
		"2026-05-21": 0,
	}
	for _, p := range alpha.DailyCosts {
		if !floatNear(p.CostUSD, wantByDay[p.Date]) {
			t.Errorf("day %s cost = %v, want %v", p.Date, p.CostUSD, wantByDay[p.Date])
		}
	}
	// MTD = sum since 2026-05-01 = 0.10 + 0.05 + 0.20 + 0.99 = 1.34.
	if !floatNear(alpha.MonthToDateUSD, 1.34) {
		t.Fatalf("MTD = %v, want ~1.34", alpha.MonthToDateUSD)
	}
	// Run count over window (5 days) = 3 (the 0.99 row is outside).
	if alpha.RunCount30D != 3 {
		t.Fatalf("RunCount30D = %d, want 3", alpha.RunCount30D)
	}

	// Beta has no runs but should still appear.
	beta := got[1]
	if beta.WorkerName != "beta" {
		t.Fatalf("second row = %q, want beta", beta.WorkerName)
	}
	if beta.MonthToDateUSD != 0 {
		t.Fatalf("beta MTD = %v, want 0", beta.MonthToDateUSD)
	}
	if beta.RunCount30D != 0 {
		t.Fatalf("beta runs = %d, want 0", beta.RunCount30D)
	}
	if len(beta.DailyCosts) != 5 {
		t.Fatalf("beta daily series len = %d", len(beta.DailyCosts))
	}
}

func TestWorkerCostAggregate_EmptyWorkspace(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	wsID, _ := seedWorkspaceAndScope(t, db, ctx)

	got, err := db.WorkerCostAggregate(ctx, wsID, 30, time.Now())
	if err != nil {
		t.Fatalf("WorkerCostAggregate: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("rows = %d, want 0", len(got))
	}
}

func TestWorkerCostAggregate_AllWorkspaces(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	wsID, scopeID := seedWorkspaceAndScope(t, db, ctx)
	w := newWorker(wsID, scopeID, "anywhere")
	if err := db.CreateWorker(ctx, w); err != nil {
		t.Fatal(err)
	}
	// Empty workspaceID = every workspace.
	got, err := db.WorkerCostAggregate(ctx, "", 30, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("rows = %d, want 1", len(got))
	}
}

// mustInsertRun is a tiny helper that creates + finalizes a WorkerRun
// in one step so the test body stays readable. costUSD is committed
// via the finalize path (matching the runner's behaviour).
func mustInsertRun(
	t *testing.T, db interface {
		CreateWorkerRun(context.Context, *store.WorkerRun) error
		UpdateWorkerRunStatus(context.Context, string, store.WorkerRunFinalize) error
	},
	ctx context.Context, workerID string, startedAt time.Time, costUSD float64, status string,
) {
	t.Helper()
	run := &store.WorkerRun{
		WorkerID:      workerID,
		StartedAt:     startedAt,
		Status:        "running",
		ModelProvider: "anthropic",
		ModelID:       "claude-opus-4-7",
	}
	if err := db.CreateWorkerRun(ctx, run); err != nil {
		t.Fatalf("create run: %v", err)
	}
	fin := store.WorkerRunFinalize{
		Status:     status,
		FinishedAt: startedAt.Add(500 * time.Millisecond),
		CostUSD:    costUSD,
	}
	if err := db.UpdateWorkerRunStatus(ctx, run.ID, fin); err != nil {
		t.Fatalf("finalize run: %v", err)
	}
}

func floatNear(a, b float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < 1e-9
}
