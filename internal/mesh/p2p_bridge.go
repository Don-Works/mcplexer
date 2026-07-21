package mesh

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/p2p"
	"github.com/don-works/mcplexer/internal/store"
)

// stripReservedTags removes transport-reserved tokens — the "p2p" marker and
// any "from:*" origin tag — from peer-supplied envelope tags. Tags are not
// covered by the envelope signature, so without this a paired peer could
// inject a "from:" tag that sourcePeerID (first-match) would treat as the
// message origin, spoofing another peer or the local self to bypass the
// dispatcher's peer-scope authorization. Splitting mirrors match.splitTags
// (comma-separated, space-trimmed); non-reserved tags are preserved in order.
func stripReservedTags(raw string) string {
	if raw == "" {
		return ""
	}
	parts := strings.Split(raw, ",")
	kept := make([]string, 0, len(parts))
	for _, p := range parts {
		t := strings.TrimSpace(p)
		if t == "" || t == "p2p" || strings.HasPrefix(t, "from:") {
			continue
		}
		kept = append(kept, t)
	}
	return strings.Join(kept, ",")
}

// p2pTransport is the narrow surface of *p2p.MeshTransport that mesh.Manager
// uses. Defined here (not in p2p) so the mesh package doesn't take a
// build-tag dependency on libp2p.
type p2pTransport interface {
	SendToPeer(ctx context.Context, peerID string, env *p2p.MeshEnvelope) error
	SendBroadcast(ctx context.Context, env *p2p.MeshEnvelope) (int, error)
	Subscribe() <-chan p2p.MeshEnvelope
}

// SelfPeerID returns the libp2p peer ID stamped on outgoing envelopes. May
// be empty when no transport is wired (single-host install).
func (m *Manager) SelfPeerID() string {
	if m == nil {
		return ""
	}
	return m.selfPeerID
}

// SetP2PTransport wires a libp2p mesh transport so cross-machine envelopes
// flow alongside the local sqlite bus. Call once after constructing the
// Manager. Passing nil disables p2p transport.
func (m *Manager) SetP2PTransport(t p2pTransport, selfPeerID string) {
	if m == nil {
		return
	}
	m.p2p = t
	m.selfPeerID = selfPeerID
}

// StartP2PBridge spawns a goroutine that subscribes to incoming libp2p
// envelopes and writes them to the mesh_messages table so local
// mesh__receive callers see them. Cancellation from ctx stops the loop.
func (m *Manager) StartP2PBridge(ctx context.Context, logger *slog.Logger) {
	if m == nil || m.p2p == nil {
		return
	}
	if logger == nil {
		logger = slog.Default()
	}
	ch := m.p2p.Subscribe()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case env, ok := <-ch:
				if !ok {
					return
				}
				if err := m.ingestEnvelope(ctx, env); err != nil {
					logger.Warn("p2p mesh ingest failed",
						"err", err, "from", env.SenderPeerID, "id", env.ID)
				}
			}
		}
	}()
}

// ingestEnvelope translates a libp2p MeshEnvelope into a store.MeshMessage
// and inserts it. Workspace resolution (G1):
//
//   - env.WorkspaceID == "" or "global" → land in the global namespace
//     (WorkspaceID=""), the same bucket a local to_workspace:"*" send
//     resolves to and the only one Receive reads for every session.
//     Workers in real workspaces refuse to trigger on these (G2).
//   - env.WorkspaceID != "" + binding exists → land in
//     binding.LocalWorkspaceID; triggers in that workspace can fire.
//   - env.WorkspaceID != "" + no binding → DROP the envelope and audit
//     it. Defense-in-depth: an unbound peer cannot pick a target
//     workspace and inject a message there.
//
// Display-name change events (kind=event + tag=display_name_change) are
// applied to the peer directory and NOT stored as messages — they're
// metadata, not content the user wants in their inbox.
//
// Regular envelopes pass the same gates as a local Send (validKind,
// non-blank content, content-size ceiling); failures are dropped with an
// audit record, mirroring the unbound-workspace deny path. M7.4 file_claim
// events remain deferred — until the FileClaimStore plumbing lands they are
// rejected here as unknown kinds rather than polluting mesh_messages.
func (m *Manager) ingestEnvelope(ctx context.Context, env p2p.MeshEnvelope) error {
	// Rename events: persist the new display name for this peer + opportunistically
	// also persist the embedded SenderDisplayName below. Don't store as a message.
	if isDisplayNameChange(env) {
		m.applyDisplayNameChange(ctx, env)
		return nil
	}

	// v0.13.0 — peer_identity events: persist the peer's age recipient.
	// Don't store as a message; metadata only.
	if isPeerIdentity(env) {
		m.applyPeerIdentity(ctx, env)
		return nil
	}

	// v0.13.0 — secret_offer events: stage the ciphertext for agent approval.
	// Inbound only; the sender's row is created by mesh__send_secret directly.
	if isSecretOffer(env) {
		m.applySecretOffer(ctx, env)
		return nil
	}

	// mesh__push_skill — skill_offer events: stage the metadata-only offer
	// for agent accept/reject. Inbound only; the sender's row is created by
	// mesh__push_skill directly.
	if isSkillOffer(env) {
		m.applySkillOffer(ctx, env)
		return nil
	}

	if isAuthSync(env) {
		m.applyAuthSync(ctx, env)
		return nil
	}

	// Every regular envelope carries SenderDisplayName as a UX hint. Persist
	// it so the peer directory tracks the sender's current device name,
	// even when they didn't explicitly broadcast a rename. NOT auth-bearing.
	//
	// Gated on the same rule as the explicit rename path: this is the second
	// way a peer-controlled string reaches p2p_peers.display_name, and
	// mesh__list_peers renders it into an agent's context. See
	// acceptablePeerDisplayName.
	if m.peerRenamer != nil && acceptablePeerDisplayName(env.SenderDisplayName) {
		_ = m.peerRenamer.UpdateDisplayName(ctx, env.SenderPeerID, env.SenderDisplayName)
	}

	// Inbound envelopes pass the same content gates as a local Send — a
	// remote peer must not be able to inject what a local agent could not
	// (unknown kinds, blank bodies, oversize payloads). Denials are
	// audited, never errored: a malicious peer doesn't get a feedback loop.
	if reason := validateInboundEnvelope(env); reason != "" {
		m.recordReceive(ctx, env.SenderPeerID, env.SenderUserID, env.Kind,
			len(env.Content), false, env.ID, "denied", reason)
		return nil
	}

	// G1 — workspace resolution. The wire format reserves the sentinel
	// "global" (and the empty string) for "broadcast / no workspace
	// scoping"; both land in the LOCAL global namespace, WorkspaceID="".
	//
	// "" — not the literal "global" — is the one bucket Receive reads for
	// every session (readableWorkspaceIDs unconditionally appends ""), and
	// it is where a local to_workspace:"*" send lands. Writing the literal
	// instead put every cross-machine broadcast in a bucket no agent reads:
	// durably stored, on the dashboard, invisible to every recipient, while
	// the sender saw a success receipt.
	//
	// This does NOT reintroduce the shared-bucket leak defaultMeshWorkspace
	// (gateway/handler_mesh.go) warns about: that was unbound LOCAL sessions
	// collapsing into one namespace, and their traffic still resolves to
	// "dir:<root>" / "session:<id>". Only envelopes the sender explicitly
	// marked as a broadcast reach this line with ""; workspace-scoped ones
	// still require a binding below.
	localWorkspaceID := ""
	if env.WorkspaceID != "" && env.WorkspaceID != "global" && env.SenderPeerID != "" {
		binding, err := m.store.GetWorkspacePeerBinding(ctx, env.SenderPeerID, env.WorkspaceID)
		if err != nil || binding == nil {
			m.recordReceive(ctx, env.SenderPeerID, env.SenderUserID, env.Kind,
				len(env.Content), false, env.ID, "denied", "unbound_workspace")
			return nil
		}
		localWorkspaceID = binding.LocalWorkspaceID
	}

	// Carry the sender's priority through (it used to be dropped on the
	// floor — every cross-peer message landed as "normal"). Unknown or
	// empty values clamp to "normal" so a peer can't mint a TTL we don't
	// recognise.
	priority := clampPriority(env.Priority)
	ttl := priorityTTL[priority]
	now := time.Now().UTC()
	audience := "*"
	if env.Recipient.Kind == "audience" && env.Recipient.Value != "" {
		audience = env.Recipient.Value
	}
	if env.Recipient.Kind == "role" && env.Recipient.Value != "" {
		audience = env.Recipient.Value
	}
	// Stamp the trusted transport markers FIRST, then append the sender's own
	// tags with any reserved tokens stripped. Envelope tags are NOT covered by
	// the signature, so a paired peer could otherwise inject its own
	// "from:<victim>" (or "from:<our-self-id>") that, because sourcePeerID
	// returns the FIRST "from:" tag, would win over the real marker and bypass
	// the dispatcher's cross-peer peer-scope gate. Putting the trusted marker
	// first AND removing peer-supplied from:/p2p tokens closes both the
	// ordering and the injection. Non-reserved tags (M4 tag_match filter
	// targets) are preserved.
	inboundTags := "p2p,from:" + env.SenderPeerID
	if sanitized := stripReservedTags(env.Tags); sanitized != "" {
		inboundTags = inboundTags + "," + sanitized
	}
	// The stored row id is minted LOCALLY, never carried from env.ID.
	//
	// mesh_messages.id doubles as the receive cursor, filtered with a
	// lexicographic `id > ?`. A peer-supplied id sorting above the local id
	// space therefore advances the cursor past every future local message
	// and kills the inbox permanently — and silently, since PendingCount
	// reads the same cursor and also reports 0.
	//
	// Malice is not required: ULIDs are timestamp-prefixed, so a peer whose
	// clock runs fast mints ids above every local id for the duration of the
	// skew. Validating the id (validateInboundEnvelope) does not help there —
	// it is well-formed, just from the future. Re-minting holds by
	// construction and makes selectOldestBatch's ULID-ordering invariant
	// (mesh.go) true on the ingest path, not just for local sends.
	//
	// Tradeoff: the primary key no longer dedupes a replayed envelope. That
	// is deliberate — dedupe belongs to the transport, which rejects
	// duplicate (sender, envelope id) pairs before ingest via a 100k-entry
	// window (p2p.MeshTransport.handleStream). The peer's id is still
	// recorded in the audit ledger below for cross-machine correlation.
	msg := &store.MeshMessage{
		ID:                newULID(),
		WorkspaceID:       localWorkspaceID,
		SessionID:         env.SenderPeerID, // remote peer acts as the agent's session for routing
		AgentName:         displayAgentName(env),
		SenderDisplayName: env.SenderDisplayName,
		Kind:              env.Kind,
		Priority:          priority,
		Content:           env.Content,
		Audience:          audience,
		Tags:              inboundTags,
		Status:            "live",
		ExpiresAt:         now.Add(ttl),
		CreatedAt:         now,
		Repo:              env.Repo,
		Branch:            env.Branch,
		WorkspacePath:     env.WorkspacePath,
		RepoRemote:        env.RepoRemote,
		ActorKind:         "peer-import", // inbound libp2p ingest (see SendRequest.ActorKind)
	}
	if err := m.store.InsertMeshMessage(ctx, msg); err != nil {
		m.recordReceive(ctx, env.SenderPeerID, env.SenderUserID, env.Kind,
			len(env.Content), true, env.ID, "error", "insert_failed")
		return err
	}
	// Enforce the live-message ceiling — mirrors the local Send path
	// (mesh.go Send). Without this, a high-volume remote peer can grow
	// the live-message count beyond the cap, burning receiver context on
	// every mesh__receive poll.
	ceiling := m.liveMessageCeiling()
	count, cErr := m.store.CountLiveMessages(ctx, localWorkspaceID)
	if cErr == nil && count > ceiling {
		excess := count - ceiling
		_, _ = m.store.ArchiveLowestPriority(ctx, localWorkspaceID, excess)
	}
	// M4 — notify subscribers (trigger dispatcher) after the inbound
	// envelope is durable. Same fast-subscriber contract as the local
	// Send path.
	m.notifySubscribers(ctx, msg)
	// Register the remote sender in the active-agents directory so the UI's
	// "Active Agents" panel can distinguish them from local socket agents.
	// Best-effort — a write failure here does not invalidate the message.
	_ = m.upsertRemoteAgent(ctx, env, localWorkspaceID)
	// Audit the successful receive (async, fire-and-forget).
	m.recordReceive(ctx, env.SenderPeerID, env.SenderUserID, env.Kind,
		len(env.Content), true, env.ID, "success", "")
	return nil
}

// upsertRemoteAgent records the inbound libp2p sender as an active mesh
// agent tagged with origin="peer:<peer_id>". This is what makes the UI's
// active-agents list explicitly distinguish remote peers from agents
// reached via the local stdio socket.
//
// localWorkspaceID is the workspace the matching mesh_message landed in
// (resolved via workspace_peer_bindings, or "" for broadcasts). We mirror
// it onto the agent row so the directory query in that workspace's UI
// surface includes this peer — except for "", which the agent-directory
// query skips entirely (activeAgentsForWorkspaces ignores blank ids), so
// broadcast-origin peers keep landing under the "global" workspace row
// they have always used.
func (m *Manager) upsertRemoteAgent(ctx context.Context, env p2p.MeshEnvelope, localWorkspaceID string) error {
	if env.SenderPeerID == "" {
		return nil
	}
	if localWorkspaceID == "" {
		localWorkspaceID = "global"
	}
	now := time.Now().UTC()
	return m.store.UpsertMeshAgent(ctx, &store.MeshAgent{
		SessionID:   env.SenderPeerID,
		WorkspaceID: localWorkspaceID,
		Name:        env.SenderDisplayName,
		ClientType:  "p2p",
		Origin:      store.MeshAgentOriginPeerPrefix + env.SenderPeerID,
		LastSeenAt:  now,
		CreatedAt:   now,
	})
}

// displayAgentName returns the most user-friendly label for the sender of
// an inbound libp2p envelope: SenderDisplayName when set, "peer:<short>"
// fallback otherwise. NOT auth-bearing — the trust anchor is still the
// libp2p PeerID + envelope signature.
func displayAgentName(env p2p.MeshEnvelope) string {
	if env.SenderDisplayName != "" {
		return env.SenderDisplayName
	}
	return "peer:" + truncatePeerID(env.SenderPeerID)
}

// errPeerOffline marks an outbound-queue row that was parked by the liveness
// precheck (peer known-disconnected) rather than a failed dial, so the queued
// row's last_error distinguishes "never dialed, peer was down" from a real
// dial failure.
var errPeerOffline = errors.New("peer not connected (liveness precheck; queued without dialing)")

// peerConnectivityProbe is an OPTIONAL capability of a p2pTransport: a cheap,
// non-dialing "is this peer connected right now?" check. *p2p.MeshTransport
// implements it; a transport that doesn't is treated as "can't tell", so the
// dispatch precheck never changes behaviour without a positive offline signal.
type peerConnectivityProbe interface {
	IsConnected(peerID string) bool
}

// peerDisconnected reports whether the transport can POSITIVELY confirm the
// peer is not currently connected. Returns false ("proceed to dial") whenever
// the transport can't answer — no probe wired, slim build, or a fake — so the
// precheck stays inert unless there is a real offline signal to act on.
func peerDisconnected(t p2pTransport, peerID string) bool {
	probe, ok := t.(peerConnectivityProbe)
	if !ok {
		return false
	}
	return !probe.IsConnected(peerID)
}

// enqueueTargeted parks a targeted envelope in the offline-delivery queue.
// Precondition: m.outboundQueue != nil. Returns nil once the row is durable
// (it retries on the peer's next reconnect / the 30s sweep); wraps both errors
// when the enqueue itself fails so the caller still surfaces a hard failure.
func (m *Manager) enqueueTargeted(
	ctx context.Context, toPeer string, msg *store.MeshMessage,
	env *p2p.MeshEnvelope, cause error,
) error {
	audienceSession := ""
	if msg.Audience != "" && msg.Audience != "*" {
		audienceSession = msg.Audience
	}
	if qErr := m.outboundQueue.Enqueue(ctx, toPeer, audienceSession, env, cause); qErr != nil {
		return fmt.Errorf("dispatch + enqueue both failed: send=%v queue=%w", cause, qErr)
	}
	return nil
}

// dispatchP2P transmits a MeshMessage via libp2p when the SendRequest
// indicated cross-machine routing. Returns nil when no p2p send was needed.
//
// Failure semantics:
//   - Targeted to_peer send fails → enqueue into mesh_outbound_queue (when
//     an OutboundQueue is wired) and return nil. The message is durable
//     and will retry when the peer comes back online. Without a queue
//     wired we keep the historical behaviour (return the error).
//   - Broadcast (audience=*) failures are NOT queued — broadcasts are
//     fire-and-forget by contract.
func (m *Manager) dispatchP2P(ctx context.Context, req SendRequest, msg *store.MeshMessage) error {
	if m == nil || m.p2p == nil {
		return nil
	}
	if req.LocalOnly {
		return nil
	}
	if req.ToPeer == "" && req.Audience != "*" {
		// Targeted role/session — never send across the wire.
		return nil
	}
	env := &p2p.MeshEnvelope{
		ID:                msg.ID,
		SenderPeerID:      m.selfPeerID,
		SenderDisplayName: m.localDisplayName(),
		Kind:              msg.Kind,
		Content:           msg.Content,
		Priority:          msg.Priority, // receivers clamp unknown values to "normal"
		Tags:              msg.Tags,     // preserve sender tags for cross-peer tag_match triggers
		TS:                msg.CreatedAt.UnixMilli(),
		Recipient:         p2p.Recipient{Kind: "audience", Value: msg.Audience},
		Repo:              msg.Repo,
		Branch:            msg.Branch,
		WorkspacePath:     msg.WorkspacePath,
		RepoRemote:        msg.RepoRemote,
		WorkspaceID:       msg.WorkspaceID, // G1 — let receivers resolve workspace via binding
	}
	if req.ToPeer != "" {
		env.Recipient = p2p.Recipient{Kind: "peer", Value: req.ToPeer}
		// Liveness precheck (B2). SendToPeer dials with a 10s timeout, so a
		// targeted send to an OFFLINE peer blocks the caller's tool-call
		// goroutine for the full dial before falling through to the durable
		// outbound queue — a 10s stall that buys nothing, because the message
		// ends up queued either way. When the transport can cheaply confirm
		// the peer isn't connected (the same non-dialing gate SendBroadcast
		// uses) AND a queue is wired to catch it, skip the dial and park
		// immediately; the row drains on the peer's next reconnect / sweep.
		// Without the probe or the queue we keep the legacy dial-then-queue
		// path, so behaviour is unchanged unless we get a positive offline
		// signal.
		if m.outboundQueue != nil && peerDisconnected(m.p2p, req.ToPeer) {
			return m.enqueueTargeted(ctx, req.ToPeer, msg, env, errPeerOffline)
		}
		sendErr := m.p2p.SendToPeer(ctx, req.ToPeer, env)
		if sendErr == nil {
			return nil
		}
		if errors.Is(sendErr, p2p.ErrP2PNotBuiltIn) {
			return nil
		}
		// Try the offline-delivery queue before surfacing the error.
		if m.outboundQueue != nil {
			return m.enqueueTargeted(ctx, req.ToPeer, msg, env, sendErr)
		}
		return sendErr
	}
	// Broadcast (audience=*) is fire-and-forget by contract AND must never
	// block the caller's tool-call goroutine. SendBroadcast already skips
	// not-connected peers, but we additionally detach onto a background
	// context (request ctx may be cancelled the moment the tool call
	// returns) with a generous overall cap, so a peer that drops mid-flight
	// can't stall the task/mesh mutation that triggered this emission.
	go func() {
		bgCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		defer cancel()
		if _, err := m.p2p.SendBroadcast(bgCtx, env); err != nil &&
			!errors.Is(err, p2p.ErrP2PNotBuiltIn) {
			slog.Default().Warn("mesh: broadcast dispatch failed",
				"id", env.ID, "kind", env.Kind, "err", err)
		}
	}()
	return nil
}

// truncatePeerID returns a short, log-friendly tail of a libp2p peer ID.
func truncatePeerID(peerID string) string {
	if len(peerID) <= 10 {
		return peerID
	}
	return peerID[len(peerID)-10:]
}

// localDisplayName returns this device's user-facing name (Settings.DisplayName).
// Stamped on every outgoing libp2p envelope as a UX hint. Empty string when
// no provider is wired (single-host install).
func (m *Manager) localDisplayName() string {
	if m == nil || m.displayNameFn == nil {
		return ""
	}
	return m.displayNameFn()
}
