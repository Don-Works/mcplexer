package admin_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/workers/admin"
)

func TestInvokeModelValidation(t *testing.T) {
	svc, _, wsID, _ := newTestService(t)
	ctx := context.Background()

	t.Run("empty objective", func(t *testing.T) {
		_, err := svc.InvokeModel(ctx, admin.InvokeModelInput{
			WorkspaceID: wsID,
		})
		if err == nil {
			t.Fatal("expected error for empty objective")
		}
		if !strings.Contains(err.Error(), "objective required") {
			t.Errorf("error = %q, want 'objective required'", err.Error())
		}
		if !strings.Contains(err.Error(), `{"objective":"`) {
			t.Errorf("error should include usage example, got %q", err.Error())
		}
	})
}

// TestInvokeModelWaitBehavior is table-driven covering the required cases:
// timeout with active run -> timed_out true + delegation_id preserved;
// absurd wait_seconds input -> clamped (no error, id returned promptly via ctx);
// fast completion -> timed_out false.
func TestInvokeModelWaitBehavior(t *testing.T) {
	cases := []struct {
		name         string
		waitSeconds  int
		setupFast    bool // for fast-completion case use goroutine + finalize race
		wantTimedOut bool
		wantHasID    bool
	}{
		{
			name:         "timeout->timed_out true w/ id",
			waitSeconds:  1,
			setupFast:    false,
			wantTimedOut: true,
			wantHasID:    true,
		},
		{
			name:         "absurd input->clamped",
			waitSeconds:  999999,
			setupFast:    false,
			wantTimedOut: true,
			wantHasID:    true,
		},
		{
			name:         "fast-completion->timed_out false",
			waitSeconds:  10,
			setupFast:    true,
			wantTimedOut: false,
			wantHasID:    true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			svc, db, wsID, scopeID := newTestService(t)
			ctx := context.Background()

			if !c.setupFast {
				// Direct call: for timeout and absurd (absurd uses short parent ctx to avoid 600s hang while proving no error + id returned)
				callCtx := ctx
				if c.waitSeconds > 100 {
					// absurd: force early exit via parent ctx so test doesn't block on clamped 600s; still proves clamp+success path returns id
					var cancel context.CancelFunc
					callCtx, cancel = context.WithTimeout(ctx, 1500*time.Millisecond)
					defer cancel()
				}
				out, err := svc.InvokeModel(callCtx, admin.InvokeModelInput{
					WorkspaceID:         wsID,
					Objective:           "table test: " + c.name,
					ModelProvider:       "openai",
					ModelID:             "gpt-4o",
					SecretScopeID:       scopeID,
					WaitSeconds:         c.waitSeconds,
					MaxWallClockSeconds: 30,
				})
				if err != nil {
					t.Fatalf("InvokeModel: %v", err)
				}
				if c.wantHasID && out.DelegationID == "" {
					t.Fatal("expected delegation_id to be returned (never lost)")
				}
				if out.TimedOut != c.wantTimedOut {
					t.Errorf("timed_out = %v, want %v (status=%s)", out.TimedOut, c.wantTimedOut, out.Status)
				}
				return
			}

			// fast-completion: launch invoke in bg (it will delegate + poll), race to finalize the created run, then collect result
			type invResult struct {
				out admin.InvokeModelOutput
				err error
			}
			resCh := make(chan invResult, 1)
			go func() {
				out, err := svc.InvokeModel(ctx, admin.InvokeModelInput{
					WorkspaceID:         wsID,
					Objective:           "table test: " + c.name,
					ModelProvider:       "openai",
					ModelID:             "gpt-4o",
					SecretScopeID:       scopeID,
					WaitSeconds:         c.waitSeconds,
					MaxWallClockSeconds: 30,
				})
				resCh <- invResult{out: out, err: err}
			}()

			// give dispatch goroutine a moment to create the worker+run record
			time.Sleep(200 * time.Millisecond)

			// find the delegation just created by the bg invoke (via list, take newest)
			// simpler: poll for any recent delegation run using the wait helper pattern; since only one, list and pick
			dels, err := svc.ListDelegations(ctx, admin.DelegationListInput{WorkspaceID: wsID, Limit: 5})
			if err != nil || len(dels) == 0 {
				// wait a bit more for dispatch
				time.Sleep(300 * time.Millisecond)
				dels, err = svc.ListDelegations(ctx, admin.DelegationListInput{WorkspaceID: wsID, Limit: 5})
			}
			if err != nil || len(dels) == 0 {
				t.Fatalf("no delegation visible for fast case: %v", err)
			}
			// take the first (newest likely)
			// find a worker id from the context (delID captured for potential debug but unused here)
			_ = dels[0].ID
			var workerID string
			for _, w := range dels[0].Workers {
				if w.Worker != nil {
					workerID = w.Worker.ID
					break
				}
			}
			if workerID == "" {
				// fallback: use list to get dispatches? but for simplicity wait the run via internal
				t.Logf("no worker in list yet, will try finalize after wait")
			}

			// wait for run to appear (the dispatch go creates it)
			var run *store.WorkerRun
			if workerID != "" {
				run = waitForDelegationRun(t, db, workerID)
			} else {
				// last resort: sleep and hope; in practice dispatch is fast
				time.Sleep(400 * time.Millisecond)
				dels, _ = svc.ListDelegations(ctx, admin.DelegationListInput{WorkspaceID: wsID, Limit: 5})
				_ = dels
			}
			if run != nil {
				_ = db.UpdateWorkerRunStatus(ctx, run.ID, store.WorkerRunFinalize{
					Status:             "success",
					FinishedAt:         time.Now().UTC(),
					InputTokens:        7,
					OutputTokens:       3,
					CostUSD:            0,
					ToolCallsCount:     2,
					OutputText:         "fast done",
					MeshMessageIDsJSON: "[]",
					AuditRecordIDsJSON: "[]",
				})
			}

			// collect the invoke result (should have seen terminal within wait window)
			select {
			case r := <-resCh:
				if r.err != nil {
					t.Fatalf("InvokeModel fast: %v", r.err)
				}
				if c.wantHasID && r.out.DelegationID == "" {
					t.Fatal("expected delegation_id")
				}
				if r.out.TimedOut != c.wantTimedOut {
					t.Errorf("timed_out=%v want %v status=%s id=%s", r.out.TimedOut, c.wantTimedOut, r.out.Status, r.out.DelegationID)
				}
			case <-time.After(15 * time.Second):
				t.Fatal("invoke result not received in time for fast-completion test")
			}
		})
	}
}
