//go:build p2p

package p2p

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

// fakeLivenessHost satisfies hostForLiveness without spinning up libp2p.
type fakeLivenessHost struct {
	mu          sync.Mutex
	self        peer.ID
	connected   map[peer.ID]struct{}
	closeCalls  atomic.Int32
	closedPeers map[peer.ID]struct{}
}

func newFakeLivenessHost(self peer.ID) *fakeLivenessHost {
	return &fakeLivenessHost{
		self:        self,
		connected:   map[peer.ID]struct{}{},
		closedPeers: map[peer.ID]struct{}{},
	}
}

func (f *fakeLivenessHost) Self() peer.ID { return f.self }

func (f *fakeLivenessHost) IsConnected(p peer.ID) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.connected[p]
	return ok
}

func (f *fakeLivenessHost) ClosePeer(p peer.ID) error {
	f.closeCalls.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.connected, p)
	f.closedPeers[p] = struct{}{}
	return nil
}

// fakePinger returns canned ping results per peer. Repeats the last entry
// when the queue runs out so a single setup can drive many ticks.
type fakePinger struct {
	mu      sync.Mutex
	results map[peer.ID][]pingResult
	last    map[peer.ID]pingResult
	calls   atomic.Int32
}

func newFakePinger() *fakePinger {
	return &fakePinger{
		results: map[peer.ID][]pingResult{},
		last:    map[peer.ID]pingResult{},
	}
}

func (f *fakePinger) Ping(_ context.Context, p peer.ID) <-chan pingResult {
	f.calls.Add(1)
	out := make(chan pingResult, 1)
	f.mu.Lock()
	defer f.mu.Unlock()
	q := f.results[p]
	var r pingResult
	switch {
	case len(q) > 0:
		r = q[0]
		f.results[p] = q[1:]
		f.last[p] = r
	default:
		r = f.last[p]
	}
	out <- r
	close(out)
	return out
}

// fakeToucher records UpdateLastSeen calls.
type fakeToucher struct {
	mu    sync.Mutex
	calls map[string]time.Time
}

func newFakeToucher() *fakeToucher { return &fakeToucher{calls: map[string]time.Time{}} }

func (f *fakeToucher) UpdateLastSeen(_ context.Context, peerID string, t time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[peerID] = t
	return nil
}

func (f *fakeToucher) seen(peerID string) (time.Time, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.calls[peerID]
	return t, ok
}

func livenessWithFakes(host hostForLiveness, lister PairedPeerLister, p pingClient, t PeerLivenessToucher) *LivenessMonitor {
	return &LivenessMonitor{
		host:             host,
		lister:           lister,
		pinger:           p,
		toucher:          t,
		logger:           silentLogger(),
		clk:              realClock{},
		interval:         time.Hour,
		pingTimeout:      time.Second,
		failureThreshold: livenessFailureThreshold,
		state:            map[peer.ID]*liveState{},
		stopCh:           make(chan struct{}),
	}
}

func TestLiveness_SuccessfulPingMarksOnlineAndTouches(t *testing.T) {
	self := fakePeer(t, "self")
	alice := fakePeer(t, "alice")

	host := newFakeLivenessHost(self)
	host.connected[alice] = struct{}{}
	pinger := newFakePinger()
	pinger.results[alice] = []pingResult{{RTT: 25 * time.Millisecond}}
	toucher := newFakeToucher()
	lister := &fakeLister{ids: []string{alice.String()}}

	m := livenessWithFakes(host, lister, pinger, toucher)
	m.tick(context.Background())

	if got := pinger.calls.Load(); got != 1 {
		t.Fatalf("ping calls = %d; want 1", got)
	}
	st := m.PeerLiveness(alice)
	if !st.Online {
		t.Errorf("alice should be online after successful ping")
	}
	if st.LastPingRTT != 25*time.Millisecond {
		t.Errorf("LastPingRTT = %v; want 25ms", st.LastPingRTT)
	}
	// touch is async — give it a moment.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if _, ok := toucher.seen(alice.String()); ok {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Errorf("UpdateLastSeen never called for alice")
}

// fakeReconnectMarker captures MarkConnected calls so the test can assert
// the liveness monitor's reverse-hook into the reconnector fired.
type fakeReconnectMarker struct {
	mu    sync.Mutex
	calls []peer.ID
}

func (f *fakeReconnectMarker) MarkConnected(p peer.ID) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, p)
}

func (f *fakeReconnectMarker) seen(p peer.ID) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c == p {
			return true
		}
	}
	return false
}

// TestLiveness_SuccessfulPingMarksReconnectorConnected is the regression test
// for the "last seen now + Searching badge" UI bug. A successful ping must
// flip the reconnector's state to "connected" even when libp2p's Connected
// notifiee event was missed (suspend/resume etc.).
func TestLiveness_SuccessfulPingMarksReconnectorConnected(t *testing.T) {
	self := fakePeer(t, "self")
	alice := fakePeer(t, "alice")

	host := newFakeLivenessHost(self)
	host.connected[alice] = struct{}{}
	pinger := newFakePinger()
	pinger.results[alice] = []pingResult{{RTT: 12 * time.Millisecond}}
	toucher := newFakeToucher()
	lister := &fakeLister{ids: []string{alice.String()}}
	marker := &fakeReconnectMarker{}

	m := livenessWithFakes(host, lister, pinger, toucher)
	m.SetReconnectMarker(marker)
	m.tick(context.Background())

	if !marker.seen(alice) {
		t.Errorf("MarkConnected never called for alice on a successful ping")
	}
}

// TestLiveness_FailedPingDoesNotMarkConnected — pings that error out must
// not falsely flip reconnect_state, otherwise the offline edge gets papered
// over.
func TestLiveness_FailedPingDoesNotMarkConnected(t *testing.T) {
	self := fakePeer(t, "self")
	alice := fakePeer(t, "alice")

	host := newFakeLivenessHost(self)
	host.connected[alice] = struct{}{}
	pinger := newFakePinger()
	pinger.results[alice] = []pingResult{{Error: errors.New("ping timeout")}}
	lister := &fakeLister{ids: []string{alice.String()}}
	marker := &fakeReconnectMarker{}

	m := livenessWithFakes(host, lister, pinger, newFakeToucher())
	m.SetReconnectMarker(marker)
	m.tick(context.Background())

	if marker.seen(alice) {
		t.Errorf("MarkConnected called on a failed ping; want skip")
	}
}

func TestLiveness_OfflinePeerCostsNothing(t *testing.T) {
	self := fakePeer(t, "self")
	alice := fakePeer(t, "alice")

	host := newFakeLivenessHost(self) // alice NOT connected
	pinger := newFakePinger()
	toucher := newFakeToucher()
	lister := &fakeLister{ids: []string{alice.String()}}

	m := livenessWithFakes(host, lister, pinger, toucher)
	m.tick(context.Background())

	if got := pinger.calls.Load(); got != 0 {
		t.Errorf("ping calls = %d; want 0 — offline peers must not generate traffic", got)
	}
	if _, ok := toucher.seen(alice.String()); ok {
		t.Errorf("UpdateLastSeen called for an offline peer; want skip")
	}
	st := m.PeerLiveness(alice)
	if st.Online {
		t.Errorf("alice marked online without a successful ping")
	}
	if st.OfflineSince.IsZero() {
		t.Errorf("OfflineSince should be set on first observation of an offline peer")
	}
}

func TestLiveness_FailureThresholdClosesConnection(t *testing.T) {
	self := fakePeer(t, "self")
	alice := fakePeer(t, "alice")

	host := newFakeLivenessHost(self)
	host.connected[alice] = struct{}{}
	pinger := newFakePinger()
	// Every ping fails — threshold = 3, so the third tick should close.
	pinger.results[alice] = []pingResult{
		{Error: errors.New("blip 1")},
		{Error: errors.New("blip 2")},
		{Error: errors.New("blip 3")},
	}
	lister := &fakeLister{ids: []string{alice.String()}}

	m := livenessWithFakes(host, lister, pinger, nil)
	// Tick 1 + 2: failure count climbs but no close yet.
	m.tick(context.Background())
	m.tick(context.Background())
	if got := host.closeCalls.Load(); got != 0 {
		t.Fatalf("ClosePeer called %d times before threshold; want 0", got)
	}
	st := m.PeerLiveness(alice)
	if st.ConsecutiveFailures != 2 {
		t.Errorf("after 2 failures, ConsecutiveFailures=%d; want 2", st.ConsecutiveFailures)
	}
	// Tick 3: threshold crossed.
	m.tick(context.Background())
	if got := host.closeCalls.Load(); got != 1 {
		t.Errorf("ClosePeer call count = %d; want 1 after %d consecutive failures", got, livenessFailureThreshold)
	}
}

func TestLiveness_OfflineSinceClearedOnSuccess(t *testing.T) {
	self := fakePeer(t, "self")
	alice := fakePeer(t, "alice")

	host := newFakeLivenessHost(self)
	pinger := newFakePinger()
	pinger.results[alice] = []pingResult{{RTT: 1 * time.Millisecond}}
	lister := &fakeLister{ids: []string{alice.String()}}

	m := livenessWithFakes(host, lister, pinger, nil)
	// First tick: offline.
	m.tick(context.Background())
	if _, off := m.OfflineSince(alice); !off {
		t.Fatalf("expected OfflineSince to report offline on first tick")
	}
	// Connection comes up; second tick succeeds.
	host.mu.Lock()
	host.connected[alice] = struct{}{}
	host.mu.Unlock()
	m.tick(context.Background())
	if _, off := m.OfflineSince(alice); off {
		t.Errorf("OfflineSince should report online after a successful ping")
	}
	st := m.PeerLiveness(alice)
	if !st.Online {
		t.Errorf("Online flag should be true after successful ping")
	}
	if st.OnlineSince.IsZero() {
		t.Errorf("OnlineSince should be stamped on the offline→online transition")
	}
}
