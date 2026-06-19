package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/don-works/mcplexer/internal/mesh"
	"github.com/don-works/mcplexer/internal/p2p"
)

// handleMeshSkillHubIndex implements the gateway-side handler for
// mesh__skill_hub_index. Pulls the registry index from the specified
// hub peer and returns it as a structured tool result.
func (h *handler) handleMeshSkillHubIndex(
	ctx context.Context, args json.RawMessage,
) (json.RawMessage, *RPCError) {
	if h.registryShare == nil {
		return marshalErrorResult(
			"Registry hub sync is not enabled. Start the daemon with --p2p " +
				"(and build with -tags p2p) to enable mesh__skill_hub_index.",
		), nil
	}
	var req struct {
		PeerID string `json:"peer_id"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	v := newValidator()
	v.requireStringWithHint("peer_id", req.PeerID,
		"hub peer libp2p ID — call mesh__list_peers")
	if env, ok := v.envelope(); ok {
		return env, nil
	}
	peerID, resolveErr := h.resolveMeshPeer(ctx, req.PeerID)
	if resolveErr != nil {
		return marshalErrorResult(mesh.FormatPeerNotPairedError(req.PeerID, resolveErr)), nil
	}

	// RequestHubIndex only exists on the p2p build. In the stub build
	// the registryShare is always nil, so we never reach here.
	entries, err := h.registryShare.RequestHubIndex(ctx, peerID)
	if err != nil {
		return marshalErrorResult(fmt.Sprintf("Hub index failed: %v", err)), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Hub returned %d skill(s):\n\n", len(entries))
	for i, e := range entries {
		fmt.Fprintf(&b, "%d. %s@v%d  (hash:%s)  %s\n",
			i+1, e.Name, e.Version, truncate(e.ContentHash, 12),
			truncate(e.Description, 60))
	}
	return marshalToolResult(b.String()), nil
}

// compile-time check that p2p.HubIndexEntry is available
var _ p2p.HubIndexEntry

// handleMeshSkillHubSearch implements mesh__skill_hub_search. It asks a
// paired hub peer to run its local registry search and returns ranked
// metadata-only hits.
func (h *handler) handleMeshSkillHubSearch(
	ctx context.Context, args json.RawMessage,
) (json.RawMessage, *RPCError) {
	if h.registryShare == nil {
		return marshalErrorResult(
			"Registry hub sync is not enabled. Start the daemon with --p2p " +
				"(and build with -tags p2p) to enable mesh__skill_hub_search.",
		), nil
	}
	var req struct {
		PeerID string `json:"peer_id"`
		Query  string `json:"query"`
		Q      string `json:"q"`
		Limit  int    `json:"limit"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	if strings.TrimSpace(req.Query) == "" {
		req.Query = req.Q
	}
	v := newValidator()
	v.requireStringWithHint("peer_id", req.PeerID,
		"hub peer libp2p ID or paired peer name — call mesh__list_peers")
	v.requireStringWithHint("query", req.Query,
		"natural-language description of the skill you want from the hub")
	if env, ok := v.envelope(); ok {
		return env, nil
	}
	peerID, resolveErr := h.resolveMeshPeer(ctx, req.PeerID)
	if resolveErr != nil {
		return marshalErrorResult(mesh.FormatPeerNotPairedError(req.PeerID, resolveErr)), nil
	}
	hits, err := h.registryShare.RequestHubSearch(ctx, peerID, req.Query, req.Limit)
	if err != nil {
		return marshalErrorResult(fmt.Sprintf("Hub search failed: %v", err)), nil
	}
	if len(hits) == 0 {
		return marshalToolResult("Hub returned no matching skills."), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Hub returned %d match(es). Pull one with mesh__skill_hub_pull(name, version).\n\n", len(hits))
	for i, hit := range hits {
		scope := hit.Scope
		if scope == "" {
			scope = "global"
		}
		fmt.Fprintf(&b, "%d. %s@v%d  (%s · score %.3f · hash:%s)\n   %s\n",
			i+1, hit.Name, hit.Version, scope, hit.Score,
			truncate(hit.ContentHash, 12), truncate(hit.Description, 120))
	}
	return marshalToolResult(b.String()), nil
}

// compile-time check that p2p.HubSearchHit is available
var _ p2p.HubSearchHit

// handleMeshSkillHubPull implements the gateway-side handler for
// mesh__skill_hub_pull. Fetches a single skill entry from the hub peer
// and publishes it into the local registry.
func (h *handler) handleMeshSkillHubPull(
	ctx context.Context, args json.RawMessage,
) (json.RawMessage, *RPCError) {
	if h.registryShare == nil {
		return marshalErrorResult(
			"Registry hub sync is not enabled. Start the daemon with --p2p " +
				"(and build with -tags p2p) to enable mesh__skill_hub_pull.",
		), nil
	}
	var req struct {
		PeerID  string `json:"peer_id"`
		Name    string `json:"name"`
		Version int    `json:"version"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	v := newValidator()
	v.requireStringWithHint("peer_id", req.PeerID,
		"hub peer libp2p ID — call mesh__list_peers")
	v.requireStringWithHint("name", req.Name,
		"skill name from the hub index — call mesh__skill_hub_index first")
	if env, ok := v.envelope(); ok {
		return env, nil
	}
	peerID, resolveErr := h.resolveMeshPeer(ctx, req.PeerID)
	if resolveErr != nil {
		return marshalErrorResult(mesh.FormatPeerNotPairedError(req.PeerID, resolveErr)), nil
	}
	_, err := h.registryShare.RequestRegistrySkill(ctx, peerID, req.Name, req.Version)
	if err != nil {
		return marshalErrorResult(fmt.Sprintf("Hub pull failed: %v", err)), nil
	}
	return marshalToolResult(fmt.Sprintf(
		"Pulled skill %q from hub peer %s.", req.Name, shortPeerID(peerID),
	)), nil
}
