package sqlite_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// insertRun is a thin helper that bypasses Create's default StartedAt
// so the test controls the row's age exactly.
func insertRun(t *testing.T, db storeWriter, ctx context.Context, workerID string, startedAt time.Time) string {
	t.Helper()
	run := &store.WorkerRun{
		WorkerID:  workerID,
		StartedAt: startedAt,
		Status:    "success",
	}
	if err := db.CreateWorkerRun(ctx, run); err != nil {
		t.Fatalf("create worker_run: %v", err)
	}
	return run.ID
}

// storeWriter is the narrow surface insertRun needs — keeps the
// helper usable from any test file in this package.
type storeWriter interface {
	CreateWorkerRun(ctx context.Context, r *store.WorkerRun) error
}

// TestPruneWorkerRunsKeepPerWorker is the core invariant: a low-volume
// worker whose runs are ALL older than the cutoff still retains
// keepPerWorker rows. The 5-old-runs-keep-3 case from the design
// brief lives here.
func TestPruneWorkerRunsKeepPerWorker(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	wsID, scopeID := seedWorkspaceAndScope(t, db, ctx)
	w := newWorker(wsID, scopeID, "low-volume")
	if err := db.CreateWorker(ctx, w); err != nil {
		t.Fatalf("create worker: %v", err)
	}

	// Five runs, each spaced 10 days apart, all older than the cutoff.
	now := time.Now().UTC()
	cutoff := now.Add(-30 * 24 * time.Hour)
	for i := 0; i < 5; i++ {
		insertRun(t, db, ctx, w.ID,
			now.Add(-(time.Duration(60+i*10))*24*time.Hour))
	}

	deleted, err := db.PruneWorkerRuns(ctx, 3, cutoff)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if deleted != 2 {
		t.Fatalf("deleted = %d, want 2", deleted)
	}

	rows, err := db.ListWorkerRuns(ctx, w.ID, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("retained = %d, want 3", len(rows))
	}
}

// TestPruneWorkerRunsTableDriven covers the corners that the unit
// shape needs to defend.
func TestPruneWorkerRunsTableDriven(t *testing.T) {
	now := time.Now().UTC()
	cutoff := now.Add(-30 * 24 * time.Hour)

	tests := []struct {
		name          string
		runsAgesDays  []int // ages relative to `now` in days (positive)
		keepPerWorker int
		cutoff        time.Time
		wantDeleted   int64
		wantRetained  int
	}{
		{
			name:          "empty table",
			runsAgesDays:  nil,
			keepPerWorker: 10,
			cutoff:        cutoff,
			wantDeleted:   0,
			wantRetained:  0,
		},
		{
			name:          "all newer than cutoff — floor doesn't matter",
			runsAgesDays:  []int{1, 5, 10, 15},
			keepPerWorker: 1,
			cutoff:        cutoff,
			wantDeleted:   0,
			wantRetained:  4,
		},
		{
			name:          "all older — floor keeps N newest",
			runsAgesDays:  []int{40, 60, 80, 100, 120},
			keepPerWorker: 3,
			cutoff:        cutoff,
			wantDeleted:   2,
			wantRetained:  3,
		},
		{
			name:          "mixed — newer rows count toward the floor",
			runsAgesDays:  []int{1, 5, 40, 60, 80, 100},
			keepPerWorker: 3,
			cutoff:        cutoff,
			wantDeleted:   3, // top-3 are 1d, 5d, 40d; 60d/80d/100d delete
			wantRetained:  3,
		},
		{
			name:          "floor larger than total run count — nothing deletes",
			runsAgesDays:  []int{40, 60, 80},
			keepPerWorker: 10,
			cutoff:        cutoff,
			wantDeleted:   0,
			wantRetained:  3,
		},
		{
			name:          "zero floor — pure age cap",
			runsAgesDays:  []int{1, 5, 40, 100},
			keepPerWorker: 0,
			cutoff:        cutoff,
			wantDeleted:   2, // 40d + 100d both older than 30d
			wantRetained:  2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newTestDB(t)
			ctx := context.Background()
			wsID, scopeID := seedWorkspaceAndScope(t, db, ctx)
			w := newWorker(wsID, scopeID, "tt")
			if err := db.CreateWorker(ctx, w); err != nil {
				t.Fatalf("create worker: %v", err)
			}
			for _, age := range tt.runsAgesDays {
				insertRun(t, db, ctx, w.ID,
					now.Add(-time.Duration(age)*24*time.Hour))
			}

			deleted, err := db.PruneWorkerRuns(ctx, tt.keepPerWorker, tt.cutoff)
			if err != nil {
				t.Fatalf("prune: %v", err)
			}
			if deleted != tt.wantDeleted {
				t.Fatalf("deleted = %d, want %d", deleted, tt.wantDeleted)
			}
			rows, err := db.ListWorkerRuns(ctx, w.ID, 0)
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			if len(rows) != tt.wantRetained {
				t.Fatalf("retained = %d, want %d", len(rows), tt.wantRetained)
			}
		})
	}
}

// TestPruneWorkerRunsPerWorkerIsolated confirms the per-worker floor
// is computed independently for each worker_id — pruning worker A
// doesn't dip into worker B's retention budget.
func TestPruneWorkerRunsPerWorkerIsolated(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	wsID, scopeID := seedWorkspaceAndScope(t, db, ctx)

	now := time.Now().UTC()
	cutoff := now.Add(-30 * 24 * time.Hour)

	workers := []*store.Worker{}
	for i := 0; i < 2; i++ {
		w := newWorker(wsID, scopeID, fmt.Sprintf("w%d", i))
		if err := db.CreateWorker(ctx, w); err != nil {
			t.Fatalf("create worker: %v", err)
		}
		workers = append(workers, w)
	}

	// Worker 0 has 4 old runs, Worker 1 has 6 old runs. With
	// keepPerWorker=2, each should retain exactly 2.
	for _, age := range []int{40, 60, 80, 100} {
		insertRun(t, db, ctx, workers[0].ID, now.Add(-time.Duration(age)*24*time.Hour))
	}
	for _, age := range []int{40, 50, 60, 70, 80, 90} {
		insertRun(t, db, ctx, workers[1].ID, now.Add(-time.Duration(age)*24*time.Hour))
	}

	deleted, err := db.PruneWorkerRuns(ctx, 2, cutoff)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if deleted != (4-2)+(6-2) {
		t.Fatalf("deleted = %d, want 6", deleted)
	}

	for i, w := range workers {
		rows, err := db.ListWorkerRuns(ctx, w.ID, 0)
		if err != nil {
			t.Fatalf("list %d: %v", i, err)
		}
		if len(rows) != 2 {
			t.Fatalf("worker %d retained %d, want 2", i, len(rows))
		}
	}
}
