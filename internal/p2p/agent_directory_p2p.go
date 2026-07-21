//go:build p2p

package p2p

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
)

// AgentDirectoryProtocol is the libp2p protocol ID for cross-peer agent
// directory gossip. Wire format: NDJSON (newline-delimited JSON), one
// frame per line, on a single bidirectional long-lived stream. Matches
// the precedent set by /mcplexer/skill/1.0.0 and /mcplexer/mesh/1.0.0.
const AgentDirectoryProtocol protocol.ID = "/mcplexer/agents/1.0.0"

// AgentDirectoryProtocolVersion is sent in the hello frame so peers can
// negotiate forward-incompatible changes. Bump on wire-incompatible work.
const AgentDirectoryProtocolVersion = 1

// MaxAgentsPerSnapshot caps how many agents a snapshot frame may carry.
// If the local directory exceeds this, the sender keeps the most-recently-
// seen entries (sorted desc by LastSeenAt) and logs a warning. Receivers
// reject snapshots beyond this cap as malformed.
const MaxAgentsPerSnapshot = 256

// MaxAgentFrameBytes caps a single NDJSON line. 64 KiB comfortably fits a
// 256-agent snapshot; beyond this the receiver drops the stream.
const MaxAgentFrameBytes = 64 * 1024

// AgentBytesPerWindow + AgentRateWindow form a sliding-window cap: a peer
// may not push more than this many bytes per window before the receiver
// drops the stream. Defends against runaway/buggy senders.
const (
	AgentBytesPerWindow = 1 << 20 // 1 MiB
	AgentRateWindow     = 60 * time.Second
)

// agentStreamReadDeadline caps how long a single read on the directory
// stream can block. Re-armed per-frame so an idle stream isn't killed
// between low-traffic periods.
const agentStreamReadDeadline = 90 * time.Second

// agentStreamWriteDeadline caps a single frame write.
const agentStreamWriteDeadline = 30 * time.Second

// agentDeltaDebounce coalesces a burst of local agent changes into one
// frame. Per spec — keeps the wire quiet during startup churn.
const agentDeltaDebounce = 250 * time.Millisecond

// Frame "type" discriminators. Stable wire constants — receivers route
// off these strings and MUST treat unknowns as forward-compat (ignore).
const (
	agentFrameHello    = "hello"
	agentFrameSnapshot = "snapshot"
	agentFrameDelta    = "delta"
	agentFrameBye      = "bye"
)

// AgentRecord is the cross-peer projection of a local agent. The local
// mesh_agents table carries extra fields (model_hint, cursor, created_at,
// origin) — only what peers need to render + route lives on the wire.
//
// SessionID is unique within the sender's mcplexer. Receivers MUST
// namespace it as "peer:<sender>:<session_id>" before storing locally so
// it cannot collide with a local session.
type AgentRecord struct {
	SessionID  string    `json:"session_id"`
	Name       string    `json:"name"`
	Role       string    `json:"role,omitempty"`
	ClientType string    `json:"client_type,omitempty"`
	LastSeenAt time.Time `json:"last_seen_at"`
	// Free-form persistent status the agent set via mesh__set_agent_status.
	// Optional on the wire so older receivers ignore it cleanly.
	Status string `json:"status,omitempty"`
	// WorkspaceID lets receivers render which workspace a peer-origin
	// agent is bound to without joining against their workspaces table
	// (which they wouldn't have anyway).
	WorkspaceID string `json:"workspace_id,omitempty"`
	// Tmux locator for the dashboard's "Focus" button. The receiver
	// combines these with p2p_peers.ssh_target (set on the receiver's
	// side per-peer) to spawn ssh -t <target> tmux attach ...
	TmuxSession string `json:"tmux_session,omitempty"`
	TmuxWindow  string `json:"tmux_window,omitempty"`
	TmuxPane    string `json:"tmux_pane,omitempty"`
}

// agentHelloFrame is sent immediately on stream open by both sides. Its
// peer_id is checked against stream.Conn().RemotePeer() — a mismatch
// closes the stream as anti-spoof. proto_version negotiation lives here
// so future versions can branch without a new protocol ID.
type agentHelloFrame struct {
	Type         string    `json:"type"` // always agentFrameHello
	PeerID       string    `json:"peer_id"`
	ProtoVersion int       `json:"proto_version"`
	TS           time.Time `json:"ts"`
}

// agentSnapshotFrame ships the sender's full local agent set. Receivers
// REPLACE — delete every row with origin = "peer:<sender>" then upsert
// the snapshot. Sent once after Hello on each side, and may be resent
// later as a resync trigger.
type agentSnapshotFrame struct {
	Type   string        `json:"type"` // always agentFrameSnapshot
	Agents []AgentRecord `json:"agents"`
	TS     time.Time     `json:"ts"`
}

// agentDeltaFrame is pushed by the sender when local state changes.
// "added" carries upsert candidates; "removed" carries the original
// (un-namespaced) session_ids the receiver should delete. Coalesced via
// the agentDeltaDebounce window.
type agentDeltaFrame struct {
	Type    string        `json:"type"` // always agentFrameDelta
	Added   []AgentRecord `json:"added,omitempty"`
	Removed []string      `json:"removed,omitempty"`
	TS      time.Time     `json:"ts"`
}

// agentByeFrame is a graceful shutdown signal. Receiver drops every row
// for origin = "peer:<sender>". Either side may send it; sending closes
// the stream after the write completes.
type agentByeFrame struct {
	Type string    `json:"type"` // always agentFrameBye
	TS   time.Time `json:"ts"`
}

// LocalAgentSource is the read-side hook the sender uses to assemble
// outbound snapshot/delta frames. The implementation filters to the
// workspaces the target peer is paired with (workspace-scoped pairing).
type LocalAgentSource interface {
	ListLocalAgents(ctx context.Context, peerID string) ([]AgentRecord, error)
}

// PeerWorkspaceLookup resolves the local workspace IDs a peer is paired
// with. The service uses it to scope outbound deltas per stream so a
// delta only carries agents in workspaces that peer is bound to. A nil
// lookup fails closed: deltas are withheld entirely.
type PeerWorkspaceLookup interface {
	ListLocalWorkspaceIDsForPeer(ctx context.Context, peerID string) ([]string, error)
}

// RemoteAgentSink consumes inbound frames. Implementations are expected
// to apply the namespacing rule ("peer:<fromPeerID>:<original_session_id>")
// and the origin tag ("peer:<fromPeerID>") before persisting — this
// package does NOT do those transforms; it just forwards the wire.
//
// All methods are best-effort from the protocol's POV: a returned error
// is logged + audited but does NOT close the stream.
type RemoteAgentSink interface {
	ApplyRemoteSnapshot(ctx context.Context, fromPeerID string, agents []AgentRecord) error
	ApplyRemoteDelta(ctx context.Context, fromPeerID string, added []AgentRecord, removed []string) error
	HandleRemoteBye(ctx context.Context, fromPeerID string) error
}

// AgentDirectoryAuditor receives a record per protocol-level event so
// the audit trail captures every paired-peer agent exchange. nil-safe.
type AgentDirectoryAuditor interface {
	RecordAgentDirectory(ctx context.Context, action, peerID, status, errMsg string)
}

// PeerPairChecker is the narrow ACL surface the protocol enforces:
// pairing-only, matching the mesh-envelope ACL. SQLPeerLookup satisfies
// this via IsPaired.
type PeerPairChecker interface {
	IsPaired(ctx context.Context, peerID string) (bool, error)
}

// Wire-level errors surfaced to callers and tests.
var (
	// ErrAgentDirNotPaired is returned by ConnectToPeer + the inbound
	// handler when the remote is not in p2p_peers (or the lookup is
	// unwired in tests). The wire never differentiates from "denied" so
	// a peer cannot probe pairing state.
	ErrAgentDirNotPaired = errors.New("p2p agents: peer not paired")

	// ErrAgentDirVersionMismatch is returned when the remote's hello
	// proto_version is unknown to this build.
	ErrAgentDirVersionMismatch = errors.New("p2p agents: protocol version mismatch")

	// ErrAgentDirFrameTooLarge wraps an oversize frame on the wire
	// (per MaxAgentFrameBytes). Defends against flood / OOM.
	ErrAgentDirFrameTooLarge = errors.New("p2p agents: frame exceeds size cap")

	// ErrAgentDirSnapshotTooLarge is returned when a peer ships a
	// snapshot beyond MaxAgentsPerSnapshot.
	ErrAgentDirSnapshotTooLarge = errors.New("p2p agents: snapshot exceeds agent cap")

	// ErrAgentDirRateLimited is returned when a peer's sustained byte
	// rate exceeds AgentBytesPerWindow per AgentRateWindow.
	ErrAgentDirRateLimited = errors.New("p2p agents: peer rate-limited")

	// ErrAgentDirSpoofedHello is returned when the hello frame's
	// claimed peer_id doesn't match the libp2p stream's RemotePeer().
	ErrAgentDirSpoofedHello = errors.New("p2p agents: hello peer_id mismatch")

	// ErrAgentDirStopped is returned by ConnectToPeer / BroadcastDelta
	// after Stop has been called.
	ErrAgentDirStopped = errors.New("p2p agents: service stopped")
)

// AgentDirectoryService glues the libp2p protocol handler to the local
// directory + remote sink. One per Host. Lifecycle:
//
//   - NewAgentDirectoryService registers the inbound stream handler
//   - ConnectToPeer dials a paired peer (only if our peer-id < theirs;
//     higher peer-id sides silently accept the inbound)
//   - BroadcastDelta is called by the mesh layer after a local agent
//     change; the service debounces + fans out to all open peer streams
//   - Stop closes every open stream and cancels the debounce loop
type AgentDirectoryService struct {
	host     *Host
	lookup   PeerPairChecker
	source   LocalAgentSource
	sink     RemoteAgentSink
	wsLookup PeerWorkspaceLookup
	auditor  AgentDirectoryAuditor
	logger   *slog.Logger
	selfID   string

	mu      sync.Mutex
	streams map[string]*agentStream // peerID -> open stream
	stopped bool

	// Debounce state for outbound delta coalescing.
	pendMu      sync.Mutex
	pendingAdd  map[string]AgentRecord // session_id -> latest record
	pendingDel  map[string]struct{}    // session_id set
	flushTimer  *time.Timer
	flushCancel context.CancelFunc
}

// agentStream wraps an open libp2p stream with its peer ID + write mutex.
type agentStream struct {
	stream    network.Stream
	peerID    string
	writeMu   sync.Mutex
	closeOnce sync.Once
	rate      *byteWindow // sliding-window byte counter for inbound rate-limit
	// reader is created ONCE per stream. libp2p muxed streams are byte
	// streams that do not preserve write boundaries, so a single fill can
	// pull the hello plus the following snapshot into the buffer. A fresh
	// bufio.Reader per frame would discard everything past the first '\n',
	// silently dropping snapshots and desyncing on mid-frame splits — so the
	// buffered remainder MUST persist across readFrame calls.
	reader *bufio.Reader
}

// byteWindow is a fixed-period byte-count sliding window. Cheap to
// compute, defensive against runaway senders. Not allocation-free under
// extreme load — that's fine; the cap fires long before the bytes do.
type byteWindow struct {
	mu       sync.Mutex
	window   time.Duration
	cap      int64
	counts   []byteSample
	totalNow int64
}

type byteSample struct {
	at time.Time
	n  int64
}

// NewAgentDirectoryService wires the protocol handler onto host and
// returns a service ready for ConnectToPeer / BroadcastDelta.
//
// host, source, sink must be non-nil. lookup may be nil for tests
// (permissive); a nil wsLookup fails closed — outbound deltas are
// withheld. auditor + logger may be nil.
func NewAgentDirectoryService(
	host *Host,
	lookup PeerPairChecker,
	source LocalAgentSource,
	sink RemoteAgentSink,
	wsLookup PeerWorkspaceLookup,
	auditor AgentDirectoryAuditor,
	logger *slog.Logger,
) *AgentDirectoryService {
	if logger == nil {
		logger = slog.Default()
	}
	s := &AgentDirectoryService{
		host:       host,
		lookup:     lookup,
		source:     source,
		sink:       sink,
		wsLookup:   wsLookup,
		auditor:    auditor,
		logger:     logger,
		selfID:     host.PeerID(),
		streams:    make(map[string]*agentStream),
		pendingAdd: make(map[string]AgentRecord),
		pendingDel: make(map[string]struct{}),
	}
	host.Inner().SetStreamHandler(AgentDirectoryProtocol, s.handleStream)
	return s
}

// Stop closes all open streams + cancels any pending debounce. Idempotent.
func (s *AgentDirectoryService) Stop() {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return
	}
	s.stopped = true
	streams := make([]*agentStream, 0, len(s.streams))
	for _, st := range s.streams {
		streams = append(streams, st)
	}
	s.streams = nil
	s.mu.Unlock()

	s.pendMu.Lock()
	if s.flushTimer != nil {
		s.flushTimer.Stop()
		s.flushTimer = nil
	}
	if s.flushCancel != nil {
		s.flushCancel()
		s.flushCancel = nil
	}
	s.pendMu.Unlock()

	for _, st := range streams {
		s.sendByeAndClose(st)
	}
}

// ListPeerStreams returns peer IDs of currently open streams. Test
// + ops surface; not part of the wire protocol.
func (s *AgentDirectoryService) ListPeerStreams() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.streams))
	for peerID := range s.streams {
		out = append(out, peerID)
	}
	sort.Strings(out)
	return out
}

// ConnectToPeer dials peerID + completes the hello + initial snapshot.
// Per spec: only the side with the lexicographically smaller peer ID
// dials; the other side silently waits for the inbound stream. Calling
// ConnectToPeer from the higher-ID side is a no-op (returns nil).
//
// On success, the stream is registered + the read pump is running in a
// goroutine. Subsequent BroadcastDelta calls fan out over this stream.
func (s *AgentDirectoryService) ConnectToPeer(ctx context.Context, peerID string) error {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		return ErrAgentDirStopped
	}
	_, alreadyOpen := s.streams[peerID]
	s.mu.Unlock()
	if alreadyOpen {
		return nil
	}
	if !shouldDial(s.selfID, peerID) {
		return nil // higher-ID side accepts only
	}
	if err := s.assertPeerPaired(ctx, peerID); err != nil {
		return err
	}
	pid, err := peer.Decode(peerID)
	if err != nil {
		return fmt.Errorf("decode peer id: %w", err)
	}
	stream, err := s.host.Inner().NewStream(ctx, pid, AgentDirectoryProtocol)
	if err != nil {
		return fmt.Errorf("open agent directory stream: %w", err)
	}
	st := &agentStream{
		stream: stream,
		peerID: peerID,
		rate:   newByteWindow(AgentBytesPerWindow, AgentRateWindow),
		reader: bufio.NewReaderSize(stream, MaxAgentFrameBytes),
	}

	if err := s.greetPeer(ctx, st); err != nil {
		st.close(s.logger)
		return err
	}
	s.registerStream(st)
	s.recordAudit(ctx, "dial_open", peerID, "ok", "")
	go s.readPump(st)
	return nil
}

// BroadcastDelta enqueues a local agent change for the next debounce
// flush. Non-blocking. The mesh layer calls this after a local upsert
// (added) or expiration/disconnect (removed). Records with an empty
// SessionID or for non-local agents must be filtered upstream — this
// method trusts its inputs.
func (s *AgentDirectoryService) BroadcastDelta(ctx context.Context, added []AgentRecord, removed []string) {
	s.mu.Lock()
	stopped := s.stopped
	s.mu.Unlock()
	if stopped {
		return
	}
	s.pendMu.Lock()
	for _, a := range added {
		if a.SessionID == "" {
			continue
		}
		s.pendingAdd[a.SessionID] = a
		// A remove queued earlier is overridden by the upsert.
		delete(s.pendingDel, a.SessionID)
	}
	for _, sid := range removed {
		if sid == "" {
			continue
		}
		s.pendingDel[sid] = struct{}{}
		// An upsert queued earlier is cancelled by the remove.
		delete(s.pendingAdd, sid)
	}
	if s.flushTimer == nil {
		flushCtx, cancel := context.WithCancel(context.Background())
		s.flushCancel = cancel
		s.flushTimer = time.AfterFunc(agentDeltaDebounce, func() {
			s.flushPending(flushCtx)
		})
	}
	s.pendMu.Unlock()
}

// flushPending fires the coalesced delta to every open stream. Errors
// per-peer close that stream and are recorded; the loop does not abort.
func (s *AgentDirectoryService) flushPending(ctx context.Context) {
	s.pendMu.Lock()
	added := make([]AgentRecord, 0, len(s.pendingAdd))
	for _, a := range s.pendingAdd {
		added = append(added, a)
	}
	removed := make([]string, 0, len(s.pendingDel))
	for sid := range s.pendingDel {
		removed = append(removed, sid)
	}
	s.pendingAdd = make(map[string]AgentRecord)
	s.pendingDel = make(map[string]struct{})
	s.flushTimer = nil
	s.flushCancel = nil
	s.pendMu.Unlock()

	if len(added) == 0 && len(removed) == 0 {
		return
	}

	s.mu.Lock()
	streams := make([]*agentStream, 0, len(s.streams))
	for _, st := range s.streams {
		streams = append(streams, st)
	}
	s.mu.Unlock()
	for _, st := range streams {
		// Scope this peer's delta to agents in the workspaces it is paired
		// with. `removed` is sent unfiltered — a session_id the peer never
		// saw is a harmless no-op on its side, and a session that left the
		// bound workspace legitimately needs to be retracted.
		peerAdded := s.filterAddedForPeer(ctx, st.peerID, added)
		if len(peerAdded) == 0 && len(removed) == 0 {
			continue
		}
		frame := agentDeltaFrame{
			Type:    agentFrameDelta,
			Added:   peerAdded,
			Removed: removed,
			TS:      time.Now().UTC(),
		}
		if err := s.writeFrame(st, frame); err != nil {
			s.logger.Debug("agent directory: delta write failed",
				"peer", st.peerID, "err", err)
			s.recordAudit(ctx, "delta_send", st.peerID, "error", err.Error())
			st.close(s.logger)
			s.dropStream(st.peerID, st)
			continue
		}
		s.recordAudit(ctx, "delta_send", st.peerID, "ok", "")
	}
}

// filterAddedForPeer returns the subset of `added` whose workspace_id is in
// the set of workspaces the peer is paired with. A missing lookup, a lookup
// error, or an unbound peer all yield an empty slice (default-deny) so a
// delta never leaks cross-workspace agents — same fail-closed contract as
// the mesh broadcast leg.
func (s *AgentDirectoryService) filterAddedForPeer(ctx context.Context, peerID string, added []AgentRecord) []AgentRecord {
	if s.wsLookup == nil {
		s.logger.Warn("agent directory: workspace binding lookup is not configured; withholding agent delta",
			"peer", peerID, "agents", len(added))
		return nil
	}
	wsIDs, err := s.wsLookup.ListLocalWorkspaceIDsForPeer(ctx, peerID)
	if err != nil || len(wsIDs) == 0 {
		return nil
	}
	allow := make(map[string]struct{}, len(wsIDs))
	for _, id := range wsIDs {
		allow[id] = struct{}{}
	}
	out := make([]AgentRecord, 0, len(added))
	for _, a := range added {
		if _, ok := allow[a.WorkspaceID]; ok {
			out = append(out, a)
		}
	}
	return out
}

// handleStream is the libp2p inbound entry point. Validates pairing,
// reads + verifies the hello, sends our own hello + snapshot, then
// runs the read pump until the stream closes.
func (s *AgentDirectoryService) handleStream(stream network.Stream) {
	remote := stream.Conn().RemotePeer().String()
	ctx := context.Background()

	if err := s.assertPeerPaired(ctx, remote); err != nil {
		s.logger.Info("agent directory: stream rejected",
			"peer", remote, "err", err)
		s.recordAudit(ctx, "accept", remote, "denied", err.Error())
		_ = stream.Close()
		return
	}
	st := &agentStream{
		stream: stream,
		peerID: remote,
		rate:   newByteWindow(AgentBytesPerWindow, AgentRateWindow),
		reader: bufio.NewReaderSize(stream, MaxAgentFrameBytes),
	}

	// Read the remote's hello first so an unauthenticated stream cannot
	// trick us into emitting our state.
	if err := s.readHello(st); err != nil {
		s.logger.Debug("agent directory: hello read failed",
			"peer", remote, "err", err)
		s.recordAudit(ctx, "accept", remote, "error", err.Error())
		st.close(s.logger)
		return
	}
	if err := s.sendHelloAndSnapshot(ctx, st); err != nil {
		s.logger.Debug("agent directory: greet failed",
			"peer", remote, "err", err)
		s.recordAudit(ctx, "accept", remote, "error", err.Error())
		st.close(s.logger)
		return
	}
	s.registerStream(st)
	s.recordAudit(ctx, "accept", remote, "ok", "")
	s.readPump(st)
}

// greetPeer is the dialer-side handshake: send hello + snapshot, then
// read remote hello. Mirror of handleStream's order.
func (s *AgentDirectoryService) greetPeer(ctx context.Context, st *agentStream) error {
	if err := s.sendHelloAndSnapshot(ctx, st); err != nil {
		return err
	}
	if err := s.readHello(st); err != nil {
		return err
	}
	return nil
}

// sendHelloAndSnapshot writes our hello + a snapshot of local agents.
// Snapshot caps at MaxAgentsPerSnapshot (sorted desc by LastSeenAt).
func (s *AgentDirectoryService) sendHelloAndSnapshot(ctx context.Context, st *agentStream) error {
	hello := agentHelloFrame{
		Type:         agentFrameHello,
		PeerID:       s.selfID,
		ProtoVersion: AgentDirectoryProtocolVersion,
		TS:           time.Now().UTC(),
	}
	if err := s.writeFrame(st, hello); err != nil {
		return fmt.Errorf("write hello: %w", err)
	}

	agents, err := s.source.ListLocalAgents(ctx, st.peerID)
	if err != nil {
		// Don't fail the stream — send an empty snapshot so the remote
		// can still register us as alive. The next ensureAgent will
		// flush a delta.
		s.logger.Warn("agent directory: list local agents",
			"peer", st.peerID, "err", err)
		agents = nil
	}
	if len(agents) > MaxAgentsPerSnapshot {
		// Keep the most-recently-seen entries. Spec: "log warn so we
		// know we hit the cap".
		sort.Slice(agents, func(i, j int) bool {
			return agents[i].LastSeenAt.After(agents[j].LastSeenAt)
		})
		s.logger.Warn("agent directory: snapshot truncated to cap",
			"peer", st.peerID,
			"local_agents", len(agents),
			"cap", MaxAgentsPerSnapshot)
		agents = agents[:MaxAgentsPerSnapshot]
	}
	snap := agentSnapshotFrame{
		Type:   agentFrameSnapshot,
		Agents: agents,
		TS:     time.Now().UTC(),
	}
	if err := s.writeFrame(st, snap); err != nil {
		return fmt.Errorf("write snapshot: %w", err)
	}
	s.recordAudit(ctx, "snapshot_send", st.peerID, "ok", "")
	return nil
}

// readHello reads exactly one frame and verifies it is a hello whose
// peer_id matches the libp2p stream's RemotePeer (anti-spoof). A
// version mismatch closes the stream.
func (s *AgentDirectoryService) readHello(st *agentStream) error {
	frame, err := s.readFrame(st)
	if err != nil {
		return err
	}
	var head struct {
		Type         string `json:"type"`
		PeerID       string `json:"peer_id"`
		ProtoVersion int    `json:"proto_version"`
	}
	if err := json.Unmarshal(frame, &head); err != nil {
		return fmt.Errorf("hello parse: %w", err)
	}
	if head.Type != agentFrameHello {
		return fmt.Errorf("agent directory: expected hello, got %q", head.Type)
	}
	if head.PeerID != st.peerID {
		return ErrAgentDirSpoofedHello
	}
	if head.ProtoVersion != AgentDirectoryProtocolVersion {
		return fmt.Errorf("%w: got %d, want %d",
			ErrAgentDirVersionMismatch, head.ProtoVersion,
			AgentDirectoryProtocolVersion)
	}
	return nil
}

// readPump reads frames until EOF or a fatal error. Snapshot/delta/bye
// frames are dispatched to the sink. Unknown types are ignored
// (forward-compat). Closes the stream + drops it from the registry on
// exit.
func (s *AgentDirectoryService) readPump(st *agentStream) {
	defer func() {
		st.close(s.logger)
		s.dropStream(st.peerID, st)
	}()
	ctx := context.Background()
	for {
		frame, err := s.readFrame(st)
		if err != nil {
			if !errors.Is(err, io.EOF) {
				s.logger.Debug("agent directory: read pump exit",
					"peer", st.peerID, "err", err)
				s.recordAudit(ctx, "read", st.peerID, "error", err.Error())
			}
			return
		}
		var head struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(frame, &head); err != nil {
			s.logger.Debug("agent directory: bad frame",
				"peer", st.peerID, "err", err)
			s.recordAudit(ctx, "read", st.peerID, "error", err.Error())
			return
		}
		switch head.Type {
		case agentFrameSnapshot:
			s.applySnapshot(ctx, st, frame)
		case agentFrameDelta:
			s.applyDelta(ctx, st, frame)
		case agentFrameBye:
			s.applyBye(ctx, st)
			return
		default:
			// Forward-compat: unknown type — log + skip.
			s.logger.Debug("agent directory: unknown frame type",
				"peer", st.peerID, "type", head.Type)
		}
	}
}

func (s *AgentDirectoryService) applySnapshot(ctx context.Context, st *agentStream, frame []byte) {
	var snap agentSnapshotFrame
	if err := json.Unmarshal(frame, &snap); err != nil {
		s.recordAudit(ctx, "snapshot_recv", st.peerID, "error", err.Error())
		return
	}
	if len(snap.Agents) > MaxAgentsPerSnapshot {
		s.logger.Warn("agent directory: snapshot exceeds cap",
			"peer", st.peerID, "agents", len(snap.Agents),
			"cap", MaxAgentsPerSnapshot)
		s.recordAudit(ctx, "snapshot_recv", st.peerID,
			"error", ErrAgentDirSnapshotTooLarge.Error())
		return
	}
	if err := s.sink.ApplyRemoteSnapshot(ctx, st.peerID, snap.Agents); err != nil {
		s.logger.Warn("agent directory: sink snapshot",
			"peer", st.peerID, "err", err)
		s.recordAudit(ctx, "snapshot_recv", st.peerID, "error", err.Error())
		return
	}
	s.recordAudit(ctx, "snapshot_recv", st.peerID, "ok", "")
}

func (s *AgentDirectoryService) applyDelta(ctx context.Context, st *agentStream, frame []byte) {
	var delta agentDeltaFrame
	if err := json.Unmarshal(frame, &delta); err != nil {
		s.recordAudit(ctx, "delta_recv", st.peerID, "error", err.Error())
		return
	}
	if err := s.sink.ApplyRemoteDelta(ctx, st.peerID, delta.Added, delta.Removed); err != nil {
		s.logger.Warn("agent directory: sink delta",
			"peer", st.peerID, "err", err)
		s.recordAudit(ctx, "delta_recv", st.peerID, "error", err.Error())
		return
	}
	s.recordAudit(ctx, "delta_recv", st.peerID, "ok", "")
}

func (s *AgentDirectoryService) applyBye(ctx context.Context, st *agentStream) {
	if err := s.sink.HandleRemoteBye(ctx, st.peerID); err != nil {
		s.logger.Warn("agent directory: sink bye",
			"peer", st.peerID, "err", err)
		s.recordAudit(ctx, "bye_recv", st.peerID, "error", err.Error())
		return
	}
	s.recordAudit(ctx, "bye_recv", st.peerID, "ok", "")
}

// sendByeAndClose attempts a graceful bye write before closing the
// stream. Used by Stop. Best-effort — write errors are logged + the
// stream is closed regardless.
func (s *AgentDirectoryService) sendByeAndClose(st *agentStream) {
	bye := agentByeFrame{Type: agentFrameBye, TS: time.Now().UTC()}
	if err := s.writeFrame(st, bye); err != nil {
		s.logger.Debug("agent directory: bye write",
			"peer", st.peerID, "err", err)
	}
	st.close(s.logger)
}

// writeFrame encodes v as JSON, appends '\n', and writes to st.stream.
// Holds the per-stream write mutex so concurrent BroadcastDelta + read
// pump shutdown writes don't interleave bytes.
func (s *AgentDirectoryService) writeFrame(st *agentStream, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal frame: %w", err)
	}
	if len(data)+1 > MaxAgentFrameBytes {
		return fmt.Errorf("%w: %d bytes", ErrAgentDirFrameTooLarge, len(data)+1)
	}
	data = append(data, '\n')
	st.writeMu.Lock()
	defer st.writeMu.Unlock()
	_ = st.stream.SetWriteDeadline(time.Now().Add(agentStreamWriteDeadline))
	if _, err := st.stream.Write(data); err != nil {
		return fmt.Errorf("write frame: %w", err)
	}
	return nil
}

// readFrame reads one '\n'-terminated frame, enforces the byte cap +
// per-peer rate limit, and returns the frame bytes (no trailing newline).
func (s *AgentDirectoryService) readFrame(st *agentStream) ([]byte, error) {
	_ = st.stream.SetReadDeadline(time.Now().Add(agentStreamReadDeadline))
	// ReadSlice on the persistent, cap-sized reader: an oversize line surfaces
	// as ErrBufferFull immediately instead of ReadBytes accumulating the whole
	// (potentially multi-GB) line before the length check could fire.
	line, err := st.reader.ReadSlice('\n')
	if errors.Is(err, bufio.ErrBufferFull) {
		return nil, fmt.Errorf("%w: > %d", ErrAgentDirFrameTooLarge, MaxAgentFrameBytes)
	}
	if len(line) > MaxAgentFrameBytes {
		return nil, fmt.Errorf("%w: %d > %d",
			ErrAgentDirFrameTooLarge, len(line), MaxAgentFrameBytes)
	}
	if err != nil && len(line) == 0 {
		return nil, err
	}
	if errRate := st.rate.add(int64(len(line))); errRate != nil {
		return nil, errRate
	}
	// Trim trailing '\n' (may be absent on the final frame at EOF).
	if n := len(line); n > 0 && line[n-1] == '\n' {
		line = line[:n-1]
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	// ReadSlice returns a view into the shared buffer that the next read
	// overwrites; copy so callers can hold the frame safely.
	out := make([]byte, len(line))
	copy(out, line)
	return out, nil
}

// shouldDial returns true if our peer ID lexicographically precedes
// theirs. Per spec: lower dials, higher accepts. Equal IDs (impossible
// in practice but defensive) return false to avoid self-connect.
func shouldDial(self, other string) bool {
	if self == "" || other == "" {
		return false
	}
	return self < other
}

// registerStream stores st under peerID. If a previous stream exists, the
// older one is closed — last-writer-wins prevents leaks across reconnect.
func (s *AgentDirectoryService) registerStream(st *agentStream) {
	s.mu.Lock()
	if s.stopped {
		s.mu.Unlock()
		st.close(s.logger)
		return
	}
	prev, ok := s.streams[st.peerID]
	s.streams[st.peerID] = st
	s.mu.Unlock()
	if ok && prev != st {
		prev.close(s.logger)
	}
}

// dropStream removes st from the registry IFF it is still the entry for
// peerID. Avoids racing a fresh registration that just arrived.
func (s *AgentDirectoryService) dropStream(peerID string, st *agentStream) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cur, ok := s.streams[peerID]; ok && cur == st {
		delete(s.streams, peerID)
	}
}

// recordAudit forwards to the optional auditor.
func (s *AgentDirectoryService) recordAudit(ctx context.Context, action, peerID, status, errMsg string) {
	if s.auditor == nil {
		return
	}
	s.auditor.RecordAgentDirectory(ctx, action, peerID, status, errMsg)
}

// assertPeerPaired enforces the only ACL the protocol cares about. The
// remote must have a row in p2p_peers. Scopes are NOT checked — agent
// gossip is treated like mesh envelopes (pairing alone authorises). nil
// lookup is permissive (test path).
func (s *AgentDirectoryService) assertPeerPaired(ctx context.Context, peerID string) error {
	if s.lookup == nil {
		return nil
	}
	ok, err := s.lookup.IsPaired(ctx, peerID)
	if err != nil {
		return fmt.Errorf("%w: %s (lookup: %v)", ErrAgentDirNotPaired, peerID, err)
	}
	if !ok {
		return fmt.Errorf("%w: %s", ErrAgentDirNotPaired, peerID)
	}
	return nil
}

// close closes the underlying stream exactly once.
func (a *agentStream) close(logger *slog.Logger) {
	a.closeOnce.Do(func() {
		if err := a.stream.Close(); err != nil && logger != nil {
			logger.Debug("agent directory: stream close",
				"peer", a.peerID, "err", err)
		}
	})
}

// newByteWindow builds a sliding-window byte counter with the given cap
// + window duration.
func newByteWindow(cap int64, window time.Duration) *byteWindow {
	return &byteWindow{cap: cap, window: window}
}

// add records n bytes at time.Now and returns ErrAgentDirRateLimited if
// the window's running total exceeds the cap. Old samples are evicted
// on every call so the counter stays bounded.
func (b *byteWindow) add(n int64) error {
	if b == nil || b.cap <= 0 || n <= 0 {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-b.window)
	// Evict old samples.
	keep := b.counts[:0]
	for _, s := range b.counts {
		if s.at.Before(cutoff) {
			b.totalNow -= s.n
			continue
		}
		keep = append(keep, s)
	}
	b.counts = keep
	b.counts = append(b.counts, byteSample{at: now, n: n})
	b.totalNow += n
	if b.totalNow > b.cap {
		return fmt.Errorf("%w: %d bytes in last %s", ErrAgentDirRateLimited,
			b.totalNow, b.window)
	}
	return nil
}
