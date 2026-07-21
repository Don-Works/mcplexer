package sqlite_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

func TestWorkerRunLifecycle(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	wsID, scopeID := seedWorkspaceAndScope(t, db, ctx)

	w := newWorker(wsID, scopeID, "run-test")
	if err := db.CreateWorker(ctx, w); err != nil {
		t.Fatal(err)
	}

	// Truncate to whole seconds so the RFC3339 round-trip on started_at
	// doesn't lose precision and confuse the derived duration check.
	started := time.Now().UTC().Truncate(time.Second)
	run := &store.WorkerRun{
		WorkerID:       w.ID,
		StartedAt:      started,
		Status:         "running",
		ModelProvider:  "anthropic",
		ModelID:        "claude-opus-4-7",
		PromptRendered: "hello world",
	}
	if err := db.CreateWorkerRun(ctx, run); err != nil {
		t.Fatalf("create run: %v", err)
	}
	if run.ID == "" {
		t.Fatal("expected run ID")
	}

	got, err := db.GetWorkerRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if got.Status != "running" || got.WorkerID != w.ID {
		t.Fatalf("round-trip: %+v", got)
	}

	count, err := db.CountRunningWorkerRuns(ctx, w.ID)
	if err != nil {
		t.Fatalf("count running: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 running, got %d", count)
	}

	fin := store.WorkerRunFinalize{
		Status:             "success",
		FinishedAt:         started.Add(750 * time.Millisecond),
		InputTokens:        100,
		OutputTokens:       50,
		CostUSD:            0.001,
		ToolCallsCount:     2,
		OutputText:         "summary",
		MeshMessageIDsJSON: `["msg-1"]`,
		AuditRecordIDsJSON: `["aud-1","aud-2"]`,
	}
	if err := db.UpdateWorkerRunStatus(ctx, run.ID, fin); err != nil {
		t.Fatalf("finalize: %v", err)
	}

	got2, _ := db.GetWorkerRun(ctx, run.ID)
	if got2.Status != "success" {
		t.Fatalf("status = %q", got2.Status)
	}
	if got2.FinishedAt == nil {
		t.Fatal("finished_at should be set")
	}
	if got2.DurationMS != 750 {
		t.Fatalf("duration_ms = %d, want 750", got2.DurationMS)
	}
	if got2.OutputText != "summary" || got2.OutputTokens != 50 {
		t.Fatalf("update fields: %+v", got2)
	}
	if got2.MeshMessageIDsJSON != `["msg-1"]` {
		t.Fatalf("mesh ids: %q", got2.MeshMessageIDsJSON)
	}

	count, _ = db.CountRunningWorkerRuns(ctx, w.ID)
	if count != 0 {
		t.Fatalf("expected 0 running after finalize, got %d", count)
	}
}

func TestWorkerRunListAndNotFound(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	wsID, scopeID := seedWorkspaceAndScope(t, db, ctx)

	w := newWorker(wsID, scopeID, "list-runs")
	if err := db.CreateWorker(ctx, w); err != nil {
		t.Fatal(err)
	}

	base := time.Now().UTC().Add(-1 * time.Hour)
	for i := 0; i < 3; i++ {
		run := &store.WorkerRun{
			WorkerID:  w.ID,
			StartedAt: base.Add(time.Duration(i) * time.Minute),
			Status:    "success",
		}
		if err := db.CreateWorkerRun(ctx, run); err != nil {
			t.Fatalf("create run %d: %v", i, err)
		}
	}

	runs, err := db.ListWorkerRuns(ctx, w.ID, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(runs) != 3 {
		t.Fatalf("expected 3 runs, got %d", len(runs))
	}
	// Ordered started_at DESC: newest first.
	if !runs[0].StartedAt.After(runs[1].StartedAt) {
		t.Fatalf("not ordered DESC: %v vs %v",
			runs[0].StartedAt, runs[1].StartedAt)
	}

	limited, err := db.ListWorkerRuns(ctx, w.ID, 2)
	if err != nil {
		t.Fatalf("list limited: %v", err)
	}
	if len(limited) != 2 {
		t.Fatalf("expected 2 with limit, got %d", len(limited))
	}

	if _, err := db.GetWorkerRun(ctx, "no-such-run"); !errors.Is(err, store.ErrWorkerRunNotFound) {
		t.Fatalf("expected ErrWorkerRunNotFound, got %v", err)
	}
	missing := store.WorkerRunFinalize{
		Status:     "success",
		FinishedAt: time.Now().UTC(),
	}
	if err := db.UpdateWorkerRunStatus(ctx, "no-such-run", missing); !errors.Is(err, store.ErrWorkerRunNotFound) {
		t.Fatalf("update missing: expected ErrWorkerRunNotFound, got %v", err)
	}
}

func TestCancelWorkerRun(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	wsID, scopeID := seedWorkspaceAndScope(t, db, ctx)

	w := newWorker(wsID, scopeID, "cancel-run")
	if err := db.CreateWorker(ctx, w); err != nil {
		t.Fatal(err)
	}
	started := time.Now().UTC().Add(-5 * time.Minute).Truncate(time.Second)
	run := &store.WorkerRun{
		WorkerID:  w.ID,
		StartedAt: started,
		Status:    "running",
	}
	if err := db.CreateWorkerRun(ctx, run); err != nil {
		t.Fatalf("create run: %v", err)
	}
	finished := started.Add(5 * time.Minute)
	if err := db.CancelRun(ctx, run.ID, finished, "manual cleanup"); err != nil {
		t.Fatalf("cancel run: %v", err)
	}
	got, err := db.GetWorkerRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("get cancelled run: %v", err)
	}
	if got.Status != "cancelled" {
		t.Fatalf("status = %q, want cancelled", got.Status)
	}
	if got.Error != "manual cleanup" {
		t.Fatalf("error = %q, want manual cleanup", got.Error)
	}
	if got.FinishedAt == nil {
		t.Fatal("finished_at was not set")
	}
	if got.DurationMS != int64(5*time.Minute/time.Millisecond) {
		t.Fatalf("duration_ms = %d", got.DurationMS)
	}
	if err := db.CancelRun(ctx, run.ID, finished, "again"); !errors.Is(err, store.ErrRunNotCancellable) {
		t.Fatalf("cancel terminal run err = %v, want ErrRunNotCancellable", err)
	}
	if err := db.CancelRun(ctx, "missing", finished, "missing"); !errors.Is(err, store.ErrWorkerRunNotFound) {
		t.Fatalf("cancel missing run err = %v, want ErrWorkerRunNotFound", err)
	}
}

func TestWorkerRunBillingRoundTrip(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	wsID, scopeID := seedWorkspaceAndScope(t, db, ctx)

	w := newWorker(wsID, scopeID, "billing-test")
	if err := db.CreateWorker(ctx, w); err != nil {
		t.Fatal(err)
	}

	run := &store.WorkerRun{
		WorkerID:           w.ID,
		StartedAt:          time.Now().UTC().Truncate(time.Second),
		Status:             "success",
		ModelProvider:      "anthropic",
		ModelID:            "claude-opus-4-7",
		BillingModel:       "subscription",
		SubscriptionBucket: "claude",
		RealCostUSD:        0,
	}
	if err := db.CreateWorkerRun(ctx, run); err != nil {
		t.Fatalf("create run: %v", err)
	}

	got, err := db.GetWorkerRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if got.BillingModel != "subscription" {
		t.Errorf("billing_model = %q, want %q", got.BillingModel, "subscription")
	}
	if got.SubscriptionBucket != "claude" {
		t.Errorf("subscription_bucket = %q, want %q", got.SubscriptionBucket, "claude")
	}
	if got.RealCostUSD != 0 {
		t.Errorf("real_cost_usd = %f, want 0", got.RealCostUSD)
	}

	// Test with non-zero real_cost_usd (metered run).
	run2 := &store.WorkerRun{
		WorkerID:     w.ID,
		StartedAt:    time.Now().UTC().Truncate(time.Second),
		Status:       "success",
		BillingModel: "metered",
		RealCostUSD:  0.0042,
	}
	if err := db.CreateWorkerRun(ctx, run2); err != nil {
		t.Fatalf("create run2: %v", err)
	}
	got2, err := db.GetWorkerRun(ctx, run2.ID)
	if err != nil {
		t.Fatalf("get run2: %v", err)
	}
	if got2.BillingModel != "metered" {
		t.Errorf("billing_model = %q, want %q", got2.BillingModel, "metered")
	}
	if got2.SubscriptionBucket != "" {
		t.Errorf("subscription_bucket = %q, want empty", got2.SubscriptionBucket)
	}
	if got2.RealCostUSD != 0.0042 {
		t.Errorf("real_cost_usd = %f, want 0.0042", got2.RealCostUSD)
	}
}

// TestScheduledJobWorkerIDRoundTrip confirms the worker_id column we added
// to scheduled_jobs in migration 048 round-trips through Create/Get and
// stays empty for legacy non-worker kinds.
func TestReapOrphanedRunningRunsSkipsDelegationWorkers(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	wsID, scopeID := seedWorkspaceAndScope(t, db, ctx)

	normalW := newWorker(wsID, scopeID, "normal-worker")
	if err := db.CreateWorker(ctx, normalW); err != nil {
		t.Fatal(err)
	}
	delegW := newWorker(wsID, scopeID, "delegate-test")
	if err := db.CreateWorker(ctx, delegW); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	past := now.Add(-5 * time.Minute)

	normalRun := &store.WorkerRun{
		WorkerID:  normalW.ID,
		StartedAt: past,
		Status:    "running",
	}
	if err := db.CreateWorkerRun(ctx, normalRun); err != nil {
		t.Fatalf("create normal run: %v", err)
	}
	delegRun := &store.WorkerRun{
		WorkerID:  delegW.ID,
		StartedAt: past,
		Status:    "running",
	}
	if err := db.CreateWorkerRun(ctx, delegRun); err != nil {
		t.Fatalf("create delegation run: %v", err)
	}

	n, err := db.ReapOrphanedRunningRuns(ctx, now, now)
	if err != nil {
		t.Fatalf("reap: %v", err)
	}
	if n != 1 {
		t.Fatalf("reaped %d rows, want 1", n)
	}

	gotNormal, _ := db.GetWorkerRun(ctx, normalRun.ID)
	if gotNormal.Status != "interrupted" {
		t.Fatalf("normal run status = %q, want interrupted", gotNormal.Status)
	}

	gotDeleg, _ := db.GetWorkerRun(ctx, delegRun.ID)
	if gotDeleg.Status != "running" {
		t.Fatalf("delegation run status = %q, want running", gotDeleg.Status)
	}

	orphans, err := db.ListOrphanedDelegationRuns(ctx)
	if err != nil {
		t.Fatalf("list orphaned delegation runs: %v", err)
	}
	if len(orphans) != 1 {
		t.Fatalf("orphaned delegation runs = %d, want 1", len(orphans))
	}
	if orphans[0].ID != delegRun.ID {
		t.Fatalf("orphan ID = %q, want %q", orphans[0].ID, delegRun.ID)
	}
}

func TestScheduledJobWorkerIDRoundTrip(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	wsID, scopeID := seedWorkspaceAndScope(t, db, ctx)
	w := newWorker(wsID, scopeID, "sched-fk")
	if err := db.CreateWorker(ctx, w); err != nil {
		t.Fatal(err)
	}

	next := time.Now().UTC().Add(time.Minute)
	j := &store.ScheduledJob{
		ID:        "sj-worker",
		Name:      "worker-tick",
		Kind:      "worker",
		Spec:      "0 9 * * *",
		Surface:   "schedule",
		Enabled:   true,
		WorkerID:  w.ID,
		NextRunAt: &next,
	}
	if err := db.CreateScheduledJob(ctx, j); err != nil {
		t.Fatalf("create scheduled_job: %v", err)
	}

	got, err := db.GetScheduledJob(ctx, j.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Kind != "worker" || got.WorkerID != w.ID {
		t.Fatalf("scheduled_job round-trip lost worker linkage: kind=%q worker_id=%q",
			got.Kind, got.WorkerID)
	}

	// A non-worker job should round-trip with empty worker_id.
	j2 := &store.ScheduledJob{
		ID:      "sj-cron",
		Name:    "vanilla",
		Kind:    "cron",
		Spec:    "0 3 * * *",
		Command: "/bin/true",
		Surface: "schedule",
		Enabled: true,
	}
	if err := db.CreateScheduledJob(ctx, j2); err != nil {
		t.Fatal(err)
	}
	got2, _ := db.GetScheduledJob(ctx, j2.ID)
	if got2.WorkerID != "" {
		t.Fatalf("non-worker job picked up worker_id %q", got2.WorkerID)
	}
}

func TestReapOrphanedRunningRuns_InterruptsRunningAndDispatched(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	wsID, scopeID := seedWorkspaceAndScope(t, db, ctx)

	w := newWorker(wsID, scopeID, "sweep-test")
	if err := db.CreateWorker(ctx, w); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC().Truncate(time.Second)

	tests := []struct {
		name      string
		startedAt time.Time
		status    string
		wantAfter string
	}{
		{"pre-boot running becomes interrupted", now.Add(-5 * time.Minute), "running", "interrupted"},
		{"pre-boot dispatched becomes interrupted", now.Add(-5 * time.Minute), "dispatched", "interrupted"},
		{"post-boot running stays running", now.Add(time.Minute), "running", "running"},
		{"post-boot dispatched stays dispatched", now.Add(time.Minute), "dispatched", "dispatched"},
		{"already success stays success", now.Add(-5 * time.Minute), "success", "success"},
		{"already failure stays failure", now.Add(-5 * time.Minute), "failure", "failure"},
		{"already cancelled stays cancelled", now.Add(-5 * time.Minute), "cancelled", "cancelled"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			run := &store.WorkerRun{
				WorkerID:  w.ID,
				StartedAt: tt.startedAt,
				Status:    tt.status,
			}
			if err := db.CreateWorkerRun(ctx, run); err != nil {
				t.Fatal(err)
			}

			_, err := db.ReapOrphanedRunningRuns(ctx, now, now)
			if err != nil {
				t.Fatal(err)
			}

			got, err := db.GetWorkerRun(ctx, run.ID)
			if err != nil {
				t.Fatal(err)
			}

			if got.Status != tt.wantAfter {
				t.Errorf("status = %q, want %q", got.Status, tt.wantAfter)
			}
		})
	}
}

func TestReapOrphanedRunningRuns_DelegationUntouched(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	wsID, scopeID := seedWorkspaceAndScope(t, db, ctx)

	delegW := newWorker(wsID, scopeID, "delegate-abcd")
	if err := db.CreateWorker(ctx, delegW); err != nil {
		t.Fatal(err)
	}
	normalW := newWorker(wsID, scopeID, "normal-worker")
	if err := db.CreateWorker(ctx, normalW); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	past := now.Add(-5 * time.Minute)

	delegRun := &store.WorkerRun{WorkerID: delegW.ID, StartedAt: past, Status: "running"}
	if err := db.CreateWorkerRun(ctx, delegRun); err != nil {
		t.Fatal(err)
	}
	delegDisp := &store.WorkerRun{WorkerID: delegW.ID, StartedAt: past, Status: "dispatched"}
	if err := db.CreateWorkerRun(ctx, delegDisp); err != nil {
		t.Fatal(err)
	}
	normalRun := &store.WorkerRun{WorkerID: normalW.ID, StartedAt: past, Status: "running"}
	if err := db.CreateWorkerRun(ctx, normalRun); err != nil {
		t.Fatal(err)
	}

	n, err := db.ReapOrphanedRunningRuns(ctx, now, now)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("reaped %d rows, want 1 (only normal worker)", n)
	}

	gotDelegRun, _ := db.GetWorkerRun(ctx, delegRun.ID)
	if gotDelegRun.Status != "running" {
		t.Errorf("delegation running run status = %q, want running", gotDelegRun.Status)
	}
	gotDelegDisp, _ := db.GetWorkerRun(ctx, delegDisp.ID)
	if gotDelegDisp.Status != "dispatched" {
		t.Errorf("delegation dispatched run status = %q, want dispatched", gotDelegDisp.Status)
	}
	gotNormal, _ := db.GetWorkerRun(ctx, normalRun.ID)
	if gotNormal.Status != "interrupted" {
		t.Errorf("normal run status = %q, want interrupted", gotNormal.Status)
	}
}

func TestReapOrphanedRunningRuns_ErrorAndTimestamp(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	wsID, scopeID := seedWorkspaceAndScope(t, db, ctx)

	w := newWorker(wsID, scopeID, "error-check")
	if err := db.CreateWorker(ctx, w); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	past := now.Add(-3 * time.Minute)

	run := &store.WorkerRun{
		WorkerID:  w.ID,
		StartedAt: past,
		Status:    "running",
	}
	if err := db.CreateWorkerRun(ctx, run); err != nil {
		t.Fatal(err)
	}

	_, err := db.ReapOrphanedRunningRuns(ctx, now, now)
	if err != nil {
		t.Fatal(err)
	}

	got, err := db.GetWorkerRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}

	if got.Error != "interrupted by daemon restart" {
		t.Errorf("error = %q, want 'interrupted by daemon restart'", got.Error)
	}
	if got.FinishedAt == nil {
		t.Fatal("finished_at is nil")
	}
	if !got.FinishedAt.Equal(now) {
		t.Errorf("finished_at = %v, want %v", got.FinishedAt, now)
	}
	if got.DurationMS <= 0 {
		t.Errorf("duration_ms = %d, want > 0", got.DurationMS)
	}
}

func TestWorkerRunGitResultPresencePreservesOrReplacesAuthoritatively(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	wsID, scopeID := seedWorkspaceAndScope(t, db, ctx)
	w := newWorker(wsID, scopeID, "git-result-presence")
	if err := db.CreateWorker(ctx, w); err != nil {
		t.Fatal(err)
	}
	started := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	run := &store.WorkerRun{
		WorkerID: w.ID, StartedAt: started, Status: "running",
		ResultBranch: "mcplexer/delegation/run", ResultCommit: "base", ResultChanged: false,
	}
	if err := db.CreateWorkerRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	initial, err := db.GetWorkerRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if initial.ResultBranch != run.ResultBranch || initial.ResultCommit != "base" || initial.ResultChanged {
		t.Fatalf("initial trusted result did not round-trip: %+v", initial)
	}

	if err := db.UpdateWorkerRunStatus(ctx, run.ID, store.WorkerRunFinalize{
		Status: "interrupted", FinishedAt: started.Add(time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	preserved, err := db.GetWorkerRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if preserved.ResultBranch != run.ResultBranch || preserved.ResultCommit != "base" || preserved.ResultChanged {
		t.Fatalf("unrelated finalizer overwrote trusted result: %+v", preserved)
	}

	run2 := &store.WorkerRun{WorkerID: w.ID, StartedAt: started, Status: "running", ResultBranch: "reserved", ResultCommit: "base"}
	if err := db.CreateWorkerRun(ctx, run2); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateWorkerRunStatus(ctx, run2.ID, store.WorkerRunFinalize{
		Status: "success", FinishedAt: started.Add(time.Minute), HasGitResult: true,
		ResultBranch: "mcplexer/delegation/run2", ResultCommit: "changed", ResultChanged: true,
	}); err != nil {
		t.Fatal(err)
	}
	replaced, err := db.GetWorkerRun(ctx, run2.ID)
	if err != nil {
		t.Fatal(err)
	}
	if replaced.ResultBranch != "mcplexer/delegation/run2" || replaced.ResultCommit != "changed" || !replaced.ResultChanged {
		t.Fatalf("authoritative result not persisted: %+v", replaced)
	}
}

func TestCancelledWorkerRunAcceptsLateTrustedSnapshotOnly(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	wsID, scopeID := seedWorkspaceAndScope(t, db, ctx)
	w := newWorker(wsID, scopeID, "cancelled-git-result")
	if err := db.CreateWorker(ctx, w); err != nil {
		t.Fatal(err)
	}
	started := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	run := &store.WorkerRun{WorkerID: w.ID, StartedAt: started, Status: "running", ResultBranch: "reserved", ResultCommit: "base"}
	if err := db.CreateWorkerRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	if err := db.CancelRun(ctx, run.ID, started.Add(30*time.Second), "operator stop"); err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateWorkerRunStatus(ctx, run.ID, store.WorkerRunFinalize{
		Status: "success", FinishedAt: started.Add(time.Minute), OutputText: "must not win",
		HasGitResult: true, ResultBranch: "reserved", ResultCommit: "snapshot", ResultChanged: true,
	}); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetWorkerRun(ctx, run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "cancelled" || got.Error != "operator stop" || got.OutputText != "" {
		t.Fatalf("late finalize clobbered cancellation: %+v", got)
	}
	if got.ResultBranch != "reserved" || got.ResultCommit != "snapshot" || !got.ResultChanged {
		t.Fatalf("late trusted snapshot was lost: %+v", got)
	}
}
