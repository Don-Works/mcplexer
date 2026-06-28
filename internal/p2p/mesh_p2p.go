//go:build p2p

package p2p

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
)

// MeshAuditor receives one record per envelope decision. Concrete impls in
// production wire to internal/audit; tests pass a slice-collector. The
// interface is intentionally narrow — no audit dependency from this package.
type MeshAuditor interface {
	Record(ctx context.Context, kind, peerID, reason, envID string)
}

// MeshPeerLookup is the narrow read of the paired-peer table the transport
// needs at receive time. Wire to internal/store.P2PPeerStore in production.
type MeshPeerLookup interface {
	IsPaired(ctx context.Context, peerID string) (bool, error)
	ListPeerIDs(ctx context.Context) ([]string, error)
}

// MeshWorkspaceLookup resolves the local workspace IDs a peer is paired
// with (the workspace_peer_bindings ACL set). SendBroadcast uses it to
// enforce workspace-scoped pairing on OUTBOUND fan-out: a workspace-scoped
// envelope is delivered only to peers bound to that workspace, so a pairing
// scoped to workspace X never carries workspace-Y mesh traffic across the
// wire — even to a peer running an older build that wouldn't drop it
// inbound. The inbound side (mesh.ingestEnvelope) enforces the same rule
// independently; this is the matching outbound half of defense-in-depth.
//
// A nil lookup (tests / slim wiring) is permissive: every connected paired
// peer receives every broadcast, preserving pre-scoping behaviour.
type MeshWorkspaceLookup interface {
	ListLocalWorkspaceIDsForPeer(ctx context.Context, peerID string) ([]string, error)
}

// MeshTransport sends + receives signed envelopes over libp2p streams. One
// instance per host. Construct with NewMeshTransport, mount the protocol
// handler with Start, and shut down with Close.
type MeshTransport struct {
	host     *Host
	lookup   MeshPeerLookup
	wsLookup MeshWorkspaceLookup
	audit    MeshAuditor
	logger   *slog.Logger

	rxMu      sync.RWMutex
	rxClosed  bool
	receivers []chan MeshEnvelope

	dedupe *dedupeWindow
}

// SetWorkspaceLookup wires the workspace-binding ACL used to scope outbound
// broadcasts to the workspaces each peer is paired with. Call once after
// construction (kept off NewMeshTransport so existing call sites are
// undisturbed). Passing nil restores permissive (pre-scoping) fan-out.
func (t *MeshTransport) SetWorkspaceLookup(ws MeshWorkspaceLookup) {
	if t == nil {
		return
	}
	t.wsLookup = ws
}

// NewMeshTransport constructs a transport. lookup may be nil — in that mode
// every peer is considered unpaired and incoming envelopes are rejected.
// audit and logger are nil-safe.
func NewMeshTransport(h *Host, lookup MeshPeerLookup, audit MeshAuditor, logger *slog.Logger) *MeshTransport {
	if logger == nil {
		logger = slog.Default()
	}
	return &MeshTransport{
		host:   h,
		lookup: lookup,
		audit:  audit,
		logger: logger,
		dedupe: newDedupeWindow(100_000),
	}
}

// Start mounts the protocol handler on the host. Safe to call once.
func (t *MeshTransport) Start() {
	if t == nil || t.host == nil {
		return
	}
	t.host.Inner().SetStreamHandler(protocol.ID(MeshProtocol), t.handleStream)
}

// Subscribe returns a channel that receives envelopes verified + accepted
// from peer streams. Buffer is 64; slow consumers drop.
func (t *MeshTransport) Subscribe() <-chan MeshEnvelope {
	ch := make(chan MeshEnvelope, 64)
	t.rxMu.Lock()
	defer t.rxMu.Unlock()
	if t.rxClosed {
		close(ch)
		return ch
	}
	t.receivers = append(t.receivers, ch)
	return ch
}

// Close drains subscribers + removes the protocol handler. Idempotent.
func (t *MeshTransport) Close() error {
	if t == nil {
		return nil
	}
	t.rxMu.Lock()
	if t.rxClosed {
		t.rxMu.Unlock()
		return nil
	}
	t.rxClosed = true
	receivers := t.receivers
	t.receivers = nil
	t.rxMu.Unlock()
	for _, ch := range receivers {
		close(ch)
	}
	if t.host != nil && t.host.Inner() != nil {
		t.host.Inner().RemoveStreamHandler(protocol.ID(MeshProtocol))
	}
	return nil
}

// SendToPeer signs `env` with the host's static identity and ships it over a
// fresh libp2p stream to peerID. Caller pre-fills env fields (id, kind,
// content, payload, recipient, ts); SenderPeerID + Signature are overwritten.
func (t *MeshTransport) SendToPeer(ctx context.Context, peerID string, env *MeshEnvelope) error {
	if t == nil || t.host == nil {
		return ErrP2PNotBuiltIn
	}
	if env == nil {
		return errors.New("p2p mesh: envelope is nil")
	}
	pid, err := peer.Decode(peerID)
	if err != nil {
		return fmt.Errorf("p2p mesh: decode peer %q: %w", peerID, err)
	}
	if err := t.signEnvelope(env); err != nil {
		return err
	}

	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	stream, err := t.host.Inner().NewStream(dialCtx, pid, protocol.ID(MeshProtocol))
	if err != nil {
		return fmt.Errorf("p2p mesh: open stream to %s: %w", peerID, err)
	}
	defer func() { _ = stream.Close() }()
	_ = stream.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if err := writeEnvelope(stream, env); err != nil {
		_ = stream.Reset()
		return fmt.Errorf("p2p mesh: write envelope: %w", err)
	}
	return nil
}

// SendBroadcast ships env to every currently-CONNECTED paired peer. Legs run
// concurrently; per-peer errors are logged but never abort the fan-out.
// Returns the count successfully sent.
//
// Live-only by design: a broadcast is ephemeral, so a peer with no open
// connection cannot receive it anyway — and dialing an offline peer here
// would block the caller for the full 10s dial timeout (×N offline peers,
// previously serial). That stall was the "mcplexer freezes" bug: every
// task_event / mesh broadcast rode this path synchronously on the tool-call
// goroutine. Offline peers are served by the durable mesh_outbound_queue on
// targeted sends, not by re-dialing them on every broadcast. We therefore
// skip any peer that is not currently connected.
func (t *MeshTransport) SendBroadcast(ctx context.Context, env *MeshEnvelope) (int, error) {
	if t == nil || t.host == nil {
		return 0, ErrP2PNotBuiltIn
	}
	if t.lookup == nil {
		return 0, errors.New("p2p mesh: no peer lookup configured")
	}
	peers, err := t.lookup.ListPeerIDs(ctx)
	if err != nil {
		return 0, fmt.Errorf("p2p mesh: list paired peers: %w", err)
	}
	self := t.host.PeerID()
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		sent int
	)
	for _, pidStr := range peers {
		if pidStr == self {
			continue
		}
		pid, decErr := peer.Decode(pidStr)
		if decErr != nil {
			t.logger.Warn("p2p mesh: broadcast skip undecodable peer",
				"peer", pidStr, "err", decErr)
			continue
		}
		if !t.host.IsConnected(pid) {
			// Not connected → skip. No dial, no stall. (Targeted sends
			// to this peer still queue via the outbound queue.)
			continue
		}
		// Workspace-scoped pairing: a workspace-scoped envelope is delivered
		// only to peers bound to that workspace. Empty / "global" envelopes
		// fan out to every paired peer (legacy + explicit broadcast). An
		// unbound peer — or a lookup error — is skipped (default-deny) so a
		// pairing scoped to workspace X cannot leak workspace-Y traffic.
		if !t.peerAuthorizedForWorkspace(ctx, pidStr, env.WorkspaceID) {
			continue
		}
		wg.Add(1)
		go func(peerID string) {
			defer wg.Done()
			copyEnv := *env
			if err := t.SendToPeer(ctx, peerID, &copyEnv); err != nil {
				t.logger.Warn("p2p mesh: broadcast leg failed",
					"peer", peerID, "err", err)
				return
			}
			mu.Lock()
			sent++
			mu.Unlock()
		}(pidStr)
	}
	wg.Wait()
	return sent, nil
}

// peerAuthorizedForWorkspace reports whether a broadcast envelope stamped
// with workspaceID may be delivered to peerID.
//
//   - workspaceID == "" or "global" → always authorized (broadcast / no
//     workspace scoping; lands in the receiver's "global" bucket).
//   - no workspace lookup wired (nil) → permissive, preserving the
//     pre-scoping fan-out behaviour relied on by tests + slim builds.
//   - otherwise → authorized iff peerID has a workspace_peer_bindings row
//     whose local_workspace_id == workspaceID. A lookup error or an unbound
//     peer is treated as NOT authorized (default-deny).
func (t *MeshTransport) peerAuthorizedForWorkspace(ctx context.Context, peerID, workspaceID string) bool {
	if workspaceID == "" || workspaceID == "global" {
		return true
	}
	if t.wsLookup == nil {
		return true
	}
	wsIDs, err := t.wsLookup.ListLocalWorkspaceIDsForPeer(ctx, peerID)
	if err != nil {
		t.logger.Warn("p2p mesh: workspace binding lookup failed; dropping broadcast leg",
			"peer", peerID, "workspace", workspaceID, "err", err)
		return false
	}
	for _, id := range wsIDs {
		if id == workspaceID {
			return true
		}
	}
	return false
}

// signEnvelope sets SenderPeerID + Signature using the host's static key.
func (t *MeshTransport) signEnvelope(env *MeshEnvelope) error {
	priv := t.host.Inner().Peerstore().PrivKey(t.host.ID())
	if priv == nil {
		return errors.New("p2p mesh: missing host private key")
	}
	env.SenderPeerID = t.host.PeerID()
	if env.TS == 0 {
		env.TS = time.Now().UnixMilli()
	}
	sig, err := priv.Sign(canonicalSigningBytes(env))
	if err != nil {
		return fmt.Errorf("p2p mesh: sign envelope: %w", err)
	}
	env.Signature = sig
	return nil
}

// fanout pushes env to every active subscriber. Slow subscribers see the
// envelope dropped (buffer full) — the transport never blocks.
func (t *MeshTransport) fanout(env MeshEnvelope) {
	t.rxMu.RLock()
	defer t.rxMu.RUnlock()
	if t.rxClosed {
		return
	}
	for _, ch := range t.receivers {
		select {
		case ch <- env:
		default:
		}
	}
}

// handleStream is the protocol handler: stream-decode one envelope, verify
// signature + paired-peer + dedupe, then fan out to subscribers. Each call
// resets the stream when the message is rejected so the remote sees the
// failure and the conn manager can prune.
func (t *MeshTransport) handleStream(s network.Stream) {
	defer func() { _ = s.Close() }()
	_ = s.SetDeadline(time.Now().Add(15 * time.Second))
	remote := s.Conn().RemotePeer()
	env, err := readEnvelope(s)
	if err != nil {
		t.audited(s.Conn().RemotePeer().String(), "decode_failed", "", err.Error())
		_ = s.Reset()
		return
	}
	if reason := t.verifyAndAuthorize(remote, &env); reason != "" {
		t.audited(remote.String(), reason, env.ID, "")
		_ = s.Reset()
		return
	}
	if t.dedupe.seen(env.SenderPeerID, env.ID) {
		t.audited(remote.String(), "duplicate", env.ID, "")
		return
	}
	t.fanout(env)
}

// audited is a nil-safe shim for sending one record to the auditor.
func (t *MeshTransport) audited(peerID, reason, envID, detail string) {
	if t.audit == nil {
		return
	}
	t.audit.Record(context.Background(), "p2p_mesh", peerID, reason, envID)
	if detail != "" {
		t.logger.Debug("p2p mesh audit detail",
			"peer", peerID, "reason", reason, "env_id", envID, "detail", detail)
	}
}
