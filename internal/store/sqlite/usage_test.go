package sqlite_test

import (
	"context"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

func TestListUsageLedgerRunsHonorsWindowAndProjectsAccounting(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := context.Background()
	workspaceID, scopeID := seedWorkspaceAndScope(t, db, ctx)
	worker := newWorker(workspaceID, scopeID, "usage-ledger")
	if err := db.CreateWorker(ctx, worker); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	insertUsageRun(t, db, ctx, worker.ID, now.Add(-time.Hour), 120, 30)
	insertUsageRun(t, db, ctx, worker.ID, now.AddDate(0, 0, -40), 999, 999)

	runs, err := db.ListUsageLedgerRuns(ctx, now.AddDate(0, 0, -30))
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("runs = %d, want 1", len(runs))
	}
	got := runs[0]
	if got.ModelProvider != "claude_cli" || got.SubscriptionBucket != "claude" {
		t.Fatalf("identity = %+v", got)
	}
	if got.InputTokens != 120 || got.OutputTokens != 30 || got.BillingModel != "subscription" {
		t.Fatalf("accounting = %+v", got)
	}
}

func TestUsageSnapshotCacheRoundTrip(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	ctx := context.Background()
	generated := time.Date(2026, 7, 11, 9, 30, 0, 0, time.UTC)
	want := store.UsageSnapshot{
		GeneratedAt: generated,
		WindowDays:  30,
		Providers: []store.ProviderSnapshot{{
			Provider: store.ProviderMiMo,
			Label:    "MiMo",
			Status:   store.StatusOK,
			Observed: store.ObservedUsage{Requests: 7, TotalTokens: 1234},
			Windows:  []store.UsageWindow{},
		}},
		OpenRouter: store.OpenRouterSnapshot{Status: store.StatusUnavailable},
	}

	if err := db.PutUsageSnapshot(ctx, "test-key", want); err != nil {
		t.Fatal(err)
	}
	got, found, err := db.GetUsageSnapshot(ctx, "test-key")
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if !got.GeneratedAt.Equal(generated) || got.WindowDays != 30 ||
		len(got.Providers) != 1 || got.Providers[0].Observed.TotalTokens != 1234 {
		t.Fatalf("snapshot = %+v", got)
	}
	if _, found, err := db.GetUsageSnapshot(ctx, "missing"); err != nil || found {
		t.Fatalf("missing found=%v err=%v", found, err)
	}
}

func insertUsageRun(
	t *testing.T,
	db interface {
		CreateWorkerRun(context.Context, *store.WorkerRun) error
		UpdateWorkerRunStatus(context.Context, string, store.WorkerRunFinalize) error
	},
	ctx context.Context,
	workerID string,
	startedAt time.Time,
	inputTokens int,
	outputTokens int,
) {
	t.Helper()
	run := &store.WorkerRun{
		WorkerID:      workerID,
		StartedAt:     startedAt,
		Status:        "running",
		ModelProvider: "claude_cli",
		ModelID:       "claude-opus-4-8",
	}
	if err := db.CreateWorkerRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateWorkerRunStatus(ctx, run.ID, store.WorkerRunFinalize{
		Status:             "success",
		FinishedAt:         startedAt.Add(time.Second),
		InputTokens:        inputTokens,
		OutputTokens:       outputTokens,
		BillingModel:       "subscription",
		SubscriptionBucket: "claude",
	}); err != nil {
		t.Fatal(err)
	}
}
