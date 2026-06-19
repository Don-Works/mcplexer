//go:build p2p

package p2p

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

// fakeHost satisfies the hostForReconnect interface but uses canned data so
// tests don't need a real libp2p stack.
type fakeHost struct {
	mu sync.Mutex

	self peer.ID

	// connected is the set of peer IDs we report as live.
	connected map[peer.ID]struct{}

	// findResults: peer ID -> AddrInfo to return on FindPeer.
	findResults map[peer.ID]peer.AddrInfo
	// findErrors: peer ID -> error to return on FindPeer (preferred over findResults).
	findErrors map[peer.ID]error

	// dialErrors: peer ID -> error to return on ConnectAddrInfo.
	dialErrors map[peer.ID]error

	findCalls    atomic.Int32
	connectCalls atomic.Int32
}

func newFakeHost(self peer.ID) *fakeHost {
	return &fakeHost{
		self:        self,
		connected:   map[peer.ID]struct{}{},
		findResults: map[peer.ID]peer.AddrInfo{},
		findErrors:  map[peer.ID]error{},
		dialErrors:  map[peer.ID]error{},
	}
}

func (f *fakeHost) Self() peer.ID { return f.self }

func (f *fakeHost) IsConnected(p peer.ID) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.connected[p]
	return ok
}

func (f *fakeHost) FindPeer(_ context.Context, p peer.ID) (peer.AddrInfo, error) {
	f.findCalls.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	if err, ok := f.findErrors[p]; ok {
		return peer.AddrInfo{}, err
	}
	if info, ok := f.findResults[p]; ok {
		return info, nil
	}
	return peer.AddrInfo{}, ErrPeerNotFoundInDHT
}

func (f *fakeHost) ConnectAddrInfo(_ context.Context, info peer.AddrInfo) error {
	f.connectCalls.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	if err, ok := f.dialErrors[info.ID]; ok {
		return err
	}
	f.connected[info.ID] = struct{}{}
	return nil
}

// fakeLister returns a fixed list of peer IDs.
type fakeLister struct {
	ids []string
	err error
}

func (f *fakeLister) ListPeerIDs(_ context.Context) ([]string, error) {
	return f.ids, f.err
}

// silentLogger discards all log output so test runs stay clean.
func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// reconnectorWithFakeHost constructs a Reconnector by directly setting the
// fields, bypassing the *Host-typed public constructor. Lets tests inject a
// fake host without standing up libp2p.
func reconnectorWithFakeHost(host hostForReconnect, lister PairedPeerLister, interval time.Duration) *Reconnector {
	if interval <= 0 {
		interval = defaultReconnectInterval
	}
	return &Reconnector{
		host:     host,
		lister:   lister,
		logger:   silentLogger(),
		interval: interval,
		stopCh:   make(chan struct{}),
	}
}

// fakePeer returns a peer.ID derived from a unique name. Decode-able and
// stable across test runs.
func fakePeer(t *testing.T, name string) peer.ID {
	t.Helper()
	// Use a SHA-256-prefixed multihash via a known-good encoding. The peer
	// package produces stable IDs from Ed25519 keys; for tests we don't need
	// real keys — peer.Decode of a valid encoded ID is enough.
	// Generate a deterministic ID by hashing the name into a Ed25519 seed.
	// Simpler: just use known fake CIDs.
	idStrs := map[string]string{
		"self":  "12D3KooWPjceQrSwdWXPyLLeABRXmuqt69Rg3sBYbU1Nft9HyQ6X",
		"alice": "12D3KooWLfzvJVmiBwzwMeH6BDhMnLmgHZUW2Q4qzKjf1upnu1ED",
		"bob":   "12D3KooWQjGYzBpgFhrSjC9NsQHrFFstk3xK8VbNn8wM1qmu7DjL",
		"carol": "12D3KooWBT9XVhWgJh1XaAxEhmvMz4uwQYjNynvbGPxfFBvBpKLi",
	}
	enc, ok := idStrs[name]
	if !ok {
		t.Fatalf("fakePeer: no fixed ID for %q", name)
	}
	pid, err := peer.Decode(enc)
	if err != nil {
		t.Fatalf("fakePeer: decode %q: %v", enc, err)
	}
	return pid
}

func TestReconnector_SkipsConnectedPeers(t *testing.T) {
	self := fakePeer(t, "self")
	alice := fakePeer(t, "alice")

	host := newFakeHost(self)
	host.connected[alice] = struct{}{} // alice already live

	lister := &fakeLister{ids: []string{alice.String()}}
	r := reconnectorWithFakeHost(host, lister, time.Hour)

	r.runOnce(context.Background())

	if got := host.findCalls.Load(); got != 0 {
		t.Errorf("FindPeer called %d times for already-connected peer; want 0", got)
	}
	if got := host.connectCalls.Load(); got != 0 {
		t.Errorf("ConnectAddrInfo called %d times; want 0", got)
	}
}

func TestReconnector_DialsDisconnectedPeers(t *testing.T) {
	self := fakePeer(t, "self")
	alice := fakePeer(t, "alice")
	bob := fakePeer(t, "bob")

	addr1, _ := multiaddr.NewMultiaddr("/ip4/100.87.197.128/tcp/49527")
	host := newFakeHost(self)
	host.connected[bob] = struct{}{} // bob is already connected
	host.findResults[alice] = peer.AddrInfo{ID: alice, Addrs: []multiaddr.Multiaddr{addr1}}

	lister := &fakeLister{ids: []string{alice.String(), bob.String()}}
	r := reconnectorWithFakeHost(host, lister, time.Hour)
	r.runOnce(context.Background())

	if got := host.findCalls.Load(); got != 1 {
		t.Errorf("FindPeer call count = %d; want 1 (only for alice)", got)
	}
	if got := host.connectCalls.Load(); got != 1 {
		t.Errorf("ConnectAddrInfo call count = %d; want 1", got)
	}
	if !host.IsConnected(alice) {
		t.Errorf("alice should be connected after runOnce")
	}
}

func TestReconnector_SkipsSelf(t *testing.T) {
	self := fakePeer(t, "self")
	host := newFakeHost(self)

	lister := &fakeLister{ids: []string{self.String()}}
	r := reconnectorWithFakeHost(host, lister, time.Hour)
	r.runOnce(context.Background())

	if got := host.findCalls.Load(); got != 0 {
		t.Errorf("FindPeer called %d times for self; want 0", got)
	}
}

func TestReconnector_SwallowsFindErrors(t *testing.T) {
	self := fakePeer(t, "self")
	alice := fakePeer(t, "alice")
	bob := fakePeer(t, "bob")
	carol := fakePeer(t, "carol")

	host := newFakeHost(self)
	host.findErrors[alice] = ErrDHTUnavailable
	host.findErrors[bob] = ErrPeerNotFoundInDHT
	host.findErrors[carol] = errors.New("network blip")

	lister := &fakeLister{ids: []string{alice.String(), bob.String(), carol.String()}}
	r := reconnectorWithFakeHost(host, lister, time.Hour)

	// Must not panic, must not connect, must process all three peers.
	r.runOnce(context.Background())

	if got := host.findCalls.Load(); got != 3 {
		t.Errorf("FindPeer call count = %d; want 3", got)
	}
	if got := host.connectCalls.Load(); got != 0 {
		t.Errorf("ConnectAddrInfo called %d times despite all FindPeer errors; want 0", got)
	}
}

func TestReconnector_ListErrorIsBestEffort(t *testing.T) {
	self := fakePeer(t, "self")
	host := newFakeHost(self)
	lister := &fakeLister{err: errors.New("db gone")}

	r := reconnectorWithFakeHost(host, lister, time.Hour)
	// Must not panic, must touch nothing.
	r.runOnce(context.Background())

	if got := host.findCalls.Load(); got != 0 {
		t.Errorf("FindPeer called %d times despite list error; want 0", got)
	}
}

func TestReconnector_StartStopRespectsContext(t *testing.T) {
	self := fakePeer(t, "self")
	host := newFakeHost(self)
	lister := &fakeLister{}
	r := reconnectorWithFakeHost(host, lister, 5*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	r.Start(ctx)
	time.Sleep(20 * time.Millisecond)
	cancel()
	// Give the goroutine a moment to observe ctx.Done.
	time.Sleep(20 * time.Millisecond)
	r.Close() // must be a no-op after ctx cancel — nothing should panic
}

func TestNewReconnector_NilSafe(t *testing.T) {
	if got := NewReconnector(nil, &fakeLister{}, 0, nil); got != nil {
		t.Errorf("NewReconnector(nil host) = %v; want nil", got)
	}
	if got := NewReconnector(&Host{}, nil, 0, nil); got != nil {
		t.Errorf("NewReconnector(nil lister) = %v; want nil", got)
	}
}

// fakeClock advances only when Advance is called. After-channels fire as
// soon as their deadline is reached by an Advance.
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

type fakeTimer struct {
	deadline time.Time
	ch       chan time.Time
	fired    bool
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Unix(1_700_000_000, 0)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{deadline: c.now.Add(d), ch: make(chan time.Time, 1)}
	c.timers = append(c.timers, t)
	return t.ch
}

// Advance moves the clock forward by d and fires any timers whose deadline
// has passed.
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	now := c.now
	timers := c.timers
	c.mu.Unlock()
	for _, t := range timers {
		if !t.fired && !now.Before(t.deadline) {
			t.fired = true
			select {
			case t.ch <- now:
			default:
			}
		}
	}
}

// reconnectorWithClock builds a fake-host-backed Reconnector that uses a
// deterministic clock and an initialised kick channel and peer state.
func reconnectorWithClock(host hostForReconnect, lister PairedPeerLister, clk clock, interval time.Duration) *Reconnector {
	if interval <= 0 {
		interval = defaultReconnectInterval
	}
	return &Reconnector{
		host:     host,
		lister:   lister,
		logger:   silentLogger(),
		clk:      clk,
		interval: interval,
		kickCh:   make(chan peer.ID, 8),
		peers:    make(map[peer.ID]*peerState),
		stopCh:   make(chan struct{}),
	}
}

// TestReconnector_KickTriggersImmediateDial: a Kick must produce a dial
// within ~100ms even when the safety-net interval is much longer.
func TestReconnector_KickTriggersImmediateDial(t *testing.T) {
	self := fakePeer(t, "self")
	alice := fakePeer(t, "alice")

	addr1, _ := multiaddr.NewMultiaddr("/ip4/100.87.197.128/tcp/49527")
	host := newFakeHost(self)
	host.findResults[alice] = peer.AddrInfo{ID: alice, Addrs: []multiaddr.Multiaddr{addr1}}

	lister := &fakeLister{ids: []string{alice.String()}}
	clk := newFakeClock()
	// 1h interval — only an event-driven path can dial inside the test.
	r := reconnectorWithClock(host, lister, clk, time.Hour)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.loop(ctx)
	defer r.Close()

	// Wait for the initial sweep to dial alice (success).
	if !waitFor(500*time.Millisecond, func() bool { return host.connectCalls.Load() >= 1 }) {
		t.Fatalf("setup: initial sweep should have dialed alice")
	}
	startCalls := host.connectCalls.Load()

	// Advance past the 1s minDialGap floor so the next dial isn't blocked
	// by spam protection.
	clk.Advance(2 * time.Second)

	// Simulate a libp2p Disconnected event.
	host.mu.Lock()
	delete(host.connected, alice)
	host.mu.Unlock()

	kickStart := time.Now()
	r.Kick(alice)

	if !waitFor(500*time.Millisecond, func() bool { return host.connectCalls.Load() > startCalls }) {
		t.Fatalf("Kick did not produce a dial within 500ms")
	}
	elapsed := time.Since(kickStart)
	if elapsed > 100*time.Millisecond {
		t.Errorf("Kick→dial latency = %v; want <100ms", elapsed)
	}
}

// TestReconnector_BackoffSchedule: three failures in a row produce gaps of
// 2s, 5s, 15s before the next attempt is permitted (per shouldDial).
func TestReconnector_BackoffSchedule(t *testing.T) {
	self := fakePeer(t, "self")
	alice := fakePeer(t, "alice")

	host := newFakeHost(self)
	host.findErrors[alice] = ErrPeerNotFoundInDHT // make every dial fail

	lister := &fakeLister{ids: []string{alice.String()}}
	clk := newFakeClock()
	r := reconnectorWithClock(host, lister, clk, time.Hour)

	// Each call to runOnce attempts exactly one dial — until backoff gates
	// the next. Schedule (per spec): 2s, 5s, 15s gaps after failures 1, 2, 3.
	step := func(label string, gap time.Duration, wantCalls int32) {
		t.Helper()
		// Advance just under the gap → dial blocked.
		clk.Advance(gap - 100*time.Millisecond)
		r.runOnce(context.Background())
		if got := host.findCalls.Load(); got != wantCalls-1 {
			t.Errorf("%s: dial fired before %s backoff: findCalls=%d want %d", label, gap, got, wantCalls-1)
		}
		// Cross the threshold → dial fires.
		clk.Advance(200 * time.Millisecond)
		r.runOnce(context.Background())
		if got := host.findCalls.Load(); got != wantCalls {
			t.Fatalf("%s: after %s backoff findCalls=%d want %d", label, gap, got, wantCalls)
		}
	}
	r.runOnce(context.Background()) // dial #1, fails → schedules 2s
	if got := host.findCalls.Load(); got != 1 {
		t.Fatalf("first sweep findCalls=%d; want 1", got)
	}
	step("after 1 failure", 2*time.Second, 2)
	step("after 2 failures", 5*time.Second, 3)
	step("after 3 failures", 15*time.Second, 4)
}

// TestReconnector_SuccessResetsBackoff: a successful dial clears the failure
// counter so the next failure starts from the 2s base again.
func TestReconnector_SuccessResetsBackoff(t *testing.T) {
	self := fakePeer(t, "self")
	alice := fakePeer(t, "alice")

	addr1, _ := multiaddr.NewMultiaddr("/ip4/127.0.0.1/tcp/1234")
	host := newFakeHost(self)
	host.findErrors[alice] = ErrPeerNotFoundInDHT

	lister := &fakeLister{ids: []string{alice.String()}}
	clk := newFakeClock()
	r := reconnectorWithClock(host, lister, clk, time.Hour)

	// Two failures: backoff should be at the 5s tier.
	r.runOnce(context.Background())
	clk.Advance(2100 * time.Millisecond)
	r.runOnce(context.Background())
	r.mu.Lock()
	gotFailures := r.peers[alice].failures
	r.mu.Unlock()
	if gotFailures != 2 {
		t.Fatalf("failures=%d after 2 failed dials; want 2", gotFailures)
	}

	// Now make the dial succeed and advance past the 5s gate.
	host.mu.Lock()
	delete(host.findErrors, alice)
	host.findResults[alice] = peer.AddrInfo{ID: alice, Addrs: []multiaddr.Multiaddr{addr1}}
	host.mu.Unlock()
	clk.Advance(5100 * time.Millisecond)
	r.runOnce(context.Background())

	r.mu.Lock()
	gotFailures = r.peers[alice].failures
	gotNext := r.peers[alice].nextAt
	r.mu.Unlock()
	if gotFailures != 0 {
		t.Errorf("failures=%d after successful dial; want 0", gotFailures)
	}
	if !gotNext.IsZero() {
		t.Errorf("nextAt=%v after successful dial; want zero", gotNext)
	}
}

// TestReconnector_SafetyTickFires: with no kicks, the safety-net tick still
// runs sweeps at the configured cadence.
func TestReconnector_SafetyTickFires(t *testing.T) {
	self := fakePeer(t, "self")
	alice := fakePeer(t, "alice")

	addr1, _ := multiaddr.NewMultiaddr("/ip4/127.0.0.1/tcp/1234")
	host := newFakeHost(self)
	host.findResults[alice] = peer.AddrInfo{ID: alice, Addrs: []multiaddr.Multiaddr{addr1}}

	lister := &fakeLister{ids: []string{alice.String()}}
	clk := newFakeClock()
	r := reconnectorWithClock(host, lister, clk, 30*time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.loop(ctx)
	defer r.Close()

	// Wait for the initial sweep — alice gets connected.
	if !waitFor(500*time.Millisecond, func() bool { return host.IsConnected(alice) }) {
		t.Fatalf("initial sweep did not connect alice")
	}

	// Force alice offline; safety tick (30s) should re-dial without a Kick.
	host.mu.Lock()
	delete(host.connected, alice)
	host.mu.Unlock()
	startCalls := host.connectCalls.Load()

	// Advance past the safety interval; the After channel fires; a sweep runs.
	clk.Advance(31 * time.Second)
	if !waitFor(500*time.Millisecond, func() bool { return host.connectCalls.Load() > startCalls }) {
		t.Fatalf("safety-net tick did not produce a dial after 30s advance")
	}
}

// TestComputeBackoff sanity-checks the exact schedule called out in the spec.
func TestComputeBackoff(t *testing.T) {
	cases := []struct {
		failures int
		want     time.Duration
	}{
		{0, 2 * time.Second},
		{1, 2 * time.Second},
		{2, 5 * time.Second},
		{3, 15 * time.Second},
		{4, 30 * time.Second},
		{5, 60 * time.Second},
		{99, 60 * time.Second},
	}
	for _, c := range cases {
		if got := computeBackoff(c.failures); got != c.want {
			t.Errorf("computeBackoff(%d)=%v; want %v", c.failures, got, c.want)
		}
	}
}

// TestReconnector_StatusConnected verifies that a successful dial sets
// reconnect_state="connected" and a non-zero last_dial_attempt_at.
func TestReconnector_StatusConnected(t *testing.T) {
	self := fakePeer(t, "self")
	alice := fakePeer(t, "alice")

	addr, _ := multiaddr.NewMultiaddr("/ip4/100.87.197.128/tcp/49527")
	host := newFakeHost(self)
	host.findResults[alice] = peer.AddrInfo{ID: alice, Addrs: []multiaddr.Multiaddr{addr}}
	lister := &fakeLister{ids: []string{alice.String()}}
	r := reconnectorWithFakeHost(host, lister, time.Hour)
	r.runOnce(context.Background())

	st := r.PeerStatus(alice)
	if st.State != ReconnectStateConnected {
		t.Errorf("State = %q; want %q", st.State, ReconnectStateConnected)
	}
	if st.LastAttempt.IsZero() {
		t.Errorf("LastAttempt should be non-zero on a successful dial")
	}
	if st.LastError != "" {
		t.Errorf("LastError = %q; want empty", st.LastError)
	}
}

// TestReconnector_StatusAlreadyConnected verifies that the "skip already
// connected" branch still records reconnect_state="connected" so the UI shows
// the right green badge for live peers.
func TestReconnector_StatusAlreadyConnected(t *testing.T) {
	self := fakePeer(t, "self")
	alice := fakePeer(t, "alice")
	host := newFakeHost(self)
	host.connected[alice] = struct{}{}
	lister := &fakeLister{ids: []string{alice.String()}}
	r := reconnectorWithFakeHost(host, lister, time.Hour)
	r.runOnce(context.Background())

	st := r.PeerStatus(alice)
	if st.State != ReconnectStateConnected {
		t.Errorf("State = %q; want %q", st.State, ReconnectStateConnected)
	}
}

// TestReconnector_StatusFindErrors maps each FindPeer error to its expected
// reconnect_state without depending on the underlying error string.
func TestReconnector_StatusFindErrors(t *testing.T) {
	self := fakePeer(t, "self")
	alice := fakePeer(t, "alice")
	bob := fakePeer(t, "bob")
	carol := fakePeer(t, "carol")

	host := newFakeHost(self)
	host.findErrors[alice] = ErrDHTUnavailable
	host.findErrors[bob] = ErrPeerNotFoundInDHT
	host.findErrors[carol] = errors.New("dial 100.87.197.128:49527 i/o timeout")
	lister := &fakeLister{ids: []string{alice.String(), bob.String(), carol.String()}}
	r := reconnectorWithFakeHost(host, lister, time.Hour)
	r.runOnce(context.Background())

	cases := []struct {
		pid     peer.ID
		want    string
		wantErr bool
	}{
		{alice, ReconnectStateDHTUnavailable, false},
		{bob, ReconnectStateNotFoundInDHT, false},
		{carol, ReconnectStateSearchingDHT, true},
	}
	for _, tc := range cases {
		st := r.PeerStatus(tc.pid)
		if st.State != tc.want {
			t.Errorf("peer %s state = %q; want %q", tc.pid, st.State, tc.want)
		}
		if tc.wantErr && st.LastError == "" {
			t.Errorf("peer %s LastError should be non-empty", tc.pid)
		}
	}
}

// TestReconnector_StatusDialFailure verifies that a FindPeer-success +
// ConnectAddrInfo-failure flow is reported as reconnect_state="dial_failed".
func TestReconnector_StatusDialFailure(t *testing.T) {
	self := fakePeer(t, "self")
	alice := fakePeer(t, "alice")

	addr, _ := multiaddr.NewMultiaddr("/ip4/100.87.197.128/tcp/49527")
	host := newFakeHost(self)
	host.findResults[alice] = peer.AddrInfo{ID: alice, Addrs: []multiaddr.Multiaddr{addr}}
	host.dialErrors[alice] = errors.New("connect 192.168.1.5:54321: connection refused")
	lister := &fakeLister{ids: []string{alice.String()}}
	r := reconnectorWithFakeHost(host, lister, time.Hour)
	r.runOnce(context.Background())

	st := r.PeerStatus(alice)
	if st.State != ReconnectStateDialFailed {
		t.Errorf("State = %q; want %q", st.State, ReconnectStateDialFailed)
	}
	if st.LastAttempt.IsZero() {
		t.Errorf("LastAttempt should be non-zero on a failed dial")
	}
	// Redaction: raw IP/port must NOT appear in the surfaced error string.
	if st.LastError == "" {
		t.Fatalf("LastError should be non-empty on dial failure")
	}
	for _, leaked := range []string{"192.168.1.5", "54321"} {
		if strings.Contains(st.LastError, leaked) {
			t.Errorf("LastError = %q leaked %q; expected redaction", st.LastError, leaked)
		}
	}
}

// TestReconnector_AllPeerStatusSnapshot verifies that AllPeerStatus returns
// every observed peer keyed by string and is independent of the source map
// (caller may mutate without affecting the reconnector).
func TestReconnector_AllPeerStatusSnapshot(t *testing.T) {
	self := fakePeer(t, "self")
	alice := fakePeer(t, "alice")
	host := newFakeHost(self)
	host.connected[alice] = struct{}{}
	lister := &fakeLister{ids: []string{alice.String()}}
	r := reconnectorWithFakeHost(host, lister, time.Hour)
	r.runOnce(context.Background())

	snap := r.AllPeerStatus()
	if _, ok := snap[alice.String()]; !ok {
		t.Fatalf("AllPeerStatus missing alice; got %+v", snap)
	}
	// Mutation of the snapshot must not affect the live store.
	delete(snap, alice.String())
	if _, ok := r.AllPeerStatus()[alice.String()]; !ok {
		t.Errorf("snapshot mutation leaked into live store")
	}
}

// TestReconnector_StatusNilSafe — calling status methods on a nil receiver
// (the stub-build pattern) returns zero values without panic.
func TestReconnector_StatusNilSafe(t *testing.T) {
	var r *Reconnector
	if st := r.PeerStatusByID("12D3KooW1"); st.State != "" {
		t.Errorf("nil PeerStatusByID returned non-zero state: %+v", st)
	}
	if m := r.AllPeerStatus(); m == nil || len(m) != 0 {
		t.Errorf("nil AllPeerStatus = %+v; want non-nil empty", m)
	}
}

// TestReconnector_MarkConnectedRefreshesStaleSearchingState is the regression
// test for the UI's "last seen now + Searching badge" contradiction. After a
// failed FindPeer leaves reconnect_state="searching_dht", a successful
// liveness ping (via MarkConnected) must flip the state back to "connected"
// so the badge agrees with last_seen.
func TestReconnector_MarkConnectedRefreshesStaleSearchingState(t *testing.T) {
	self := fakePeer(t, "self")
	alice := fakePeer(t, "alice")

	host := newFakeHost(self)
	// Seed a stale "searching" state by forcing FindPeer to fail with a
	// generic transport error (mapped to searching_dht).
	host.findErrors[alice] = errors.New("dial 100.87.197.128:49527 i/o timeout")
	lister := &fakeLister{ids: []string{alice.String()}}
	r := reconnectorWithFakeHost(host, lister, time.Hour)
	r.runOnce(context.Background())

	if st := r.PeerStatus(alice); st.State != ReconnectStateSearchingDHT {
		t.Fatalf("precondition: State = %q; want %q", st.State, ReconnectStateSearchingDHT)
	}

	// Simulate the liveness monitor observing a successful ping while the
	// reconnector still thinks the peer is being searched for.
	r.MarkConnected(alice)

	if st := r.PeerStatus(alice); st.State != ReconnectStateConnected {
		t.Errorf("After MarkConnected: State = %q; want %q", st.State, ReconnectStateConnected)
	}
}

// TestReconnector_MarkConnectedFiresOnlineObserverOnce verifies that calling
// MarkConnected runs the offline→online observer exactly once per edge —
// repeated calls while still connected stay silent. This is what wakes the
// mesh outbound queue on natural reconnects (libp2p auto-dial) that the
// reconnector itself didn't initiate.
func TestReconnector_MarkConnectedFiresOnlineObserverOnce(t *testing.T) {
	self := fakePeer(t, "self")
	alice := fakePeer(t, "alice")

	host := newFakeHost(self)
	lister := &fakeLister{ids: []string{alice.String()}}
	r := reconnectorWithFakeHost(host, lister, time.Hour)

	var fired atomic.Int32
	r.AddOnlineObserver(func(id string) {
		if id == alice.String() {
			fired.Add(1)
		}
	})

	r.MarkConnected(alice)
	r.MarkConnected(alice)
	r.MarkConnected(alice)

	// Observer runs in its own goroutine — give it a moment to register.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) && fired.Load() < 1 {
		time.Sleep(5 * time.Millisecond)
	}
	if got := fired.Load(); got != 1 {
		t.Errorf("observer fired %d times; want exactly 1", got)
	}
}

// TestReconnector_MarkConnectedNilSafe — stub-build / pre-Start callers can
// invoke MarkConnected without spinning up the loop.
func TestReconnector_MarkConnectedNilSafe(t *testing.T) {
	var r *Reconnector
	r.MarkConnected(fakePeer(t, "alice")) // must not panic
}

// TestRedactDialError table-tests the IP-stripping regex.
func TestRedactDialError(t *testing.T) {
	cases := []struct {
		name string
		in   error
		// any of these substrings must NOT appear in the output
		mustNotContain []string
	}{
		{
			name:           "ipv4 with port",
			in:             errors.New("dial 100.87.197.128:49527 timeout"),
			mustNotContain: []string{"100.87.197.128", "49527"},
		},
		{
			name:           "multiaddr",
			in:             errors.New("connect /ip4/10.0.0.1/tcp/4001 refused"),
			mustNotContain: []string{"10.0.0.1", "4001"},
		},
		{
			name:           "ipv6 bracketed",
			in:             errors.New("dial [fe80::1]:8080 unreachable"),
			mustNotContain: []string{"fe80::1", "8080"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := redactDialError(tc.in)
			for _, leak := range tc.mustNotContain {
				if strings.Contains(got, leak) {
					t.Errorf("redactDialError(%q) = %q leaked %q", tc.in, got, leak)
				}
			}
		})
	}
	if redactDialError(nil) != "" {
		t.Errorf("redactDialError(nil) should be empty")
	}
}
