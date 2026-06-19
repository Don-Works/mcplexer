//go:build p2p

package p2p

import (
	"context"
	"errors"
	"log/slog"
	"regexp"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
)

// Backoff schedule constants. The reconnector stays event-driven on the happy
// path — these values cap how aggressively it retries when dials keep failing
// and bound the worst-case "offline" window when libp2p drops events on the
// floor (suspend/resume edge cases).
const (
	// maxBackoff caps the retry cadence per peer at one minute.
	maxBackoff = 60 * time.Second
	// minDialGap floors how often we touch a single peer regardless of how
	// many disconnect events arrive in quick succession.
	minDialGap = 1 * time.Second
	// safetyTickInterval is the ceiling on how long we wait between sweeps
	// when nothing else nudges us — catches peers libp2p forgot to notify
	// about (laptop suspend pre-libp2p-event, e.g.).
	safetyTickInterval = 5 * time.Minute
	// offlineCutoff is how long a peer must have been offline before the
	// reconnector starts throttling DHT sweeps for it. Below this we keep
	// the eager retry behaviour; above it we drop to offlineSweepGap.
	offlineCutoff = 10 * time.Minute
	// offlineSweepGap is the minimum gap between DHT lookups for a peer
	// that has been offline > offlineCutoff. Caps the cost of long-offline
	// peers at a single FindPeer per 30 minutes — explicit Kicks (incoming
	// pair, manual reconnect) still bypass the gate.
	offlineSweepGap = 30 * time.Minute
)

// defaultReconnectInterval is retained for callers that pass an interval to
// NewReconnector. The value drives the safety-net sweep cadence; the happy
// path is event-driven via Kick on libp2p Disconnected events.
const defaultReconnectInterval = safetyTickInterval

// Reconnect state strings — surfaced verbatim in JSON via reconnect_state.
// Stable contract: UI logic switches on these literals. Don't rename.
const (
	ReconnectStateConnected      = "connected"
	ReconnectStateSearchingDHT   = "searching_dht"
	ReconnectStateDialFailed     = "dial_failed"
	ReconnectStateNotFoundInDHT  = "not_found_in_dht"
	ReconnectStateDHTUnavailable = "dht_unavailable"
)

// ReconnectStatus is the per-peer telemetry the reconnector exposes. Strings
// (rather than time.Time/error) keep it JSON-friendly without a custom
// MarshalJSON. Empty values mean "no data yet".
type ReconnectStatus struct {
	LastAttempt time.Time `json:"last_dial_attempt_at,omitempty"`
	LastError   string    `json:"last_dial_error,omitempty"`
	State       string    `json:"reconnect_state,omitempty"`
}

// PairedPeerLister returns the libp2p peer IDs of every actively-paired
// remote peer. Implemented in production by *SQLPeerLookup.
type PairedPeerLister interface {
	ListPeerIDs(ctx context.Context) ([]string, error)
}

// hostForReconnect is the subset of *Host the reconnector touches. Carved out
// as an interface so unit tests can swap a fake host that doesn't run libp2p.
type hostForReconnect interface {
	Self() peer.ID
	IsConnected(p peer.ID) bool
	FindPeer(ctx context.Context, p peer.ID) (peer.AddrInfo, error)
	ConnectAddrInfo(ctx context.Context, info peer.AddrInfo) error
}

// LivenessOracle is the optional read the reconnector consults to decide
// whether a peer has been offline long enough to warrant DHT-search
// throttling. Implemented in production by *LivenessMonitor. nil is fine —
// callers that don't run a monitor get the original eager behaviour.
type LivenessOracle interface {
	OfflineSince(p peer.ID) (time.Time, bool)
}

// Reconnector keeps live connections to paired peers across IP changes,
// network transitions, and daemon restarts. It is event-driven: a libp2p
// Disconnected event for a paired peer triggers an immediate dial (subject
// to a 1s per-peer floor); failed dials back off per peer (2s, 5s, 15s,
// 30s, 60s capped); a 5-minute safety-net tick covers events libp2p drops.
//
// One Reconnector per Host. Construct with NewReconnector, start with Start,
// stop with Close.
type Reconnector struct {
	host   hostForReconnect
	lister PairedPeerLister
	logger *slog.Logger
	clk    clock

	// interval is the safety-net sweep cadence. Overridable for tests;
	// defaults to safetyTickInterval.
	interval time.Duration

	// kickCh is buffered; producers (network notifiee) never block. The loop
	// drains it and runs a focused dial for the kicked peer.
	kickCh chan peer.ID

	mu    sync.Mutex
	peers map[peer.ID]*peerState

	// notifiee is the registered libp2p network.Notifiee, retained so we can
	// StopNotify on Close.
	notifiee network.Notifiee
	innerNet network.Network

	stopOnce sync.Once
	stopCh   chan struct{}

	statusMu sync.Mutex
	status   map[peer.ID]ReconnectStatus

	// liveness is consulted by the offline throttle. nil disables the
	// throttle and keeps the historical eager behaviour.
	liveness LivenessOracle
	// lastOfflineSweep records the wall-clock time of the most recent
	// throttled-mode FindPeer per peer. Used together with offlineSweepGap.
	lastOfflineSweep map[peer.ID]time.Time

	// onlineObservers receive a callback when a paired peer transitions
	// from "not connected" to "connected" (via a successful dial or the
	// already-connected branch). Mesh's offline-delivery queue subscribes
	// here so it can drain queued messages immediately on reconnect.
	observerMu      sync.Mutex
	onlineObservers []func(peerID string)
	// lastObservedConnected lets us emit the online callback exactly once
	// per offline-to-online edge — repeated tryReconnect runs on a
	// peer that's been continuously connected don't re-fire the hook.
	lastObservedConnected map[peer.ID]bool
}

// AddOnlineObserver registers fn to be called whenever a paired peer
// transitions from offline → online. fn must be cheap + non-blocking (the
// observer runs on the reconnector's main loop goroutine) — push work to
// a background goroutine if you need to do anything heavy. Safe to call
// before or after Start. Passing nil is a no-op.
func (r *Reconnector) AddOnlineObserver(fn func(peerID string)) {
	if r == nil || fn == nil {
		return
	}
	r.observerMu.Lock()
	defer r.observerMu.Unlock()
	r.onlineObservers = append(r.onlineObservers, fn)
}

// notifyOnline fires every registered observer for peerID. Each observer
// runs in its own goroutine so a slow consumer doesn't stall the
// reconnector. Idempotent at the edge: callers wrap this in a state check.
func (r *Reconnector) notifyOnline(peerID string) {
	r.observerMu.Lock()
	obs := make([]func(string), len(r.onlineObservers))
	copy(obs, r.onlineObservers)
	r.observerMu.Unlock()
	for _, fn := range obs {
		go fn(peerID)
	}
}

// observePeerEdge updates the offline→online edge memory for pid and
// returns true when this call represents a fresh transition (so the
// caller should fire onlineObservers). Locks reconnector.mu briefly.
func (r *Reconnector) observePeerEdge(pid peer.ID, connectedNow bool) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.lastObservedConnected == nil {
		r.lastObservedConnected = make(map[peer.ID]bool)
	}
	prev, seen := r.lastObservedConnected[pid]
	r.lastObservedConnected[pid] = connectedNow
	if !connectedNow {
		return false
	}
	// First sight or a flip from offline → online.
	return !seen || !prev
}

// NewReconnector constructs a Reconnector. host and lister must be non-nil.
// A nil logger is replaced with slog.Default(). interval <= 0 falls back to
// defaultReconnectInterval; it controls the safety-net sweep cadence only —
// the happy path is event-driven via Kick on libp2p Disconnected events.
//
// Returns nil when host or lister is nil so cmd-side wiring can call this
// unconditionally without checking p2p enable state.
func NewReconnector(
	host *Host, lister PairedPeerLister,
	interval time.Duration, logger *slog.Logger,
) *Reconnector {
	if host == nil || lister == nil {
		return nil
	}
	if logger == nil {
		logger = slog.Default()
	}
	if interval <= 0 {
		interval = defaultReconnectInterval
	}
	return &Reconnector{
		host:                  host,
		lister:                lister,
		logger:                logger,
		clk:                   realClock{},
		interval:              interval,
		kickCh:                make(chan peer.ID, 64),
		peers:                 make(map[peer.ID]*peerState),
		stopCh:                make(chan struct{}),
		innerNet:              host.Inner().Network(),
		status:                make(map[peer.ID]ReconnectStatus),
		lastOfflineSweep:      make(map[peer.ID]time.Time),
		lastObservedConnected: make(map[peer.ID]bool),
	}
}

// SetLivenessOracle wires a LivenessOracle into the reconnector so it can
// throttle DHT searches for long-offline peers. Safe to call before Start.
// Passing nil disables the throttle (default behaviour).
func (r *Reconnector) SetLivenessOracle(o LivenessOracle) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.liveness = o
}

// Start kicks off the loop in a goroutine and registers a libp2p network
// notifiee that pushes Connected + Disconnected events for paired peers into
// the kick channel. Connected events keep the reconnect_state in sync after a
// natural reconnect (e.g. libp2p auto-dial) — without them, a peer that flips
// "searching → connected" leaves reconnect_state stuck at "searching" until
// the next 5-minute safety sweep. Returns immediately. Stop with Close.
func (r *Reconnector) Start(ctx context.Context) {
	if r == nil {
		return
	}
	r.notifiee = &network.NotifyBundle{
		ConnectedF: func(_ network.Network, c network.Conn) {
			r.onConnect(c.RemotePeer())
		},
		DisconnectedF: func(_ network.Network, c network.Conn) {
			r.onDisconnect(c.RemotePeer())
		},
	}
	if r.innerNet != nil {
		r.innerNet.Notify(r.notifiee)
	}
	go r.loop(ctx)
}

// Close stops the reconnector. Idempotent; safe on a nil receiver.
func (r *Reconnector) Close() {
	if r == nil {
		return
	}
	r.stopOnce.Do(func() {
		if r.innerNet != nil && r.notifiee != nil {
			r.innerNet.StopNotify(r.notifiee)
		}
		close(r.stopCh)
	})
}

// Kick schedules an immediate reconnect attempt for peerID. Safe to call
// from any goroutine. Non-blocking: drops the kick if the channel is full —
// the safety-net tick will catch it on the next sweep.
func (r *Reconnector) Kick(peerID peer.ID) {
	if r == nil {
		return
	}
	select {
	case r.kickCh <- peerID:
	default:
	}
}

// onDisconnect is the network.Notifiee callback. We don't gate on "is paired"
// here — a synchronous DB lookup from a libp2p callback is a footgun; the
// loop filters non-paired peers when it processes the kick.
func (r *Reconnector) onDisconnect(p peer.ID) {
	r.Kick(p)
}

// onConnect is the network.Notifiee callback fired when libp2p reports a new
// active connection. We route through the same kick channel as disconnects —
// the loop's handleKick → tryReconnect → IsConnected=true branch then refreshes
// reconnect_state to "connected" (which is what the UI badge reads).
func (r *Reconnector) onConnect(p peer.ID) {
	r.Kick(p)
}

// MarkConnected stamps reconnect_state="connected" for peerID and fires the
// online-edge callback if this is a fresh offline→online transition. Safe to
// call from any goroutine; idempotent. Used by the liveness monitor to keep
// reconnect_state fresh even when libp2p Connected events are missed
// (suspend/resume, dropped notifiee firings, etc.) — pings only succeed when
// the connection is actually live, so this is a reliable secondary signal.
func (r *Reconnector) MarkConnected(peerID peer.ID) {
	if r == nil || peerID == "" {
		return
	}
	r.recordStatus(peerID, ReconnectStateConnected, "")
	r.clearOfflineSweep(peerID)
	if r.observePeerEdge(peerID, true) {
		r.notifyOnline(peerID.String())
	}
}

func (r *Reconnector) loop(ctx context.Context) {
	if r.clk == nil {
		r.clk = realClock{}
	}
	// Run once immediately so a fresh restart doesn't have to wait for an
	// event before trying paired peers.
	r.runSweep(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.stopCh:
			return
		case pid := <-r.kickCh:
			r.handleKick(ctx, pid)
		case <-r.clk.After(r.interval):
			r.runSweep(ctx)
		}
	}
}

// runOnce is retained for backwards-compat with existing tests; the live
// loop calls runSweep.
func (r *Reconnector) runOnce(ctx context.Context) { r.runSweep(ctx) }

// runSweep performs a full safety-net sweep over every paired peer. Cheap
// because per-peer backoff state filters out peers we just dialed.
func (r *Reconnector) runSweep(ctx context.Context) {
	if r == nil || r.host == nil || r.lister == nil {
		return
	}
	ids, err := r.lister.ListPeerIDs(ctx)
	if err != nil {
		r.logger.Debug("p2p reconnector: list peers failed", "err", err)
		return
	}
	for _, idStr := range ids {
		select {
		case <-ctx.Done():
			return
		case <-r.stopCh:
			return
		default:
		}
		r.tryReconnect(ctx, idStr, false)
	}
}

// handleKick processes a single kick. We confirm the peer is paired by
// listing — cheap (a single SQL query) and authoritative.
func (r *Reconnector) handleKick(ctx context.Context, pid peer.ID) {
	if r.host == nil || r.lister == nil {
		return
	}
	ids, err := r.lister.ListPeerIDs(ctx)
	if err != nil {
		r.logger.Debug("p2p reconnector: list peers failed on kick", "err", err)
		return
	}
	target := pid.String()
	for _, idStr := range ids {
		if idStr == target {
			r.tryReconnect(ctx, idStr, true)
			return
		}
	}
	// Not paired — ignore.
}

// tryReconnect handles a single paired peer. Skips already-connected peers,
// looks up addresses via DHT, and dials. Subject to per-peer backoff and a
// 1s minimum dial gap. All errors are debug-logged — the reconnector is
// best-effort and never propagates.
func (r *Reconnector) tryReconnect(ctx context.Context, idStr string, kicked bool) {
	pid, err := peer.Decode(idStr)
	if err != nil {
		r.logger.Debug("p2p reconnector: bad peer id", "id", idStr, "err", err)
		return
	}
	if pid == r.host.Self() {
		return
	}
	if r.host.IsConnected(pid) {
		r.recordSuccess(pid)
		r.recordStatus(pid, ReconnectStateConnected, "")
		r.clearOfflineSweep(pid)
		if r.observePeerEdge(pid, true) {
			r.notifyOnline(pid.String())
		}
		return
	}
	// Not connected yet — record the edge so a subsequent successful
	// dial fires the online-transition callback.
	r.observePeerEdge(pid, false)
	if !r.shouldDial(pid, kicked) {
		return
	}
	if !kicked && r.shouldSkipOffline(pid) {
		return
	}
	r.attemptDial(ctx, pid)
}

// shouldSkipOffline returns true when the reconnector has a LivenessOracle
// reporting this peer has been offline > offlineCutoff AND the last
// throttled sweep was within offlineSweepGap. Idea: long-offline peers cost
// at most one DHT FindPeer per offlineSweepGap; explicit Kicks (ignored
// here — caller branches on `kicked` first) always bypass the gate.
func (r *Reconnector) shouldSkipOffline(pid peer.ID) bool {
	if r.liveness == nil {
		return false
	}
	since, ok := r.liveness.OfflineSince(pid)
	if !ok {
		return false
	}
	now := r.clockNow()
	if now.Sub(since) < offlineCutoff {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.lastOfflineSweep == nil {
		r.lastOfflineSweep = make(map[peer.ID]time.Time)
	}
	if last, seen := r.lastOfflineSweep[pid]; seen && now.Sub(last) < offlineSweepGap {
		return true
	}
	r.lastOfflineSweep[pid] = now
	return false
}

// clearOfflineSweep is called when we observe a peer is connected — drops
// any throttle state so a subsequent disconnect starts fresh.
func (r *Reconnector) clearOfflineSweep(pid peer.ID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.lastOfflineSweep != nil {
		delete(r.lastOfflineSweep, pid)
	}
}

// attemptDial does the FindPeer + Connect work, updating both the
// per-peer backoff state and the user-visible reconnect telemetry.
func (r *Reconnector) attemptDial(ctx context.Context, pid peer.ID) {
	now := time.Now().UTC()
	info, err := r.host.FindPeer(ctx, pid)
	if err != nil {
		r.recordFindError(pid, now, err)
		// DHT-unavailable shouldn't grow backoff (transient infra issue).
		if !errors.Is(err, ErrDHTUnavailable) {
			r.recordFailure(pid)
		}
		return
	}
	if err := r.host.ConnectAddrInfo(ctx, info); err != nil {
		r.recordAttempt(pid, now, ReconnectStateDialFailed, redactDialError(err))
		r.logger.Debug("p2p reconnector: dial failed", "peer", pid, "err", err)
		r.recordFailure(pid)
		return
	}
	r.recordSuccess(pid)
	r.recordAttempt(pid, now, ReconnectStateConnected, "")
	if r.observePeerEdge(pid, true) {
		r.notifyOnline(pid.String())
	}
	r.logger.Info("p2p reconnector: reconnected paired peer",
		"peer", pid, "addrs", multiaddrsAsStrings(info.Addrs))
}

// recordFindError maps a FindPeer error to a reconnect_state and persists it.
// Split out so tryReconnect stays under the 50-LoC budget.
func (r *Reconnector) recordFindError(pid peer.ID, now time.Time, err error) {
	switch {
	case errors.Is(err, ErrDHTUnavailable):
		r.recordAttempt(pid, now, ReconnectStateDHTUnavailable, "")
	case errors.Is(err, ErrPeerNotFoundInDHT):
		r.recordAttempt(pid, now, ReconnectStateNotFoundInDHT, "")
		r.logger.Debug("p2p reconnector: peer not in dht", "peer", pid)
	default:
		r.recordAttempt(pid, now, ReconnectStateSearchingDHT, redactDialError(err))
		r.logger.Debug("p2p reconnector: find peer failed", "peer", pid, "err", err)
	}
}

// recordAttempt updates LastAttempt + State + LastError atomically.
func (r *Reconnector) recordAttempt(pid peer.ID, t time.Time, state, errStr string) {
	r.statusMu.Lock()
	defer r.statusMu.Unlock()
	if r.status == nil {
		r.status = make(map[peer.ID]ReconnectStatus)
	}
	r.status[pid] = ReconnectStatus{LastAttempt: t, State: state, LastError: errStr}
}

// recordStatus updates only State + LastError — used for the "already
// connected, no dial happened" branch where there is no fresh attempt to
// timestamp. Preserves any prior LastAttempt.
func (r *Reconnector) recordStatus(pid peer.ID, state, errStr string) {
	r.statusMu.Lock()
	defer r.statusMu.Unlock()
	if r.status == nil {
		r.status = make(map[peer.ID]ReconnectStatus)
	}
	cur := r.status[pid]
	cur.State = state
	cur.LastError = errStr
	r.status[pid] = cur
}

// PeerStatus returns the latest reconnect status for a peer. Zero-value
// (empty State) when the reconnector has not yet seen this peer.
func (r *Reconnector) PeerStatus(pid peer.ID) ReconnectStatus {
	if r == nil {
		return ReconnectStatus{}
	}
	r.statusMu.Lock()
	defer r.statusMu.Unlock()
	return r.status[pid]
}

// PeerStatusByID is the string-keyed variant — convenience for callers that
// have a peer ID string and don't want to round-trip through peer.Decode.
// Returns the zero value on a malformed peer ID.
func (r *Reconnector) PeerStatusByID(idStr string) ReconnectStatus {
	if r == nil {
		return ReconnectStatus{}
	}
	pid, err := peer.Decode(idStr)
	if err != nil {
		return ReconnectStatus{}
	}
	return r.PeerStatus(pid)
}

// AllPeerStatus snapshots the entire status map keyed by peer ID string for
// JSON-friendly consumption. Always non-nil.
func (r *Reconnector) AllPeerStatus() map[string]ReconnectStatus {
	out := make(map[string]ReconnectStatus)
	if r == nil {
		return out
	}
	r.statusMu.Lock()
	defer r.statusMu.Unlock()
	for pid, st := range r.status {
		out[pid.String()] = st
	}
	return out
}

// dialErrorRedactor strips IP literals + ports from a libp2p dial error so
// the JSON we surface to the UI doesn't leak network topology into a
// long-lived audit trail. Patterns covered:
//   - ip4/<addr>/tcp/<port>      (multiaddr form in libp2p errors)
//   - <addr>:<port>               (Go net.Dial form)
var dialErrorRedactor = regexp.MustCompile(
	`(?:/ip[46]/[0-9a-fA-F:.]+(?:/(?:tcp|udp)/\d+)?)|` +
		`(?:\b(?:\d{1,3}\.){3}\d{1,3}(?::\d+)?\b)|` +
		`(?:\[[0-9a-fA-F:]+\](?::\d+)?)`,
)

// redactDialError returns the error string with IPs/ports replaced by "[…]".
// Empty errors stay empty; non-nil errors that produce an empty string
// fall back to a generic placeholder so the UI sees *some* signal.
func redactDialError(err error) string {
	if err == nil {
		return ""
	}
	out := dialErrorRedactor.ReplaceAllString(err.Error(), "[…]")
	if out == "" {
		return "dial error"
	}
	return out
}
