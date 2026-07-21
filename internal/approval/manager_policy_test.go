package approval

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/google/uuid"
)

func TestManager_PolicyDeny_AutoResolvesAfterGrace(t *testing.T) {
	s := newMemStore()
	mgr := NewManager(s, NewBus())
	mgr.SetPolicyResolver(&PolicyResolver{Policy: PolicyDeny}, 10*time.Millisecond)

	a := &store.ToolApproval{
		ID:               uuid.NewString(),
		RequestSessionID: "sess-1",
		ToolName:         "github__delete_repo",
		Justification:    "test",
		TimeoutSec:       5,
		Surface:          "mcp",
	}

	approved, err := mgr.RequestApproval(context.Background(), a)
	if err != nil {
		t.Fatalf("RequestApproval: %v", err)
	}
	if approved {
		t.Error("expected denied")
	}
	rec, _ := s.GetToolApproval(context.Background(), a.ID)
	if rec.Status != "denied" {
		t.Errorf("status = %q, want denied", rec.Status)
	}
	if rec.ApproverType != "afk-policy" {
		t.Errorf("approver_type = %q, want afk-policy", rec.ApproverType)
	}
}

func TestManager_PolicyQueue_DoesNotAutoResolve(t *testing.T) {
	s := newMemStore()
	mgr := NewManager(s, NewBus())
	mgr.SetPolicyResolver(&PolicyResolver{Policy: PolicyQueue}, 5*time.Millisecond)

	a := &store.ToolApproval{
		ID:               uuid.NewString(),
		RequestSessionID: "sess-1",
		ToolName:         "github__create_issue",
		Justification:    "test",
		TimeoutSec:       1, // short timeout — but we resolve manually first
		Surface:          "mcp",
	}

	done := make(chan struct{})
	var approved atomic.Bool
	go func() {
		ok, _ := mgr.RequestApproval(context.Background(), a)
		approved.Store(ok)
		close(done)
	}()

	// Wait past the grace period; PolicyQueue should leave it pending.
	time.Sleep(50 * time.Millisecond)

	rec, _ := s.GetToolApproval(context.Background(), a.ID)
	if rec.Status != "pending" {
		t.Fatalf("after grace, status = %q, want still pending", rec.Status)
	}

	// Resolve normally.
	if err := mgr.Resolve(a.ID, "sess-2", "dashboard", "human approved", true); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	<-done
	if !approved.Load() {
		t.Error("expected approved=true after manual resolve")
	}
}

func TestManager_PolicyTrustedAllow_AutoApproves(t *testing.T) {
	s := newMemStore()
	mgr := NewManager(s, NewBus())
	rules := []store.ApprovalRule{
		{
			ID:       "rule-1",
			Surface:  "mcp",
			Pattern:  "github__create_issue",
			Decision: "allow",
			Priority: 10,
		},
	}
	mgr.SetPolicyResolver(&PolicyResolver{
		Policy: PolicyTrustedAllow,
		Rules:  rules,
	}, 10*time.Millisecond)

	a := &store.ToolApproval{
		ID:               uuid.NewString(),
		RequestSessionID: "sess-1",
		ToolName:         "github__create_issue",
		Justification:    "test",
		TimeoutSec:       5,
		Surface:          "mcp",
	}
	approved, err := mgr.RequestApproval(context.Background(), a)
	if err != nil {
		t.Fatalf("RequestApproval: %v", err)
	}
	if !approved {
		t.Error("expected approved=true via trusted-allowlist")
	}
}

func TestManager_PolicyTrustedAllow_NoMatchKeepsPending(t *testing.T) {
	s := newMemStore()
	mgr := NewManager(s, NewBus())
	// Rule that won't match.
	rules := []store.ApprovalRule{
		{ID: "rule-1", Surface: "shell", Pattern: "*", Decision: "allow", Priority: 10},
	}
	mgr.SetPolicyResolver(&PolicyResolver{
		Policy: PolicyTrustedAllow,
		Rules:  rules,
	}, 5*time.Millisecond)

	a := &store.ToolApproval{
		ID:               uuid.NewString(),
		RequestSessionID: "sess-1",
		ToolName:         "github__create_issue",
		Justification:    "test",
		TimeoutSec:       1,
		Surface:          "mcp",
	}

	// Should time out (no rule match → keep pending → existing timer fires).
	approved, _ := mgr.RequestApproval(context.Background(), a)
	if approved {
		t.Error("expected approved=false on timeout-no-match")
	}
	rec, _ := s.GetToolApproval(context.Background(), a.ID)
	if rec.Status != "timeout" {
		t.Errorf("status = %q, want timeout", rec.Status)
	}
}

func TestManager_PolicyMeshPeer_AutoApproves(t *testing.T) {
	s := newMemStore()
	mgr := NewManager(s, NewBus())
	fake := &fakePeerApprover{approved: true, reason: "peer ok"}
	mgr.SetPolicyResolver(&PolicyResolver{
		Policy:       PolicyMeshPeer,
		PeerApprover: fake,
		TrustedPeers: []string{"peer-A"},
	}, 10*time.Millisecond)

	a := &store.ToolApproval{
		ID:               uuid.NewString(),
		RequestSessionID: "sess-1",
		ToolName:         "github__delete_repo",
		Justification:    "test",
		TimeoutSec:       5,
		Surface:          "mcp",
	}
	approved, err := mgr.RequestApproval(context.Background(), a)
	if err != nil {
		t.Fatalf("RequestApproval: %v", err)
	}
	if !approved {
		t.Error("expected approved=true via mesh peer")
	}
	if fake.calls != 1 {
		t.Errorf("expected 1 peer call, got %d", fake.calls)
	}
}

// TestManager_PolicyTrustedAllow_ApproverAttributed verifies that when a rule
// auto-approves, the approver_session_id in the DB is set to "rule:<id>" so
// the dashboard can show exactly which rule fired. This is the structured
// attribution path for the "allow + audit everything" wildcard rule.
func TestManager_PolicyTrustedAllow_ApproverAttributed(t *testing.T) {
	s := newMemStore()
	mgr := NewManager(s, NewBus())
	rules := []store.ApprovalRule{
		{
			ID:       "rule-audit-all",
			Surface:  "shell",
			Pattern:  "*",
			Decision: "allow",
			Priority: 999,
		},
	}
	mgr.SetPolicyResolver(&PolicyResolver{
		Policy: PolicyTrustedAllow,
		Rules:  rules,
	}, 10*time.Millisecond)

	a := &store.ToolApproval{
		ID:               uuid.NewString(),
		RequestSessionID: "sess-1",
		ToolName:         "bash",
		Justification:    "test",
		TimeoutSec:       5,
		Surface:          "shell",
	}
	approved, err := mgr.RequestApproval(context.Background(), a)
	if err != nil {
		t.Fatalf("RequestApproval: %v", err)
	}
	if !approved {
		t.Error("expected approved=true via wildcard rule")
	}
	rec, err := s.GetToolApproval(context.Background(), a.ID)
	if err != nil {
		t.Fatalf("GetToolApproval: %v", err)
	}
	if rec.ApproverType != "afk-policy" {
		t.Errorf("approver_type = %q, want afk-policy", rec.ApproverType)
	}
	if rec.ApproverSessionID != "rule:rule-audit-all" {
		t.Errorf("approver_session_id = %q, want rule:rule-audit-all", rec.ApproverSessionID)
	}
}

// TestManager_PolicyTrustedAllow_SyncResolveSuppressesPending verifies the
// load-bearing UX fix: when a rule auto-approves, the bus emits ONLY a
// "resolved" event (no transient "pending"), and the notify publisher fires
// "approval_approved" with priority="low" — not the old "approval_pending"
// at priority="high" that triggered a flash strip for every Bash command.
//
// The dashboard's message_id dedup used to silently drop the resolved
// follow-up, so the tray row would say "Approval requested" forever even
// after the underlying row resolved. Suppressing the pending publish at the
// source fixes both bugs in one go.
func TestManager_PolicyTrustedAllow_SyncResolveSuppressesPending(t *testing.T) {
	s := newMemStore()
	bus := NewBus()
	sub := bus.Subscribe()
	defer bus.Unsubscribe(sub)
	notif := &fakeNotifyPublisher{}
	mgr := NewManager(s, bus)
	mgr.SetNotifyPublisher(notif)

	rules := []store.ApprovalRule{
		{ID: "wildcard", Surface: "shell", Pattern: "*", Decision: "allow", Priority: 999},
	}
	mgr.SetPolicyResolver(&PolicyResolver{
		Policy: PolicyTrustedAllow,
		Rules:  rules,
	}, 10*time.Millisecond)

	a := &store.ToolApproval{
		ID:               uuid.NewString(),
		RequestSessionID: "sess-1",
		ToolName:         "bash",
		Surface:          "shell",
		Justification:    "ls -la",
		TimeoutSec:       5,
	}

	approved, err := mgr.RequestApproval(context.Background(), a)
	if err != nil {
		t.Fatalf("RequestApproval: %v", err)
	}
	if !approved {
		t.Fatal("expected approved=true via wildcard rule")
	}

	// Store row should be resolved with rule attribution.
	rec, _ := s.GetToolApproval(context.Background(), a.ID)
	if rec.Status != "approved" {
		t.Errorf("status = %q, want approved", rec.Status)
	}
	if rec.ApproverSessionID != "rule:wildcard" {
		t.Errorf("approver_session_id = %q, want rule:wildcard", rec.ApproverSessionID)
	}

	// Bus must emit ONLY resolved — no transient pending event.
	gotPending, gotResolved := false, false
	deadline := time.After(100 * time.Millisecond)
drain:
	for {
		select {
		case evt := <-sub:
			if evt.Approval != nil && evt.Approval.ID != a.ID {
				continue
			}
			switch evt.Type {
			case "pending":
				gotPending = true
			case "resolved":
				gotResolved = true
				break drain
			}
		case <-deadline:
			break drain
		}
	}
	if gotPending {
		t.Error("bus emitted 'pending' for sync-resolved approval; should be resolved-only")
	}
	if !gotResolved {
		t.Error("bus did not emit 'resolved' for sync-resolved approval")
	}

	// Notify must fire exactly once, as approval_approved.
	calls := notif.calledFor(a.ID)
	if len(calls) != 1 {
		t.Errorf("notify.Publish fired %d times, want exactly 1 (no pending flash)", len(calls))
	}
	if len(calls) > 0 && calls[0].kind != "approval_approved" {
		t.Errorf("notify kind = %q, want approval_approved", calls[0].kind)
	}
}

// TestManager_PolicyTrustedAllow_NoMatchStillPending verifies that the
// sync path doesn't accidentally swallow no-match requests — they MUST
// fall through to the existing async path so a human can still resolve
// them. Regression guard for the sync-resolve fix.
func TestManager_PolicyTrustedAllow_NoMatchStillPending(t *testing.T) {
	s := newMemStore()
	bus := NewBus()
	mgr := NewManager(s, bus)
	notif := &fakeNotifyPublisher{}
	mgr.SetNotifyPublisher(notif)

	// Rule won't match (different surface).
	rules := []store.ApprovalRule{
		{ID: "r1", Surface: "mcp", Pattern: "*", Decision: "allow", Priority: 10},
	}
	mgr.SetPolicyResolver(&PolicyResolver{
		Policy: PolicyTrustedAllow,
		Rules:  rules,
	}, 10*time.Millisecond)

	a := &store.ToolApproval{
		ID:               uuid.NewString(),
		RequestSessionID: "sess-1",
		ToolName:         "bash",
		Surface:          "shell",
		Justification:    "test",
		TimeoutSec:       1,
	}

	approved, _ := mgr.RequestApproval(context.Background(), a)
	if approved {
		t.Error("expected approved=false (no match → pending → timeout)")
	}
	// No-match must produce the normal pending flow's pending notify
	// event (so a human sees it). The resolved event also fires on
	// timeout. So we expect at least the approval_pending kind.
	calls := notif.calledFor(a.ID)
	sawPending := false
	for _, c := range calls {
		if c.kind == "approval_pending" {
			sawPending = true
		}
	}
	if !sawPending {
		t.Error("expected approval_pending notify for no-match (human window must still fire)")
	}
}

func TestManager_NoPolicy_BackwardsCompatible(t *testing.T) {
	// Sanity: with no policy installed, behaviour must match pre-M5.
	s := newMemStore()
	mgr := NewManager(s, NewBus())

	a := &store.ToolApproval{
		ID:               uuid.NewString(),
		RequestSessionID: "sess-1",
		ToolName:         "tool",
		Justification:    "test",
		TimeoutSec:       1,
	}
	approved, _ := mgr.RequestApproval(context.Background(), a)
	if approved {
		t.Error("expected approved=false (timeout) with no policy")
	}
	rec, _ := s.GetToolApproval(context.Background(), a.ID)
	if rec.Status != "timeout" {
		t.Errorf("status = %q, want timeout", rec.Status)
	}
}
