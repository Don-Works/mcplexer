package admin_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/workers/admin"
)

func TestWaitForDelegationValidation(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	ctx := context.Background()

	t.Run("empty delegation_id", func(t *testing.T) {
		_, err := svc.WaitForDelegation(ctx, admin.WaitForDelegationInput{})
		if err == nil {
			t.Fatal("expected error for empty delegation_id")
		}
		if !strings.Contains(err.Error(), "delegation_id required") {
			t.Errorf("error = %q, want 'delegation_id required'", err.Error())
		}
		if !strings.Contains(err.Error(), `{"delegation_id":"`) {
			t.Errorf("error should include usage example, got %q", err.Error())
		}
	})

	t.Run("timeout exceeds max", func(t *testing.T) {
		_, err := svc.WaitForDelegation(ctx, admin.WaitForDelegationInput{
			DelegationID:   "del-test",
			TimeoutSeconds: 601,
		})
		if err == nil {
			t.Fatal("expected error for timeout > max")
		}
		if !strings.Contains(err.Error(), "max 600") {
			t.Errorf("error = %q, want timeout max message", err.Error())
		}
	})

	t.Run("poll interval below min", func(t *testing.T) {
		_, err := svc.WaitForDelegation(ctx, admin.WaitForDelegationInput{
			DelegationID:   "del-test",
			PollIntervalMS: 499,
		})
		if err == nil {
			t.Fatal("expected error for poll interval below min")
		}
		if !strings.Contains(err.Error(), "min 500") {
			t.Errorf("error = %q, want poll interval min message", err.Error())
		}
	})

	t.Run("poll interval above max", func(t *testing.T) {
		_, err := svc.WaitForDelegation(ctx, admin.WaitForDelegationInput{
			DelegationID:   "del-test",
			PollIntervalMS: 10001,
		})
		if err == nil {
			t.Fatal("expected error for poll interval above max")
		}
		if !strings.Contains(err.Error(), "max 10000") {
			t.Errorf("error = %q, want poll interval max message", err.Error())
		}
	})

	t.Run("not found", func(t *testing.T) {
		_, err := svc.WaitForDelegation(ctx, admin.WaitForDelegationInput{
			DelegationID:   "del-no-such-id",
			TimeoutSeconds: 1,
			PollIntervalMS: 500,
		})
		if err == nil {
			t.Fatal("expected error for nonexistent delegation")
		}
		if !strings.Contains(err.Error(), "not found") {
			t.Errorf("error = %q, want 'not found'", err.Error())
		}
	})
}

func TestWaitForDelegationFindsTerminal(t *testing.T) {
	svc, db, wsID, scopeID := newTestService(t)
	ctx := context.Background()

	out, err := svc.Delegate(ctx, admin.DelegationInput{
		WorkspaceID:         wsID,
		Objective:           "Test terminal delegation.",
		ModelProvider:       "openai",
		ModelID:             "gpt-4o",
		SecretScopeID:       scopeID,
		MaxWallClockSeconds: 30,
		ReviewRequired:      boolPtr(false),
	})
	if err != nil {
		t.Fatalf("Delegate: %v", err)
	}

	run := waitForDelegationRun(t, db, out.Dispatches[0].WorkerID)
	if err := db.UpdateWorkerRunStatus(ctx, run.ID, store.WorkerRunFinalize{
		Status:             "success",
		FinishedAt:         time.Now().UTC(),
		InputTokens:        42,
		OutputTokens:       17,
		CostUSD:            0.01,
		ToolCallsCount:     3,
		OutputText:         "all good",
		MeshMessageIDsJSON: "[]",
		AuditRecordIDsJSON: "[]",
	}); err != nil {
		t.Fatalf("finalize run: %v", err)
	}

	result, err := svc.WaitForDelegation(ctx, admin.WaitForDelegationInput{
		DelegationID:   out.DelegationID,
		WorkspaceID:    wsID,
		TimeoutSeconds: 5,
		PollIntervalMS: 500,
	})
	if err != nil {
		t.Fatalf("WaitForDelegation: %v", err)
	}
	if result.Status != "success" {
		t.Errorf("status = %q, want success", result.Status)
	}
	if result.TimedOut {
		t.Error("TimedOut should be false for a terminal delegation")
	}
	if result.InputTokens != 42 {
		t.Errorf("input_tokens = %d, want 42", result.InputTokens)
	}
	if result.OutputTokens != 17 {
		t.Errorf("output_tokens = %d, want 17", result.OutputTokens)
	}
	if result.CostUSD != 0.01 {
		t.Errorf("cost_usd = %f, want 0.01", result.CostUSD)
	}
	if result.ToolCalls != 3 {
		t.Errorf("tool_calls = %d, want 3", result.ToolCalls)
	}
}

func TestWaitForDelegationTimesOut(t *testing.T) {
	svc, _, wsID, scopeID := newTestService(t)
	ctx := context.Background()

	out, err := svc.Delegate(ctx, admin.DelegationInput{
		WorkspaceID:         wsID,
		Objective:           "Test timeout - run never completes.",
		ModelProvider:       "openai",
		ModelID:             "gpt-4o",
		SecretScopeID:       scopeID,
		MaxWallClockSeconds: 30,
		ReviewRequired:      boolPtr(false),
	})
	if err != nil {
		t.Fatalf("Delegate: %v", err)
	}

	result, err := svc.WaitForDelegation(ctx, admin.WaitForDelegationInput{
		DelegationID:   out.DelegationID,
		WorkspaceID:    wsID,
		TimeoutSeconds: 1,
		PollIntervalMS: 500,
	})
	if err != nil {
		t.Fatalf("WaitForDelegation: %v", err)
	}
	if !result.TimedOut {
		t.Error("TimedOut should be true when run never completes")
	}
}

// TestWaitForDelegationFreshDispatchDoesNotPrematureNeedsReview verifies the
// core bugfix: a freshly created delegation with review_required=true must
// report a non-terminal status (dispatched) so that wait_for_delegation does
// not return needs_review before any worker run has started.
func TestWaitForDelegationFreshDispatchDoesNotPrematureNeedsReview(t *testing.T) {
	svc, _, wsID, scopeID := newTestService(t)
	ctx := context.Background()

	out, err := svc.Delegate(ctx, admin.DelegationInput{
		WorkspaceID:         wsID,
		Objective:           "Fresh dispatch must not be needs_review.",
		ModelProvider:       "openai",
		ModelID:             "gpt-4o",
		SecretScopeID:       scopeID,
		MaxWallClockSeconds: 30,
		ReviewRequired:      boolPtr(true),
	})
	if err != nil {
		t.Fatalf("Delegate: %v", err)
	}

	result, err := svc.WaitForDelegation(ctx, admin.WaitForDelegationInput{
		DelegationID:   out.DelegationID,
		WorkspaceID:    wsID,
		TimeoutSeconds: 1,
		PollIntervalMS: 500,
	})
	if err != nil {
		t.Fatalf("WaitForDelegation: %v", err)
	}
	if !result.TimedOut {
		t.Error("expected timeout because no run was ever started")
	}
	if result.Status == "needs_review" {
		t.Fatalf("status = %q on fresh dispatch; must not be needs_review before any run exists", result.Status)
	}
	if result.Status != "dispatched" {
		t.Logf("status = %q (acceptable non-terminal for no-run case)", result.Status)
	}
}

// TestWaitForDelegationFindsTerminalNeedsReview covers the positive case:
// after a run exists and completes, with review_required and no review yet,
// wait_for_delegation must return the terminal needs_review status.
func TestWaitForDelegationFindsTerminalNeedsReview(t *testing.T) {
	svc, db, wsID, scopeID := newTestService(t)
	ctx := context.Background()

	out, err := svc.Delegate(ctx, admin.DelegationInput{
		WorkspaceID:         wsID,
		Objective:           "Test needs_review terminal after run.",
		ModelProvider:       "openai",
		ModelID:             "gpt-4o",
		SecretScopeID:       scopeID,
		MaxWallClockSeconds: 30,
		ReviewRequired:      boolPtr(true),
	})
	if err != nil {
		t.Fatalf("Delegate: %v", err)
	}

	run := waitForDelegationRun(t, db, out.Dispatches[0].WorkerID)
	if err := db.UpdateWorkerRunStatus(ctx, run.ID, store.WorkerRunFinalize{
		Status:             "success",
		FinishedAt:         time.Now().UTC(),
		InputTokens:        10,
		OutputTokens:       5,
		CostUSD:            0,
		ToolCallsCount:     0,
		OutputText:         "done",
		MeshMessageIDsJSON: "[]",
		AuditRecordIDsJSON: "[]",
	}); err != nil {
		t.Fatalf("finalize run: %v", err)
	}

	result, err := svc.WaitForDelegation(ctx, admin.WaitForDelegationInput{
		DelegationID:   out.DelegationID,
		WorkspaceID:    wsID,
		TimeoutSeconds: 5,
		PollIntervalMS: 500,
	})
	if err != nil {
		t.Fatalf("WaitForDelegation: %v", err)
	}
	if result.Status != "needs_review" {
		t.Errorf("status = %q, want needs_review (terminal after run, unreviewed)", result.Status)
	}
	if result.TimedOut {
		t.Error("TimedOut should be false when delegation reached needs_review")
	}
}
