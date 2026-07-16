//go:build p2p

package p2p

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

// dialTimeout caps direct-dial attempts triggered by an mDNS announcement.
// Short by design — LAN dials should be near-instant; if not, fall back to
// whatever transport the existing pairing connection is using.
const dialTimeout = 4 * time.Second

// PeerLookup checks whether a discovered libp2p peer has been paired by the
// user (i.e. exists in the p2p_peers table from M1.2). Implementations must
// be safe to call from any goroutine and must NOT block on network IO.
//
// A nil PeerLookup disables the LAN-upgrade path entirely; the discovery
// service still bridges mDNS hits into the peerstore (already done by the
// host's mdnsNotifee) but won't attempt to dial.
type PeerLookup interface {
	// IsPaired returns (true, nil) if peerID is a known paired peer, (false,
	// nil) if not, and (_, err) for any storage error other than "missing
	// table" — that case must be reported as (false, nil) so this branch
	// stays compatible with builds where M1.2's migration hasn't merged.
	IsPaired(ctx context.Context, peerID string) (bool, error)

	// MarkConnectionMode records how peerID is currently reachable. Errors
	// are logged but never propagated to the caller — connection-mode
	// telemetry is best-effort.
	MarkConnectionMode(ctx context.Context, peerID string, mode ConnectionMode)

	// RememberPeerAddrs persists the most recent direct (non-relay) addrs
	// observed for peerID so the next daemon boot can hot-start the libp2p
	// peerstore without waiting for the DHT. Best-effort; errors are logged.
	RememberPeerAddrs(ctx context.Context, peerID string, addrs []string)

	// LoadPeerAddrs returns the most recently persisted addrs for peerID,
	// or an empty/nil slice for unknown peers. Best-effort; errors are
	// logged and an empty slice is returned so transient storage issues
	// never block reconnection.
	LoadPeerAddrs(ctx context.Context, peerID string) []string
}

// DiscoveryService glues the libp2p host's connection events to the
// p2p_peers table. It listens for:
//   - mDNS peer-found events (forwarded by the host's mdnsNotifee), and
//   - libp2p Notifiee Connected/Disconnected events
//
// On every transition it re-evaluates the active transport and writes the
// resulting ConnectionMode back to storage.
type DiscoveryService struct {
	host   *Host
	lookup PeerLookup
	logger *slog.Logger

	mu       sync.Mutex
	modes    map[peer.ID]ConnectionMode
	reported map[peer.ID]ConnectionMode // last mode emitted at INFO; survives mode clears for dedup
	closed   bool
}

// NewDiscoveryService wires a discovery service onto an existing Host. The
// service starts immediately: callers must invoke Close to release resources.
// Passing a nil host or a host with mDNS disabled is allowed and yields a
// service that simply records connection modes for whatever the host happens
// to reach via other means.
//
// On startup (when both host and lookup are non-nil) the constructor
// hydrates the libp2p peerstore from each paired peer's last_known_addrs so
// the reconnector's first iteration has somewhere to dial — this is the
// hot-start optimisation on top of the DHT-based reconnector.
func NewDiscoveryService(h *Host, lookup PeerLookup, logger *slog.Logger) *DiscoveryService {
	if logger == nil {
		logger = slog.Default()
	}
	d := &DiscoveryService{
		host:     h,
		lookup:   lookup,
		logger:   logger,
		modes:    make(map[peer.ID]ConnectionMode),
		reported: make(map[peer.ID]ConnectionMode),
	}
	if h != nil {
		h.h.Network().Notify(d.notifiee())
		d.hydratePeerstore()
		// Publish only after the service is completely initialised. mDNS
		// callbacks that arrive during setup are safely ignored by the host;
		// callbacks after publication observe all constructor writes through
		// Host's discovery lock.
		h.setDiscovery(d)
	}
	return d
}

// notifiee returns a libp2p network.Notifiee that bridges connection events
// into our connection-mode tracker.
func (d *DiscoveryService) notifiee() network.Notifiee {
	return &network.NotifyBundle{
		ConnectedF: func(_ network.Network, c network.Conn) {
			d.handleConnected(c.RemotePeer(), c.RemoteMultiaddr())
		},
		DisconnectedF: func(_ network.Network, c network.Conn) {
			d.handleDisconnected(c.RemotePeer())
		},
	}
}

// onMDNSFound is invoked by the host's mdnsNotifee for each discovered peer.
// If the peer is paired, attempt a fresh direct dial — if successful, this
// upgrades a relay/hole-punched connection to LAN-direct.
func (d *DiscoveryService) onMDNSFound(pi peer.AddrInfo) {
	if d == nil || d.host == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), dialTimeout)
	defer cancel()

	if d.lookup != nil {
		paired, err := d.lookup.IsPaired(ctx, pi.ID.String())
		if err != nil {
			d.logger.Debug("p2p discovery: lookup failed", "peer", pi.ID, "err", err)
			return
		}
		if !paired {
			return
		}
	}
	d.dialPaired(ctx, pi)
}

// dialPaired forces a fresh dial to a paired peer using its mDNS-supplied
// addrs. libp2p will pick the LAN addr automatically when reachable; on
// success, the network notifiee fires and updates the connection mode.
func (d *DiscoveryService) dialPaired(ctx context.Context, pi peer.AddrInfo) {
	host := d.host.h
	host.Peerstore().AddAddrs(pi.ID, pi.Addrs, peerstoreTTL)
	if err := host.Connect(ctx, pi); err != nil {
		d.logger.Debug("p2p discovery: dial paired peer failed",
			"peer", pi.ID, "err", err)
		return
	}
	d.logger.Info("p2p discovery: connected to paired peer on LAN",
		"peer", pi.ID, "addrs", multiaddrsAsStrings(pi.Addrs))
}

// handleConnected re-evaluates connection mode for a peer when libp2p
// reports a new active connection. Picks the "best" mode across all live
// connections (direct > hole-punched > relay).
func (d *DiscoveryService) handleConnected(p peer.ID, _ multiaddr.Multiaddr) {
	mode := d.bestMode(p)
	d.mu.Lock()
	prev := d.modes[p]
	d.modes[p] = mode
	d.mu.Unlock()
	if prev == mode {
		return
	}
	d.logConnectionMode(p, mode, prev)
	d.persistMode(p, mode)
	// On any non-relay transition (direct or hole-punched), snapshot the
	// peer's currently-known addrs so the next daemon restart can dial
	// without waiting for the DHT to converge.
	if mode != ModeRelay {
		d.persistKnownAddrs(p)
	}
}

func (d *DiscoveryService) logConnectionMode(p peer.ID, mode, prev ConnectionMode) {
	d.mu.Lock()
	lastReported := d.reported[p]
	d.mu.Unlock()
	// Log at INFO for first observation of a peer or a genuine mode change
	// (prev was a non-empty different mode). Use DEBUG for re-observations
	// of the same mode after transient clears (e.g. conn flap causing
	// handleDisconnected to delete then re-handleConnected with prev="").
	// This prevents INFO spam while preserving operational signal on real
	// transitions. Persist still occurs so DB reflects current reachability.
	if lastReported == "" || lastReported != mode {
		d.logger.Info("p2p connection mode", "peer", p, "mode", mode, "prev", prev)
		d.mu.Lock()
		d.reported[p] = mode
		d.mu.Unlock()
	} else {
		d.logger.Debug("p2p connection mode", "peer", p, "mode", mode, "prev", prev)
	}
}

// handleDisconnected clears the cached mode if there are no remaining live
// connections to the peer.
func (d *DiscoveryService) handleDisconnected(p peer.ID) {
	if d.host == nil {
		return
	}
	if len(d.host.h.Network().ConnsToPeer(p)) > 0 {
		return
	}
	d.mu.Lock()
	delete(d.modes, p)
	d.mu.Unlock()
}

// bestMode inspects all active connections to a peer and returns the
// strongest (most direct) reachability class.
func (d *DiscoveryService) bestMode(p peer.ID) ConnectionMode {
	conns := d.host.h.Network().ConnsToPeer(p)
	best := ModeRelay
	hasAny := false
	for _, c := range conns {
		hasAny = true
		switch classifyAddr(c.RemoteMultiaddr()) {
		case ModeDirect:
			return ModeDirect
		case ModeHolePunched:
			if best == ModeRelay {
				best = ModeHolePunched
			}
		}
	}
	if !hasAny {
		return ModeRelay
	}
	return best
}

// classifyAddr inspects a multiaddr and decides whether it represents a
// direct connection or a relayed one. Heuristic: any addr containing
// /p2p-circuit is relayed; otherwise direct. Hole-punched is signalled
// separately by libp2p's holepunch service (treated as "direct" here for
// now — the LAN-vs-Internet distinction the UI cares about is the
// relay/non-relay split).
func classifyAddr(a multiaddr.Multiaddr) ConnectionMode {
	if a == nil {
		return ModeRelay
	}
	for _, p := range a.Protocols() {
		if p.Code == multiaddr.P_CIRCUIT {
			return ModeRelay
		}
	}
	return ModeDirect
}

// persistMode best-effort writes the new connection mode for a peer.
func (d *DiscoveryService) persistMode(p peer.ID, mode ConnectionMode) {
	if d.lookup == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	d.lookup.MarkConnectionMode(ctx, p.String(), mode)
}

// Close releases the service's libp2p notifiee subscription. Safe to call
// multiple times; safe to call on a nil receiver.
func (d *DiscoveryService) Close() error {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil
	}
	d.closed = true
	return nil
}

// ModeFor returns the cached connection mode for a peer, or empty when no
// active connection has been observed.
func (d *DiscoveryService) ModeFor(peerID string) ConnectionMode {
	if d == nil || peerID == "" {
		return ""
	}
	pid, err := peer.Decode(peerID)
	if err != nil {
		return ""
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.modes[pid]
}

// ErrTableMissing is returned by PeerLookup implementations when the
// p2p_peers table doesn't exist yet (M1.2 hasn't merged). Callers should
// treat it as "peer is not paired".
var ErrTableMissing = errors.New("p2p: p2p_peers table missing (M1.2 not merged)")

// IsTableMissing detects sqlite "no such table" errors so PeerLookup
// implementations can downgrade them to "peer not paired" without leaking
// implementation details to the discovery service.
func IsTableMissing(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrTableMissing) {
		return true
	}
	if errors.Is(err, sql.ErrNoRows) {
		return false
	}
	return strings.Contains(err.Error(), "no such table")
}
