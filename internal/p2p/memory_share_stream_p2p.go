//go:build p2p

package p2p

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
)

// handleMemoryStream is the server side of /mcplexer/memory/1.0.0. It
// reads the first JSON line and dispatches to offer or request handler.
// Non-paired peers and missing scopes are rejected.
//
// Security posture on rejection: the wire response is a CONSTANT-shape
// `memoryShareError{Code:"denied", Denial:{Code:"no_scope", Peer:<them>}}`
// envelope. We deliberately strip the scope string + the underlying
// error message so a Tier-2/3 peer can't distinguish "not paired" from
// "scope mesh.memory_request not granted" from "memory not found" from
// "memory exists but in a workspace you're not granted". The local
// audit row still captures the full error text — only the wire reply
// is constant. See newDenyError + memoryShareError doc.
func (s *MemoryShareService) handleMemoryStream(stream network.Stream) {
	defer func() { _ = stream.Close() }()

	remote := stream.Conn().RemotePeer().String()
	ctx := context.Background()

	peerScopes, err := s.checkMemoryRemoteAllowed(ctx, remote)
	if err != nil {
		s.logger.Info("memory stream rejected", "peer", remote, "error", err)
		s.recordAudit(ctx, "stream_rejected", remote, "", "denied", err.Error())
		_ = writeJSONLine(stream, newDenyError(remote))
		return
	}

	_ = stream.SetReadDeadline(time.Now().Add(memoryShareReadDeadline))
	br := bufio.NewReader(stream)
	line, err := br.ReadBytes('\n')
	if err != nil {
		s.logger.Debug("memory stream read header", "peer", remote, "error", err)
		return
	}
	s.dispatchMemoryInbound(ctx, stream, remote, peerScopes, line)
}

func (s *MemoryShareService) dispatchMemoryInbound(
	ctx context.Context, stream network.Stream, remote string,
	peerScopes []string, line []byte,
) {
	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(line, &head); err != nil {
		s.logger.Debug("memory stream parse", "peer", remote, "error", err)
		return
	}
	switch head.Type {
	case "offer":
		s.handleMemoryInboundOffer(ctx, remote, line)
	case "request":
		s.handleMemoryInboundRequest(ctx, stream, remote, peerScopes, line)
	default:
		// Malformed-envelope replies are still narrative — the sender
		// needs the type name they sent so they can debug. This branch
		// is fundamentally non-secret (we're echoing back the bytes
		// they sent us), so the constant-shape rule doesn't apply.
		_ = writeJSONLine(stream, memoryShareError{
			Type: "error", Code: "bad_request",
			Message: "unknown message type: " + head.Type,
		})
	}
}

// handleMemoryInboundOffer caches the offer in-memory + stores it
// via the recorder (if configured). Receiving an offer doesn't
// auto-import — the user/agent must call mesh__request_memory.
func (s *MemoryShareService) handleMemoryInboundOffer(
	ctx context.Context, remote string, line []byte,
) {
	var off MemoryOffer
	if err := json.Unmarshal(line, &off); err != nil {
		s.logger.Debug("memory offer parse", "peer", remote, "error", err)
		return
	}
	s.mu.Lock()
	s.offers[remote+"|"+off.RemoteID] = off
	s.mu.Unlock()
	if s.recorder != nil {
		// Best-effort persist — failure to record never blocks the
		// wire ack. The in-memory cache is the safety net.
		if err := s.recorder.RecordOffer(ctx, remote, "", &off); err != nil {
			s.logger.Warn("memory offer record",
				"peer", remote, "remote_id", off.RemoteID, "error", err)
		}
	}
	s.recordAudit(ctx, "offer_received", remote, off.RemoteID, "pending", "")
	s.logger.Info("memory offer received",
		"peer", remote, "remote_id", off.RemoteID,
		"name", off.Name, "kind", off.Kind, "size_bytes", off.SizeBytes)
	// Tier-1 silent replication: if an auto-puller is wired and it admits
	// this offer (SameUser tier, peer not opted out, not already imported)
	// pull the full payload in the background so the memory lands silently.
	s.maybeAutoPull(remote, off)
}

// maybeAutoPull fires a background RequestMemory for an offer the
// auto-pull policy hook admits. No-op when no puller is configured, no
// receiver is wired (we couldn't import the payload anyway), or the hook
// declines. Idempotent: a per-(peer, remote) inflight guard prevents a
// re-offer from spawning a second concurrent pull. Async + non-blocking:
// the wire offer-ack is never delayed by the pull round-trip. All logging
// is at debug — silent replication shouldn't spam info logs.
func (s *MemoryShareService) maybeAutoPull(peerID string, offer MemoryOffer) {
	// Snapshot the setter-mutated autoPuller under the lock — SetAutoPuller
	// writes it under s.mu, so an unlocked read here is a data race. receiver
	// + pullFn are constructor-set and never reassigned, so reading them
	// unlocked is safe. Capture puller in a local and use it throughout
	// (incl. the goroutine's OnAutoPulled) so a concurrent SetAutoPuller
	// can't swap it mid-pull.
	s.mu.Lock()
	puller := s.autoPuller
	s.mu.Unlock()
	if puller == nil || s.receiver == nil {
		return
	}
	ctx := context.Background()
	if !puller.ShouldAutoPull(ctx, peerID, &offer) {
		return
	}
	key := peerID + "|" + offer.RemoteID
	s.mu.Lock()
	if _, busy := s.inflight[key]; busy {
		s.mu.Unlock()
		s.logger.Debug("memory auto-pull skipped: already in flight",
			"peer", peerID, "remote_id", offer.RemoteID)
		return
	}
	s.inflight[key] = struct{}{}
	s.mu.Unlock()

	// Acquire a concurrency slot WITHOUT blocking the offer-receive path. A
	// full channel means too many pulls are already in flight: drop this one
	// (release the inflight guard so a later re-offer can retry) rather than
	// stack up goroutines/streams under an offer flood.
	select {
	case s.autoPullSem <- struct{}{}:
	default:
		s.mu.Lock()
		delete(s.inflight, key)
		s.mu.Unlock()
		s.logger.Debug("memory auto-pull skipped: concurrency cap reached",
			"peer", peerID, "remote_id", offer.RemoteID, "cap", autoPullMaxConcurrent)
		return
	}

	go func() {
		defer func() {
			<-s.autoPullSem
			s.mu.Lock()
			delete(s.inflight, key)
			s.mu.Unlock()
		}()
		s.logger.Debug("memory auto-pull starting",
			"peer", peerID, "remote_id", offer.RemoteID, "name", offer.Name)
		localID, err := s.pullFn(ctx, peerID, offer.RemoteID)
		if err != nil {
			s.logger.Debug("memory auto-pull failed",
				"peer", peerID, "remote_id", offer.RemoteID, "error", err)
			return
		}
		puller.OnAutoPulled(ctx, peerID, offer.RemoteID, localID)
		s.logger.Debug("memory auto-pull landed",
			"peer", peerID, "remote_id", offer.RemoteID, "local_id", localID)
	}()
}

// handleMemoryInboundRequest writes the payload back to the requester.
// One JSON line containing the full MemoryPayload, or a constant-shape
// memoryShareError deny envelope.
//
// Every failure mode — no provider, lookup error, memory not found,
// memory in an un-granted workspace, payload too large — funnels through
// `newDenyError(remote)`. The wire reply is byte-identical across these
// causes; the local audit row captures the real cause for forensics.
// This is the load-bearing constant-shape posture: a Tier-2/3 peer
// MUST NOT be able to side-channel-infer "this memory exists but I'm
// not allowed to see it" vs "this memory doesn't exist".
func (s *MemoryShareService) handleMemoryInboundRequest(
	ctx context.Context, stream network.Stream, remote string,
	peerScopes []string, line []byte,
) {
	// Provider-missing is the only case where we DON'T audit per-request:
	// the daemon is structurally not configured to serve, not a per-peer
	// scope failure. Still answer with the constant-shape envelope so an
	// attacker can't probe "is this daemon a memory provider?".
	if s.provider == nil {
		_ = writeJSONLine(stream, newDenyError(remote))
		return
	}
	var req MemoryRequest
	if err := json.Unmarshal(line, &req); err != nil {
		// Malformed-request reply IS narrative — the sender needs the
		// parse error to debug their own marshalling. Not a leak (we're
		// echoing what they sent, no resource lookup happened yet).
		_ = writeJSONLine(stream, memoryShareError{
			Type: "error", Code: "bad_request", Message: err.Error(),
		})
		return
	}
	payload, err := s.provider.GetMemoryPayload(ctx, remote, req.RemoteID, peerScopes)
	if err != nil {
		// Every error path — including ErrMemoryNotFound, ErrMemoryShareDenied,
		// and any internal SQL error — collapses to the constant deny
		// shape on the wire. The audit row preserves the original
		// err.Error() text so the local operator can debug.
		s.recordAudit(ctx, "request_received", remote, req.RemoteID, "error", err.Error())
		_ = writeJSONLine(stream, newDenyError(remote))
		return
	}
	if int64(len(payload.Content)) > MaxMemoryBytes {
		// Size-cap rejections are NOT a side-channel — the cap is a
		// well-known constant, not a function of the memory's existence
		// or scope. Even so, we map it through the deny envelope to keep
		// the surface uniform; the audit row distinguishes.
		s.recordAudit(ctx, "request_received", remote, req.RemoteID,
			"error", ErrMemoryTooLarge.Error())
		_ = writeJSONLine(stream, newDenyError(remote))
		return
	}
	payload.Type = "memory"
	payload.RemoteID = req.RemoteID
	_ = stream.SetWriteDeadline(time.Now().Add(memoryShareReadDeadline))
	if err := writeJSONLine(stream, payload); err != nil {
		s.logger.Warn("memory stream write",
			"peer", remote, "remote_id", req.RemoteID, "error", err)
		return
	}
	s.recordAudit(ctx, "request_served", remote, req.RemoteID, "ok", "")
}

// checkMemoryRemoteAllowed enforces pairing + the coarse boolean
// `mesh.memory_request` scope grant. Returns the peer's full grant slice
// on success so per-request handlers can do the finer-grained
// per-workspace scope check at the provider layer (SQL-side).
//
// On failure returns the underlying err — the caller (handleMemoryStream)
// audits the err.Error() text but the wire reply is the constant deny
// envelope, NOT the err message. Never propagate the returned error to
// the network response.
func (s *MemoryShareService) checkMemoryRemoteAllowed(
	ctx context.Context, peerID string,
) ([]string, error) {
	if s.lookup == nil {
		return nil, fmt.Errorf("%w: no lookup configured", ErrPeerNotPaired)
	}
	p, err := s.lookup.GetPairedPeer(ctx, peerID)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrPeerNotPaired, peerID)
	}
	if p.Revoked {
		return nil, fmt.Errorf("%w: %s revoked", ErrPeerNotPaired, peerID)
	}
	if !hasScope(p.Scopes, memoryShareScopeName) {
		return nil, fmt.Errorf("%w: scope %s required",
			ErrMemoryShareDenied, memoryShareScopeName)
	}
	return append([]string(nil), p.Scopes...), nil
}

// readMemoryPayload reads the one-line response: either a JSON
// memoryShareError (on failure) or a JSON MemoryPayload (on success).
// We peek at the first character to disambiguate — `{` is JSON either
// way, so we must parse + check the type field.
func readMemoryPayload(stream network.Stream) (*MemoryPayload, error) {
	br := bufio.NewReader(stream)
	_ = stream.SetReadDeadline(time.Now().Add(memoryShareReadDeadline))
	line, err := br.ReadBytes('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("read reply: %w", err)
	}
	if len(line) == 0 {
		return nil, errors.New("read reply: empty response")
	}
	// Peek at the type field to know whether to decode as payload or
	// error. We parse once into the union by type-tagged sniff.
	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(line, &head); err != nil {
		return nil, fmt.Errorf("decode reply head: %w", err)
	}
	switch head.Type {
	case "memory":
		var payload MemoryPayload
		if err := json.Unmarshal(line, &payload); err != nil {
			return nil, fmt.Errorf("decode payload: %w", err)
		}
		return &payload, nil
	case "error":
		return nil, decodeMemoryStreamError(line)
	default:
		return nil, fmt.Errorf("unknown reply type %q", head.Type)
	}
}

// decodeMemoryStreamError parses the wire deny envelope. Post-JTAC65 the
// receive side ALWAYS sees Code="denied" for any peer-facing rejection
// (the legacy "not_found" / "too_large" cases were collapsed to "denied"
// to close the side-channel — see memoryShareError doc). The local audit
// row on the sending daemon still distinguishes the original cause; over
// the wire the requester gets a uniform ErrMemoryShareDenied.
//
// The "bad_request" branch is preserved because it's not a peer-data
// disclosure — it echoes the requester's own malformed bytes back to
// them so they can debug their marshalling.
func decodeMemoryStreamError(line []byte) error {
	var e memoryShareError
	if err := json.Unmarshal(line, &e); err != nil {
		return fmt.Errorf("decode error reply: %w", err)
	}
	switch e.Code {
	case "denied":
		return ErrMemoryShareDenied
	case "bad_request":
		return fmt.Errorf("remote rejected as bad_request: %s", e.Message)
	default:
		// Forward-compat: a future protocol bump might add codes. Don't
		// blow up; surface a generic denied so the caller treats it as
		// "no payload coming".
		return ErrMemoryShareDenied
	}
}
