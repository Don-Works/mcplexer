package gateway

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/don-works/mcplexer/internal/mesh"
	"github.com/don-works/mcplexer/internal/skillregistry"
	"github.com/don-works/mcplexer/internal/store"
)

// mesh__push_skill — p2p registry-skill PUSH (outbox/inbox + accept/reject).
//
// Four tools, mirroring mesh__send_secret but for registry skills:
//   mesh__push_skill          — sender: ship a metadata-only offer to a peer.
//   mesh__list_pending_skills — receiver: list inbound offers awaiting decision.
//   mesh__accept_skill        — receiver: pull the full skill + publish locally.
//   mesh__reject_skill        — receiver: discard the offer.
//
// The offer carries metadata only (the body + bundle blow the 1 MiB mesh
// envelope cap). On accept the receiver pulls the full content from the
// sender over /mcplexer/skill-registry/1.0.0 — so the sender must still be
// online and have granted the receiver mesh.registry_request.

const defaultSkillOfferExpirySeconds = 7 * 24 * 3600 // 7 days
const maxSkillOfferExpirySeconds = 30 * 24 * 3600    // 30 days

// handleMeshPushSkill ships a metadata-only skill offer to a paired peer and
// records an outbound row so the sender can track the decision.
func (h *handler) handleMeshPushSkill(ctx context.Context, args json.RawMessage) (json.RawMessage, *RPCError) {
	if h.mesh == nil {
		return marshalErrorResult("Agent mesh is not enabled."), nil
	}
	if h.skillRegistry == nil {
		return marshalErrorResult("Skills registry is not enabled."), nil
	}
	if h.store == nil {
		return marshalErrorResult("Store is not wired."), nil
	}
	var req struct {
		ToPeer           string `json:"to_peer"`
		Name             string `json:"name"`
		Version          int    `json:"version,omitempty"`
		ExpiresInSeconds int    `json:"expires_in_seconds,omitempty"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
		}
	}
	req.ToPeer = strings.TrimSpace(req.ToPeer)
	req.Name = strings.TrimSpace(req.Name)
	v := newValidator()
	v.requireStringWithHint("to_peer", req.ToPeer, "peer display name or short ID — call mesh__list_peers")
	v.requireStringWithHint("name", req.Name, "registry skill name to push — call mcpx__skill_list")
	if env, ok := v.envelope(); ok {
		return env, nil
	}

	ref := skillregistry.VersionRef{Latest: true}
	if req.Version > 0 {
		ref = skillregistry.VersionRef{Version: req.Version}
	}
	entry, err := h.skillRegistry.Get(ctx, h.sessionScope(ctx), req.Name, ref)
	if errors.Is(err, store.ErrNotFound) {
		return marshalErrorResult(fmt.Sprintf("Skill %q not found in the local registry.", req.Name)), nil
	}
	if err != nil {
		return marshalErrorResult(fmt.Sprintf("look up skill: %v", err)), nil
	}

	peerID, resolveErr := h.resolveMeshPeer(ctx, req.ToPeer)
	if resolveErr != nil {
		return marshalErrorResult(mesh.FormatPeerNotPairedError(req.ToPeer, resolveErr)), nil
	}

	expires := req.ExpiresInSeconds
	if expires <= 0 {
		expires = defaultSkillOfferExpirySeconds
	}
	if expires > maxSkillOfferExpirySeconds {
		expires = maxSkillOfferExpirySeconds
	}
	expiresAt := time.Now().UTC().Add(time.Duration(expires) * time.Second)
	offerID := ulid.MustNew(ulid.Timestamp(time.Now()), rand.Reader).String()

	wire := mesh.SkillOfferWire{
		OfferID:      offerID,
		Name:         entry.Name,
		Version:      entry.Version,
		ContentHash:  entry.ContentHash,
		BundleSHA256: entry.BundleSHA256,
		Description:  entry.Description,
		ExpiresAt:    expiresAt,
	}
	if _, err := h.mesh.SendSkillOffer(ctx, peerID, wire); err != nil {
		return marshalErrorResult(fmt.Sprintf("send: %v", err)), nil
	}

	row := &store.SkillOffer{
		OfferID:      offerID,
		Direction:    "outbound",
		PeerID:       peerID,
		Name:         entry.Name,
		Version:      entry.Version,
		ContentHash:  entry.ContentHash,
		BundleSHA256: entry.BundleSHA256,
		Description:  entry.Description,
		Status:       "pending",
		CreatedAt:    time.Now().UTC(),
		ExpiresAt:    expiresAt,
	}
	if err := h.store.InsertSkillOffer(ctx, row); err != nil {
		return marshalToolResult(fmt.Sprintf(
			"Skill offer %s@v%d sent to %q (offer_id=%s) — but failed to record outbound row: %v",
			entry.Name, entry.Version, req.ToPeer, offerID, err)), nil
	}
	return marshalToolResult(fmt.Sprintf(
		"Skill offer pushed to %q.\n  offer_id: %s\n  skill:    %s@v%d\n  expires:  %s\n\nRecipient must call `mesh__accept_skill { offer_id }` to pull + publish it (or `mesh__reject_skill`).",
		req.ToPeer, offerID, entry.Name, entry.Version, expiresAt.Format(time.RFC3339))), nil
}

// handleMeshListPendingSkills lists pending skill offers for the given
// direction (default "inbound").
func (h *handler) handleMeshListPendingSkills(ctx context.Context, args json.RawMessage) (json.RawMessage, *RPCError) {
	if h.store == nil {
		return marshalErrorResult("Store is not wired."), nil
	}
	var req struct {
		Direction string `json:"direction,omitempty"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
		}
	}
	direction := strings.TrimSpace(req.Direction)
	if direction == "" {
		direction = "inbound"
	}
	v := newValidator()
	v.requireOneOf("direction", direction, "inbound", "outbound")
	if env, ok := v.envelope(); ok {
		return env, nil
	}

	_, _ = h.store.ExpireOldSkillOffers(ctx, time.Now().UTC())

	offers, err := h.store.ListPendingSkillOffers(ctx, direction)
	if err != nil {
		return marshalErrorResult(fmt.Sprintf("list offers: %v", err)), nil
	}
	if len(offers) == 0 {
		return marshalToolResult(fmt.Sprintf("No pending %s skill offers.", direction)), nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "## Pending %s skill offers (%d)\n", direction, len(offers))
	for _, o := range offers {
		peerLabel := o.PeerID
		if p, err := h.store.GetPeer(ctx, o.PeerID); err == nil && p.DisplayName != "" {
			peerLabel = p.DisplayName
		}
		fmt.Fprintf(&b, "\n- offer_id: %s\n  skill:    %s@v%d\n  peer:     %s\n  created:  %s\n  expires:  %s\n",
			o.OfferID, o.Name, o.Version, peerLabel,
			o.CreatedAt.Format(time.RFC3339), o.ExpiresAt.Format(time.RFC3339))
		if o.Description != "" {
			fmt.Fprintf(&b, "  desc:     %s\n", truncate(o.Description, 100))
		}
	}
	if direction == "inbound" {
		b.WriteString("\nCall `mesh__accept_skill { offer_id }` to pull + publish, or `mesh__reject_skill { offer_id }` to discard.\n")
	}
	return marshalToolResult(b.String()), nil
}

// handleMeshAcceptSkill pulls the offered skill from the sender over the
// registry-share protocol and publishes it into the local registry.
func (h *handler) handleMeshAcceptSkill(ctx context.Context, args json.RawMessage) (json.RawMessage, *RPCError) {
	if h.store == nil {
		return marshalErrorResult("Store is not wired."), nil
	}
	if h.registryShare == nil {
		return marshalErrorResult(
			"Registry hub sync is not enabled. Start the daemon with --p2p (and build with -tags p2p) to accept pushed skills."), nil
	}
	offer, rpcErr := h.loadDecidableSkillOffer(ctx, args)
	if rpcErr != nil {
		return rpcErr.result, rpcErr.err
	}

	if _, err := h.registryShare.RequestRegistrySkill(ctx, offer.PeerID, offer.Name, offer.Version); err != nil {
		return marshalErrorResult(fmt.Sprintf("pull from sender failed: %v", err)), nil
	}

	publishedVersion := offer.Version
	if h.skillRegistry != nil {
		if e, gerr := h.skillRegistry.Get(ctx, h.sessionScope(ctx), offer.Name, skillregistry.VersionRef{Latest: true}); gerr == nil {
			publishedVersion = e.Version
		}
	}
	if err := h.store.DecideSkillOffer(ctx, offer.OfferID, "accepted", time.Now().UTC(), publishedVersion); err != nil {
		return marshalToolResult(fmt.Sprintf(
			"Skill %q pulled + published (v%d), but failed to mark offer decided: %v",
			offer.Name, publishedVersion, err)), nil
	}
	return marshalToolResult(fmt.Sprintf(
		"Accepted skill offer %s. Pulled %s@v%d from %s and published it into the local registry.",
		offer.OfferID, offer.Name, publishedVersion, shortPeerID(offer.PeerID))), nil
}

// handleMeshRejectSkill marks a pending inbound offer as rejected. Nothing
// is pulled or published.
func (h *handler) handleMeshRejectSkill(ctx context.Context, args json.RawMessage) (json.RawMessage, *RPCError) {
	if h.store == nil {
		return marshalErrorResult("Store is not wired."), nil
	}
	offer, rpcErr := h.loadDecidableSkillOffer(ctx, args)
	if rpcErr != nil {
		return rpcErr.result, rpcErr.err
	}
	if err := h.store.DecideSkillOffer(ctx, offer.OfferID, "rejected", time.Now().UTC(), 0); err != nil {
		return marshalErrorResult(fmt.Sprintf("decide: %v", err)), nil
	}
	return marshalToolResult(fmt.Sprintf("Skill offer %s rejected.", offer.OfferID)), nil
}

// skillOfferRPCErr bundles a tool-result/RPCError pair so the accept/reject
// handlers share the same load+validate preamble.
type skillOfferRPCErr struct {
	result json.RawMessage
	err    *RPCError
}

// loadDecidableSkillOffer parses offer_id, fetches the row, and validates it
// is an inbound, pending, non-expired offer. Returns a non-nil error wrapper
// the caller should return as-is.
func (h *handler) loadDecidableSkillOffer(ctx context.Context, args json.RawMessage) (*store.SkillOffer, *skillOfferRPCErr) {
	var req struct {
		OfferID string `json:"offer_id"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return nil, &skillOfferRPCErr{err: &RPCError{Code: CodeInvalidParams, Message: err.Error()}}
		}
	}
	req.OfferID = strings.TrimSpace(req.OfferID)
	v := newValidator()
	v.requireStringWithHint("offer_id", req.OfferID, "call mesh__list_pending_skills to see pending offer ids")
	if env, ok := v.envelope(); ok {
		return nil, &skillOfferRPCErr{result: env}
	}
	offer, err := h.store.GetSkillOffer(ctx, req.OfferID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, &skillOfferRPCErr{result: marshalErrorResult(fmt.Sprintf("offer %s not found", req.OfferID))}
		}
		return nil, &skillOfferRPCErr{result: marshalErrorResult(fmt.Sprintf("get offer: %v", err))}
	}
	if offer.Direction != "inbound" {
		return nil, &skillOfferRPCErr{result: marshalErrorResult("only inbound offers can be decided")}
	}
	if offer.Status != "pending" {
		return nil, &skillOfferRPCErr{result: marshalErrorResult(fmt.Sprintf("offer is already %s", offer.Status))}
	}
	if time.Now().UTC().After(offer.ExpiresAt) {
		_ = h.store.DecideSkillOffer(ctx, req.OfferID, "expired", time.Now().UTC(), 0)
		return nil, &skillOfferRPCErr{result: marshalErrorResult("offer has expired")}
	}
	return offer, nil
}
