//go:build p2p

package p2p

import (
	"log/slog"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/protocol/holepunch"
	"github.com/multiformats/go-multiaddr"
)

// classifyConns reduces a slice of network.Conn objects to a single ConnectionMode.
// Order of precedence (most-significant first):
//
//  1. ModeRelay — any conn over a circuit (Limited or /p2p-circuit) wins;
//     the user perceives the peer as "via relay" even if a direct dial is
//     concurrently in flight.
//  2. ModeHolePunched — a direct conn exists AND the holepunch tracker
//     records a successful punch for this peer.
//  3. ModeDirect — a direct conn exists, no relay involvement.
//  4. ModeNone — empty input.
func classifyConns(conns []network.Conn, holePunched bool) ConnectionMode {
	if len(conns) == 0 {
		return ModeNone
	}
	hasDirect := false
	for _, c := range conns {
		if isRelayConn(c) {
			return ModeRelay
		}
		hasDirect = true
	}
	if !hasDirect {
		return ModeNone
	}
	if holePunched {
		return ModeHolePunched
	}
	return ModeDirect
}

// isRelayConn reports whether a conn is a circuit-v2 relay-mediated conn.
// We check both the runtime "Limited" stat (set by the relay transport) and
// the multiaddr — the former is authoritative on libp2p ≥ v0.31, the latter
// is a defensive fallback for older transports or future refactors.
func isRelayConn(c network.Conn) bool {
	if c.Stat().Limited {
		return true
	}
	return multiaddrHasCircuit(c.RemoteMultiaddr())
}

// multiaddrHasCircuit returns true if the multiaddr contains a /p2p-circuit
// component (P_CIRCUIT = 290).
func multiaddrHasCircuit(ma multiaddr.Multiaddr) bool {
	if ma == nil {
		return false
	}
	found := false
	multiaddr.ForEach(ma, func(c multiaddr.Component) bool {
		if c.Protocol().Code == multiaddr.P_CIRCUIT {
			found = true
			return false
		}
		return true
	})
	return found
}

// holePunchTracker records the outcome of DCUtR attempts so we can report
// "hole-punched" vs "direct" for the connection-mode reporter. We retain
// outcomes for a short TTL so a peer briefly disconnecting and reconnecting
// directly doesn't get mis-labelled.
//
// Implements holepunch.EventTracer so libp2p can plumb events into us.
type holePunchTracker struct {
	mu        sync.RWMutex
	successes map[peer.ID]time.Time
	logger    *slog.Logger
	stopped   bool
}

// holePunchTTL is how long a hole-punch success stays "remembered". After
// this, we fall back to reporting ModeDirect — the connection long outlived
// the punch, so calling it "hole-punched" is misleading.
const holePunchTTL = 30 * time.Minute

func newHolePunchTracker(logger *slog.Logger) *holePunchTracker {
	if logger == nil {
		logger = slog.Default()
	}
	return &holePunchTracker{
		successes: make(map[peer.ID]time.Time),
		logger:    logger,
	}
}

// Trace implements holepunch.EventTracer. We only care about EndHolePunchEvt
// outcomes — failure is logged at debug; success is recorded so subsequent
// ConnectionMode() calls return ModeHolePunched.
//
// Note: holepunch.Event.Peer is the LOCAL peer ID and Event.Remote is the
// peer we punched against — that's the one we want to key on.
func (t *holePunchTracker) Trace(evt *holepunch.Event) {
	if t == nil || evt == nil {
		return
	}
	if evt.Type != holepunch.EndHolePunchEvtT {
		return
	}
	end, ok := evt.Evt.(*holepunch.EndHolePunchEvt)
	if !ok {
		return
	}
	t.recordOutcome(evt.Remote, end)
}

// recordOutcome stores a successful punch against the tracker. Failures are
// logged at debug for visibility but don't change any state.
func (t *holePunchTracker) recordOutcome(p peer.ID, end *holepunch.EndHolePunchEvt) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.stopped {
		return
	}
	if !end.Success {
		t.logger.Debug("p2p hole-punch failed",
			"peer", p, "error", end.Error, "duration", end.EllapsedTime)
		return
	}
	t.successes[p] = time.Now()
	t.logger.Debug("p2p hole-punch succeeded", "peer", p, "duration", end.EllapsedTime)
}

// wasHolePunched returns true if a successful hole-punch was recorded for
// this peer within holePunchTTL.
func (t *holePunchTracker) wasHolePunched(p peer.ID) bool {
	if t == nil {
		return false
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	ts, ok := t.successes[p]
	if !ok {
		return false
	}
	return time.Since(ts) < holePunchTTL
}

// close marks the tracker as stopped — subsequent Trace calls become no-ops.
// The underlying map is dropped to free memory.
func (t *holePunchTracker) close() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.stopped = true
	t.successes = nil
}
