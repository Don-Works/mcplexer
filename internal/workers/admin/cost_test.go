package admin_test

import (
	"context"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/workers/admin"
)

func TestServiceCostAggregate_EmptyWorkspace(t *testing.T) {
	svc, _, wsID, _ := newTestService(t)
	out, err := svc.CostAggregate(context.Background(), admin.CostAggregateInput{
		WorkspaceID: wsID,
		Days:        30,
	})
	if err != nil {
		t.Fatalf("CostAggregate: %v", err)
	}
	if out.Days != 30 {
		t.Fatalf("Days = %d, want 30", out.Days)
	}
	if out.WorkspaceID != wsID {
		t.Fatalf("WorkspaceID = %q", out.WorkspaceID)
	}
	if len(out.Workers) != 0 {
		t.Fatalf("Workers = %d, want 0", len(out.Workers))
	}
	if out.TotalMTDUSD != 0 {
		t.Fatalf("TotalMTDUSD = %v", out.TotalMTDUSD)
	}
}

func TestServiceCostAggregate_DaysDefault(t *testing.T) {
	svc, _, wsID, _ := newTestService(t)
	out, err := svc.CostAggregate(context.Background(), admin.CostAggregateInput{
		WorkspaceID: wsID,
	})
	if err != nil {
		t.Fatalf("CostAggregate: %v", err)
	}
	if out.Days != 30 {
		t.Fatalf("default Days = %d, want 30", out.Days)
	}
}

func TestServiceCostAggregate_WithRuns(t *testing.T) {
	svc, db, wsID, scopeID := newTestService(t)
	ctx := context.Background()
	w, err := svc.Create(ctx, baseCreate(wsID, scopeID))
	if err != nil {
		t.Fatalf("create worker: %v", err)
	}
	// Insert one finalized run carrying a non-zero cost so the
	// aggregator has something to roll up.
	started := time.Now().UTC().Add(-1 * time.Hour)
	run := &store.WorkerRun{
		WorkerID:      w.ID,
		StartedAt:     started,
		Status:        "running",
		ModelProvider: "anthropic",
		ModelID:       "claude-opus-4-7",
	}
	if err := db.CreateWorkerRun(ctx, run); err != nil {
		t.Fatalf("create run: %v", err)
	}
	fin := store.WorkerRunFinalize{
		Status:     "success",
		FinishedAt: started.Add(time.Minute),
		CostUSD:    0.42,
	}
	if err := db.UpdateWorkerRunStatus(ctx, run.ID, fin); err != nil {
		t.Fatalf("finalize run: %v", err)
	}

	out, err := svc.CostAggregate(ctx, admin.CostAggregateInput{
		WorkspaceID: wsID,
		Days:        30,
	})
	if err != nil {
		t.Fatalf("CostAggregate: %v", err)
	}
	if len(out.Workers) != 1 {
		t.Fatalf("Workers = %d, want 1", len(out.Workers))
	}
	if out.TotalMTDUSD == 0 {
		t.Fatalf("TotalMTDUSD = 0, want > 0")
	}
	if out.TotalRuns30D != 1 {
		t.Fatalf("TotalRuns30D = %d, want 1", out.TotalRuns30D)
	}
}
