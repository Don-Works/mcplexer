package approval

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/google/uuid"
)

// memStore is an in-memory implementation of store.ToolApprovalStore for tests.
type memStore struct {
	mu        sync.Mutex
	approvals map[string]*store.ToolApproval
}

func newMemStore() *memStore {
	return &memStore{approvals: make(map[string]*store.ToolApproval)}
}

func (m *memStore) CreateToolApproval(_ context.Context, a *store.ToolApproval) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if a.ID == "" {
		a.ID = uuid.NewString()
	}
	if a.CreatedAt.IsZero() {
		a.CreatedAt = time.Now().UTC()
	}
	if a.Status == "" {
		a.Status = "pending"
	}
	cp := *a
	m.approvals[a.ID] = &cp
	return nil
}

func (m *memStore) GetToolApproval(_ context.Context, id string) (*store.ToolApproval, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.approvals[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	cp := *a
	return &cp, nil
}

func (m *memStore) ListPendingApprovals(_ context.Context) ([]store.ToolApproval, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []store.ToolApproval
	for _, a := range m.approvals {
		if a.Status == "pending" {
			out = append(out, *a)
		}
	}
	return out, nil
}

func (m *memStore) ListResolvedApprovals(_ context.Context, limit int) ([]store.ToolApproval, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []store.ToolApproval
	for _, a := range m.approvals {
		if a.Status != "pending" {
			out = append(out, *a)
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *memStore) ResolveToolApproval(_ context.Context, id, status, approverSID, approverType, resolution string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.approvals[id]
	if !ok {
		return store.ErrNotFound
	}
	if a.Status != "pending" {
		return store.ErrNotFound
	}
	a.Status = status
	a.ApproverSessionID = approverSID
	a.ApproverType = approverType
	a.Resolution = resolution
	now := time.Now().UTC()
	a.ResolvedAt = &now
	return nil
}

func (m *memStore) ExpirePendingApprovals(_ context.Context, before time.Time) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, a := range m.approvals {
		if a.Status == "pending" && a.CreatedAt.Before(before) {
			a.Status = "timeout"
			now := time.Now().UTC()
			a.ResolvedAt = &now
			n++
		}
	}
	return n, nil
}

func (m *memStore) GetApprovalMetrics(_ context.Context, _, _ time.Time) (*store.ApprovalMetrics, error) {
	return &store.ApprovalMetrics{}, nil
}

func TestRequestApproval_Approved(t *testing.T) {
	s := newMemStore()
	bus := NewBus()
	mgr := NewManager(s, bus)

	a := &store.ToolApproval{
		ID:               uuid.NewString(),
		RequestSessionID: "session-1",
		ToolName:         "github__create_issue",
		Justification:    "need to file a bug",
		TimeoutSec:       5,
	}

	var approved bool
	var err error
	done := make(chan struct{})
	go func() {
		approved, err = mgr.RequestApproval(context.Background(), a)
		close(done)
	}()

	// Wait for the approval to appear in pending.
	time.Sleep(50 * time.Millisecond)

	pending := mgr.ListPending("")
	if len(pending) != 1 {
		t.Fatalf("expected 1 pending, got %d", len(pending))
	}

	if err := mgr.Resolve(a.ID, "session-2", "mcp_agent", "looks good", true); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	<-done
	if err != nil {
		t.Fatalf("RequestApproval: %v", err)
	}
	if !approved {
		t.Error("expected approved=true")
	}
}

func TestRequestApproval_Denied(t *testing.T) {
	s := newMemStore()
	mgr := NewManager(s, NewBus())

	a := &store.ToolApproval{
		ID:               uuid.NewString(),
		RequestSessionID: "session-1",
		ToolName:         "github__delete_repo",
		Justification:    "cleanup",
		TimeoutSec:       5,
	}

	done := make(chan struct{})
	var approved bool
	go func() {
		approved, _ = mgr.RequestApproval(context.Background(), a)
		close(done)
	}()

	time.Sleep(50 * time.Millisecond)
	if err := mgr.Resolve(a.ID, "session-2", "dashboard", "too dangerous", false); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	<-done
	if approved {
		t.Error("expected approved=false")
	}
}

func TestRequestApproval_SelfApproval(t *testing.T) {
	s := newMemStore()
	mgr := NewManager(s, NewBus())

	a := &store.ToolApproval{
		ID:               uuid.NewString(),
		RequestSessionID: "session-1",
		ToolName:         "github__create_issue",
		Justification:    "test",
		TimeoutSec:       5,
	}

	go func() {
		mgr.RequestApproval(context.Background(), a) //nolint:errcheck
	}()

	time.Sleep(50 * time.Millisecond)

	err := mgr.Resolve(a.ID, "session-1", "mcp_agent", "self approve", true)
	if err != ErrSelfApproval {
		t.Fatalf("expected ErrSelfApproval, got %v", err)
	}

	// Dashboard approval from the same MCP session is now ALSO rejected.
	// Previously dashboard short-circuited the self-approval check, which
	// allowed a caller in possession of the API token to self-approve a
	// tool call from the same MCP session.
	err = mgr.Resolve(a.ID, "session-1", "dashboard", "human approved", true)
	if err != ErrSelfApproval {
		t.Fatalf("dashboard self-approval should be rejected, got %v", err)
	}

	// Dashboard approval from a distinct session ID succeeds.
	err = mgr.Resolve(a.ID, "dashboard:abc123", "dashboard", "human approved", true)
	if err != nil {
		t.Fatalf("dashboard resolve from distinct session: %v", err)
	}
}

func TestRequestApproval_Timeout(t *testing.T) {
	s := newMemStore()
	mgr := NewManager(s, NewBus())

	a := &store.ToolApproval{
		ID:               uuid.NewString(),
		RequestSessionID: "session-1",
		ToolName:         "github__create_issue",
		Justification:    "test",
		TimeoutSec:       1, // 1 second timeout
	}

	approved, err := mgr.RequestApproval(context.Background(), a)
	if err != nil {
		t.Fatalf("RequestApproval: %v", err)
	}
	if approved {
		t.Error("expected approved=false on timeout")
	}

	// Verify the DB record was updated.
	rec, _ := s.GetToolApproval(context.Background(), a.ID)
	if rec.Status != "timeout" {
		t.Errorf("status = %q, want timeout", rec.Status)
	}
}

func TestRequestApproval_ContextCancelled(t *testing.T) {
	s := newMemStore()
	mgr := NewManager(s, NewBus())

	ctx, cancel := context.WithCancel(context.Background())

	a := &store.ToolApproval{
		ID:               uuid.NewString(),
		RequestSessionID: "session-1",
		ToolName:         "github__create_issue",
		Justification:    "test",
		TimeoutSec:       60,
	}

	done := make(chan struct{})
	go func() {
		mgr.RequestApproval(ctx, a) //nolint:errcheck
		close(done)
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	rec, _ := s.GetToolApproval(context.Background(), a.ID)
	if rec.Status != "cancelled" {
		t.Errorf("status = %q, want cancelled", rec.Status)
	}
}

func TestConcurrentResolve(t *testing.T) {
	s := newMemStore()
	mgr := NewManager(s, NewBus())

	a := &store.ToolApproval{
		ID:               uuid.NewString(),
		RequestSessionID: "session-1",
		ToolName:         "github__create_issue",
		Justification:    "test",
		TimeoutSec:       5,
	}

	go func() {
		mgr.RequestApproval(context.Background(), a) //nolint:errcheck
	}()

	time.Sleep(50 * time.Millisecond)

	// Race two resolvers.
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			errs[idx] = mgr.Resolve(a.ID, "session-2", "dashboard", "ok", true)
		}(i)
	}
	wg.Wait()

	// Exactly one should succeed, one should fail.
	successes := 0
	for _, err := range errs {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Errorf("expected exactly 1 success, got %d (errs: %v)", successes, errs)
	}
}

func TestListPending_ExcludesSelf(t *testing.T) {
	s := newMemStore()
	mgr := NewManager(s, NewBus())

	a1 := &store.ToolApproval{
		ID:               uuid.NewString(),
		RequestSessionID: "session-1",
		ToolName:         "tool_a",
		TimeoutSec:       60,
	}
	a2 := &store.ToolApproval{
		ID:               uuid.NewString(),
		RequestSessionID: "session-2",
		ToolName:         "tool_b",
		TimeoutSec:       60,
	}

	go func() { mgr.RequestApproval(context.Background(), a1) }() //nolint:errcheck
	go func() { mgr.RequestApproval(context.Background(), a2) }() //nolint:errcheck
	time.Sleep(50 * time.Millisecond)

	// Session-1 should only see session-2's request.
	pending := mgr.ListPending("session-1")
	if len(pending) != 1 {
		t.Fatalf("expected 1 pending, got %d", len(pending))
	}
	if pending[0].RequestSessionID != "session-2" {
		t.Errorf("expected session-2's request, got %s", pending[0].RequestSessionID)
	}

	// Empty string should see both.
	all := mgr.ListPending("")
	if len(all) != 2 {
		t.Errorf("expected 2 pending, got %d", len(all))
	}

	// Cleanup
	mgr.Shutdown()
}

func TestShutdown_CancelsPending(t *testing.T) {
	s := newMemStore()
	mgr := NewManager(s, NewBus())

	a := &store.ToolApproval{
		ID:               uuid.NewString(),
		RequestSessionID: "session-1",
		ToolName:         "github__create_issue",
		Justification:    "test",
		TimeoutSec:       60,
	}

	done := make(chan struct{})
	var approved bool
	go func() {
		approved, _ = mgr.RequestApproval(context.Background(), a)
		close(done)
	}()

	time.Sleep(50 * time.Millisecond)
	mgr.Shutdown()
	<-done

	if approved {
		t.Error("expected approved=false after shutdown")
	}

	rec, _ := s.GetToolApproval(context.Background(), a.ID)
	if rec.Status != "cancelled" {
		t.Errorf("status = %q, want cancelled", rec.Status)
	}
}

// TestPublishExternal_PublishesResolvedEvent verifies that
// PublishExternal — used by kind=mesh_grant_consent (a row that's
// persisted as already-approved and so never goes through the
// pending/Resolve cycle) — fans out a "resolved" ApprovalEvent on the
// bus. This is the signal subscribers (the SSE handler, the dashboard
// Recent History pane) rely on to render consent-recorded rows live.
func TestPublishExternal_PublishesResolvedEvent(t *testing.T) {
	bus := NewBus()
	sub := bus.Subscribe()
	defer bus.Unsubscribe(sub)

	mgr := NewManager(newMemStore(), bus)

	rec := &store.ToolApproval{
		ID:                   "consent-1",
		Status:               "approved",
		Kind:                 "mesh_grant_consent",
		Surface:              "mesh",
		ToolName:             "mesh__grant_peer_scope",
		Summary:              "Granted mesh.skill_request to peer 12D3...",
		OriginatingWorkspace: "ws-alpha",
		Resolution:           "consent recorded",
	}
	mgr.PublishExternal(rec)

	select {
	case evt := <-sub:
		if evt.Type != "resolved" {
			t.Errorf("event type = %q, want resolved", evt.Type)
		}
		if evt.Approval == nil || evt.Approval.ID != "consent-1" {
			t.Errorf("event approval = %+v, want id=consent-1", evt.Approval)
		}
		if evt.Approval.Kind != "mesh_grant_consent" {
			t.Errorf("event kind = %q, want mesh_grant_consent", evt.Approval.Kind)
		}
		if evt.Approval.Summary == "" {
			t.Error("event summary empty; consent UI relies on it")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("PublishExternal did not publish on the bus within 500ms")
	}
}

// TestPublishExternal_NilSafe ensures PublishExternal tolerates a nil
// approval (defensive — the caller in handler_mesh_scope.go shouldn't
// pass nil, but we don't want a panic to take down the gateway).
func TestPublishExternal_NilSafe(t *testing.T) {
	mgr := NewManager(newMemStore(), NewBus())
	// Should not panic; nothing observable to check.
	mgr.PublishExternal(nil)
}

// fakeNotifyPublisher records every Publish call so tests can assert
// notify-side wiring still fires when dangerous mode is on.
type fakeNotifyPublisher struct {
	mu    sync.Mutex
	calls []notifyCall
}

type notifyCall struct {
	messageID string
	kind      string
	tags      string
}

func (f *fakeNotifyPublisher) Publish(messageID, _, _, kind, _, _, _, tags, _ string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, notifyCall{messageID: messageID, kind: kind, tags: tags})
}

func (f *fakeNotifyPublisher) calledFor(id string) []notifyCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []notifyCall
	for _, c := range f.calls {
		if c.messageID == id {
			out = append(out, c)
		}
	}
	return out
}

// TestRequestApproval_DangerousMode covers the global toggle that
// disables every approval gate. Table-driven so the three contractual
// outcomes (off → normal flow, on → instant approve + audit + notify,
// on → no pending leak) all live next to each other.
func TestRequestApproval_DangerousMode(t *testing.T) {
	tests := []struct {
		name           string
		dangerous      bool
		resolveDuring  bool // only meaningful for the "off" case
		wantApproved   bool
		wantStatus     string
		wantResolution string
	}{
		{
			name:           "off_normal_flow",
			dangerous:      false,
			resolveDuring:  true,
			wantApproved:   true,
			wantStatus:     "approved",
			wantResolution: "looks good",
		},
		{
			name:           "on_instant_approve",
			dangerous:      true,
			wantApproved:   true,
			wantStatus:     "approved",
			wantResolution: "dangerous-mode bypass",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newMemStore()
			bus := NewBus()
			sub := bus.Subscribe()
			defer bus.Unsubscribe(sub)
			notif := &fakeNotifyPublisher{}
			mgr := NewManager(s, bus)
			mgr.SetNotifyPublisher(notif)
			mgr.SetDangerousModeProvider(func() bool { return tc.dangerous })

			a := &store.ToolApproval{
				ID:               uuid.NewString(),
				RequestSessionID: "session-1",
				ToolName:         "github__delete_repo",
				Surface:          "mcp",
				Justification:    "blast through",
				TimeoutSec:       5,
			}

			done := make(chan struct{})
			var approved bool
			var reqErr error
			go func() {
				approved, reqErr = mgr.RequestApproval(context.Background(), a)
				close(done)
			}()

			if tc.resolveDuring {
				// Give the goroutine a chance to register in pending.
				time.Sleep(20 * time.Millisecond)
				if err := mgr.Resolve(
					a.ID, "session-2", "mcp_agent", tc.wantResolution, true,
				); err != nil {
					t.Fatalf("Resolve: %v", err)
				}
			}

			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("RequestApproval did not return within 1s")
			}

			if reqErr != nil {
				t.Fatalf("RequestApproval err = %v", reqErr)
			}
			if approved != tc.wantApproved {
				t.Errorf("approved = %v, want %v", approved, tc.wantApproved)
			}

			// Store reflects the resolved state for both branches.
			rec, err := s.GetToolApproval(context.Background(), a.ID)
			if err != nil {
				t.Fatalf("GetToolApproval: %v", err)
			}
			if rec.Status != tc.wantStatus {
				t.Errorf("status = %q, want %q", rec.Status, tc.wantStatus)
			}
			if rec.Resolution != tc.wantResolution {
				t.Errorf("resolution = %q, want %q", rec.Resolution, tc.wantResolution)
			}

			// Dangerous-mode branch must NOT leak a pending entry — the
			// hot path is supposed to short-circuit before pending is
			// populated.
			if tc.dangerous {
				if got := mgr.ListPending(""); len(got) != 0 {
					t.Errorf("dangerous mode left %d pending entries", len(got))
				}
				if rec.ApproverType != "system" {
					t.Errorf("approver_type = %q, want system", rec.ApproverType)
				}
				if rec.ApproverSessionID != "dangerous-mode" {
					t.Errorf("approver_session_id = %q, want dangerous-mode", rec.ApproverSessionID)
				}
				// Notify must still fire so the dashboard tray shows
				// what was bypassed — but only once (the
				// approval_approved event). The earlier "pending"
				// notify publish was removed so the user no longer sees
				// a spurious "Approval requested" flash for every
				// dangerous-mode call.
				calls := notif.calledFor(a.ID)
				if len(calls) != 1 {
					t.Errorf("notify.Publish fired %d times, want exactly 1 (resolved only)", len(calls))
				}
				if len(calls) > 0 && calls[0].kind != "approval_approved" {
					t.Errorf("notify kind = %q, want approval_approved (no pending flash)", calls[0].kind)
				}
				// Bus must surface ONLY the resolved event — the
				// pending intermezzo was removed (it fired an
				// "Approval requested" flash, then the front-end's
				// message_id dedup dropped the resolved follow-up so
				// the tray row never flipped to "approved").
				gotPending, gotResolved := false, false
				deadline := time.After(200 * time.Millisecond)
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
						}
						if gotResolved {
							break drain
						}
					case <-deadline:
						break drain
					}
				}
				if gotPending {
					t.Error("bus emitted 'pending' for dangerous-mode bypass; should be resolved-only")
				}
				if !gotResolved {
					t.Error("bus did not emit 'resolved' for dangerous-mode bypass")
				}
			}
		})
	}
}

// TestRequestApproval_DangerousMode_NoProvider keeps the historical
// behaviour locked in: when no provider has been installed, the manager
// behaves exactly as before (no bypass, request blocks until resolved).
func TestRequestApproval_DangerousMode_NoProvider(t *testing.T) {
	s := newMemStore()
	mgr := NewManager(s, NewBus())
	// Intentionally no SetDangerousModeProvider call.

	a := &store.ToolApproval{
		ID:               uuid.NewString(),
		RequestSessionID: "session-1",
		ToolName:         "github__create_issue",
		TimeoutSec:       5,
	}

	done := make(chan struct{})
	go func() {
		_, _ = mgr.RequestApproval(context.Background(), a)
		close(done)
	}()

	time.Sleep(30 * time.Millisecond)
	// Nil provider must not be treated as "on".
	if got := mgr.ListPending(""); len(got) != 1 {
		t.Fatalf("expected 1 pending (normal flow), got %d", len(got))
	}
	_ = mgr.Resolve(a.ID, "session-2", "mcp_agent", "ok", true)
	<-done
}

func TestRequestApproval_RedactsArgumentsBeforePersistence(t *testing.T) {
	s := newMemStore()
	mgr := NewManager(s, NewBus())
	secret := strings.Join([]string{"ghp", "_aBcDeFgHiJkLmNoPqRsTuVwXyZ0123456789"}, "")
	a := &store.ToolApproval{
		ID:               uuid.NewString(),
		RequestSessionID: "session-1",
		ToolName:         "github__create_issue",
		Arguments:        `{"pat":"` + secret + `"}`,
		TimeoutSec:       5,
	}

	done := make(chan error, 1)
	go func() {
		_, err := mgr.RequestApproval(context.Background(), a)
		done <- err
	}()

	var rec *store.ToolApproval
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		got, err := s.GetToolApproval(context.Background(), a.ID)
		if err == nil {
			rec = got
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if rec == nil {
		t.Fatal("approval was not persisted")
	}
	assertApprovalArgumentsRedacted(t, rec.Arguments, secret)

	if err := mgr.Resolve(a.ID, "session-2", "dashboard", "ok", true); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RequestApproval: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("RequestApproval did not return after resolve")
	}
}

func TestRequestApproval_RedactsArgumentsDangerousMode(t *testing.T) {
	s := newMemStore()
	mgr := NewManager(s, NewBus())
	mgr.SetDangerousModeProvider(func() bool { return true })
	secret := strings.Join([]string{"sk", "-proj-", "abcdefghijklmnopqrstuvwxyz123456"}, "")
	a := &store.ToolApproval{
		ID:               uuid.NewString(),
		RequestSessionID: "session-1",
		ToolName:         "openai__responses_create",
		Arguments:        `{"api_key":"` + secret + `"}`,
		TimeoutSec:       5,
	}

	approved, err := mgr.RequestApproval(context.Background(), a)
	if err != nil {
		t.Fatalf("RequestApproval: %v", err)
	}
	if !approved {
		t.Fatal("expected dangerous mode to approve")
	}
	rec, err := s.GetToolApproval(context.Background(), a.ID)
	if err != nil {
		t.Fatalf("GetToolApproval: %v", err)
	}
	assertApprovalArgumentsRedacted(t, rec.Arguments, secret)
}

func TestRequestApproval_RedactsArgumentsPreDecidedPolicy(t *testing.T) {
	s := newMemStore()
	mgr := NewManager(s, NewBus())
	mgr.SetPolicyResolver(&PolicyResolver{Policy: PolicyDeny}, 10*time.Millisecond)
	secret := "Bearer abcdef0123456789ABCDEF0123456789"
	a := &store.ToolApproval{
		ID:               uuid.NewString(),
		RequestSessionID: "session-1",
		ToolName:         "github__delete_repo",
		Arguments:        `{"authorization":"` + secret + `"}`,
		TimeoutSec:       5,
	}

	approved, err := mgr.RequestApproval(context.Background(), a)
	if err != nil {
		t.Fatalf("RequestApproval: %v", err)
	}
	if approved {
		t.Fatal("expected policy deny")
	}
	rec, err := s.GetToolApproval(context.Background(), a.ID)
	if err != nil {
		t.Fatalf("GetToolApproval: %v", err)
	}
	if rec.Status != "denied" {
		t.Fatalf("status = %q, want denied", rec.Status)
	}
	assertApprovalArgumentsRedacted(t, rec.Arguments, secret)
}

func assertApprovalArgumentsRedacted(t *testing.T, got, secret string) {
	t.Helper()
	if strings.Contains(got, secret) {
		t.Fatalf("approval arguments still contain secret %q: %s", secret, got)
	}
	if !strings.Contains(got, "[REDACTED]") {
		t.Fatalf("approval arguments missing redaction marker: %s", got)
	}
}
