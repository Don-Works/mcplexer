package admin_test

import (
	"context"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/workers/admin"
)

func TestServiceSweepDelegationRetention_ExpiredWorkerArchived(t *testing.T) {
	svc, db, wsID, scopeID := newTestService(t)
	ctx := context.Background()

	out, err := svc.Delegate(ctx, admin.DelegationInput{
		WorkspaceID:         wsID,
		Objective:           "Old expired task.",
		ModelProvider:       "anthropic",
		ModelID:             "claude-sonnet-4-5",
		SecretScopeID:       scopeID,
		MaxWallClockSeconds: 30,
	})
	if err != nil {
		t.Fatalf("Delegate: %v", err)
	}
	run := waitForDelegationRun(t, db, out.Dispatches[0].WorkerID)
	if err := db.UpdateWorkerRunStatus(ctx, run.ID, store.WorkerRunFinalize{
		Status:             "success",
		FinishedAt:         time.Now().UTC(),
		InputTokens:        1000,
		OutputTokens:       200,
		CostUSD:            0.05,
		ToolCallsCount:     5,
		MeshMessageIDsJSON: "[]",
		AuditRecordIDsJSON: "[]",
	}); err != nil {
		t.Fatalf("finish run: %v", err)
	}

	n, err := svc.SweepDelegationRetention(ctx, time.Nanosecond)
	if err != nil {
		t.Fatalf("SweepDelegationRetention: %v", err)
	}
	if n != 1 {
		t.Fatalf("archived = %d, want 1", n)
	}

	rows, err := svc.ListDelegations(ctx, admin.DelegationListInput{WorkspaceID: wsID})
	if err != nil {
		t.Fatalf("ListDelegations: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("delegations after sweep = %d, want 0 (archived should be excluded)", len(rows))
	}
}

func TestServiceSweepDelegationRetention_ActiveRunVetosArchive(t *testing.T) {
	svc, db, wsID, scopeID := newTestService(t)
	ctx := context.Background()

	out, err := svc.Delegate(ctx, admin.DelegationInput{
		WorkspaceID:         wsID,
		Objective:           "Active task with running run.",
		ModelProvider:       "anthropic",
		ModelID:             "claude-sonnet-4-5",
		SecretScopeID:       scopeID,
		MaxWallClockSeconds: 30,
	})
	if err != nil {
		t.Fatalf("Delegate: %v", err)
	}
	_ = waitForDelegationRun(t, db, out.Dispatches[0].WorkerID)

	n, err := svc.SweepDelegationRetention(ctx, time.Hour)
	if err != nil {
		t.Fatalf("SweepDelegationRetention: %v", err)
	}
	if n != 0 {
		t.Fatalf("archived = %d, want 0 (running run must veto)", n)
	}

	rows, err := svc.ListDelegations(ctx, admin.DelegationListInput{WorkspaceID: wsID})
	if err != nil {
		t.Fatalf("ListDelegations: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("delegations after sweep = 0, want 1 (active delegation must survive)")
	}

	got := findDelegation(t, rows, out.DelegationID)
	if got.Status != "running" {
		t.Fatalf("status = %q, want running", got.Status)
	}
}

func TestServiceSweepDelegationRetention_AwaitingApprovalVetosArchive(t *testing.T) {
	svc, db, wsID, scopeID := newTestService(t)
	ctx := context.Background()

	out, err := svc.Delegate(ctx, admin.DelegationInput{
		WorkspaceID:         wsID,
		Objective:           "Awaiting approval task.",
		ModelProvider:       "anthropic",
		ModelID:             "claude-sonnet-4-5",
		SecretScopeID:       scopeID,
		MaxWallClockSeconds: 30,
	})
	if err != nil {
		t.Fatalf("Delegate: %v", err)
	}
	run := waitForDelegationRun(t, db, out.Dispatches[0].WorkerID)
	if err := db.UpdateWorkerRunStatus(ctx, run.ID, store.WorkerRunFinalize{
		Status:             "awaiting_approval",
		FinishedAt:         time.Now().UTC(),
		MeshMessageIDsJSON: "[]",
		AuditRecordIDsJSON: "[]",
	}); err != nil {
		t.Fatalf("finalize run as awaiting_approval: %v", err)
	}

	n, err := svc.SweepDelegationRetention(ctx, time.Hour)
	if err != nil {
		t.Fatalf("SweepDelegationRetention: %v", err)
	}
	if n != 0 {
		t.Fatalf("archived = %d, want 0 (awaiting_approval must veto)", n)
	}
}

func TestServiceSweepDelegationRetention_RecentFinishedRunVetosArchive(t *testing.T) {
	svc, db, wsID, scopeID := newTestService(t)
	ctx := context.Background()

	out, err := svc.Delegate(ctx, admin.DelegationInput{
		WorkspaceID:         wsID,
		Objective:           "Recently finished task.",
		ModelProvider:       "anthropic",
		ModelID:             "claude-sonnet-4-5",
		SecretScopeID:       scopeID,
		MaxWallClockSeconds: 30,
	})
	if err != nil {
		t.Fatalf("Delegate: %v", err)
	}
	run := waitForDelegationRun(t, db, out.Dispatches[0].WorkerID)
	if err := db.UpdateWorkerRunStatus(ctx, run.ID, store.WorkerRunFinalize{
		Status:             "success",
		FinishedAt:         time.Now().UTC(),
		InputTokens:        500,
		OutputTokens:       100,
		CostUSD:            0.02,
		MeshMessageIDsJSON: "[]",
		AuditRecordIDsJSON: "[]",
	}); err != nil {
		t.Fatalf("finish run: %v", err)
	}

	n, err := svc.SweepDelegationRetention(ctx, 24*time.Hour)
	if err != nil {
		t.Fatalf("SweepDelegationRetention: %v", err)
	}
	if n != 0 {
		t.Fatalf("archived = %d, want 0 (recent finished run must veto)", n)
	}

	rows, err := svc.ListDelegations(ctx, admin.DelegationListInput{WorkspaceID: wsID})
	if err != nil {
		t.Fatalf("ListDelegations: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("delegations after sweep = 0, want 1 (recent delegation must survive)")
	}
}

func TestServiceSweepDelegationRetention_ZeroRetentionUsesDefaultAndArchivesNothingForFreshWorker(t *testing.T) {
	svc, sdb, wsID, scopeID := newTestService(t)
	ctx := context.Background()

	out, err := svc.Delegate(ctx, admin.DelegationInput{
		WorkspaceID:         wsID,
		Objective:           "Fresh task.",
		ModelProvider:       "anthropic",
		ModelID:             "claude-sonnet-4-5",
		SecretScopeID:       scopeID,
		MaxWallClockSeconds: 30,
	})
	if err != nil {
		t.Fatalf("Delegate: %v", err)
	}
	_ = waitForDelegationRun(t, sdb, out.Dispatches[0].WorkerID)

	// retention=0 uses DefaultDelegationRetention (14 days) — a freshly
	// created worker is well within that window, so no archive.
	n, err := svc.SweepDelegationRetention(ctx, 0)
	if err != nil {
		t.Fatalf("SweepDelegationRetention: %v", err)
	}
	if n != 0 {
		t.Fatalf("archived = %d, want 0 (fresh worker should not expire with default retention)", n)
	}
}

func TestServiceSweepDelegationRetention_OnlyDelegationWorkers(t *testing.T) {
	svc, _, wsID, scopeID := newTestService(t)
	ctx := context.Background()

	_, err := svc.Create(ctx, admin.CreateInput{
		Name:           "non-delegate-worker",
		ModelProvider:  "anthropic",
		ModelID:        "claude-sonnet-4-5",
		SecretScopeID:  scopeID,
		PromptTemplate: "Do normal work.",
		ScheduleSpec:   "manual",
		WorkspaceID:    wsID,
	})
	if err != nil {
		t.Fatalf("Create non-delegate worker: %v", err)
	}

	n, err := svc.SweepDelegationRetention(ctx, time.Nanosecond)
	if err != nil {
		t.Fatalf("SweepDelegationRetention: %v", err)
	}
	if n != 0 {
		t.Fatalf("archived = %d, want 0 (non-delegate worker must never be archived)", n)
	}

	rows, err := svc.List(ctx, admin.ListInput{WorkspaceID: wsID})
	if err != nil {
		t.Fatalf("List workers: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("workers remaining = %d, want 1 (non-delegate worker must survive)", len(rows))
	}
}

func TestServiceDelegationAggregateTracksUnknownCostRuns(t *testing.T) {
	svc, db, wsID, scopeID := newTestService(t)
	ctx := context.Background()

	out, err := svc.Delegate(ctx, admin.DelegationInput{
		WorkspaceID:            wsID,
		Objective:              "Aggregate missing telemetry.",
		ModelProvider:          "anthropic",
		ModelID:                "claude-sonnet-4-5",
		SecretScopeID:          scopeID,
		BaselineTokensEstimate: 50000,
		BaselineCostUSD:        2.50,
		MaxWallClockSeconds:    30,
	})
	if err != nil {
		t.Fatalf("Delegate: %v", err)
	}
	run := waitForDelegationRun(t, db, out.Dispatches[0].WorkerID)
	if err := db.UpdateWorkerRunStatus(ctx, run.ID, store.WorkerRunFinalize{
		Status:             "success",
		FinishedAt:         time.Now().UTC(),
		InputTokens:        0,
		OutputTokens:       0,
		CostUSD:            0,
		MeshMessageIDsJSON: "[]",
		AuditRecordIDsJSON: "[]",
	}); err != nil {
		t.Fatalf("finalize run: %v", err)
	}

	rows, err := svc.ListDelegations(ctx, admin.DelegationListInput{WorkspaceID: wsID})
	if err != nil {
		t.Fatalf("ListDelegations: %v", err)
	}
	got := findDelegation(t, rows, out.DelegationID)
	if got.Aggregate.UnknownCostRuns != 1 {
		t.Fatalf("UnknownCostRuns = %d, want 1", got.Aggregate.UnknownCostRuns)
	}
	if !got.Aggregate.CostAllMissing {
		t.Fatal("CostAllMissing should be true when every successful run has missing accounting")
	}
	if got.Aggregate.SavingsConfidence == "estimated" {
		t.Fatal("SavingsConfidence should not be 'estimated' when all cost is missing")
	}
}
