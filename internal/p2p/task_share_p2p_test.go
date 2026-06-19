//go:build p2p

package p2p

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// fakeTaskProvider is the offering-side hook with a single task in
// memory. Returns ErrTaskNotFound for any other id.
type fakeTaskProvider struct {
	remoteID string
	payload  *TaskPayloadEnvelope
}

func (f *fakeTaskProvider) GetTaskPayload(_ context.Context, id string) (*TaskPayloadEnvelope, error) {
	if id != f.remoteID {
		return nil, ErrTaskNotFound
	}
	return f.payload, nil
}

// fakeTaskReceiver records inbound offers + lets the test inject a
// "this passes / this fails" decision via state + err.
type fakeTaskReceiver struct {
	mu      sync.Mutex
	gotEnv  *TaskOfferEnvelope
	gotFrom string
	state   string // returned state on success
	offerID string
	err     error
}

func (f *fakeTaskReceiver) HandleIncomingTaskOffer(
	_ context.Context, fromPeerID string, env *TaskOfferEnvelope,
) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gotEnv = env
	f.gotFrom = fromPeerID
	if f.err != nil {
		return "", "", f.err
	}
	return f.state, f.offerID, nil
}

// fakeTaskAuditor counts events keyed by action+status.
type fakeTaskAuditor struct {
	mu     sync.Mutex
	events []string
}

func (f *fakeTaskAuditor) RecordTaskShare(_ context.Context, action, _, _, status, _ string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, action+":"+status)
}

func (f *fakeTaskAuditor) seen(want string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, e := range f.events {
		if e == want {
			return true
		}
	}
	return false
}

// pairTaskHosts mirrors pairHosts from skill_share_p2p_test.go — both
// sides record the other as paired (no scope needed for the task share
// service; scopes are enforced inside the receiver hook).
func pairTaskHosts(a, b *Host, lookA, lookB *fakePairedLookup) {
	lookA.addPaired(b.PeerID(), nil)
	lookB.addPaired(a.PeerID(), nil)
}

// TestTaskShare_OfferRoundTrip is the happy-path acceptance test:
// host A sends an offer to host B, B's receiver acks "pending", the
// state propagates back through the ack envelope.
func TestTaskShare_OfferRoundTrip(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	a := startTestHost(t, "a")
	defer func() { _ = a.Close() }()
	b := startTestHost(t, "b")
	defer func() { _ = b.Close() }()

	lookA, lookB := newFakeLookup(), newFakeLookup()
	pairTaskHosts(a, b, lookA, lookB)
	connectHosts(t, context.Background(), a, b)

	recvB := &fakeTaskReceiver{state: "pending", offerID: "offer-123"}
	auditA, auditB := &fakeTaskAuditor{}, &fakeTaskAuditor{}

	aSvc := NewTaskShareService(a, lookA, nil, nil, auditA, nil)
	NewTaskShareService(b, lookB, nil, recvB, auditB, nil)

	env := TaskOfferEnvelope{
		EnvelopeNonce:       "nonce-1",
		EnvelopeCreatedAt:   time.Now().UTC(),
		RemoteTaskID:        "task-1",
		RemoteWorkspaceID:   "ws-A",
		RemoteWorkspaceName: "personal",
		Title:               "Review PR",
		DescriptionPreview:  "Please review the new tasks endpoint.",
		StatusPreview:       "open",
		PriorityPreview:     "normal",
	}
	ack, err := aSvc.OfferTask(ctx, b.PeerID(), env)
	if err != nil {
		t.Fatalf("OfferTask: %v", err)
	}
	if ack.State != "pending" {
		t.Errorf("ack.State = %q, want %q", ack.State, "pending")
	}
	if ack.OfferID != "offer-123" {
		t.Errorf("ack.OfferID = %q, want %q", ack.OfferID, "offer-123")
	}
	if recvB.gotFrom != a.PeerID() {
		t.Errorf("receiver got from %q, want %q", recvB.gotFrom, a.PeerID())
	}
	if recvB.gotEnv == nil || recvB.gotEnv.Title != env.Title {
		t.Errorf("receiver did not see expected envelope: got %+v", recvB.gotEnv)
	}
	if !auditA.seen("offer:ok") {
		t.Errorf("sender audit missing offer:ok, got %v", auditA.events)
	}
	if !auditB.seen("offer_received:pending") {
		t.Errorf("receiver audit missing offer_received:pending, got %v", auditB.events)
	}
}

// TestTaskShare_RejectsUnpaired pins the pairing gate — a host that
// isn't on B's paired list gets denied + B audits "stream_rejected".
func TestTaskShare_RejectsUnpaired(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	a := startTestHost(t, "a")
	defer func() { _ = a.Close() }()
	b := startTestHost(t, "b")
	defer func() { _ = b.Close() }()

	lookA, lookB := newFakeLookup(), newFakeLookup()
	// Only A considers B paired; B doesn't know A.
	lookA.addPaired(b.PeerID(), nil)
	connectHosts(t, context.Background(), a, b)

	recvB := &fakeTaskReceiver{state: "pending"}
	auditB := &fakeTaskAuditor{}

	aSvc := NewTaskShareService(a, lookA, nil, nil, nil, nil)
	NewTaskShareService(b, lookB, nil, recvB, auditB, nil)

	_, err := aSvc.OfferTask(ctx, b.PeerID(), TaskOfferEnvelope{
		EnvelopeNonce: "n1", RemoteTaskID: "t1", RemoteWorkspaceID: "ws-A",
		Title: "x", EnvelopeCreatedAt: time.Now().UTC(),
	})
	if err == nil {
		t.Fatal("OfferTask: expected error, got nil")
	}
	if !errors.Is(err, ErrTaskOfferDenied) {
		t.Fatalf("err = %v, want ErrTaskOfferDenied", err)
	}
	if !auditB.seen("stream_rejected:denied") {
		t.Errorf("receiver missing stream_rejected:denied audit, got %v", auditB.events)
	}
}

// TestTaskShare_RequestPayload verifies Phase B: B opens a request
// stream to A, A serves the full payload back.
func TestTaskShare_RequestPayload(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	a := startTestHost(t, "a")
	defer func() { _ = a.Close() }()
	b := startTestHost(t, "b")
	defer func() { _ = b.Close() }()

	lookA, lookB := newFakeLookup(), newFakeLookup()
	pairTaskHosts(a, b, lookA, lookB)
	connectHosts(t, context.Background(), a, b)

	payload := &TaskPayloadEnvelope{
		Title:       "Review PR",
		Description: "Full description of the work.",
		Status:      "open",
		Priority:    "normal",
		Meta:        "reviewer: max",
	}
	provA := &fakeTaskProvider{remoteID: "task-1", payload: payload}

	NewTaskShareService(a, lookA, provA, nil, nil, nil)
	bSvc := NewTaskShareService(b, lookB, nil, nil, nil, nil)

	got, err := bSvc.RequestTaskPayload(ctx, a.PeerID(), "nonce-1", "task-1")
	if err != nil {
		t.Fatalf("RequestTaskPayload: %v", err)
	}
	if got.Title != payload.Title || got.Description != payload.Description {
		t.Errorf("payload mismatch: got %+v, want %+v", got, payload)
	}
}

// TestTaskShare_RequestNotFound verifies the not-found path: B asks for
// a task A doesn't have, the typed sentinel propagates back to B.
func TestTaskShare_RequestNotFound(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	a := startTestHost(t, "a")
	defer func() { _ = a.Close() }()
	b := startTestHost(t, "b")
	defer func() { _ = b.Close() }()

	lookA, lookB := newFakeLookup(), newFakeLookup()
	pairTaskHosts(a, b, lookA, lookB)
	connectHosts(t, context.Background(), a, b)

	provA := &fakeTaskProvider{remoteID: "real-task"}
	NewTaskShareService(a, lookA, provA, nil, nil, nil)
	bSvc := NewTaskShareService(b, lookB, nil, nil, nil, nil)

	_, err := bSvc.RequestTaskPayload(ctx, a.PeerID(), "nonce-1", "missing-task")
	if err == nil {
		t.Fatal("RequestTaskPayload: expected error, got nil")
	}
	if !errors.Is(err, ErrTaskNotFound) {
		t.Fatalf("err = %v, want ErrTaskNotFound", err)
	}
}

// TestTaskShare_ReceiverDeny_PropagatesError verifies that when the
// receiver hook returns a typed error (denied), the wire turns it into
// the right error code + the sender sees the typed sentinel.
func TestTaskShare_ReceiverDeny_PropagatesError(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	a := startTestHost(t, "a")
	defer func() { _ = a.Close() }()
	b := startTestHost(t, "b")
	defer func() { _ = b.Close() }()

	lookA, lookB := newFakeLookup(), newFakeLookup()
	pairTaskHosts(a, b, lookA, lookB)
	connectHosts(t, context.Background(), a, b)

	recvB := &fakeTaskReceiver{err: ErrTaskOfferDenied}

	aSvc := NewTaskShareService(a, lookA, nil, nil, nil, nil)
	NewTaskShareService(b, lookB, nil, recvB, nil, nil)

	_, err := aSvc.OfferTask(ctx, b.PeerID(), TaskOfferEnvelope{
		EnvelopeNonce: "n1", RemoteTaskID: "t1", RemoteWorkspaceID: "ws-A",
		Title: "x", EnvelopeCreatedAt: time.Now().UTC(),
	})
	if err == nil {
		t.Fatal("OfferTask: expected error, got nil")
	}
	if !errors.Is(err, ErrTaskOfferDenied) {
		t.Fatalf("err = %v, want ErrTaskOfferDenied wrapped", err)
	}
}

// TestTaskShare_AssignFlag_PropagatesIsDirectAssign verifies that
// AssignTaskRemote sets the is_direct_assign flag on the envelope the
// receiver sees — this is what triggers task_assign scope checks +
// auto-accept on the receiving side.
func TestTaskShare_AssignFlag_PropagatesIsDirectAssign(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	a := startTestHost(t, "a")
	defer func() { _ = a.Close() }()
	b := startTestHost(t, "b")
	defer func() { _ = b.Close() }()

	lookA, lookB := newFakeLookup(), newFakeLookup()
	pairTaskHosts(a, b, lookA, lookB)
	connectHosts(t, context.Background(), a, b)

	recvB := &fakeTaskReceiver{state: "auto_accepted"}
	aSvc := NewTaskShareService(a, lookA, nil, nil, nil, nil)
	NewTaskShareService(b, lookB, nil, recvB, nil, nil)

	_, err := aSvc.AssignTaskRemote(ctx, b.PeerID(), TaskOfferEnvelope{
		EnvelopeNonce: "n1", RemoteTaskID: "t1", RemoteWorkspaceID: "ws-A",
		Title: "x", EnvelopeCreatedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("AssignTaskRemote: %v", err)
	}
	if recvB.gotEnv == nil || !recvB.gotEnv.IsDirectAssign {
		t.Errorf("receiver did not see IsDirectAssign=true: %+v", recvB.gotEnv)
	}
}
