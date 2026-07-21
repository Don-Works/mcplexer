package admin_test

import (
	"context"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/workers/admin"
)

func batchItem(wsID, objective string) admin.DelegationInput {
	return admin.DelegationInput{
		WorkspaceID:         wsID,
		Objective:           objective,
		ModelProvider:       "grok_cli",
		ModelID:             "grok-build",
		WorkerIsolation:     "none",
		MaxWallClockSeconds: 30,
	}
}

func TestDelegateBatchDispatchesEachItem(t *testing.T) {
	t.Setenv("MCPLEXER_ALLOW_GROK_CLI", "1")
	svc, _, wsID, _ := newTestService(t)
	ctx := context.Background()

	out, err := svc.DelegateBatch(ctx, admin.BatchDelegationInput{
		SharedRepoBrief: "MCPlexer: Go MCP gateway.",
		Delegations: []admin.DelegationInput{
			batchItem(wsID, "Summarise the gateway package."),
			batchItem(wsID, "Summarise the store package."),
		},
	})
	if err != nil {
		t.Fatalf("DelegateBatch: %v", err)
	}
	if !strings.HasPrefix(out.BatchID, "batch-") {
		t.Fatalf("batch id = %q", out.BatchID)
	}
	if len(out.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(out.Items))
	}
	for i, item := range out.Items {
		if item.Error != "" {
			t.Fatalf("item %d errored: %s", i, item.Error)
		}
		if len(item.Output.Dispatches) == 0 {
			t.Fatalf("item %d dispatched no workers", i)
		}
		// Shared brief flowed into each item (brief tokens > 0).
		if item.Output.BriefTokens == 0 {
			t.Fatalf("item %d missing shared brief injection", i)
		}
	}
}

func TestDelegateBatchFailFastOnInvalidItem(t *testing.T) {
	svc, db, wsID, _ := newTestService(t)
	ctx := context.Background()

	bad := batchItem(wsID, "") // empty objective fails normalize
	_, err := svc.DelegateBatch(ctx, admin.BatchDelegationInput{
		Delegations: []admin.DelegationInput{batchItem(wsID, "valid"), bad},
	})
	if err == nil || !strings.Contains(err.Error(), "delegation[1]") {
		t.Fatalf("err = %v, want fail-fast on item 1", err)
	}
	// Fail-fast: no workers created for the valid item either.
	workers, listErr := db.ListWorkers(ctx, wsID, false)
	if listErr != nil {
		t.Fatal(listErr)
	}
	if len(workers) != 0 {
		t.Fatalf("fail-fast must create no workers, got %d", len(workers))
	}
}

func TestDelegateBatchCrossItemOverlapWarning(t *testing.T) {
	svc, _, wsID, scopeID := newTestService(t)
	ctx := context.Background()

	mk := func(obj string) admin.DelegationInput {
		return admin.DelegationInput{
			WorkspaceID: wsID, Objective: obj,
			ModelProvider: "anthropic", ModelID: "claude-sonnet-4-5", SecretScopeID: scopeID,
			WorkerIsolation: "worktree", TouchesFiles: []string{"internal/a.go"},
			ToolAllowlistJSON: `["mcpx__workspace_write_file"]`, MaxWallClockSeconds: 30,
		}
	}
	a := mk("Edit the shared file.")
	b := mk("Also edit the shared file.")

	out, err := svc.DelegateBatch(ctx, admin.BatchDelegationInput{
		Delegations: []admin.DelegationInput{a, b},
	})
	if err != nil {
		t.Fatalf("DelegateBatch: %v", err)
	}
	if len(out.Warnings) != 1 || !strings.Contains(out.Warnings[0], "internal/a.go") {
		t.Fatalf("cross-item overlap warning missing: %v", out.Warnings)
	}
}

func TestDelegateBatchRejectsExcessTotalDispatches(t *testing.T) {
	t.Setenv("MCPLEXER_ALLOW_GROK_CLI", "1")
	svc, _, wsID, _ := newTestService(t)
	ctx := context.Background()

	// 7 items x parallelism 10 = 70 dispatches > the 60 aggregate cap,
	// even though each item is under the per-delegation 20 cap and the
	// batch is under the 20-item cap.
	items := make([]admin.DelegationInput, 7)
	for i := range items {
		items[i] = batchItem(wsID, "fan out")
		items[i].Parallelism = 10
	}
	_, err := svc.DelegateBatch(ctx, admin.BatchDelegationInput{Delegations: items})
	if err == nil || !strings.Contains(err.Error(), "would dispatch") {
		t.Fatalf("err = %v, want aggregate-dispatch rejection", err)
	}
}

func TestDelegateBatchRejectsEmptyAndOversize(t *testing.T) {
	svc, _, wsID, _ := newTestService(t)
	ctx := context.Background()

	if _, err := svc.DelegateBatch(ctx, admin.BatchDelegationInput{}); err == nil {
		t.Fatal("empty batch accepted")
	}
	big := make([]admin.DelegationInput, 21)
	for i := range big {
		big[i] = batchItem(wsID, "x")
	}
	if _, err := svc.DelegateBatch(ctx, admin.BatchDelegationInput{Delegations: big}); err == nil {
		t.Fatal("oversize batch accepted")
	}
}
