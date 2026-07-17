//go:build p2p

package p2p

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
)

// handleStream is the server side of /mcplexer/skill/1.0.0. It reads the
// first JSON message, decides if it's an offer (we cache it) or a request
// (we serve a bundle). Non-paired peers and missing scopes are rejected.
func (s *SkillShareService) handleStream(stream network.Stream) {
	defer func() { _ = stream.Close() }()

	remote := stream.Conn().RemotePeer().String()
	ctx := context.Background()

	if err := s.checkRemoteAllowed(ctx, remote); err != nil {
		s.logger.Info("skill stream rejected", "peer", remote, "error", err)
		s.recordAudit(ctx, "stream_rejected", remote, "", "denied", err.Error())
		_ = writeJSONLine(stream, skillShareError{
			Type: "error", Code: "denied", Message: ErrSkillShareDenied.Error(),
		})
		return
	}

	_ = stream.SetReadDeadline(time.Now().Add(skillShareReadDeadline))
	line, err := readLimitedLine(stream, maxShareControlLineBytes)
	if err != nil {
		s.logger.Debug("skill stream read header", "peer", remote, "error", err)
		return
	}
	s.dispatchInbound(ctx, stream, remote, line)
}

// dispatchInbound parses the first JSON line and dispatches to offer or
// request handlers. Unknown types are reported back over the stream.
func (s *SkillShareService) dispatchInbound(
	ctx context.Context, stream network.Stream, remote string, line []byte,
) {
	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(line, &head); err != nil {
		s.logger.Debug("skill stream parse", "peer", remote, "error", err)
		return
	}
	switch head.Type {
	case "offer":
		s.handleInboundOffer(ctx, remote, line)
	case "request":
		s.handleInboundRequest(ctx, stream, remote, line)
	default:
		_ = writeJSONLine(stream, skillShareError{
			Type: "error", Code: "bad_request",
			Message: "unknown message type: " + head.Type,
		})
	}
}

// handleInboundOffer caches the offer for later UI/agent review. Receiving
// an offer doesn't auto-install — the user/agent must call mesh__request_skill.
func (s *SkillShareService) handleInboundOffer(
	ctx context.Context, remote string, line []byte,
) {
	var off SkillOffer
	if err := json.Unmarshal(line, &off); err != nil {
		s.logger.Debug("skill offer parse", "peer", remote, "error", err)
		return
	}
	s.mu.Lock()
	s.offers[remote+"|"+off.Name] = off
	s.mu.Unlock()
	s.recordAudit(ctx, "offer_received", remote, off.Name, "pending", "")
	s.logger.Info("skill offer received",
		"peer", remote, "skill", off.Name, "version", off.Version,
		"signer", off.SignerPubkey, "size_bytes", off.SizeBytes)
}

// handleInboundRequest streams the bundle bytes back to the requester. The
// reply frame is: 4-byte BE length + sig bytes + 4-byte BE length + bundle
// bytes. The caller (readBundleResponse) decodes this in the reverse order.
func (s *SkillShareService) handleInboundRequest(
	ctx context.Context, stream network.Stream, remote string, line []byte,
) {
	var req SkillRequest
	if err := json.Unmarshal(line, &req); err != nil {
		_ = writeJSONLine(stream, skillShareError{
			Type: "error", Code: "bad_request", Message: err.Error(),
		})
		return
	}
	bundle, sig, err := s.provider.GetSkillBundle(ctx, req.Name, req.Version)
	if err != nil {
		s.recordAudit(ctx, "request_received", remote, req.Name, "error", err.Error())
		_ = writeJSONLine(stream, skillShareError{
			Type: "error", Code: "not_found", Message: err.Error(),
		})
		return
	}
	if int64(len(bundle)) > MaxSkillBundleBytes {
		_ = writeJSONLine(stream, skillShareError{
			Type: "error", Code: "too_large",
			Message: ErrSkillBundleTooLarge.Error(),
		})
		return
	}
	if err := writeBundleFrame(stream, sig, bundle); err != nil {
		s.logger.Warn("skill stream write", "peer", remote, "error", err)
		return
	}
	s.recordAudit(ctx, "request_served", remote, req.Name, "ok", "")
}

// checkRemoteAllowed enforces the two pre-conditions: the peer must be in
// the paired list (active, not revoked) AND the mesh.skill_request scope
// must be true on the peer's record.
func (s *SkillShareService) checkRemoteAllowed(ctx context.Context, peerID string) error {
	p, err := s.lookup.GetPairedPeer(ctx, peerID)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrPeerNotPaired, peerID)
	}
	if p.Revoked {
		return fmt.Errorf("%w: %s revoked", ErrPeerNotPaired, peerID)
	}
	if !hasScope(p.Scopes, skillShareScopeName) {
		return fmt.Errorf("%w: scope %s required", ErrSkillShareDenied, skillShareScopeName)
	}
	return nil
}

// hasScope returns true if name appears in scopes.
func hasScope(scopes []string, name string) bool {
	for _, s := range scopes {
		if s == name {
			return true
		}
	}
	return false
}

// writeJSONLine encodes v as JSON terminated by '\n'. Used for the small
// header messages (offer, request, error) — never for bundle bytes.
func writeJSONLine(w io.Writer, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	data = append(data, '\n')
	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	return nil
}

// writeBundleFrame writes len(sig)+sig+len(bundle)+bundle. We use 4-byte
// big-endian lengths so the receiver can pre-allocate a buffer and refuse
// oversize frames before reading.
func writeBundleFrame(w io.Writer, sig, bundle []byte) error {
	if err := writeChunk(w, sig); err != nil {
		return fmt.Errorf("write sig: %w", err)
	}
	if err := writeChunk(w, bundle); err != nil {
		return fmt.Errorf("write bundle: %w", err)
	}
	return nil
}

// writeChunk writes a 4-byte BE length-prefixed chunk.
func writeChunk(w io.Writer, b []byte) error {
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(b)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	if _, err := w.Write(b); err != nil {
		return err
	}
	return nil
}

// readBundleResponse parses the reply: it can be either an error JSON line
// (on failure) or sig+bundle frames (on success). We peek at the first byte
// — '{' means JSON error, anything else means binary frame.
func readBundleResponse(stream network.Stream) (*SkillOffer, []byte, []byte, error) {
	// Bound every read through this reader (the two length-prefixed chunks are
	// separately capped by readChunk, but the '{'-prefixed error path reads a
	// newline-terminated line that would otherwise be unbounded).
	br := bufio.NewReader(io.LimitReader(stream, shareLineCap(MaxSkillBundleBytes)))
	first, err := br.Peek(1)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("peek: %w", err)
	}
	if first[0] == '{' {
		return nil, nil, nil, decodeStreamError(br)
	}
	sig, err := readChunk(br, MaxSkillBundleBytes)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("read sig: %w", err)
	}
	bundle, err := readChunk(br, MaxSkillBundleBytes)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("read bundle: %w", err)
	}
	return &SkillOffer{SizeBytes: int64(len(bundle))}, bundle, sig, nil
}

// decodeStreamError parses a JSON skillShareError and returns it as a
// typed error. Used when the server replies with "error" instead of bytes.
func decodeStreamError(br *bufio.Reader) error {
	line, err := br.ReadBytes('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("read error reply: %w", err)
	}
	var e skillShareError
	if err := json.Unmarshal(line, &e); err != nil {
		return fmt.Errorf("decode error reply: %w", err)
	}
	switch e.Code {
	case "denied":
		return fmt.Errorf("%w: %s", ErrSkillShareDenied, e.Message)
	case "not_found":
		return fmt.Errorf("%w: %s", ErrSkillNotInstalled, e.Message)
	case "too_large":
		return fmt.Errorf("%w: %s", ErrSkillBundleTooLarge, e.Message)
	default:
		return fmt.Errorf("remote error: %s: %s", e.Code, e.Message)
	}
}

// readChunk reads a 4-byte BE length prefix followed by that many bytes.
// Returns ErrSkillBundleTooLarge when the announced length exceeds the cap.
func readChunk(br *bufio.Reader, max int64) ([]byte, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(br, hdr[:]); err != nil {
		return nil, err
	}
	n := int64(binary.BigEndian.Uint32(hdr[:]))
	if n > max {
		return nil, fmt.Errorf("%w: %d > %d", ErrSkillBundleTooLarge, n, max)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(br, buf); err != nil {
		return nil, err
	}
	return buf, nil
}
