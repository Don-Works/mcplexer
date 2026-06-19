package sqlite_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// seedWorkspaceAndScope creates the workspace + auth_scope rows that
// every Worker test needs. Returns (workspaceID, scopeID).
func seedWorkspaceAndScope(
	t *testing.T, db storeAndCloser, ctx context.Context,
) (string, string) {
	t.Helper()
	ws := &store.Workspace{Name: "worker-ws", DefaultPolicy: "allow"}
	if err := db.CreateWorkspace(ctx, ws); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	as := &store.AuthScope{Name: "anthropic-key", Type: "env"}
	if err := db.CreateAuthScope(ctx, as); err != nil {
		t.Fatalf("create auth scope: %v", err)
	}
	return ws.ID, as.ID
}

// storeAndCloser narrows the test handle to the methods these tests
// need without importing sqlite.DB everywhere.
type storeAndCloser interface {
	CreateWorkspace(ctx context.Context, w *store.Workspace) error
	CreateAuthScope(ctx context.Context, a *store.AuthScope) error
}

func newWorker(wsID, scopeID, name string) *store.Worker {
	return &store.Worker{
		Name:           name,
		Description:    "test worker",
		ModelProvider:  "anthropic",
		ModelID:        "claude-opus-4-7",
		SecretScopeID:  scopeID,
		PromptTemplate: "Summarize {topic} for {audience}.",
		ParametersJSON: `{"topic":"go","audience":"devs"}`,
		ScheduleSpec:   "0 9 * * *",
		ExecMode:       "propose",
		Enabled:        true,
		WorkspaceID:    wsID,
	}
}

func TestWorkerCRUD(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	wsID, scopeID := seedWorkspaceAndScope(t, db, ctx)

	w := newWorker(wsID, scopeID, "daily-digest")
	w.SkillName = "summarize"
	w.SkillVersion = "1"
	w.ToolAllowlistJSON = `["github__search_issues"]`
	w.OutputChannelsJSON = `[{"kind":"mesh"}]`
	w.MemoryScopeID = "mem-1"

	if err := db.CreateWorker(ctx, w); err != nil {
		t.Fatalf("create worker: %v", err)
	}
	if w.ID == "" {
		t.Fatal("expected ID to be generated")
	}

	got, err := db.GetWorker(ctx, w.ID)
	if err != nil {
		t.Fatalf("get worker: %v", err)
	}
	if got.Name != "daily-digest" {
		t.Fatalf("name = %q", got.Name)
	}
	if got.ModelProvider != "anthropic" || got.ModelID != "claude-opus-4-7" {
		t.Fatalf("model = %s/%s", got.ModelProvider, got.ModelID)
	}
	if got.SecretScopeID != scopeID {
		t.Fatalf("secret_scope_id = %q", got.SecretScopeID)
	}
	if got.MemoryScopeID != "mem-1" {
		t.Fatalf("memory_scope_id = %q", got.MemoryScopeID)
	}
	if got.ExecMode != "propose" || got.ConcurrencyPolicy != "skip" {
		t.Fatalf("defaults: exec=%q conc=%q",
			got.ExecMode, got.ConcurrencyPolicy)
	}
	if got.ParametersJSON != `{"topic":"go","audience":"devs"}` {
		t.Fatalf("params json mismatch: %q", got.ParametersJSON)
	}

	byName, err := db.GetWorkerByName(ctx, wsID, "daily-digest")
	if err != nil {
		t.Fatalf("get by name: %v", err)
	}
	if byName.ID != w.ID {
		t.Fatalf("id mismatch from GetWorkerByName")
	}

	got.Description = "updated"
	got.ModelID = "claude-sonnet-4-7"
	got.Enabled = false
	if err := db.UpdateWorker(ctx, got); err != nil {
		t.Fatalf("update: %v", err)
	}
	got2, _ := db.GetWorker(ctx, w.ID)
	if got2.Description != "updated" || got2.ModelID != "claude-sonnet-4-7" || got2.Enabled {
		t.Fatalf("update not persisted: %+v", got2)
	}

	if err := db.DeleteWorker(ctx, w.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := db.GetWorker(ctx, w.ID); !errors.Is(err, store.ErrWorkerNotFound) {
		t.Fatalf("expected ErrWorkerNotFound, got %v", err)
	}
}

func TestWorkerWorkspaceAccessDefaultAndReplace(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	wsID, scopeID := seedWorkspaceAndScope(t, db, ctx)
	other := &store.Workspace{Name: "worker-ws-other", DefaultPolicy: "allow"}
	if err := db.CreateWorkspace(ctx, other); err != nil {
		t.Fatalf("create other workspace: %v", err)
	}

	w := newWorker(wsID, scopeID, "workspace-access")
	if err := db.CreateWorker(ctx, w); err != nil {
		t.Fatalf("create worker: %v", err)
	}

	got, err := db.GetWorker(ctx, w.ID)
	if err != nil {
		t.Fatalf("get worker: %v", err)
	}
	if len(got.WorkspaceAccess) != 1 ||
		got.WorkspaceAccess[0].WorkspaceID != wsID ||
		got.WorkspaceAccess[0].Access != store.WorkerWorkspaceAccessWrite {
		t.Fatalf("default workspace access = %+v", got.WorkspaceAccess)
	}

	if err := db.ReplaceWorkerWorkspaceAccess(ctx, w.ID, []store.WorkerWorkspaceAccess{
		{WorkspaceID: other.ID, Access: store.WorkerWorkspaceAccessRead},
	}); err != nil {
		t.Fatalf("replace workspace access: %v", err)
	}
	grants, err := db.ListWorkerWorkspaceAccess(ctx, w.ID)
	if err != nil {
		t.Fatalf("list workspace access: %v", err)
	}
	if len(grants) != 2 {
		t.Fatalf("grants len = %d, want 2: %+v", len(grants), grants)
	}
	byWorkspace := map[string]string{}
	for _, g := range grants {
		byWorkspace[g.WorkspaceID] = g.Access
	}
	if byWorkspace[wsID] != store.WorkerWorkspaceAccessWrite {
		t.Fatalf("home grant = %q, want write", byWorkspace[wsID])
	}
	if byWorkspace[other.ID] != store.WorkerWorkspaceAccessRead {
		t.Fatalf("other grant = %q, want read", byWorkspace[other.ID])
	}
	if err := db.ReplaceWorkerWorkspaceAccess(ctx, w.ID, []store.WorkerWorkspaceAccess{
		{WorkspaceID: other.ID, Access: "admin"},
	}); err == nil {
		t.Fatal("expected invalid access to fail")
	}
}

func TestWorkerListEnabledOnly(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	wsID, scopeID := seedWorkspaceAndScope(t, db, ctx)

	enabled := newWorker(wsID, scopeID, "w-enabled")
	if err := db.CreateWorker(ctx, enabled); err != nil {
		t.Fatal(err)
	}
	disabled := newWorker(wsID, scopeID, "w-disabled")
	disabled.Enabled = false
	if err := db.CreateWorker(ctx, disabled); err != nil {
		t.Fatal(err)
	}

	all, err := db.ListWorkers(ctx, wsID, false)
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 workers, got %d", len(all))
	}

	onlyEnabled, err := db.ListWorkers(ctx, wsID, true)
	if err != nil {
		t.Fatalf("list enabled: %v", err)
	}
	if len(onlyEnabled) != 1 {
		t.Fatalf("expected 1 enabled, got %d", len(onlyEnabled))
	}
	if onlyEnabled[0].Name != "w-enabled" {
		t.Fatalf("wrong worker returned: %q", onlyEnabled[0].Name)
	}
}

func TestWorkerDuplicateName(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	wsID, scopeID := seedWorkspaceAndScope(t, db, ctx)

	a := newWorker(wsID, scopeID, "dup")
	if err := db.CreateWorker(ctx, a); err != nil {
		t.Fatal(err)
	}
	b := newWorker(wsID, scopeID, "dup")
	err := db.CreateWorker(ctx, b)
	if !errors.Is(err, store.ErrAlreadyExists) {
		t.Fatalf("expected ErrAlreadyExists, got %v", err)
	}
}

// TestWorkerBudgetCapsRoundTrip verifies that the six per-worker cap
// columns + auto_paused_reason round-trip through Create/Get/Update.
// M1 regression guard: changing one of the columns in 049 without
// updating the Go-side scanWorker / Update statement is the most
// likely silent break.
func TestWorkerBudgetCapsRoundTrip(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	wsID, scopeID := seedWorkspaceAndScope(t, db, ctx)

	w := newWorker(wsID, scopeID, "capped-worker")
	w.MaxInputTokens = 9000
	w.MaxOutputTokens = 2048
	w.MaxToolCalls = 25
	w.MaxWallClockSeconds = 60
	w.MaxMonthlyCostUSD = 1.5
	w.MaxConsecutiveFailures = 3
	w.AutoPausedReason = "" // initial
	if err := db.CreateWorker(ctx, w); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := db.GetWorker(ctx, w.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.MaxInputTokens != 9000 || got.MaxOutputTokens != 2048 {
		t.Fatalf("tokens: %d/%d", got.MaxInputTokens, got.MaxOutputTokens)
	}
	if got.MaxToolCalls != 25 || got.MaxWallClockSeconds != 60 {
		t.Fatalf("calls/wall: %d/%d", got.MaxToolCalls, got.MaxWallClockSeconds)
	}
	if got.MaxMonthlyCostUSD != 1.5 {
		t.Fatalf("monthly cost: %v", got.MaxMonthlyCostUSD)
	}
	if got.MaxConsecutiveFailures != 3 {
		t.Fatalf("consec failures: %d", got.MaxConsecutiveFailures)
	}

	// auto_paused_reason is the runner's hand-off back to the operator.
	got.AutoPausedReason = "monthly budget exceeded"
	got.Enabled = false
	if err := db.UpdateWorker(ctx, got); err != nil {
		t.Fatalf("update: %v", err)
	}
	got2, _ := db.GetWorker(ctx, w.ID)
	if got2.AutoPausedReason != "monthly budget exceeded" {
		t.Fatalf("auto_paused_reason: %q", got2.AutoPausedReason)
	}
}

func TestSumCostThisMonth(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	wsID, scopeID := seedWorkspaceAndScope(t, db, ctx)
	w := newWorker(wsID, scopeID, "cost-worker")
	if err := db.CreateWorker(ctx, w); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)

	// Two runs this month, one last month.
	thisMonth := []float64{0.25, 0.75}
	for i, c := range thisMonth {
		r := &store.WorkerRun{
			WorkerID:  w.ID,
			StartedAt: monthStart.Add(time.Hour * time.Duration(i+1)),
			Status:    "success",
			CostUSD:   c,
		}
		if err := db.CreateWorkerRun(ctx, r); err != nil {
			t.Fatalf("create run %d: %v", i, err)
		}
	}
	last := &store.WorkerRun{
		WorkerID:  w.ID,
		StartedAt: monthStart.Add(-24 * time.Hour),
		Status:    "success",
		CostUSD:   10.0,
	}
	if err := db.CreateWorkerRun(ctx, last); err != nil {
		t.Fatal(err)
	}

	sum, err := db.SumCostThisMonth(ctx, w.ID, now)
	if err != nil {
		t.Fatalf("sum: %v", err)
	}
	want := 1.0
	if sum < want-0.0001 || sum > want+0.0001 {
		t.Fatalf("sum = %v, want %v", sum, want)
	}
}

func TestLastFailureStatuses(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	wsID, scopeID := seedWorkspaceAndScope(t, db, ctx)
	w := newWorker(wsID, scopeID, "streak-worker")
	if err := db.CreateWorker(ctx, w); err != nil {
		t.Fatal(err)
	}

	// Status timeline (newest last): success, failure, failure, failure
	now := time.Now().UTC()
	runs := []struct {
		started time.Time
		status  string
	}{
		{now.Add(-4 * time.Hour), "success"},
		{now.Add(-3 * time.Hour), "failure"},
		{now.Add(-2 * time.Hour), "failure"},
		{now.Add(-1 * time.Hour), "failure"},
		{now, "running"}, // must be excluded
	}
	for _, r := range runs {
		row := &store.WorkerRun{
			WorkerID:  w.ID,
			StartedAt: r.started,
			Status:    r.status,
		}
		if err := db.CreateWorkerRun(ctx, row); err != nil {
			t.Fatalf("create run %v: %v", r, err)
		}
	}

	got, err := db.LastFailureStatuses(ctx, w.ID, 3)
	if err != nil {
		t.Fatalf("last: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	for i, s := range got {
		if s != "failure" {
			t.Fatalf("got[%d] = %q, want failure", i, s)
		}
	}
}

func TestWorkerApprovalCRUD(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	wsID, scopeID := seedWorkspaceAndScope(t, db, ctx)
	w := newWorker(wsID, scopeID, "approval-worker")
	if err := db.CreateWorker(ctx, w); err != nil {
		t.Fatal(err)
	}
	run := &store.WorkerRun{WorkerID: w.ID, Status: "awaiting_approval"}
	if err := db.CreateWorkerRun(ctx, run); err != nil {
		t.Fatal(err)
	}

	app := &store.WorkerApproval{
		WorkerID:  w.ID,
		RunID:     run.ID,
		ToolName:  "github__post_comment",
		ToolInput: `{"comment":"hi"}`,
		Reason:    "write-class tool, propose-mode",
	}
	if err := db.CreateWorkerApproval(ctx, app); err != nil {
		t.Fatalf("create: %v", err)
	}
	if app.ID == "" {
		t.Fatal("ID should be generated")
	}

	got, err := db.GetWorkerApproval(ctx, app.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != "pending" || got.ToolName != "github__post_comment" {
		t.Fatalf("loaded: %+v", got)
	}

	pending, err := db.ListWorkerApprovals(ctx, "pending", 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(pending) != 1 {
		t.Fatalf("pending list = %d", len(pending))
	}
	n, err := db.CountPendingWorkerApprovals(ctx)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("count = %d", n)
	}

	if err := db.DecideWorkerApproval(
		ctx, app.ID, "approved", "operator", "resumed-1", time.Now().UTC(),
	); err != nil {
		t.Fatalf("decide: %v", err)
	}
	got2, _ := db.GetWorkerApproval(ctx, app.ID)
	if got2.Status != "approved" || got2.Decision != "approved" {
		t.Fatalf("after decide: %+v", got2)
	}
	if got2.ResumedRunID != "resumed-1" || got2.DecidedAt == nil {
		t.Fatalf("resumed_run_id / decided_at not persisted: %+v", got2)
	}

	// Second decide on same row must fail with ErrWorkerApprovalNotFound
	err = db.DecideWorkerApproval(ctx, app.ID, "rejected", "x", "", time.Now())
	if !errors.Is(err, store.ErrWorkerApprovalNotFound) {
		t.Fatalf("re-decide: expected ErrWorkerApprovalNotFound, got %v", err)
	}
}

func TestWorkerNotFoundOnUpdateDelete(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	wsID, scopeID := seedWorkspaceAndScope(t, db, ctx)

	w := newWorker(wsID, scopeID, "ghost")
	w.ID = "no-such-worker"
	err := db.UpdateWorker(ctx, w)
	if !errors.Is(err, store.ErrWorkerNotFound) {
		t.Fatalf("update: expected ErrWorkerNotFound, got %v", err)
	}
	err = db.DeleteWorker(ctx, "no-such-worker")
	if !errors.Is(err, store.ErrWorkerNotFound) {
		t.Fatalf("delete: expected ErrWorkerNotFound, got %v", err)
	}
	_, err = db.GetWorkerByName(ctx, wsID, "no-such-name")
	if !errors.Is(err, store.ErrWorkerNotFound) {
		t.Fatalf("get-by-name: expected ErrWorkerNotFound, got %v", err)
	}
}
