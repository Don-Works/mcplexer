package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/don-works/mcplexer/internal/idtrunc"
	"github.com/don-works/mcplexer/internal/mesh"
	"github.com/don-works/mcplexer/internal/p2p"
	"github.com/don-works/mcplexer/internal/skills"
)

// handleMeshOfferSkill is the gateway-side handler for mesh__offer_skill.
// Validates args, then delegates to the SkillShareService. Errors are
// returned as user-friendly tool result text (not JSON-RPC errors) so the
// agent can recover without aborting the entire turn.
func (h *handler) handleMeshOfferSkill(
	ctx context.Context, args json.RawMessage,
) (json.RawMessage, *RPCError) {
	if h.skillShare == nil {
		return marshalErrorResult(
			"Skill share is not enabled. Start the daemon with --p2p (and " +
				"build with -tags p2p) to enable mesh__offer_skill.",
		), nil
	}
	var req struct {
		PeerID    string `json:"peer_id"`
		SkillName string `json:"skill_name"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	v := newValidator()
	v.requireStringWithHint("peer_id", req.PeerID,
		"peer libp2p ID — call mesh__list_peers")
	v.requireStringWithHint("skill_name", req.SkillName,
		"skill name as installed locally — call mcpx__skill_list")
	if env, ok := v.envelope(); ok {
		return env, nil
	}
	peerID, resolveErr := h.resolveMeshPeer(ctx, req.PeerID)
	if resolveErr != nil {
		return marshalErrorResult(mesh.FormatPeerNotPairedError(req.PeerID, resolveErr)), nil
	}
	err := h.skillShare.OfferSkill(ctx, peerID, req.SkillName)
	if h.mesh != nil {
		meta := h.sessionMeshMeta(ctx)
		auditStatus := "success"
		errMsg := ""
		if err != nil {
			auditStatus = "error"
			errMsg = err.Error()
		}
		h.mesh.RecordSkillOffer(ctx, meta, peerID, req.SkillName, "", auditStatus, errMsg)
	}
	if err != nil {
		return marshalErrorResult(formatSkillShareError(err)), nil
	}
	return marshalToolResult(fmt.Sprintf(
		"Offered skill %q to peer %s — they will receive a SkillOffer notification.",
		req.SkillName, shortPeerID(peerID),
	)), nil
}

// handleMeshRequestSkill is the gateway-side handler for mesh__request_skill.
// Blocks until install completes (success) or the user/agent declines (the
// downstream SkillReceiver returns an error). The blocking semantics are
// what the spec asks for — agents `await` naturally.
func (h *handler) handleMeshRequestSkill(
	ctx context.Context, args json.RawMessage,
) (json.RawMessage, *RPCError) {
	if h.skillShare == nil {
		return marshalErrorResult(
			"Skill share is not enabled. Start the daemon with --p2p (and " +
				"build with -tags p2p) to enable mesh__request_skill.",
		), nil
	}
	var req struct {
		PeerID    string `json:"peer_id"`
		SkillName string `json:"skill_name"`
		Version   string `json:"version"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
	}
	v := newValidator()
	v.requireStringWithHint("peer_id", req.PeerID,
		"peer libp2p ID — call mesh__list_peers")
	v.requireStringWithHint("skill_name", req.SkillName,
		"skill name as registered on the remote peer")
	if env, ok := v.envelope(); ok {
		return env, nil
	}
	peerID, resolveErr := h.resolveMeshPeer(ctx, req.PeerID)
	if resolveErr != nil {
		return marshalErrorResult(mesh.FormatPeerNotPairedError(req.PeerID, resolveErr)), nil
	}
	offer, err := h.skillShare.RequestSkill(ctx, peerID, req.SkillName, req.Version)
	if h.mesh != nil {
		meta := h.sessionMeshMeta(ctx)
		auditStatus := "success"
		errMsg := ""
		version := req.Version
		if err != nil {
			auditStatus = "error"
			errMsg = err.Error()
		} else if offer != nil {
			version = offer.Version
		}
		h.mesh.RecordRequestSkill(ctx, meta, peerID, req.SkillName, version, auditStatus, errMsg)
	}
	if err != nil {
		return marshalErrorResult(formatSkillShareError(err)), nil
	}
	return marshalToolResult(fmt.Sprintf(
		"Installed skill %q (version %q, %d bytes) from peer %s. The capability "+
			"review passed and the bundle was signature-verified.",
		req.SkillName, offer.Version, offer.SizeBytes, shortPeerID(peerID),
	)), nil
}

// formatSkillShareError translates the typed sentinel errors from the
// SkillShareService into agent-friendly explanations. Anything else falls
// through to the raw error string.
func formatSkillShareError(err error) string {
	switch {
	case errors.Is(err, p2p.ErrP2PNotBuiltIn):
		return "Skill share requires the p2p build tag. Rebuild mcplexer with `-tags p2p` and enable --p2p in the daemon."
	case errors.Is(err, p2p.ErrPeerNotPaired):
		return "That peer is not in the paired-peers list. Pair via the desktop UI or `mcplexer p2p pair`."
	case errors.Is(err, p2p.ErrSkillShareDenied):
		return "The peer refused the skill request. Check that the mesh.skill_request scope is granted on both sides (for installed .mcskill share over /mcplexer/skill/1.0.0; use mesh.registry_request for registry entries)."
	case errors.Is(err, p2p.ErrSkillNotInstalled):
		return "That peer does not have the requested skill installed."
	case errors.Is(err, skills.ErrBundleCacheMissing):
		return "That skill is installed but cannot be shared — re-install it to enable mesh sharing."
	case errors.Is(err, p2p.ErrSkillBundleTooLarge):
		return fmt.Sprintf("The skill bundle exceeds the %d byte cap.", p2p.MaxSkillBundleBytes)
	default:
		return fmt.Sprintf("Skill share failed: %v", err)
	}
}

// shortPeerID truncates a libp2p peer ID for log/UI display. Keeps the last
// 12 chars so the unique suffix is visible without filling the screen.
func shortPeerID(id string) string {
	return idtrunc.Ellipsis(id, 6, 8)
}
