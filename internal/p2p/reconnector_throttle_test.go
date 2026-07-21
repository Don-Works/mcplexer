//go:build p2p

package p2p

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

// fixedClock returns a controllable Now() for throttle tests.
type fixedClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fixedClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fixedClock) After(_ time.Duration) <-chan time.Time {
	// Tests drive the loop manually via runOnce — never wait on timers.
	return make(chan time.Time)
}

func (c *fixedClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// fixedOracle reports a peer as offline since a stamped time.
type fixedOracle struct {
	mu      sync.Mutex
	offline map[peer.ID]time.Time
}

func newFixedOracle() *fixedOracle { return &fixedOracle{offline: map[peer.ID]time.Time{}} }

func (o *fixedOracle) OfflineSince(p peer.ID) (time.Time, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	t, ok := o.offline[p]
	return t, ok
}

func (o *fixedOracle) markOfflineAt(p peer.ID, t time.Time) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.offline[p] = t
}

func reconnectorWithClockAndOracle(host hostForReconnect, lister PairedPeerLister, clk clock, oracle LivenessOracle) *Reconnector {
	return &Reconnector{
		host:             host,
		lister:           lister,
		logger:           silentLogger(),
		interval:         time.Hour,
		stopCh:           make(chan struct{}),
		clk:              clk,
		liveness:         oracle,
		lastOfflineSweep: map[peer.ID]time.Time{},
	}
}

func TestReconnector_OfflineThrottle_SkipsRepeatedDHTSearch(t *testing.T) {
	self := fakePeer(t, "self")
	alice := fakePeer(t, "alice")

	host := newFakeHost(self)
	host.findErrors[alice] = ErrPeerNotFoundInDHT
	lister := &fakeLister{ids: []string{alice.String()}}

	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	clk := &fixedClock{now: now}
	oracle := newFixedOracle()
	// alice has been offline for 20 minutes — past offlineCutoff (10m).
	oracle.markOfflineAt(alice, now.Add(-20*time.Minute))

	r := reconnectorWithClockAndOracle(host, lister, clk, oracle)

	// First sweep: throttle records lastOfflineSweep, lets the dial through.
	r.runOnce(context.Background())
	if got := host.findCalls.Load(); got != 1 {
		t.Fatalf("first sweep: FindPeer calls = %d; want 1 (throttle records timestamp)", got)
	}

	// Second sweep 5 minutes later: well within offlineSweepGap (30m), throttle skips.
	clk.advance(5 * time.Minute)
	r.runOnce(context.Background())
	if got := host.findCalls.Load(); got != 1 {
		t.Errorf("after 5min: FindPeer calls = %d; want 1 — throttle should skip", got)
	}

	// Advance past offlineSweepGap: next sweep dials again.
	clk.advance(offlineSweepGap + time.Second)
	r.runOnce(context.Background())
	if got := host.findCalls.Load(); got != 2 {
		t.Errorf("after %v: FindPeer calls = %d; want 2 — throttle should release", offlineSweepGap, got)
	}
}

func TestReconnector_OfflineThrottle_KickBypasses(t *testing.T) {
	self := fakePeer(t, "self")
	alice := fakePeer(t, "alice")

	host := newFakeHost(self)
	host.findErrors[alice] = ErrPeerNotFoundInDHT
	lister := &fakeLister{ids: []string{alice.String()}}

	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	clk := &fixedClock{now: now}
	oracle := newFixedOracle()
	oracle.markOfflineAt(alice, now.Add(-2*time.Hour)) // long offline

	r := reconnectorWithClockAndOracle(host, lister, clk, oracle)

	// Prime the throttle with a sweep.
	r.runOnce(context.Background())
	calls := host.findCalls.Load()

	// Two more kicks, each separated by enough time to clear minDialGap
	// AND the post-failure backoff. Throttle must NOT gate explicit kicks
	// even though the peer is still long-offline per the oracle.
	clk.advance(2*time.Second + 100*time.Millisecond)
	r.handleKick(context.Background(), alice)
	clk.advance(5 * time.Second)
	r.handleKick(context.Background(), alice)
	if got := host.findCalls.Load(); got != calls+2 {
		t.Errorf("after 2 kicks: FindPeer calls grew by %d; want 2 (throttle should not gate kicks)", got-calls)
	}
}

func TestReconnector_OfflineThrottle_BelowCutoffNoSkip(t *testing.T) {
	self := fakePeer(t, "self")
	alice := fakePeer(t, "alice")

	host := newFakeHost(self)
	host.findErrors[alice] = ErrPeerNotFoundInDHT
	lister := &fakeLister{ids: []string{alice.String()}}

	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	clk := &fixedClock{now: now}
	oracle := newFixedOracle()
	// Only 3 minutes offline — below offlineCutoff (10m). Reconnector
	// should keep the historical eager behaviour: every sweep dials.
	oracle.markOfflineAt(alice, now.Add(-3*time.Minute))

	r := reconnectorWithClockAndOracle(host, lister, clk, oracle)

	// Advance between sweeps to clear the per-peer post-failure backoff
	// (2s on failure 1, 5s on 2, 15s on 3) — we're testing the offline
	// *throttle* here, not the regular backoff.
	r.runOnce(context.Background())
	clk.advance(maxBackoff + time.Second)
	r.runOnce(context.Background())
	clk.advance(maxBackoff + time.Second)
	r.runOnce(context.Background())
	if got := host.findCalls.Load(); got != 3 {
		t.Errorf("3 sweeps below cutoff: FindPeer calls = %d; want 3 (no throttle)", got)
	}
}

func TestReconnector_OfflineThrottle_ClearsOnReconnect(t *testing.T) {
	self := fakePeer(t, "self")
	alice := fakePeer(t, "alice")

	host := newFakeHost(self)
	host.findErrors[alice] = ErrPeerNotFoundInDHT
	lister := &fakeLister{ids: []string{alice.String()}}

	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	clk := &fixedClock{now: now}
	oracle := newFixedOracle()
	oracle.markOfflineAt(alice, now.Add(-1*time.Hour))

	r := reconnectorWithClockAndOracle(host, lister, clk, oracle)
	r.runOnce(context.Background()) // primes throttle

	// Peer comes back; oracle stops reporting offline; reconnect via Kick.
	host.mu.Lock()
	host.connected[alice] = struct{}{}
	host.mu.Unlock()
	oracle.mu.Lock()
	delete(oracle.offline, alice)
	oracle.mu.Unlock()
	r.runOnce(context.Background()) // observes connected → clearOfflineSweep

	// Peer goes offline again, freshly. Re-record offline. Advance the
	// clock past minDialGap so the regular per-peer floor isn't what
	// gates this run.
	clk.advance(maxBackoff + time.Second)
	host.mu.Lock()
	delete(host.connected, alice)
	host.mu.Unlock()
	oracle.markOfflineAt(alice, clk.Now().Add(-15*time.Minute))

	calls := host.findCalls.Load()
	r.runOnce(context.Background())
	if got := host.findCalls.Load(); got != calls+1 {
		t.Errorf("after reconnect+disconnect: FindPeer calls grew by %d; want 1 (throttle cleared)", got-calls)
	}
}
