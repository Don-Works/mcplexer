//go:build p2p

package p2p

import (
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

// clock is the time seam the reconnector uses so tests can assert backoff
// scheduling without sleeping in real wall-clock seconds.
type clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

type realClock struct{}

func (realClock) Now() time.Time                         { return time.Now() }
func (realClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// peerState tracks per-peer retry scheduling.
type peerState struct {
	failures int       // consecutive failed dials; reset on success
	nextAt   time.Time // earliest next-attempt time
	lastDial time.Time // floor for minDialGap
}

// clockNow returns the current time from r.clk, falling back to time.Now()
// when callers constructed Reconnector via a struct literal without setting
// clk (existing tests bypass NewReconnector).
func (r *Reconnector) clockNow() time.Time {
	if r.clk == nil {
		return time.Now()
	}
	return r.clk.Now()
}

// peerStateLocked fetches (and lazily creates) the peerState for pid. Caller
// must hold r.mu.
func (r *Reconnector) peerStateLocked(pid peer.ID) *peerState {
	if r.peers == nil {
		r.peers = make(map[peer.ID]*peerState)
	}
	st := r.peers[pid]
	if st == nil {
		st = &peerState{}
		r.peers[pid] = st
	}
	return st
}

// shouldDial enforces both the per-peer minDialGap and the backoff schedule.
// kicked=true skips the backoff gate (a Disconnected event is the strongest
// possible signal to retry now); the minDialGap floor still applies.
func (r *Reconnector) shouldDial(pid peer.ID, kicked bool) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.clockNow()
	st := r.peerStateLocked(pid)
	if !st.lastDial.IsZero() && now.Sub(st.lastDial) < minDialGap {
		return false
	}
	if !kicked && !st.nextAt.IsZero() && now.Before(st.nextAt) {
		return false
	}
	st.lastDial = now
	return true
}

// recordSuccess clears the backoff for a peer.
func (r *Reconnector) recordSuccess(pid peer.ID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.peers == nil {
		return
	}
	if st := r.peers[pid]; st != nil {
		st.failures = 0
		st.nextAt = time.Time{}
	}
}

// recordFailure increments the failure count and schedules the next attempt
// per the user-facing schedule: 2s, 5s, 15s, 30s, 60s, 60s...
func (r *Reconnector) recordFailure(pid peer.ID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	st := r.peerStateLocked(pid)
	st.failures++
	st.nextAt = r.clockNow().Add(computeBackoff(st.failures))
}

// computeBackoff returns the gap before retry attempt N (1-indexed).
// Schedule: 2s, 5s, 15s, 30s, 60s, 60s...
func computeBackoff(failures int) time.Duration {
	switch {
	case failures <= 1:
		return 2 * time.Second
	case failures == 2:
		return 5 * time.Second
	case failures == 3:
		return 15 * time.Second
	case failures == 4:
		return 30 * time.Second
	default:
		return maxBackoff
	}
}
