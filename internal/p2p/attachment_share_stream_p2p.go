//go:build p2p

package p2p

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
)

// handleAttachmentStream is the server side of /mcplexer/attachment/1.0.0.
// It reads one JSON request line and replies with either a payload line
// or an attachmentShareError line. Non-paired peers and peers missing the
// mesh.attachment_request scope are rejected at the door — the wire never
// distinguishes between them so a peer can't probe our pairing state.
func (s *AttachmentShareService) handleAttachmentStream(stream network.Stream) {
	defer func() { _ = stream.Close() }()

	remote := stream.Conn().RemotePeer().String()
	ctx := context.Background()

	if err := s.checkAttachmentRemoteAllowed(ctx, remote); err != nil {
		s.logger.Info("attachment stream rejected", "peer", remote, "error", err)
		s.recordAudit(ctx, "stream_rejected", remote, "", "denied", err.Error())
		_ = writeJSONLine(stream, attachmentShareError{
			Type: "error", Code: "denied", Message: ErrAttachmentShareDenied.Error(),
		})
		return
	}

	_ = stream.SetReadDeadline(time.Now().Add(attachmentShareReadDeadline))
	line, err := readLimitedLine(stream, maxShareControlLineBytes)
	if err != nil {
		s.logger.Debug("attachment stream read header", "peer", remote, "error", err)
		return
	}
	s.dispatchAttachmentInbound(ctx, stream, remote, line)
}

// dispatchAttachmentInbound parses the first JSON line and dispatches.
// Only "request" is honoured today — the cross-peer surface is pull-only;
// senders don't push offers because attachment lifetimes are bound to
// the task they hang off, not advertised independently.
func (s *AttachmentShareService) dispatchAttachmentInbound(
	ctx context.Context, stream network.Stream, remote string, line []byte,
) {
	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(line, &head); err != nil {
		s.logger.Debug("attachment stream parse", "peer", remote, "error", err)
		return
	}
	switch head.Type {
	case "request":
		s.handleAttachmentInboundRequest(ctx, stream, remote, line)
	default:
		_ = writeJSONLine(stream, attachmentShareError{
			Type: "error", Code: "bad_request",
			Message: "unknown message type: " + head.Type,
		})
	}
}

// handleAttachmentInboundRequest reads the request, looks up the
// attachment via the provider, and writes the payload back as one JSON
// line. On any failure we emit attachmentShareError instead — never
// leak provider-internal errors verbatim because they may carry path
// fragments from the data dir.
func (s *AttachmentShareService) handleAttachmentInboundRequest(
	ctx context.Context, stream network.Stream, remote string, line []byte,
) {
	if s.provider == nil {
		_ = writeJSONLine(stream, attachmentShareError{
			Type: "error", Code: "not_found",
			Message: "no provider configured",
		})
		return
	}
	var req AttachmentRequest
	if err := json.Unmarshal(line, &req); err != nil {
		_ = writeJSONLine(stream, attachmentShareError{
			Type: "error", Code: "bad_request", Message: err.Error(),
		})
		return
	}
	payload, err := s.provider.GetAttachmentPayload(ctx, req.ID, remote)
	if err != nil {
		code := "not_found"
		message := ErrAttachmentNotFound.Error()
		if !errors.Is(err, ErrAttachmentNotFound) {
			code = "internal"
			// NEVER echo the provider's error verbatim: it can carry data-dir
			// path fragments / usernames (read attachment blob: open
			// /Users/<user>/.mcplexer/...). Keep the detail in the local audit
			// row only and send a fixed generic message on the wire.
			message = "internal error"
		}
		s.recordAudit(ctx, "request_received", remote, req.ID, "error", err.Error())
		_ = writeJSONLine(stream, attachmentShareError{
			Type: "error", Code: code, Message: message,
		})
		return
	}
	if payload.SizeBytes > MaxAttachmentBytes {
		_ = writeJSONLine(stream, attachmentShareError{
			Type: "error", Code: "too_large",
			Message: ErrAttachmentTooLarge.Error(),
		})
		return
	}
	payload.Type = "attachment"
	_ = stream.SetWriteDeadline(time.Now().Add(attachmentShareReadDeadline))
	if err := writeJSONLine(stream, payload); err != nil {
		s.logger.Warn("attachment stream write",
			"peer", remote, "id", req.ID, "error", err)
		return
	}
	s.recordAudit(ctx, "request_served", remote, req.ID, "ok", "")
}

// checkAttachmentRemoteAllowed enforces pairing + scope grant on the
// receive side. Returns one error sentinel either way — the caller logs
// the wrapped reason but never reveals it on the wire.
func (s *AttachmentShareService) checkAttachmentRemoteAllowed(ctx context.Context, peerID string) error {
	if s.lookup == nil {
		return fmt.Errorf("%w: no lookup configured", ErrPeerNotPaired)
	}
	p, err := s.lookup.GetPairedPeer(ctx, peerID)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrPeerNotPaired, peerID)
	}
	if p.Revoked {
		return fmt.Errorf("%w: %s revoked", ErrPeerNotPaired, peerID)
	}
	if !hasScope(p.Scopes, attachmentShareScopeName) {
		return fmt.Errorf("%w: scope %s required",
			ErrAttachmentShareDenied, attachmentShareScopeName)
	}
	return nil
}

// readAttachmentPayload reads the one-line reply: either a payload JSON
// or an error JSON. We peek the type field to disambiguate (both shapes
// start with '{').
func readAttachmentPayload(stream network.Stream) (*AttachmentPayload, error) {
	_ = stream.SetReadDeadline(time.Now().Add(attachmentShareReadDeadline))
	line, err := readLimitedLine(stream, shareLineCap(MaxAttachmentBytes))
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("read reply: %w", err)
	}
	if len(line) == 0 {
		return nil, errors.New("read reply: empty response")
	}
	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(line, &head); err != nil {
		return nil, fmt.Errorf("decode reply head: %w", err)
	}
	switch head.Type {
	case "attachment":
		var payload AttachmentPayload
		if err := json.Unmarshal(line, &payload); err != nil {
			return nil, fmt.Errorf("decode payload: %w", err)
		}
		return &payload, nil
	case "error":
		return nil, decodeAttachmentStreamError(line)
	default:
		return nil, fmt.Errorf("unknown reply type %q", head.Type)
	}
}

// decodeAttachmentStreamError translates a wire-level error reply into
// a typed sentinel so the gateway can branch with errors.Is.
func decodeAttachmentStreamError(line []byte) error {
	var e attachmentShareError
	if err := json.Unmarshal(line, &e); err != nil {
		return fmt.Errorf("decode error reply: %w", err)
	}
	switch e.Code {
	case "denied":
		return fmt.Errorf("%w: %s", ErrAttachmentShareDenied, e.Message)
	case "not_found":
		return fmt.Errorf("%w: %s", ErrAttachmentNotFound, e.Message)
	case "too_large":
		return fmt.Errorf("%w: %s", ErrAttachmentTooLarge, e.Message)
	default:
		return fmt.Errorf("remote error: %s: %s", e.Code, e.Message)
	}
}
