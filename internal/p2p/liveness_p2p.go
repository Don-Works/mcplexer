//go:build p2p

package p2p

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/protocol/ping"
)

// Liveness tuning. The values keep the monitor cheap on the wire (one tiny
// ping every 30s per connected paired peer, zero traffic to offline peers)
// while flipping a stuck connection to offline within ~3 minutes worst case.
const (
	// livenessPingInterval is the cadence at which we ping every connected
	// paired peer. 30s comfortably stays under typical NAT/firewall idle
	// timeouts (often 60–120s) so the connection itself stays warm.
	livenessPingInterval = 30 * time.Second
	// livenessPingTimeout caps a single ping attempt. libp2p's ping service
	// usually returns RTT in <100ms on a healthy stream — 5s is generous.
	livenessPingTimeout = 5 * time.Second
	// livenessFailureThreshold is the number of consecutive failed pings
	// before we close the connection. 3 × 30s ≈ 90s of silence — enough to
	// ride out a transient blip without leaving a half-dead stream around.
	livenessFailureThreshold = 3
)

// PeerLivenessToucher is the narrow write the monitor needs to refresh the
// p2p_peers.last_seen column on every successful ping. Implemented in
// production by store.P2PPeerStore. Optional — nil is fine for tests.
type PeerLivenessToucher interface {
	UpdateLastSeen(ctx context.Context, peerID string, t time.Time) error
}

// pingClient is the subset of libp2p's *ping.PingService the monitor needs.
// Carved out as an interface so tests can provide a deterministic ping fake
// without spinning up a full libp2p stack.
type pingClient interface {
	Ping(ctx context.Context, p peer.ID) <-chan pingResult
}

// pingResult mirrors ping.Result so we don't leak the libp2p type into our
// abstraction.
type pingResult struct {
	RTT   time.Duration
	Error error
}

// hostForLiveness is the subset of *Host the monitor touches. Carved out as
// an interface so unit tests can swap a fake without spinning up libp2p.
type hostForLiveness interface {
	Self() peer.ID
	IsConnected(p peer.ID) bool
	ClosePeer(p peer.ID) error
}

// ReconnectMarker is the narrow write the monitor uses to refresh the
// Reconnector's reconnect_state on every successful ping. Without this hook,
// a peer that flipped from "searching → connected" via libp2p auto-dial leaves
// reconnect_state stuck at "searching" until the next 5-minute safety sweep —
// which contradicts the freshly-updated last_seen and confuses the UI badge.
// Optional — nil keeps the historical behaviour.
type ReconnectMarker interface {
	MarkConnected(p peer.ID)
}

// LivenessStatus is the per-peer view the rest of the daemon reads.
type LivenessStatus struct {
	Online              bool          `json:"online"`
	OnlineSince         time.Time     `json:"online_since,omitempty"`
	OfflineSince        time.Time     `json:"offline_since,omitempty"`
	LastPingAt          time.Time     `json:"last_ping_at,omitempty"`
	LastPingRTT         time.Duration `json:"last_ping_rtt_ns,omitempty"`
	ConsecutiveFailures int           `json:"consecutive_failures,omitempty"`
}

// LivenessMonitor pings every connected paired peer on a fixed cadence,
// closing connections that miss the failure threshold and refreshing the
// per-peer last_seen timestamp on every success. It runs alongside the
// Reconnector — the monitor produces "offline" signals; the reconnector
// reacts to them. Liveness traffic uses libp2p's built-in /ipfs/ping/1.0.0
// protocol and never touches the mesh transport, so it does not appear in
// mesh_messages or the UI agent-mesh log.
type LivenessMonitor struct {
	host    hostForLiveness
	lister  PairedPeerLister
	pinger  pingClient
	toucher PeerLivenessToucher
	marker  ReconnectMarker
	logger  *slog.Logger
	clk     clock

	interval         time.Duration
	pingTimeout      time.Duration
	failureThreshold int

	mu    sync.Mutex
	state map[peer.ID]*liveState

	stopOnce sync.Once
	stopCh   chan struct{}
}

type liveState struct {
	online              bool
	onlineSince         time.Time
	offlineSince        time.Time
	lastPingAt          time.Time
	lastPingRTT         time.Duration
	consecutiveFailures int
}

// NewLivenessMonitor constructs a monitor. host and pinger must be non-nil;
// returns nil otherwise so cmd-side wiring can call this unconditionally.
// lister is required (the monitor only acts on paired peers). toucher and
// logger are optional.
func NewLivenessMonitor(
	host *Host, lister PairedPeerLister,
	toucher PeerLivenessToucher, logger *slog.Logger,
) *LivenessMonitor {
	if host == nil || lister == nil {
		return nil
	}
	if logger == nil {
		logger = slog.Default()
	}
	pinger := host.Pinger()
	if pinger == nil {
		return nil
	}
	return &LivenessMonitor{
		host:             livenessHostAdapter{h: host},
		lister:           lister,
		pinger:           libp2pPingAdapter{svc: pinger},
		toucher:          toucher,
		logger:           logger,
		clk:              realClock{},
		interval:         livenessPingInterval,
		pingTimeout:      livenessPingTimeout,
		failureThreshold: livenessFailureThreshold,
		state:            make(map[peer.ID]*liveState),
		stopCh:           make(chan struct{}),
	}
}

// livenessHostAdapter bridges the production *Host to hostForLiveness.
type livenessHostAdapter struct{ h *Host }

func (a livenessHostAdapter) Self() peer.ID              { return a.h.Self() }
func (a livenessHostAdapter) IsConnected(p peer.ID) bool { return a.h.IsConnected(p) }
func (a livenessHostAdapter) ClosePeer(p peer.ID) error  { return a.h.Inner().Network().ClosePeer(p) }

// libp2pPingAdapter bridges *ping.PingService to pingClient. The libp2p
// service's Ping channel produces ping.Result values; we translate to our
// internal pingResult so the rest of the monitor doesn't import libp2p.
type libp2pPingAdapter struct{ svc *ping.PingService }

func (a libp2pPingAdapter) Ping(ctx context.Context, p peer.ID) <-chan pingResult {
	src := a.svc.Ping(ctx, p)
	out := make(chan pingResult, 1)
	go func() {
		defer close(out)
		select {
		case r, ok := <-src:
			if !ok {
				out <- pingResult{Error: context.Canceled}
				return
			}
			out <- pingResult{RTT: r.RTT, Error: r.Error}
		case <-ctx.Done():
			out <- pingResult{Error: ctx.Err()}
		}
	}()
	return out
}

// SetReconnectMarker wires the LivenessMonitor to push successful pings as
// "connected" signals into the given marker (typically *Reconnector). Safe to
// call before Start. Passing nil disables the hook (default behaviour).
func (m *LivenessMonitor) SetReconnectMarker(rm ReconnectMarker) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.marker = rm
}

// Start runs the ping loop in a goroutine. Returns immediately.
func (m *LivenessMonitor) Start(ctx context.Context) {
	if m == nil {
		return
	}
	go m.loop(ctx)
}

// Close stops the monitor. Idempotent; safe on a nil receiver.
func (m *LivenessMonitor) Close() {
	if m == nil {
		return
	}
	m.stopOnce.Do(func() { close(m.stopCh) })
}

// OfflineSince reports how long peer p has been offline. Returns the zero
// time + false when the peer is currently online OR the monitor has not yet
// observed it (in which case the caller should fall back to its own logic).
func (m *LivenessMonitor) OfflineSince(p peer.ID) (time.Time, bool) {
	if m == nil {
		return time.Time{}, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	st, ok := m.state[p]
	if !ok || st.online {
		return time.Time{}, false
	}
	return st.offlineSince, true
}

// IsOnline reports whether the most recent liveness observation considered
// peer p online. peerID is the string form of the libp2p peer ID; a
// malformed input returns false. Unknown peers (never observed) return
// false — callers that prefer the "optimistic, try anyway" stance should
// keep their own fallback.
func (m *LivenessMonitor) IsOnline(peerID string) bool {
	if m == nil || peerID == "" {
		return false
	}
	pid, err := peer.Decode(peerID)
	if err != nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	st, ok := m.state[pid]
	if !ok {
		return false
	}
	return st.online
}

// PeerLiveness returns the latest liveness snapshot for p (zero-value when
// unknown).
func (m *LivenessMonitor) PeerLiveness(p peer.ID) LivenessStatus {
	if m == nil {
		return LivenessStatus{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	st, ok := m.state[p]
	if !ok {
		return LivenessStatus{}
	}
	return snapshotState(st)
}

// AllLiveness snapshots every tracked peer keyed by peer-ID string.
func (m *LivenessMonitor) AllLiveness() map[string]LivenessStatus {
	out := make(map[string]LivenessStatus)
	if m == nil {
		return out
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for pid, st := range m.state {
		out[pid.String()] = snapshotState(st)
	}
	return out
}

func snapshotState(st *liveState) LivenessStatus {
	return LivenessStatus{
		Online:              st.online,
		OnlineSince:         st.onlineSince,
		OfflineSince:        st.offlineSince,
		LastPingAt:          st.lastPingAt,
		LastPingRTT:         st.lastPingRTT,
		ConsecutiveFailures: st.consecutiveFailures,
	}
}

func (m *LivenessMonitor) loop(ctx context.Context) {
	// Initial sweep so a freshly-restarted daemon stamps liveness for every
	// peer that came back already-connected (e.g. via auto-dial on boot).
	m.tick(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-m.stopCh:
			return
		case <-m.clk.After(m.interval):
			m.tick(ctx)
		}
	}
}

// tick walks every paired peer and pings the ones that libp2p reports as
// connected. Offline peers are recorded in state but generate zero traffic
// — that's the whole point.
func (m *LivenessMonitor) tick(ctx context.Context) {
	ids, err := m.lister.ListPeerIDs(ctx)
	if err != nil {
		m.logger.Debug("liveness: list peers failed", "err", err)
		return
	}
	self := m.host.Self()
	for _, idStr := range ids {
		select {
		case <-ctx.Done():
			return
		case <-m.stopCh:
			return
		default:
		}
		pid, err := peer.Decode(idStr)
		if err != nil || pid == self {
			continue
		}
		if !m.host.IsConnected(pid) {
			m.markOffline(pid)
			continue
		}
		m.pingPeer(ctx, pid)
	}
}

// pingPeer issues a single ping with timeout and updates state from the
// result. Successful pings refresh last_seen in the SQL store so the
// dashboard's "peers online" tile reflects real liveness without any new
// query path. Failed pings increment the per-peer failure counter; once it
// crosses the threshold we close every connection to the peer, which
// triggers the reconnector's existing Disconnected handler.
func (m *LivenessMonitor) pingPeer(ctx context.Context, pid peer.ID) {
	pctx, cancel := context.WithTimeout(ctx, m.pingTimeout)
	defer cancel()
	res, ok := <-m.pinger.Ping(pctx, pid)
	now := m.clk.Now()
	if !ok || res.Error != nil {
		m.recordFailure(pid, now)
		return
	}
	m.recordSuccess(pid, now, res.RTT)
	if m.toucher != nil {
		// Touch outside the monitor's mutex; UpdateLastSeen is best-effort
		// and a wedged DB shouldn't stall the next tick.
		go m.touch(pid, now)
	}
	// Snapshot the marker under the lock — SetReconnectMarker may be called
	// concurrently with Start. The marker call itself is cheap (in-memory
	// map write) so we run it inline rather than spawning a goroutine.
	m.mu.Lock()
	rm := m.marker
	m.mu.Unlock()
	if rm != nil {
		rm.MarkConnected(pid)
	}
}

func (m *LivenessMonitor) touch(pid peer.ID, now time.Time) {
	tctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := m.toucher.UpdateLastSeen(tctx, pid.String(), now); err != nil {
		m.logger.Debug("liveness: update last_seen", "peer", pid, "err", err)
	}
}

func (m *LivenessMonitor) recordSuccess(pid peer.ID, now time.Time, rtt time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.peerStateLocked(pid)
	if !st.online {
		st.onlineSince = now
		st.offlineSince = time.Time{}
	}
	st.online = true
	st.lastPingAt = now
	st.lastPingRTT = rtt
	st.consecutiveFailures = 0
}

func (m *LivenessMonitor) recordFailure(pid peer.ID, now time.Time) {
	m.mu.Lock()
	st := m.peerStateLocked(pid)
	st.lastPingAt = now
	st.consecutiveFailures++
	failed := st.consecutiveFailures
	wasOnline := st.online
	m.mu.Unlock()
	if failed < m.failureThreshold {
		return
	}
	if wasOnline {
		m.logger.Info("liveness: peer unresponsive — closing connection",
			"peer", pid, "consecutive_failures", failed)
	}
	m.markOffline(pid)
	_ = m.host.ClosePeer(pid)
}

func (m *LivenessMonitor) markOffline(pid peer.ID) {
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.peerStateLocked(pid)
	if st.online {
		st.offlineSince = m.clk.Now()
	} else if st.offlineSince.IsZero() {
		st.offlineSince = m.clk.Now()
	}
	st.online = false
}

func (m *LivenessMonitor) peerStateLocked(pid peer.ID) *liveState {
	if m.state == nil {
		m.state = make(map[peer.ID]*liveState)
	}
	st := m.state[pid]
	if st == nil {
		st = &liveState{}
		m.state[pid] = st
	}
	return st
}
