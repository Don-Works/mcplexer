// offer_test.go — coverage of the Phase 3 inbound offer pipeline:
// scope check, throttle, staleness, persistence + state mapping.
//
// The outbound Offer/AssignRemote paths and Phase B fetch are
// integration-tested in internal/p2p/task_share_p2p_test.go (they
// require a live libp2p host); here we exercise the service layer's
// decision logic via a fake peerScope lookup so we can stress all the
// gate combinations without spinning up the network.
package tasks_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/p2p"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/tasks"
)

// fakePeerScope is a programmable scope lookup; AddScope grants a
// scope to a peer, HasPeerScope answers accordingly.
type fakePeerScope struct {
	mu     sync.Mutex
	scopes map[string]map[string]bool // peerID -> scope -> granted
}

func newFakePeerScope() *fakePeerScope {
	return &fakePeerScope{scopes: make(map[string]map[string]bool)}
}

func (f *fakePeerScope) AddScope(peerID, scope string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, ok := f.scopes[peerID]
	if !ok {
		m = map[string]bool{}
		f.scopes[peerID] = m
	}
	m[scope] = true
}

func (f *fakePeerScope) HasPeerScope(_ context.Context, peerID, scope string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.scopes[peerID][scope], nil
}

func newOfferTestSvc(t *testing.T) (*tasks.Service, *fakePeerScope, string) {
	t.Helper()
	svc, db, wsID := newSvc(t)
	scopes := newFakePeerScope()
	svc.SetWorkspaceLookup(db)
	svc.SetPeerScopeLookup(scopes)
	// Stamp a synthetic local peer id so persistIncoming can satisfy
	// CreateTaskOffer's NOT-NULL ToPeerID constraint without needing a
	// real libp2p host.
	svc.SetLocalPeerID("self-peer")
	return svc, scopes, wsID
}

func makeOfferEnvelope(directAssign bool, workspaceName string) *p2p.TaskOfferEnvelope {
	return &p2p.TaskOfferEnvelope{
		EnvelopeKind:        p2p.TaskEnvelopeKindOffer,
		EnvelopeNonce:       "nonce-" + workspaceName,
		EnvelopeCreatedAt:   time.Now().UTC(),
		IsDirectAssign:      directAssign,
		RemoteTaskID:        "remote-task-1",
		RemoteWorkspaceID:   "ws-A",
		RemoteWorkspaceName: workspaceName,
		Title:               "Review the new endpoint",
		StatusPreview:       "open",
	}
}

// TestHandleIncomingOffer_PendingOnScope verifies the happy path: a
// peer with task_offer:<workspace> scope sends a plain offer, the
// receiver persists it as pending.
func TestHandleIncomingOffer_PendingOnScope(t *testing.T) {
	ctx := context.Background()
	svc, scopes, _ := newOfferTestSvc(t)
	scopes.AddScope("peer-A", "task_offer:ws1")

	env := makeOfferEnvelope(false, "ws1")
	state, offerID, err := svc.HandleIncomingTaskOffer(ctx, "peer-A", env)
	if err != nil {
		t.Fatalf("HandleIncomingTaskOffer: %v", err)
	}
	if state != store.TaskOfferPending {
		t.Errorf("state = %q, want %q", state, store.TaskOfferPending)
	}
	if offerID == "" {
		t.Error("offerID is empty")
	}
}

// TestHandleIncomingOffer_RejectedUnscoped verifies the no-scope path:
// a peer without task_offer:<workspace> sees state=rejected_unscoped.
func TestHandleIncomingOffer_RejectedUnscoped(t *testing.T) {
	ctx := context.Background()
	svc, _, _ := newOfferTestSvc(t)

	env := makeOfferEnvelope(false, "ws1")
	state, _, err := svc.HandleIncomingTaskOffer(ctx, "peer-A", env)
	if err == nil {
		t.Fatal("expected an error for unscoped peer")
	}
	if state != store.TaskOfferRejectedUnscoped {
		t.Errorf("state = %q, want %q", state, store.TaskOfferRejectedUnscoped)
	}
}

// TestHandleIncomingOffer_WildcardScope verifies task_offer:* grants
// access to any workspace.
func TestHandleIncomingOffer_WildcardScope(t *testing.T) {
	ctx := context.Background()
	svc, scopes, _ := newOfferTestSvc(t)
	scopes.AddScope("peer-A", "task_offer:*")

	env := makeOfferEnvelope(false, "any-workspace")
	state, _, err := svc.HandleIncomingTaskOffer(ctx, "peer-A", env)
	if err != nil {
		t.Fatalf("HandleIncomingTaskOffer: %v", err)
	}
	if state != store.TaskOfferPending {
		t.Errorf("state = %q, want %q", state, store.TaskOfferPending)
	}
}

// TestHandleIncomingOffer_DirectAssignRequiresTaskAssign verifies that
// is_direct_assign=true requires the stronger task_assign scope —
// task_offer alone is NOT enough for auto-accept.
func TestHandleIncomingOffer_DirectAssignRequiresTaskAssign(t *testing.T) {
	ctx := context.Background()
	svc, scopes, _ := newOfferTestSvc(t)
	scopes.AddScope("peer-A", "task_offer:ws1") // not task_assign

	env := makeOfferEnvelope(true, "ws1")
	state, _, err := svc.HandleIncomingTaskOffer(ctx, "peer-A", env)
	if err == nil {
		t.Fatal("expected an error — direct-assign needs task_assign scope")
	}
	if state != store.TaskOfferRejectedUnscoped {
		t.Errorf("state = %q, want %q", state, store.TaskOfferRejectedUnscoped)
	}
}

// TestHandleIncomingOffer_AutoAcceptOnTaskAssign verifies the
// auto-accept path: direct-assign + task_assign:<ws> = auto_accepted.
// We don't have a live taskShare service in this test so the Phase B
// fetch fails — but the offer row still lands as auto_accepted.
func TestHandleIncomingOffer_AutoAcceptOnTaskAssign(t *testing.T) {
	ctx := context.Background()
	svc, scopes, _ := newOfferTestSvc(t)
	scopes.AddScope("peer-A", "task_assign:ws1")

	env := makeOfferEnvelope(true, "ws1")
	state, offerID, err := svc.HandleIncomingTaskOffer(ctx, "peer-A", env)
	// HandleIncomingTaskOffer is allowed to return nil even if the
	// inline auto-accept hook fails — Phase B is best-effort.
	if err != nil {
		t.Fatalf("HandleIncomingTaskOffer: %v", err)
	}
	if state != store.TaskOfferAutoAccepted {
		t.Errorf("state = %q, want %q", state, store.TaskOfferAutoAccepted)
	}
	if offerID == "" {
		t.Error("offerID is empty")
	}
}

// TestHandleIncomingOffer_StalenessRejected verifies the staleness
// window: envelopes older than 24h get rejected even with scope.
func TestHandleIncomingOffer_StalenessRejected(t *testing.T) {
	ctx := context.Background()
	svc, scopes, _ := newOfferTestSvc(t)
	scopes.AddScope("peer-A", "task_offer:*")

	env := makeOfferEnvelope(false, "ws1")
	env.EnvelopeCreatedAt = time.Now().UTC().Add(-48 * time.Hour)

	state, _, err := svc.HandleIncomingTaskOffer(ctx, "peer-A", env)
	if err == nil {
		t.Fatal("expected ErrTaskExpired for stale envelope")
	}
	if state != store.TaskOfferExpired {
		t.Errorf("state = %q, want %q", state, store.TaskOfferExpired)
	}
}

// TestHandleIncomingOffer_ThrottleKicksIn verifies the 60-msg/60s
// budget — after 60 envelopes from the same peer/workspace, the next
// one returns state=rejected_throttle.
func TestHandleIncomingOffer_ThrottleKicksIn(t *testing.T) {
	ctx := context.Background()
	svc, scopes, _ := newOfferTestSvc(t)
	scopes.AddScope("peer-A", "task_offer:*")

	// Burst N envelopes at the budget limit; expect all to succeed.
	for i := 0; i < 60; i++ {
		env := makeOfferEnvelope(false, "ws1")
		env.EnvelopeNonce = "nonce-" + time.Now().Format(time.RFC3339Nano) +
			"-" + envBurstSuffix(i)
		state, _, err := svc.HandleIncomingTaskOffer(ctx, "peer-A", env)
		if err != nil {
			t.Fatalf("burst %d: %v", i, err)
		}
		if state != store.TaskOfferPending {
			t.Fatalf("burst %d: state = %q, want pending", i, state)
		}
	}
	// 61st should land state=rejected_throttle.
	env := makeOfferEnvelope(false, "ws1")
	env.EnvelopeNonce = "nonce-overflow"
	state, _, err := svc.HandleIncomingTaskOffer(ctx, "peer-A", env)
	if err == nil {
		t.Fatal("expected throttle error")
	}
	if state != store.TaskOfferRejectedThrottle {
		t.Errorf("state = %q, want %q", state, store.TaskOfferRejectedThrottle)
	}
}

// envBurstSuffix is a tiny helper for generating unique nonces in the
// throttle burst test — avoids relying on monotonic time stamps for
// uniqueness when the test loop runs faster than time.Now's resolution.
func envBurstSuffix(i int) string {
	return string(rune('A' + (i % 26)))
}

// TestDeclineOffer marks the offer declined and stamps the reason.
func TestDeclineOffer(t *testing.T) {
	ctx := context.Background()
	svc, scopes, _ := newOfferTestSvc(t)
	scopes.AddScope("peer-A", "task_offer:ws1")

	env := makeOfferEnvelope(false, "ws1")
	_, offerID, err := svc.HandleIncomingTaskOffer(ctx, "peer-A", env)
	if err != nil {
		t.Fatalf("HandleIncomingTaskOffer: %v", err)
	}
	if err := svc.DeclineOffer(ctx, offerID, "not relevant"); err != nil {
		t.Fatalf("DeclineOffer: %v", err)
	}
	offer, err := svc.GetOffer(ctx, offerID)
	if err != nil {
		t.Fatalf("GetOffer: %v", err)
	}
	if offer.State != store.TaskOfferDeclined {
		t.Errorf("state = %q, want %q", offer.State, store.TaskOfferDeclined)
	}
	if offer.DeclinedReason != "not relevant" {
		t.Errorf("reason = %q, want %q", offer.DeclinedReason, "not relevant")
	}
}

// TestListOffers verifies basic filtering: list-by-direction excludes
// the other direction.
func TestListOffers(t *testing.T) {
	ctx := context.Background()
	svc, scopes, _ := newOfferTestSvc(t)
	scopes.AddScope("peer-A", "task_offer:*")

	env := makeOfferEnvelope(false, "ws1")
	if _, _, err := svc.HandleIncomingTaskOffer(ctx, "peer-A", env); err != nil {
		t.Fatalf("seed offer: %v", err)
	}
	in, err := svc.ListOffers(ctx, store.TaskOfferFilter{Direction: "incoming"})
	if err != nil {
		t.Fatalf("ListOffers incoming: %v", err)
	}
	if len(in) != 1 {
		t.Errorf("incoming count = %d, want 1", len(in))
	}
	out, err := svc.ListOffers(ctx, store.TaskOfferFilter{Direction: "outgoing"})
	if err != nil {
		t.Fatalf("ListOffers outgoing: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("outgoing count = %d, want 0", len(out))
	}
}
