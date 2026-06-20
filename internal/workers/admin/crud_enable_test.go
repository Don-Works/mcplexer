package admin_test

import (
	"context"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/workers/admin"
	"github.com/don-works/mcplexer/internal/workers/runner"
)

func TestServicePauseCancelsRunningRuns(t *testing.T) {
	svc, db, wsID, scopeID := newTestService(t)
	ctx := context.Background()
	w, err := svc.Create(ctx, baseCreate(wsID, scopeID))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	run := &store.WorkerRun{
		WorkerID:  w.ID,
		Status:    runner.StatusRunning,
		StartedAt: time.Now().UTC().Add(-time.Minute),
	}
	if err := db.CreateWorkerRun(ctx, run); err != nil {
		t.Fatalf("seed run: %v", err)
	}

	paused, err := svc.Pause(ctx, w.ID)
	if err != nil {
		t.Fatalf("pause: %v", err)
	}
	if paused.Enabled {
		t.Fatal("worker still enabled after pause")
	}
	got, err := db.GetWorkerRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if got.Status != runner.StatusCancelled {
		t.Fatalf("run status = %q, want cancelled", got.Status)
	}
	if got.Error != "cancelled by operator: worker disabled" {
		t.Fatalf("cancel reason = %q", got.Error)
	}
}

func TestServicePauseSignalsLiveRunner(t *testing.T) {
	svc, db, wsID, scopeID := newTestService(t)
	ctx := context.Background()
	w, err := svc.Create(ctx, baseCreate(wsID, scopeID))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	stub := &stubRunner{cancelReturn: true}
	svc.SetRunnerForTest(stub)
	run := &store.WorkerRun{
		WorkerID:  w.ID,
		Status:    runner.StatusRunning,
		StartedAt: time.Now().UTC().Add(-time.Minute),
	}
	if err := db.CreateWorkerRun(ctx, run); err != nil {
		t.Fatalf("seed run: %v", err)
	}

	if _, err := svc.Pause(ctx, w.ID); err != nil {
		t.Fatalf("pause: %v", err)
	}
	if len(stub.cancelCalls) != 1 || stub.cancelCalls[0][0] != run.ID {
		t.Fatalf("runner.Cancel calls = %v, want one for %s", stub.cancelCalls, run.ID)
	}
	if stub.cancelCalls[0][1] != "cancelled by operator: worker disabled" {
		t.Fatalf("runner.Cancel reason = %q", stub.cancelCalls[0][1])
	}
	got, err := db.GetWorkerRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if got.Status != runner.StatusRunning {
		t.Fatalf("live-run row status = %q, want running until runner finalizes", got.Status)
	}
}

func TestServiceUpdateEnabledFalseCancelsRunningRuns(t *testing.T) {
	svc, db, wsID, scopeID := newTestService(t)
	ctx := context.Background()
	w, err := svc.Create(ctx, baseCreate(wsID, scopeID))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	run := &store.WorkerRun{
		WorkerID:  w.ID,
		Status:    runner.StatusRunning,
		StartedAt: time.Now().UTC().Add(-time.Minute),
	}
	if err := db.CreateWorkerRun(ctx, run); err != nil {
		t.Fatalf("seed run: %v", err)
	}

	enabled := false
	updated, err := svc.Update(ctx, admin.UpdateInput{ID: w.ID, Enabled: &enabled})
	if err != nil {
		t.Fatalf("update enabled=false: %v", err)
	}
	if updated.Enabled {
		t.Fatal("worker still enabled after update")
	}
	got, err := db.GetWorkerRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if got.Status != runner.StatusCancelled {
		t.Fatalf("run status = %q, want cancelled", got.Status)
	}
}
