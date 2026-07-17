package p2p

import (
	"sync"
	"time"
)

// pairingRateLimiter caps how often a single peer (and the daemon as a
// whole) may attempt a 6-digit pairing code. With a 5-minute TTL and only
// 10^6 codes, an unlimited attacker who knows our PeerID could brute-force
// the space; rate-limiting reduces the realistic attack surface to far
// less than that. Limits are enforced regardless of whether the attempt
// matched a valid code — wrong codes count.
type pairingRateLimiter struct {
	mu sync.Mutex

	perPeer    map[string]*peerAttemptCounter
	globalHits []time.Time

	perPeerMax    int
	globalMax     int
	window        time.Duration
	lockoutWindow time.Duration
	lockoutAfter  int
}

type peerAttemptCounter struct {
	hits []time.Time
	// lockoutHits is a SEPARATE, longer-window (lockoutWindow) attempt history
	// used to drive the hard lockout. The rolling-window hits slice is trimmed
	// to `window` and capped at perPeerMax, so it can never reach lockoutAfter
	// — the lockout was previously unreachable dead code. Every attempt (incl.
	// window/global-rejected ones) counts here so sustained hammering locks a
	// peer out.
	lockoutHits []time.Time
	lockedUntil time.Time
}

// maxPairingRateLimitPeers bounds the perPeer map. Pairing streams are
// reachable pre-pairing and the peer ID is attacker-chosen, so without
// eviction the map grows one entry per distinct ID forever. When it crosses
// this, entries whose windows have fully drained and are not locked are swept.
const maxPairingRateLimitPeers = 4096

func newPairingRateLimiter() *pairingRateLimiter {
	return &pairingRateLimiter{
		perPeer:       make(map[string]*peerAttemptCounter),
		perPeerMax:    8,
		globalMax:     60,
		window:        time.Minute,
		lockoutAfter:  20, // 20 attempts in any single peer's history
		lockoutWindow: 30 * time.Minute,
	}
}

// allow records an attempt by the given peer ID and returns true if the
// attempt should proceed. A return of false means the caller must reject
// the handshake without checking the code.
func (l *pairingRateLimiter) allow(peerID string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	cutoff := now.Add(-l.window)
	lockoutCutoff := now.Add(-l.lockoutWindow)

	l.globalHits = trimTimes(l.globalHits, cutoff)
	if len(l.perPeer) > maxPairingRateLimitPeers {
		l.prunePeersLocked(cutoff, lockoutCutoff, now)
	}

	pc, ok := l.perPeer[peerID]
	if !ok {
		pc = &peerAttemptCounter{}
		l.perPeer[peerID] = pc
	}

	// countLockout records the attempt toward the hard lockout and engages it
	// once cumulative pressure crosses the threshold.
	countLockout := func() {
		pc.lockoutHits = trimTimes(pc.lockoutHits, lockoutCutoff)
		pc.lockoutHits = append(pc.lockoutHits, now)
		if len(pc.lockoutHits) >= l.lockoutAfter {
			pc.lockedUntil = now.Add(l.lockoutWindow)
		}
	}

	// Hard lockout from a prior threshold crossing: reject and re-arm so a
	// peer that keeps hammering stays out for the full window.
	if !pc.lockedUntil.IsZero() && now.Before(pc.lockedUntil) {
		countLockout()
		return false
	}

	// Global window check.
	if len(l.globalHits) >= l.globalMax {
		return false
	}

	// Per-peer rolling window cap. A rejection here still counts toward the
	// lockout so sustained per-minute-cap hammering eventually locks out.
	pc.hits = trimTimes(pc.hits, cutoff)
	if len(pc.hits) >= l.perPeerMax {
		countLockout()
		return false
	}

	pc.hits = append(pc.hits, now)
	l.globalHits = append(l.globalHits, now)
	countLockout()
	return true
}

// prunePeersLocked evicts perPeer entries whose rolling and lockout windows
// have both fully drained and which are not currently locked. Caller holds mu.
func (l *pairingRateLimiter) prunePeersLocked(cutoff, lockoutCutoff, now time.Time) {
	for id, pc := range l.perPeer {
		if !pc.lockedUntil.IsZero() && now.Before(pc.lockedUntil) {
			continue
		}
		pc.hits = trimTimes(pc.hits, cutoff)
		pc.lockoutHits = trimTimes(pc.lockoutHits, lockoutCutoff)
		if len(pc.hits) == 0 && len(pc.lockoutHits) == 0 {
			delete(l.perPeer, id)
		}
	}
}

// trimTimes returns ts trimmed to entries strictly after cutoff. The slice
// is truncated in place; callers should treat the return value as the
// authoritative result.
func trimTimes(ts []time.Time, cutoff time.Time) []time.Time {
	out := ts[:0]
	for _, t := range ts {
		if t.After(cutoff) {
			out = append(out, t)
		}
	}
	return out
}
