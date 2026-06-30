package main

import (
	"context"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/scheduler"
	"github.com/don-works/mcplexer/internal/store"
)

func findJob(t *testing.T, jobs []store.ScheduledJob, id string) *store.ScheduledJob {
	t.Helper()
	for i := range jobs {
		if jobs[i].ID == id {
			return &jobs[i]
		}
	}
	return nil
}

// ensureBrwReconcileJobs seeds an interval-fallback job and a file_watch job,
// both carrying the sentinel command so dispatch routes them to the executor.
func TestEnsureBrwReconcileJobs_SeedsBoth(t *testing.T) {
	db := newRetentionTestDB(t)
	ctx := context.Background()

	if err := ensureBrwReconcileJobs(ctx, db, 5*time.Minute, "/tmp/browser-profiles.json"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	jobs, err := db.ListScheduledJobs(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}

	iv := findJob(t, jobs, brwReconcileIntervalJobID)
	if iv == nil {
		t.Fatalf("interval job not seeded")
	}
	if iv.Kind != scheduler.KindInterval {
		t.Errorf("interval kind = %q, want %q", iv.Kind, scheduler.KindInterval)
	}
	if iv.Spec != "5m0s" {
		t.Errorf("interval spec = %q, want 5m0s", iv.Spec)
	}
	if iv.Command != scheduler.BrwReconcileCommand {
		t.Errorf("interval command = %q, want sentinel", iv.Command)
	}
	if iv.NextRunAt == nil {
		t.Errorf("interval job must have NextRunAt so the heap arms it")
	}
	if !iv.Enabled {
		t.Errorf("interval job should be enabled")
	}

	w := findJob(t, jobs, brwReconcileWatchJobID)
	if w == nil {
		t.Fatalf("watch job not seeded")
	}
	if w.Kind != scheduler.KindFileWatch {
		t.Errorf("watch kind = %q, want %q", w.Kind, scheduler.KindFileWatch)
	}
	if w.Spec != "/tmp/browser-profiles.json" {
		t.Errorf("watch spec = %q", w.Spec)
	}
	if w.Command != scheduler.BrwReconcileCommand {
		t.Errorf("watch command = %q, want sentinel", w.Command)
	}
	if w.NextRunAt != nil {
		t.Errorf("file_watch job must NOT have NextRunAt (event-driven)")
	}
}

// Re-seeding must not duplicate rows and must refresh the interval spec when
// MCPLEXER_BRW_INTERVAL changes across a restart.
func TestEnsureBrwReconcileJobs_IdempotentAndUpdates(t *testing.T) {
	db := newRetentionTestDB(t)
	ctx := context.Background()

	if err := ensureBrwReconcileJobs(ctx, db, 5*time.Minute, "/tmp/p.json"); err != nil {
		t.Fatalf("first seed: %v", err)
	}
	if err := ensureBrwReconcileJobs(ctx, db, 10*time.Minute, "/tmp/p.json"); err != nil {
		t.Fatalf("second seed: %v", err)
	}

	jobs, _ := db.ListScheduledJobs(ctx)
	intervalHits, watchHits := 0, 0
	for _, j := range jobs {
		switch j.ID {
		case brwReconcileIntervalJobID:
			intervalHits++
		case brwReconcileWatchJobID:
			watchHits++
		}
	}
	if intervalHits != 1 {
		t.Errorf("interval row count = %d, want 1", intervalHits)
	}
	if watchHits != 1 {
		t.Errorf("watch row count = %d, want 1", watchHits)
	}

	iv := findJob(t, jobs, brwReconcileIntervalJobID)
	if iv == nil || iv.Spec != "10m0s" {
		t.Errorf("interval spec not refreshed: %+v", iv)
	}
}

// An empty policy path falls back to the canonical brw default, so a watch
// job is still seeded (file_watch trigger works out of the box when on).
func TestEnsureBrwReconcileJobs_EmptyPolicyFallsBack(t *testing.T) {
	db := newRetentionTestDB(t)
	ctx := context.Background()

	if err := ensureBrwReconcileJobs(ctx, db, 0, ""); err != nil {
		t.Fatalf("seed: %v", err)
	}
	jobs, _ := db.ListScheduledJobs(ctx)

	iv := findJob(t, jobs, brwReconcileIntervalJobID)
	if iv == nil || iv.Spec != "5m0s" {
		t.Errorf("zero interval should default to 5m0s, got %+v", iv)
	}
	w := findJob(t, jobs, brwReconcileWatchJobID)
	if w == nil {
		t.Fatalf("watch job should be seeded with the default policy path")
	}
	if w.Spec == "" {
		t.Errorf("watch spec should be the default policy path, got empty")
	}
}
