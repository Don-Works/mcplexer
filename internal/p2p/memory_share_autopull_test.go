// memory_share_autopull_test.go — exercises the Tier-1 silent auto-pull
// path on the offer-receive side. A SameUser (Tier-1) offer from a peer
// that hasn't opted out is pulled SILENTLY (RequestMemory fired in the
// background); an opted-out host stays OFFER-only; a non-SameUser tier is
// never auto-pulled regardless of opt-out.
//
// The tier + opt-out policy lives in the injected MemoryAutoPuller; these
// tests drive it through a fake puller that mirrors the production
// decision so the wiring (offer received → gate consulted → pull fired)
// is locked down without standing up a live host + connected peer.

//go:build p2p

package p2p

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"
)

// fakeTier mirrors the consent tiers the production puller distinguishes.
type fakeTier string

const (
	tierSameUser fakeTier = "same_user"
	tierSameOrg  fakeTier = "same_org"
	tierCrossOrg fakeTier = "cross_org"
)

// fakeAutoPuller encodes the SAME decision the cmd-side memoryAutoPuller
// makes: auto-pull only when the peer is Tier-1 (SameUser) AND the host
// hasn't opted out. Records every OnAutoPulled callback for assertions.
type fakeAutoPuller struct {
	tier      fakeTier
	optedOut  bool
	mu        sync.Mutex
	gateCalls int
	pulled    []string // remoteIDs we were told landed
}

func (f *fakeAutoPuller) ShouldAutoPull(_ context.Context, _ string, _ *MemoryOffer) bool {
	f.mu.Lock()
	f.gateCalls++
	f.mu.Unlock()
	if f.tier != tierSameUser {
		return false
	}
	return !f.optedOut
}

func (f *fakeAutoPuller) OnAutoPulled(_ context.Context, _, remoteID, _ string) {
	f.mu.Lock()
	f.pulled = append(f.pulled, remoteID)
	f.mu.Unlock()
}

// recordingReceiver is a no-op receiver — its presence is required for
// maybeAutoPull to proceed (a nil receiver disables auto-pull because we
// couldn't import the payload anyway).
type recordingReceiver struct{}

func (recordingReceiver) HandleIncomingMemory(
	_ context.Context, _ string, p *MemoryPayload,
) (string, error) {
	return "local-" + p.RemoteID, nil
}

// newAutoPullTestService builds a host-less service with a stubbed pullFn
// so we can observe pull invocations without networking. Returns the
// service plus a channel that fires (carrying the remote_id) every time
// pullFn runs.
func newAutoPullTestService(puller MemoryAutoPuller) (*MemoryShareService, chan string) {
	s := NewMemoryShareService(nil, nil, nil, recordingReceiver{}, nil, nil, nil)
	s.SetAutoPuller(puller)
	pulls := make(chan string, 8)
	s.pullFn = func(_ context.Context, _, remoteID string) (string, error) {
		pulls <- remoteID
		return "local-" + remoteID, nil
	}
	return s, pulls
}

// offerLine marshals an offer to the wire line the inbound handler reads.
func offerLine(t *testing.T, remoteID string) []byte {
	t.Helper()
	b, err := json.Marshal(MemoryOffer{
		Type: "offer", RemoteID: remoteID, Name: "n-" + remoteID, Kind: "note",
	})
	if err != nil {
		t.Fatalf("marshal offer: %v", err)
	}
	return b
}

// waitPull blocks for one pull invocation (or fails on timeout). Returns
// the remote_id that was pulled.
func waitPull(t *testing.T, pulls chan string) string {
	t.Helper()
	select {
	case rid := <-pulls:
		return rid
	case <-time.After(2 * time.Second):
		t.Fatal("expected RequestMemory to be invoked, but it was not")
		return ""
	}
}

// expectNoPull asserts no pull fires within a short window.
func expectNoPull(t *testing.T, pulls chan string) {
	t.Helper()
	select {
	case rid := <-pulls:
		t.Fatalf("expected NO auto-pull, but RequestMemory fired for %q", rid)
	case <-time.After(250 * time.Millisecond):
	}
}

// TestAutoPullSameUserNotOptedOut: a Tier-1 SameUser offer from a host
// that hasn't opted out is pulled silently.
func TestAutoPullSameUserNotOptedOut(t *testing.T) {
	puller := &fakeAutoPuller{tier: tierSameUser, optedOut: false}
	s, pulls := newAutoPullTestService(puller)

	s.handleMemoryInboundOffer(context.Background(), "12D3KooWPeerA", offerLine(t, "mem-1"))

	if got := waitPull(t, pulls); got != "mem-1" {
		t.Fatalf("pulled wrong remote_id: got %q want mem-1", got)
	}
	// OnAutoPulled must have been invoked so the owner can stamp accept.
	waitForCallback(t, puller, "mem-1")
}

// TestAutoPullOptedOut: a Tier-1 offer is NOT pulled when the host opted
// out via mesh.auto_replicate_off.
func TestAutoPullOptedOut(t *testing.T) {
	puller := &fakeAutoPuller{tier: tierSameUser, optedOut: true}
	s, pulls := newAutoPullTestService(puller)

	s.handleMemoryInboundOffer(context.Background(), "12D3KooWPeerA", offerLine(t, "mem-2"))

	expectNoPull(t, pulls)
	if puller.gateCalls == 0 {
		t.Fatal("expected the auto-pull gate to be consulted even when opted out")
	}
}

// TestAutoPullNonSameUserTier: a non-SameUser (same_org / cross_org) offer
// is NEVER auto-pulled, regardless of opt-out.
func TestAutoPullNonSameUserTier(t *testing.T) {
	for _, tier := range []fakeTier{tierSameOrg, tierCrossOrg} {
		puller := &fakeAutoPuller{tier: tier, optedOut: false}
		s, pulls := newAutoPullTestService(puller)

		s.handleMemoryInboundOffer(context.Background(), "12D3KooWPeerB", offerLine(t, "mem-3"))

		expectNoPull(t, pulls)
	}
}

// TestAutoPullNoPullerStaysOfferOnly: with no auto-puller wired the
// service never pulls — the legacy OFFER-only behaviour.
func TestAutoPullNoPullerStaysOfferOnly(t *testing.T) {
	s := NewMemoryShareService(nil, nil, nil, recordingReceiver{}, nil, nil, nil)
	pulls := make(chan string, 1)
	s.pullFn = func(_ context.Context, _, remoteID string) (string, error) {
		pulls <- remoteID
		return "", nil
	}
	s.handleMemoryInboundOffer(context.Background(), "12D3KooWPeerC", offerLine(t, "mem-4"))
	expectNoPull(t, pulls)
}

// TestAutoPullInflightDedup: two offers for the same (peer, remote) while
// the first pull is still mid-flight only fire ONE RequestMemory.
func TestAutoPullInflightDedup(t *testing.T) {
	puller := &fakeAutoPuller{tier: tierSameUser, optedOut: false}
	s := NewMemoryShareService(nil, nil, nil, recordingReceiver{}, nil, nil, nil)
	s.SetAutoPuller(puller)
	release := make(chan struct{})
	var calls int
	var mu sync.Mutex
	s.pullFn = func(_ context.Context, _, remoteID string) (string, error) {
		mu.Lock()
		calls++
		mu.Unlock()
		<-release // block so the second offer arrives while in-flight
		return "local-" + remoteID, nil
	}
	ctx := context.Background()
	s.handleMemoryInboundOffer(ctx, "12D3KooWPeerD", offerLine(t, "mem-5"))
	s.handleMemoryInboundOffer(ctx, "12D3KooWPeerD", offerLine(t, "mem-5"))
	// Give the second offer a moment to hit the in-flight guard.
	time.Sleep(100 * time.Millisecond)
	close(release)
	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("expected exactly 1 pull for a re-offer in flight, got %d", calls)
	}
}

// waitForCallback polls until the puller records the OnAutoPulled
// callback for remoteID, or fails on timeout.
func waitForCallback(t *testing.T, p *fakeAutoPuller, remoteID string) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		p.mu.Lock()
		for _, r := range p.pulled {
			if r == remoteID {
				p.mu.Unlock()
				return
			}
		}
		p.mu.Unlock()
		select {
		case <-deadline:
			t.Fatalf("OnAutoPulled never fired for %q", remoteID)
		case <-time.After(10 * time.Millisecond):
		}
	}
}
