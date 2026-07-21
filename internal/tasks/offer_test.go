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
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/p2p"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/tasks"
)

type offerFixture struct {
	svc *tasks.Service
	db  interface {
		store.Store
		store.CollaborationStore
	}
	workspaceID string
	shareID     string
	ownerID     string
	principalID string
	peerID      string
}

func TestRetryPendingHomePublicationsDeclinesAfterCapabilityRemoval(t *testing.T) {
	ctx := context.Background()
	svc, db, workspaceID := newSvc(t)
	svc.SetTaskShare(&p2p.TaskShareService{})
	task, err := svc.Create(ctx, tasks.CreateOptions{
		WorkspaceID: workspaceID, Title: "Queued monitor finding",
		CreatedBySessionID: "monitor",
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	membership := &store.WorkspaceMembership{
		ShareID: "remote-share", HomePeerID: "home-peer",
		RemoteWorkspaceID: "home-workspace", LocalWorkspaceID: workspaceID,
		WorkspaceName: "Operations", Capabilities: []string{store.CapabilityTasksPublish},
		AccessEpoch: 2, Status: store.WorkspaceShareStatusActive,
		JoinedAt: now, UpdatedAt: now,
	}
	if err := db.UpsertWorkspaceMembership(ctx, membership); err != nil {
		t.Fatal(err)
	}
	offer := &store.TaskOffer{
		ID: "queued-offer", TaskID: task.ID, RemoteTaskID: task.ID,
		ShareID: membership.ShareID, AccessEpoch: membership.AccessEpoch,
		FromPeerID: "monitor-peer", ToPeerID: membership.HomePeerID,
		RemoteWorkspaceID:   membership.RemoteWorkspaceID,
		RemoteWorkspaceName: membership.WorkspaceName, WorkspaceID: workspaceID,
		Title: task.Title, IsDirectAssign: true, EnvelopeNonce: "queued-nonce",
		EnvelopeCreatedAt: now, Direction: "outgoing", State: store.TaskOfferPending,
		CreatedAt: now,
	}
	if err := db.CreateTaskOffer(ctx, offer); err != nil {
		t.Fatal(err)
	}
	membership.Capabilities = []string{}
	membership.AccessEpoch++
	membership.UpdatedAt = now.Add(time.Minute)
	if err := db.UpsertWorkspaceMembership(ctx, membership); err != nil {
		t.Fatal(err)
	}

	attempted, delivered, err := svc.RetryPendingHomePublications(ctx, membership.HomePeerID)
	if err != nil || attempted != 0 || delivered != 0 {
		t.Fatalf("retry after revocation = attempted %d delivered %d err %v", attempted, delivered, err)
	}
	got, err := db.GetTaskOffer(ctx, offer.ID)
	if err != nil || got.State != store.TaskOfferDeclined ||
		got.DeclinedReason != "current workspace capabilities no longer permit publication" {
		t.Fatalf("revoked pending offer = %#v, %v", got, err)
	}
}

func TestHandleIncomingOffer_StaleMirrorEditBecomesConflict(t *testing.T) {
	ctx := context.Background()
	fixture := newOfferTestSvc(t)
	fixture.grant(t, store.CapabilityWorkspaceView, store.CapabilityTasksEdit)
	homeTask := &store.Task{
		ID: "remote-task-1", WorkspaceID: fixture.workspaceID,
		Title: "canonical home value", HlcAt: "00000000000000000000000000000042",
	}
	if err := fixture.db.CreateTask(ctx, homeTask); err != nil {
		t.Fatal(err)
	}
	env := fixture.envelope(t, true, "ws1")
	env.BaseHLC = "00000000000000000000000000000041"
	state, _, err := fixture.svc.HandleIncomingTaskOffer(ctx, fixture.peerID, env)
	if !errors.Is(err, p2p.ErrTaskConflict) || state != store.TaskOfferConflict {
		t.Fatalf("stale edit = state %q, err %v", state, err)
	}
	got, err := fixture.db.GetTask(ctx, homeTask.ID)
	if err != nil || got.Title != homeTask.Title || got.HlcAt != homeTask.HlcAt {
		t.Fatalf("home task changed on conflict: %#v, %v", got, err)
	}
	offers, err := fixture.db.ListTaskOffers(ctx, store.TaskOfferFilter{
		Direction: "incoming", State: store.TaskOfferConflict,
	})
	if err != nil || len(offers) != 1 || offers[0].BaseHLC != env.BaseHLC {
		t.Fatalf("conflict audit offer = %#v, %v", offers, err)
	}
}

func newOfferTestSvc(t *testing.T) *offerFixture {
	t.Helper()
	svc, db, wsID := newSvc(t)
	svc.SetWorkspaceLookup(db)
	svc.SetLocalPeerID("self-peer")
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	owner := &store.Principal{ID: "offer-owner", Kind: store.PrincipalKindPerson, DisplayName: "Owner", Status: store.PrincipalStatusActive, IsLocalOwner: true, CreatedAt: now}
	if err := db.CreatePrincipal(context.Background(), owner); err != nil {
		t.Fatal(err)
	}
	share := &store.WorkspaceShare{ShareID: "offer-share", LocalWorkspaceID: wsID, HomePeerID: "self-peer", OwnerPrincipalID: owner.ID, CreatedAt: now}
	if err := db.CreateWorkspaceShare(context.Background(), share); err != nil {
		t.Fatal(err)
	}
	principal := &store.Principal{ID: "offer-sender", Kind: store.PrincipalKindPerson, DisplayName: "Sender", Status: store.PrincipalStatusActive, CreatedAt: now}
	if err := db.CreatePrincipal(context.Background(), principal); err != nil {
		t.Fatal(err)
	}
	verified := now
	key := &store.PrincipalIdentityKey{ID: "offer-key", PrincipalID: principal.ID, CanonicalPublicKey: "ssh-ed25519 AAAAOfferTest", Fingerprint: "SHA256:offer-test", Algorithm: "ssh-ed25519", Status: store.PrincipalKeyStatusActive, CreatedAt: now, VerifiedAt: &verified}
	if err := db.AddPrincipalIdentityKey(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	token := sha256.Sum256([]byte("offer-invitation"))
	invitation := &store.PrincipalInvitation{ID: "offer-invite", TokenHash: token[:], Purpose: store.InvitationPurposeAddDevice, PrincipalID: principal.ID, IdentityKeyID: key.ID, CreatedByPrincipalID: owner.ID, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	if err := db.CreatePrincipalInvitation(context.Background(), invitation, nil); err != nil {
		t.Fatal(err)
	}
	nonce := sha256.Sum256([]byte("offer-nonce"))
	transcript := sha256.Sum256([]byte("offer-transcript"))
	challenge := &store.PrincipalIdentityChallenge{ID: "offer-challenge", InvitationID: invitation.ID, InitiatorPeerID: "peer-A", ResponderPeerID: "self-peer", NonceHash: nonce[:], TranscriptHash: hex.EncodeToString(transcript[:]), IssuedAt: now, ExpiresAt: now.Add(5 * time.Minute)}
	if err := db.CreatePrincipalIdentityChallenge(context.Background(), challenge); err != nil {
		t.Fatal(err)
	}
	_, _, err := db.ActivateInvitedDevice(context.Background(), store.InvitedDeviceActivation{InvitationID: invitation.ID, InvitationTokenHash: token[:], ChallengeID: challenge.ID, PeerID: "peer-A", ResponderPeerID: "self-peer", DisplayName: "Sender device", DeviceKind: "laptop", BindingVersion: store.DeviceBindingVersionV1, BindingTranscriptHash: challenge.TranscriptHash, BindingSignature: []byte("verified"), At: now.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	return &offerFixture{svc: svc, db: db, workspaceID: wsID, shareID: share.ShareID, ownerID: owner.ID, principalID: principal.ID, peerID: "peer-A"}
}

func (f *offerFixture) grant(t *testing.T, capabilities ...string) {
	t.Helper()
	_, _, err := f.db.SetWorkspaceGrants(context.Background(), store.WorkspaceGrantSet{ShareID: f.shareID, PrincipalID: f.principalID, Capabilities: capabilities, CreatedByPrincipalID: f.ownerID, At: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
}

func (f *offerFixture) envelope(t *testing.T, directAssign bool, workspaceName string) *p2p.TaskOfferEnvelope {
	t.Helper()
	share, err := f.db.GetWorkspaceShare(context.Background(), f.shareID)
	if err != nil {
		t.Fatal(err)
	}
	return &p2p.TaskOfferEnvelope{
		EnvelopeKind:        p2p.TaskEnvelopeKindOffer,
		EnvelopeNonce:       "nonce-" + workspaceName,
		EnvelopeCreatedAt:   time.Now().UTC(),
		IsDirectAssign:      directAssign,
		RemoteTaskID:        "remote-task-1",
		ShareID:             f.shareID,
		AccessEpoch:         share.AccessEpoch,
		Visibility:          store.TaskVisibilityPrivate,
		VisibilityEpoch:     1,
		RemoteWorkspaceID:   "ws-A",
		RemoteWorkspaceName: workspaceName,
		Title:               "Review the new endpoint",
		StatusPreview:       "open",
	}
}

// TestHandleIncomingOffer_PendingOnExactGrant verifies the happy path: a
// proof-bound principal with view + create sends a plain offer, which the
// receiver persists as pending.
func TestHandleIncomingOffer_PendingOnExactGrant(t *testing.T) {
	ctx := context.Background()
	fixture := newOfferTestSvc(t)
	fixture.grant(t, store.CapabilityWorkspaceView, store.CapabilityTasksCreate)

	env := fixture.envelope(t, false, "ws1")
	state, offerID, err := fixture.svc.HandleIncomingTaskOffer(ctx, fixture.peerID, env)
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

// TestHandleIncomingOffer_RejectedUnscoped verifies that a peer without an
// exact collaboration grant sees state=rejected_unscoped.
func TestHandleIncomingOffer_RejectedUnscoped(t *testing.T) {
	ctx := context.Background()
	fixture := newOfferTestSvc(t)

	env := fixture.envelope(t, false, "ws1")
	state, _, err := fixture.svc.HandleIncomingTaskOffer(ctx, fixture.peerID, env)
	if err == nil {
		t.Fatal("expected an error for unscoped peer")
	}
	if state != store.TaskOfferRejectedUnscoped {
		t.Errorf("state = %q, want %q", state, store.TaskOfferRejectedUnscoped)
	}
}

// TestHandleIncomingOffer_WildcardScope verifies a legacy wildcard is
// ignored even for a proof-bound peer.
func TestHandleIncomingOffer_WildcardScope(t *testing.T) {
	ctx := context.Background()
	fixture := newOfferTestSvc(t)
	if err := fixture.db.AddPeer(ctx, &store.P2PPeer{PeerID: fixture.peerID, DisplayName: "legacy"}); err != nil {
		t.Fatal(err)
	}
	if err := fixture.db.GrantPeerScope(ctx, fixture.peerID, "task_offer:*"); err != nil {
		t.Fatal(err)
	}

	env := fixture.envelope(t, false, "any-workspace")
	state, _, err := fixture.svc.HandleIncomingTaskOffer(ctx, fixture.peerID, env)
	if err == nil || state != store.TaskOfferRejectedUnscoped {
		t.Fatalf("legacy wildcard result = %q, %v", state, err)
	}
}

// TestHandleIncomingOffer_DirectCreateUsesCreateCapability verifies that
// transport fast-pathing does not smuggle assignment semantics into a task
// that carries no assignee. Exact workspace.view + tasks.create is enough;
// Phase B still fails visibly here because this unit fixture has no wire.
func TestHandleIncomingOffer_DirectCreateUsesCreateCapability(t *testing.T) {
	ctx := context.Background()
	fixture := newOfferTestSvc(t)
	fixture.grant(t, store.CapabilityWorkspaceView, store.CapabilityTasksCreate)

	env := fixture.envelope(t, true, "ws1")
	state, offerID, err := fixture.svc.HandleIncomingTaskOffer(ctx, fixture.peerID, env)
	if err == nil || offerID == "" {
		t.Fatalf("direct create should pass authorization then expose Phase B failure: id=%q err=%v", offerID, err)
	}
	if state != store.TaskOfferAutoAccepted {
		t.Errorf("state = %q, want %q", state, store.TaskOfferAutoAccepted)
	}
}

// TestHandleIncomingOffer_AutoAcceptOnContributorGrant verifies that an exact
// contributor grant authorizes the direct-create auto-accept path.
// We don't have a live taskShare service in this test so the Phase B
// fetch fails. The offer row remains auditable, but the sender must not
// receive a false success acknowledgement.
func TestHandleIncomingOffer_AutoAcceptOnContributorGrant(t *testing.T) {
	ctx := context.Background()
	fixture := newOfferTestSvc(t)
	fixture.grant(t, store.CapabilityWorkspaceView, store.CapabilityTasksCreate)

	env := fixture.envelope(t, true, "ws1")
	state, offerID, err := fixture.svc.HandleIncomingTaskOffer(ctx, fixture.peerID, env)
	if err == nil {
		t.Fatal("expected the failed Phase B fetch to reach the sender")
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
	fixture := newOfferTestSvc(t)
	fixture.grant(t, store.CapabilityWorkspaceView, store.CapabilityTasksCreate)

	env := fixture.envelope(t, false, "ws1")
	env.EnvelopeCreatedAt = time.Now().UTC().Add(-48 * time.Hour)

	state, _, err := fixture.svc.HandleIncomingTaskOffer(ctx, fixture.peerID, env)
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
	fixture := newOfferTestSvc(t)
	fixture.grant(t, store.CapabilityWorkspaceView, store.CapabilityTasksCreate)

	// Burst N envelopes at the budget limit; expect all to succeed.
	for i := 0; i < 60; i++ {
		env := fixture.envelope(t, false, "ws1")
		env.EnvelopeNonce = "nonce-" + time.Now().Format(time.RFC3339Nano) +
			"-" + envBurstSuffix(i)
		state, _, err := fixture.svc.HandleIncomingTaskOffer(ctx, fixture.peerID, env)
		if err != nil {
			t.Fatalf("burst %d: %v", i, err)
		}
		if state != store.TaskOfferPending {
			t.Fatalf("burst %d: state = %q, want pending", i, state)
		}
	}
	// 61st should land state=rejected_throttle.
	env := fixture.envelope(t, false, "ws1")
	env.EnvelopeNonce = "nonce-overflow"
	state, _, err := fixture.svc.HandleIncomingTaskOffer(ctx, fixture.peerID, env)
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
	fixture := newOfferTestSvc(t)
	fixture.grant(t, store.CapabilityWorkspaceView, store.CapabilityTasksCreate)

	env := fixture.envelope(t, false, "ws1")
	_, offerID, err := fixture.svc.HandleIncomingTaskOffer(ctx, fixture.peerID, env)
	if err != nil {
		t.Fatalf("HandleIncomingTaskOffer: %v", err)
	}
	if err := fixture.svc.DeclineOffer(ctx, offerID, "not relevant"); err != nil {
		t.Fatalf("DeclineOffer: %v", err)
	}
	offer, err := fixture.svc.GetOffer(ctx, offerID)
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
	fixture := newOfferTestSvc(t)
	fixture.grant(t, store.CapabilityWorkspaceView, store.CapabilityTasksCreate)

	env := fixture.envelope(t, false, "ws1")
	if _, _, err := fixture.svc.HandleIncomingTaskOffer(ctx, fixture.peerID, env); err != nil {
		t.Fatalf("seed offer: %v", err)
	}
	in, err := fixture.svc.ListOffers(ctx, store.TaskOfferFilter{Direction: "incoming"})
	if err != nil {
		t.Fatalf("ListOffers incoming: %v", err)
	}
	if len(in) != 1 {
		t.Errorf("incoming count = %d, want 1", len(in))
	}
	out, err := fixture.svc.ListOffers(ctx, store.TaskOfferFilter{Direction: "outgoing"})
	if err != nil {
		t.Fatalf("ListOffers outgoing: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("outgoing count = %d, want 0", len(out))
	}
}
