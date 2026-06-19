// lease_stamp_test.go — regression coverage for the lease auto-stamp in
// Service.Update and Service.Heartbeat.
//
// The high-severity bug these tests pin (service.go lease auto-stamp):
// the stamp was gated on the LITERAL `t.Status == "doing"` while the
// auto-claim path and Claim honour the workspace vocabulary's
// kind="working" statuses via isWorkingStatus. A task claimed into a
// CUSTOM working status (e.g. vocab "in_progress") therefore got an
// assignee but NEVER a lease — the next passive sweep reclaimed it as a
// "no-lease working zombie", and hasActiveLocalLease returned false so a
// converging peer push could stomp the live work. The fix swaps the
// literal checks for isWorkingStatus, keeping it in lock-step with the
// store's taskWorkingStatusPredicate.
package tasks_test

import (
	"context"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/tasks"
)

// TestLeaseStampOnWorkingStatus is the load-bearing regression: a claim
// or status transition into ANY working-kind status must stamp a lease,
// while a transition into a non-working status must not. The table's
// custom-vocab "in_progress" row is the case that fails with the
// pre-fix literal-"doing" gate.
func TestLeaseStampOnWorkingStatus(t *testing.T) {
	cases := []struct {
		name       string
		vocab      *store.TaskStatusVocab // optional vocab entry to declare
		status     string                 // status to claim/transition into
		wantLeased bool
	}{
		{
			name:       "literal doing fallback (no vocab) stamps lease",
			status:     "doing",
			wantLeased: true,
		},
		{
			name:       "custom working-kind status stamps lease",
			vocab:      &store.TaskStatusVocab{StatusText: "in_progress", Kind: "working"},
			status:     "in_progress",
			wantLeased: true,
		},
		{
			name:       "non-working status does not stamp lease",
			vocab:      &store.TaskStatusVocab{StatusText: "blocked", Kind: "blocked"},
			status:     "blocked",
			wantLeased: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			svc, db, wsID := newSvc(t)
			if tc.vocab != nil {
				tc.vocab.WorkspaceID = wsID
				if err := db.UpsertTaskStatusVocab(ctx, tc.vocab); err != nil {
					t.Fatalf("UpsertTaskStatusVocab: %v", err)
				}
			}
			task, err := svc.Create(ctx, tasks.CreateOptions{
				WorkspaceID: wsID, Title: "lease subject", CreatedBySessionID: "agent-a",
			})
			if err != nil {
				t.Fatalf("Create: %v", err)
			}

			before := time.Now().UTC()
			got, err := svc.Claim(ctx, wsID, task.ID, tc.status, "owner-session", "")
			if err != nil {
				t.Fatalf("Claim: %v", err)
			}
			after := time.Now().UTC().Add(tasks.LeaseTTL)

			if !tc.wantLeased {
				if got.LeaseExpiresAt != nil {
					t.Fatalf("expected NO lease for non-working status %q, got %v",
						tc.status, got.LeaseExpiresAt)
				}
				return
			}
			if got.LeaseExpiresAt == nil {
				t.Fatalf("expected a lease stamped for working status %q, got nil — "+
					"the literal-\"doing\" gate would fail here", tc.status)
			}
			// Lease should be ~now+LeaseTTL: not before the call started and
			// not after the call's expected expiry window.
			exp := *got.LeaseExpiresAt
			if exp.Before(before.Add(tasks.LeaseTTL).Add(-time.Second)) || exp.After(after.Add(time.Second)) {
				t.Errorf("lease %v outside expected window [%v, %v]",
					exp, before.Add(tasks.LeaseTTL), after)
			}
		})
	}
}

// TestLeaseStampViaUpdateCustomWorkingStatus mirrors the Claim case but
// drives it through a bare Update Status patch with an explicit
// assignee, exercising the assigneeJustSet branch of the stamp.
func TestLeaseStampViaUpdateCustomWorkingStatus(t *testing.T) {
	ctx := context.Background()
	svc, db, wsID := newSvc(t)
	if err := db.UpsertTaskStatusVocab(ctx, &store.TaskStatusVocab{
		WorkspaceID: wsID, StatusText: "in_progress", Kind: "working",
	}); err != nil {
		t.Fatalf("UpsertTaskStatusVocab: %v", err)
	}
	task, _ := svc.Create(ctx, tasks.CreateOptions{
		WorkspaceID: wsID, Title: "via update", CreatedBySessionID: "agent-a",
	})
	status := "in_progress"
	got, err := svc.Update(ctx, wsID, task.ID, tasks.UpdatePatch{
		Status:             &status,
		Assignee:           &tasks.Assignee{SessionID: "owner-session"},
		UpdatedBySessionID: "owner-session",
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got.LeaseExpiresAt == nil {
		t.Fatalf("expected lease stamped on Update into custom working status, got nil")
	}
}

// TestServiceHeartbeat covers the Service.Heartbeat primitive end to
// end: argument guards, the workspace gate, the non-assignee silent
// no-op, and the owning-session lease extension.
func TestServiceHeartbeat(t *testing.T) {
	t.Run("empty id is an error", func(t *testing.T) {
		ctx := context.Background()
		svc, _, wsID := newSvc(t)
		if err := svc.Heartbeat(ctx, wsID, "", "owner-session"); err == nil {
			t.Fatalf("expected error for empty id")
		}
	})

	t.Run("empty session is an error", func(t *testing.T) {
		ctx := context.Background()
		svc, _, wsID := newSvc(t)
		task, _ := svc.Create(ctx, tasks.CreateOptions{
			WorkspaceID: wsID, Title: "hb", CreatedBySessionID: "agent-a",
		})
		if err := svc.Heartbeat(ctx, wsID, task.ID, ""); err == nil {
			t.Fatalf("expected error for empty session id")
		}
	})

	t.Run("cross-workspace id returns ErrNotFound", func(t *testing.T) {
		ctx := context.Background()
		svc, _, wsID := newSvc(t)
		task, _ := svc.Create(ctx, tasks.CreateOptions{
			WorkspaceID: wsID, Title: "hb", CreatedBySessionID: "agent-a",
		})
		err := svc.Heartbeat(ctx, "some-other-workspace", task.ID, "owner-session")
		if err != store.ErrNotFound {
			t.Fatalf("expected ErrNotFound for cross-workspace heartbeat, got %v", err)
		}
	})

	t.Run("wrong session is a silent no-op", func(t *testing.T) {
		ctx := context.Background()
		svc, db, wsID := newSvc(t)
		task, _ := svc.Create(ctx, tasks.CreateOptions{
			WorkspaceID: wsID, Title: "hb", CreatedBySessionID: "agent-a",
		})
		if _, err := svc.Claim(ctx, wsID, task.ID, "", "owner-session", ""); err != nil {
			t.Fatalf("claim: %v", err)
		}
		owned, _ := db.GetTask(ctx, task.ID)
		leaseBefore := *owned.LeaseExpiresAt
		// Backdate so we'd notice an unexpected bump.
		if _, err := db.HeartbeatTask(ctx, task.ID, "owner-session", -1*time.Hour); err != nil {
			t.Fatalf("backdate: %v", err)
		}
		backdated, _ := db.GetTask(ctx, task.ID)

		// A heartbeat from a session that does NOT own the row must return
		// nil (silent no-op) and must NOT extend the lease.
		if err := svc.Heartbeat(ctx, wsID, task.ID, "intruder-session"); err != nil {
			t.Fatalf("non-assignee heartbeat should be a silent no-op, got %v", err)
		}
		after, _ := db.GetTask(ctx, task.ID)
		if !after.LeaseExpiresAt.Equal(*backdated.LeaseExpiresAt) {
			t.Errorf("non-assignee heartbeat must not bump the lease: before=%v after=%v",
				backdated.LeaseExpiresAt, after.LeaseExpiresAt)
		}
		_ = leaseBefore
	})

	t.Run("owning session extends the lease", func(t *testing.T) {
		ctx := context.Background()
		svc, db, wsID := newSvc(t)
		task, _ := svc.Create(ctx, tasks.CreateOptions{
			WorkspaceID: wsID, Title: "hb", CreatedBySessionID: "agent-a",
		})
		if _, err := svc.Claim(ctx, wsID, task.ID, "", "owner-session", ""); err != nil {
			t.Fatalf("claim: %v", err)
		}
		// Backdate the lease so an extension is observable as moving the
		// expiry from the past into the future.
		if _, err := db.HeartbeatTask(ctx, task.ID, "owner-session", -1*time.Hour); err != nil {
			t.Fatalf("backdate: %v", err)
		}
		before := time.Now().UTC()
		if err := svc.Heartbeat(ctx, wsID, task.ID, "owner-session"); err != nil {
			t.Fatalf("owning heartbeat: %v", err)
		}
		got, _ := db.GetTask(ctx, task.ID)
		if got.LeaseExpiresAt == nil {
			t.Fatalf("expected lease after heartbeat, got nil")
		}
		if !got.LeaseExpiresAt.After(before) {
			t.Errorf("owning heartbeat must push the lease into the future, got %v", got.LeaseExpiresAt)
		}
	})
}

// TestServiceHeartbeatDoesNotFireBrainHook pins that a heartbeat — a
// pure lease bump — does NOT trip the dual-write BrainHook. The
// heartbeat publishes a lightweight SSE update but must stay quiet on
// the on-disk-serialize path (it changes only the volatile lease
// window, not durable task content), otherwise every 5-minute heartbeat
// would churn the canonical .md files.
func TestServiceHeartbeatDoesNotFireBrainHook(t *testing.T) {
	ctx := context.Background()
	svc, db, wsID := newSvc(t)
	hook := newFakeBrainHook()
	svc.SetBrainHook(hook)

	task, _ := svc.Create(ctx, tasks.CreateOptions{
		WorkspaceID: wsID, Title: "hb", CreatedBySessionID: "agent-a",
	})
	if _, err := svc.Claim(ctx, wsID, task.ID, "", "owner-session", ""); err != nil {
		t.Fatalf("claim: %v", err)
	}
	// Backdate so the heartbeat does real work (bumps a past lease).
	if _, err := db.HeartbeatTask(ctx, task.ID, "owner-session", -1*time.Hour); err != nil {
		t.Fatalf("backdate: %v", err)
	}

	before := hook.writeCount(task.ID)
	if err := svc.Heartbeat(ctx, wsID, task.ID, "owner-session"); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	if got := hook.writeCount(task.ID); got != before {
		t.Errorf("heartbeat must not fire the brain hook: writeCount before=%d after=%d", before, got)
	}
}
