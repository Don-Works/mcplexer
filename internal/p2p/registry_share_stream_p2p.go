//go:build p2p

package p2p

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
)

// handleStream is the responder side of /mcplexer/skill-registry/1.0.0.
// Reads one RegistryRequest, looks the entry up via the provider, and
// writes back either an error JSON line or framed (body, bundle).
func (s *RegistryShareService) handleStream(stream network.Stream) {
	defer func() { _ = stream.Close() }()
	remote := stream.Conn().RemotePeer().String()
	ctx := context.Background()

	if err := s.checkRemoteAllowed(ctx, remote); err != nil {
		s.logger.Info("registry stream rejected", "peer", remote, "error", err)
		s.recordAudit(ctx, "registry_stream_rejected", remote, "", "denied", err.Error())
		_ = writeJSONLine(stream, skillShareError{
			Type: "error", Code: "denied", Message: ErrSkillShareDenied.Error(),
		})
		return
	}

	_ = stream.SetReadDeadline(time.Now().Add(skillShareReadDeadline))
	br := bufio.NewReader(stream)
	line, err := br.ReadBytes('\n')
	if err != nil {
		s.logger.Debug("registry stream read header", "peer", remote, "error", err)
		return
	}
	var head struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(line, &head); err != nil {
		_ = writeJSONLine(stream, skillShareError{
			Type: "error", Code: "bad_request", Message: err.Error(),
		})
		return
	}
	switch head.Type {
	case "request":
		s.handleInboundRegistryRequest(ctx, stream, remote, line)
	case "index":
		s.handleInboundIndexRequest(ctx, stream, remote)
	case "search":
		s.handleInboundSearchRequest(ctx, stream, remote, line)
	default:
		_ = writeJSONLine(stream, skillShareError{
			Type: "error", Code: "bad_request",
			Message: "registry protocol accepts request, index, and search frames; got " + head.Type,
		})
	}
}

// handleInboundRegistryRequest fetches the body + bundle from the
// provider and streams them back. Errors are returned as JSON lines.
func (s *RegistryShareService) handleInboundRegistryRequest(
	ctx context.Context, stream network.Stream, remote string, line []byte,
) {
	var req RegistryRequest
	if err := json.Unmarshal(line, &req); err != nil {
		_ = writeJSONLine(stream, skillShareError{
			Type: "error", Code: "bad_request", Message: err.Error(),
		})
		return
	}
	body, bundle, _, err := s.provider.GetRegistryEntry(ctx, req.Name, req.Version)
	if err != nil {
		s.recordAudit(ctx, "registry_serve", remote, req.Name, "error", err.Error())
		code := "not_found"
		if !errors.Is(err, ErrRegistryEntryNotFound) {
			code = "internal_error"
		}
		_ = writeJSONLine(stream, skillShareError{
			Type: "error", Code: code, Message: err.Error(),
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
	if err := writeChunk(stream, []byte(body)); err != nil {
		s.logger.Warn("registry write body", "peer", remote, "error", err)
		return
	}
	if err := writeChunk(stream, bundle); err != nil {
		s.logger.Warn("registry write bundle", "peer", remote, "error", err)
		return
	}
	s.recordAudit(ctx, "registry_serve", remote, req.Name, "ok",
		fmt.Sprintf("body=%dB bundle=%dB", len(body), len(bundle)))
}

// checkRemoteAllowed mirrors the skill-share gate but uses the
// registry-specific scope so the two surfaces can be granted
// independently.
func (s *RegistryShareService) checkRemoteAllowed(ctx context.Context, peerID string) error {
	p, err := s.lookup.GetPairedPeer(ctx, peerID)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrPeerNotPaired, peerID)
	}
	if p.Revoked {
		return fmt.Errorf("%w: %s revoked", ErrPeerNotPaired, peerID)
	}
	if !hasScope(p.Scopes, registryShareScopeName) {
		return fmt.Errorf("%w: scope %s required", ErrSkillShareDenied, registryShareScopeName)
	}
	return nil
}
