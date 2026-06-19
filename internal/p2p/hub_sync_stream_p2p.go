//go:build p2p

package p2p

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
)

// handleInboundIndexRequest serves the hub index to a requesting peer.
// The response is a single JSON line containing HubIndexResponse.
func (s *RegistryShareService) handleInboundIndexRequest(
	ctx context.Context, stream network.Stream, remote string,
) {
	if s.indexProvider == nil {
		_ = writeJSONLine(stream, skillShareError{
			Type: "error", Code: "not_implemented",
			Message: "hub index is not configured on this peer",
		})
		return
	}
	entries, err := s.indexProvider.ListIndexEntries(ctx)
	if err != nil {
		s.recordAudit(ctx, "hub_index", remote, "", "error", err.Error())
		_ = writeJSONLine(stream, skillShareError{
			Type: "error", Code: "internal_error", Message: err.Error(),
		})
		return
	}
	resp := HubIndexResponse{Entries: entries}
	data, err := json.Marshal(resp)
	if err != nil {
		s.recordAudit(ctx, "hub_index", remote, "", "error", err.Error())
		return
	}
	data = append(data, '\n')
	_ = stream.SetWriteDeadline(time.Now().Add(skillShareReadDeadline))
	if _, err := stream.Write(data); err != nil {
		s.logger.Warn("hub index write", "peer", remote, "error", err)
		return
	}
	s.recordAudit(ctx, "hub_index", remote, "", "ok",
		fmt.Sprintf("entries=%d", len(entries)))
}

// handleInboundSearchRequest serves ranked hub search results to a
// requesting peer. The response is a single JSON line containing
// HubSearchResponse and never includes skill body or bundle bytes.
func (s *RegistryShareService) handleInboundSearchRequest(
	ctx context.Context, stream network.Stream, remote string, line []byte,
) {
	if s.searchProvider == nil {
		_ = writeJSONLine(stream, skillShareError{
			Type: "error", Code: "not_implemented",
			Message: "hub search is not configured on this peer",
		})
		return
	}
	var req HubSearchRequest
	if err := json.Unmarshal(line, &req); err != nil {
		_ = writeJSONLine(stream, skillShareError{
			Type: "error", Code: "bad_request", Message: err.Error(),
		})
		return
	}
	if req.Q == "" {
		_ = writeJSONLine(stream, skillShareError{
			Type: "error", Code: "bad_request", Message: "q is required",
		})
		return
	}
	limit := req.Limit
	if limit <= 0 {
		limit = 5
	}
	if limit > 20 {
		limit = 20
	}
	hits, err := s.searchProvider.SearchIndexEntries(ctx, req.Q, limit)
	if err != nil {
		s.recordAudit(ctx, "hub_search", remote, "", "error", err.Error())
		_ = writeJSONLine(stream, skillShareError{
			Type: "error", Code: "internal_error", Message: err.Error(),
		})
		return
	}
	resp := HubSearchResponse{Hits: hits}
	data, err := json.Marshal(resp)
	if err != nil {
		s.recordAudit(ctx, "hub_search", remote, "", "error", err.Error())
		return
	}
	data = append(data, '\n')
	_ = stream.SetWriteDeadline(time.Now().Add(skillShareReadDeadline))
	if _, err := stream.Write(data); err != nil {
		s.logger.Warn("hub search write", "peer", remote, "error", err)
		return
	}
	s.recordAudit(ctx, "hub_search", remote, "", "ok",
		fmt.Sprintf("q=%s hits=%d", req.Q, len(hits)))
}

// readIndexResponse parses the hub index reply from a stream. The
// response is either a JSON error line (starts with '{' and has
// type:"error") or a JSON line with HubIndexResponse.
func readIndexResponse(br *bufio.Reader) ([]HubIndexEntry, error) {
	line, err := br.ReadBytes('\n')
	if err != nil {
		return nil, fmt.Errorf("read index response: %w", err)
	}
	var peek struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(line, &peek); err != nil {
		return nil, fmt.Errorf("parse index response: %w", err)
	}
	if peek.Type == "error" {
		var e skillShareError
		if err := json.Unmarshal(line, &e); err != nil {
			return nil, fmt.Errorf("parse error response: %w", err)
		}
		return nil, fmt.Errorf("remote error: %s: %s", e.Code, e.Message)
	}
	var resp HubIndexResponse
	if err := json.Unmarshal(line, &resp); err != nil {
		return nil, fmt.Errorf("parse index entries: %w", err)
	}
	return resp.Entries, nil
}

// readSearchResponse parses the hub search reply from a stream. The
// response is either a JSON error line or a HubSearchResponse line.
func readSearchResponse(br *bufio.Reader) ([]HubSearchHit, error) {
	line, err := br.ReadBytes('\n')
	if err != nil {
		return nil, fmt.Errorf("read search response: %w", err)
	}
	var peek struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(line, &peek); err != nil {
		return nil, fmt.Errorf("parse search response: %w", err)
	}
	if peek.Type == "error" {
		var e skillShareError
		if err := json.Unmarshal(line, &e); err != nil {
			return nil, fmt.Errorf("parse error response: %w", err)
		}
		return nil, fmt.Errorf("remote error: %s: %s", e.Code, e.Message)
	}
	var resp HubSearchResponse
	if err := json.Unmarshal(line, &resp); err != nil {
		return nil, fmt.Errorf("parse search hits: %w", err)
	}
	return resp.Hits, nil
}

// RequestHubIndex dials peerID, sends an index request, and returns the
// hub's skill entries.
func (s *RegistryShareService) RequestHubIndex(
	ctx context.Context, peerID string,
) ([]HubIndexEntry, error) {
	if s.host == nil {
		return nil, ErrHubSyncNotImplemented
	}
	pid, err := peer.Decode(peerID)
	if err != nil {
		return nil, fmt.Errorf("decode peer id: %w", err)
	}
	stream, err := s.host.Inner().NewStream(ctx, pid, RegistryShareProtocol)
	if err != nil {
		s.recordAudit(ctx, "hub_index_dial", peerID, "", "error", err.Error())
		return nil, fmt.Errorf("open stream: %w", err)
	}
	defer func() { _ = stream.Close() }()

	req := HubIndexRequest{Type: "index"}
	if err := writeJSONLine(stream, req); err != nil {
		return nil, fmt.Errorf("write index request: %w", err)
	}
	_ = stream.SetReadDeadline(time.Now().Add(skillShareReadDeadline))
	br := bufio.NewReader(stream)
	entries, err := readIndexResponse(br)
	if err != nil {
		s.recordAudit(ctx, "hub_index_read", peerID, "", "error", err.Error())
		return nil, err
	}
	s.recordAudit(ctx, "hub_index_ok", peerID, "", "ok",
		fmt.Sprintf("entries=%d", len(entries)))
	return entries, nil
}

// RequestHubSearch dials peerID, sends a search request, and returns
// ranked metadata-only matches from the hub's registry.
func (s *RegistryShareService) RequestHubSearch(
	ctx context.Context, peerID, q string, limit int,
) ([]HubSearchHit, error) {
	if s.host == nil {
		return nil, ErrHubSyncNotImplemented
	}
	if q == "" {
		return nil, fmt.Errorf("q is required")
	}
	pid, err := peer.Decode(peerID)
	if err != nil {
		return nil, fmt.Errorf("decode peer id: %w", err)
	}
	stream, err := s.host.Inner().NewStream(ctx, pid, RegistryShareProtocol)
	if err != nil {
		s.recordAudit(ctx, "hub_search_dial", peerID, "", "error", err.Error())
		return nil, fmt.Errorf("open stream: %w", err)
	}
	defer func() { _ = stream.Close() }()

	req := HubSearchRequest{Type: "search", Q: q, Limit: limit}
	if err := writeJSONLine(stream, req); err != nil {
		return nil, fmt.Errorf("write search request: %w", err)
	}
	_ = stream.SetReadDeadline(time.Now().Add(skillShareReadDeadline))
	br := bufio.NewReader(stream)
	hits, err := readSearchResponse(br)
	if err != nil {
		s.recordAudit(ctx, "hub_search_read", peerID, "", "error", err.Error())
		return nil, err
	}
	s.recordAudit(ctx, "hub_search_ok", peerID, "", "ok",
		fmt.Sprintf("q=%s hits=%d", q, len(hits)))
	return hits, nil
}
