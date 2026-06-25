package admin_test

import (
	"context"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/workers/admin"
	"github.com/don-works/mcplexer/internal/workers/runner"
)

func TestExtendDelegationBudgetUpdatesRunningWorkers(t *testing.T) {
	t.Setenv("MCPLEXER_ALLOW_OPENCODE_CLI", "1")
	svc, db, wsID, _ := newTestService(t)
	fr := &fakeRunner{runID: "run-from-dispatch"}
	svc.SetRunnerForTest(fr)
	ctx := context.Background()

	out, err := svc.Delegate(ctx, admin.DelegationInput{
		WorkspaceID:         wsID,
		Objective:           "exercise live budget extension",
		WorkerMode:          "review",
		ModelProvider:       "opencode_cli",
		ModelID:             "minimax/MiniMax-M3",
		MaxToolCalls:        20,
		MaxWallClockSeconds: 120,
	})
	if err != nil {
		t.Fatalf("delegate: %v", err)
	}
	workerID := out.Dispatches[0].WorkerID
	run := &store.WorkerRun{
		ID:            "run-live-budget",
		WorkerID:      workerID,
		WorkspaceID:   wsID,
		StartedAt:     time.Now().UTC(),
		Status:        runner.StatusRunning,
		ModelProvider: "opencode_cli",
		ModelID:       "minimax/MiniMax-M3",
	}
	if err := db.CreateWorkerRun(ctx, run); err != nil {
		t.Fatalf("seed run: %v", err)
	}

	got, err := svc.ExtendDelegationBudget(ctx, admin.DelegationBudgetInput{
		DelegationID:               out.DelegationID,
		AdditionalToolCalls:        15,
		AdditionalWallClockSeconds: 60,
	})
	if err != nil {
		t.Fatalf("extend: %v", err)
	}
	if got.Updated != 1 || len(got.Updates) != 1 {
		t.Fatalf("updates = %+v, want one", got)
	}
	if !got.Updates[0].LiveUpdated {
		t.Fatalf("live_updated = false, want true")
	}
	w, err := db.GetWorker(ctx, workerID)
	if err != nil {
		t.Fatalf("get worker: %v", err)
	}
	if w.MaxToolCalls != 35 {
		t.Fatalf("max_tool_calls = %d, want 35", w.MaxToolCalls)
	}
	if w.MaxWallClockSeconds != 180 {
		t.Fatalf("max_wall_clock_seconds = %d, want 180", w.MaxWallClockSeconds)
	}
	if len(fr.refreshCalls) == 0 || fr.refreshCalls[len(fr.refreshCalls)-1] != run.ID {
		t.Fatalf("refresh calls = %v, want final call for %s", fr.refreshCalls, run.ID)
	}
}

func TestExtendDelegationBudgetRejectsNonIncrease(t *testing.T) {
	svc, db, wsID, scopeID := newTestService(t)
	ctx := context.Background()
	w, err := svc.Create(ctx, admin.CreateInput{
		Name:                "budget-bot",
		ModelProvider:       "anthropic",
		ModelID:             "claude-sonnet-4-5",
		SecretScopeID:       scopeID,
		PromptTemplate:      "work",
		ScheduleSpec:        "0 9 * * *",
		WorkspaceID:         wsID,
		MaxToolCalls:        20,
		MaxWallClockSeconds: 120,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	run := &store.WorkerRun{
		ID:        "run-budget-reject",
		WorkerID:  w.ID,
		StartedAt: time.Now().UTC(),
		Status:    runner.StatusRunning,
	}
	if err := db.CreateWorkerRun(ctx, run); err != nil {
		t.Fatalf("seed run: %v", err)
	}
	lower := 10
	if _, err := svc.ExtendDelegationBudget(ctx, admin.DelegationBudgetInput{
		RunID:        run.ID,
		WorkspaceID:  wsID,
		MaxToolCalls: &lower,
	}); err == nil {
		t.Fatal("extend accepted lower cap, want error")
	}
}
