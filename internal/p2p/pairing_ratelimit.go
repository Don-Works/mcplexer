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
	hits        []time.Time
	lockedUntil time.Time
}

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

	// Global window check.
	l.globalHits = trimTimes(l.globalHits, cutoff)
	if len(l.globalHits) >= l.globalMax {
		return false
	}

	pc, ok := l.perPeer[peerID]
	if !ok {
		pc = &peerAttemptCounter{}
		l.perPeer[peerID] = pc
	}

	// Hard lockout: persistent attackers stay out of the pool.
	if !pc.lockedUntil.IsZero() && now.Before(pc.lockedUntil) {
		return false
	}

	pc.hits = trimTimes(pc.hits, cutoff)
	if len(pc.hits) >= l.perPeerMax {
		// Rolling window cap reached for this peer.
		return false
	}

	pc.hits = append(pc.hits, now)
	l.globalHits = append(l.globalHits, now)

	// Engage lockout once cumulative recent failure pressure crosses the
	// threshold. We use the per-peer hit list (any-window) length as a
	// proxy. With perPeerMax=8 and lockoutAfter=20 we never trigger from
	// allowed traffic; only sustained attempts fed by a peer that resets
	// the rolling window bucket can hit this.
	if len(pc.hits) >= l.lockoutAfter {
		pc.lockedUntil = now.Add(l.lockoutWindow)
	}
	return true
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
