package gateway

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/don-works/mcplexer/internal/mesh"
	"github.com/don-works/mcplexer/internal/secrets"
	"github.com/don-works/mcplexer/internal/store"
)

// v0.13.0 — mesh__send_secret MCP tools.
//
// Four tools:
//   mesh__send_secret           — sender side: encrypt + ship over the mesh.
//   mesh__list_pending_secrets  — receiver side: list inbound offers awaiting decision.
//   mesh__accept_secret         — receiver side: decrypt + return plaintext.
//   mesh__reject_secret         — receiver side: discard.
//
// Receiver-side decision is via the agent (no web UI in v0.13.0). The
// `mesh__accept_secret` tool returns the plaintext directly to the
// calling agent's context. v0.14.0 adds a dashboard approval toast and
// the option to land the plaintext in the auth_scopes secrets store.
//
// All four tools are universally available (not CWD-gated) — they're
// useful from any agent session that legitimately needs to share or
// receive a secret.

const defaultSecretExpirySeconds = 24 * 3600 // 24h
const maxSecretExpirySeconds = 7 * 24 * 3600 // 7 days

// handleMeshSendSecret encrypts plaintext to the recipient peer's age
// recipient and ships a secret_offer envelope over the mesh.
func (h *handler) handleMeshSendSecret(ctx context.Context, args json.RawMessage) (json.RawMessage, *RPCError) {
	if h.mesh == nil {
		return marshalErrorResult("Agent mesh is not enabled."), nil
	}
	if h.store == nil {
		return marshalErrorResult("Store is not wired."), nil
	}

	var req struct {
		ToPeer           string            `json:"to_peer"`
		Name             string            `json:"name"`
		Value            string            `json:"value"`
		Metadata         map[string]string `json:"metadata,omitempty"`
		ExpiresInSeconds int               `json:"expires_in_seconds,omitempty"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
		}
	}
	req.ToPeer = strings.TrimSpace(req.ToPeer)
	req.Name = strings.TrimSpace(req.Name)
	v := newValidator()
	v.requireStringWithHint("to_peer", req.ToPeer,
		"peer display name or short ID — call mesh__list_peers")
	v.requireStringWithHint("name", req.Name,
		"short label for the secret (e.g. \"PROD_DB_URL\")")
	v.requireStringWithHint("value", req.Value,
		"the secret plaintext to encrypt and ship")
	if env, ok := v.envelope(); ok {
		return env, nil
	}
	if len(req.Value) > secrets.MaxSecretPlaintextBytes {
		return marshalErrorResult(fmt.Sprintf(
			"value %d bytes exceeds limit %d", len(req.Value), secrets.MaxSecretPlaintextBytes)), nil
	}

	// Resolve full / short id / display name → full peer ID.
	peerID, resolveErr := h.resolveMeshPeer(ctx, req.ToPeer)
	if resolveErr != nil {
		return marshalErrorResult(mesh.FormatPeerNotPairedError(req.ToPeer, resolveErr)), nil
	}

	// Look up the peer's age recipient (learned via peer_identity gossip).
	peer, err := h.store.GetPeer(ctx, peerID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return marshalErrorResult(fmt.Sprintf("peer %q not paired", req.ToPeer)), nil
		}
		return marshalErrorResult(fmt.Sprintf("look up peer: %v", err)), nil
	}
	if peer.SecretTransferRecipient == "" {
		return marshalErrorResult(fmt.Sprintf(
			"peer %q has not announced their secret-transfer recipient yet — wait for the next peer_identity gossip (or ask them to restart their daemon)", req.ToPeer)), nil
	}

	expires := req.ExpiresInSeconds
	if expires <= 0 {
		expires = defaultSecretExpirySeconds
	}
	if expires > maxSecretExpirySeconds {
		expires = maxSecretExpirySeconds
	}
	expiresAt := time.Now().UTC().Add(time.Duration(expires) * time.Second)

	// Encrypt with age.
	ciphertext, err := secrets.EncryptToRecipient(peer.SecretTransferRecipient, []byte(req.Value))
	if err != nil {
		return marshalErrorResult(fmt.Sprintf("encrypt: %v", err)), nil
	}

	offerID := ulid.MustNew(ulid.Timestamp(time.Now()), rand.Reader).String()

	// Ship over the mesh.
	wire := mesh.SecretOfferWire{
		OfferID:    offerID,
		Name:       req.Name,
		Ciphertext: base64.StdEncoding.EncodeToString(ciphertext),
		Metadata:   req.Metadata,
		ExpiresAt:  expiresAt,
	}
	if _, err := h.mesh.SendSecretOffer(ctx, peerID, wire); err != nil {
		return marshalErrorResult(fmt.Sprintf("send: %v", err)), nil
	}

	// Record outbound row so the sender can track delivery.
	row := &store.SecretOffer{
		OfferID:    offerID,
		Direction:  "outbound",
		PeerID:     peerID,
		Name:       req.Name,
		Metadata:   req.Metadata,
		Ciphertext: ciphertext,
		Status:     "pending",
		CreatedAt:  time.Now().UTC(),
		ExpiresAt:  expiresAt,
	}
	if err := h.store.InsertSecretOffer(ctx, row); err != nil {
		// Not fatal — the offer is already on the wire. Log via tool result.
		return marshalToolResult(fmt.Sprintf(
			"Secret offer sent to %q (offer_id=%s, expires %s) — but failed to record outbound row locally: %v",
			req.ToPeer, offerID, expiresAt.Format(time.RFC3339), err)), nil
	}

	return marshalToolResult(fmt.Sprintf(
		"Secret offer sent to %q.\n  offer_id: %s\n  name:     %s\n  expires:  %s\n\nRecipient must call `mesh__accept_secret` to retrieve.",
		req.ToPeer, offerID, req.Name, expiresAt.Format(time.RFC3339))), nil
}

// handleMeshListPendingSecrets lists pending secret offers for the given
// direction (default: "inbound"). Never returns ciphertext.
func (h *handler) handleMeshListPendingSecrets(ctx context.Context, args json.RawMessage) (json.RawMessage, *RPCError) {
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

	// Reap expired rows lazily so the listing stays clean.
	_, _ = h.store.ExpireOldSecretOffers(ctx, time.Now().UTC())

	offers, err := h.store.ListPendingSecretOffers(ctx, direction)
	if err != nil {
		return marshalErrorResult(fmt.Sprintf("list offers: %v", err)), nil
	}

	if len(offers) == 0 {
		return marshalToolResult(fmt.Sprintf("No pending %s secret offers.", direction)), nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "## Pending %s secret offers (%d)\n", direction, len(offers))
	for _, o := range offers {
		peerLabel := o.PeerID
		if p, err := h.store.GetPeer(ctx, o.PeerID); err == nil && p.DisplayName != "" {
			peerLabel = p.DisplayName
		}
		fmt.Fprintf(&b, "\n- offer_id: %s\n  name:     %s\n  peer:     %s\n  created:  %s\n  expires:  %s\n",
			o.OfferID, o.Name, peerLabel,
			o.CreatedAt.Format(time.RFC3339), o.ExpiresAt.Format(time.RFC3339))
		if len(o.Metadata) > 0 {
			fmt.Fprintf(&b, "  metadata: %v\n", o.Metadata)
		}
	}
	if direction == "inbound" {
		b.WriteString("\nCall `mesh__accept_secret { offer_id, save_as }` or `mesh__reject_secret { offer_id }` to decide.\n")
	}
	// Inbound offers carry three peer-authored strings — the peer's display
	// name, the offer name, and free-form metadata — rendered straight into
	// the agent's context. Wrap in the <untrusted-content> trust marker;
	// builtin results never reach sanitizeToolResult.
	wrap := h.meshFieldSanitizer(ctx, MeshPrefix+"list_pending_secrets", true)
	return marshalToolResult(wrap(b.String())), nil
}

// handleMeshAcceptSecret decrypts a pending inbound offer with the local
// age identity and returns the plaintext to the calling agent.
func (h *handler) handleMeshAcceptSecret(ctx context.Context, args json.RawMessage) (json.RawMessage, *RPCError) {
	if h.store == nil {
		return marshalErrorResult("Store is not wired."), nil
	}
	if h.secretTransferKey == nil {
		return marshalErrorResult("Secret transfer key not configured — cannot decrypt."), nil
	}
	var req struct {
		OfferID string `json:"offer_id"`
		SaveAs  string `json:"save_as,omitempty"` // user-chosen label for audit
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
		}
	}
	req.OfferID = strings.TrimSpace(req.OfferID)
	v := newValidator()
	v.requireStringWithHint("offer_id", req.OfferID,
		"call mesh__list_pending_secrets to see pending offer ids")
	if env, ok := v.envelope(); ok {
		return env, nil
	}

	offer, err := h.store.GetSecretOffer(ctx, req.OfferID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return marshalErrorResult(fmt.Sprintf("offer %s not found", req.OfferID)), nil
		}
		return marshalErrorResult(fmt.Sprintf("get offer: %v", err)), nil
	}
	if offer.Direction != "inbound" {
		return marshalErrorResult("only inbound offers can be accepted"), nil
	}
	if offer.Status != "pending" {
		return marshalErrorResult(fmt.Sprintf("offer is already %s — cannot accept", offer.Status)), nil
	}
	if time.Now().UTC().After(offer.ExpiresAt) {
		_ = h.store.DecideSecretOffer(ctx, req.OfferID, "expired", time.Now().UTC(), "")
		return marshalErrorResult("offer has expired"), nil
	}

	plaintext, err := secrets.DecryptWithIdentity(h.secretTransferKey, offer.Ciphertext)
	if err != nil {
		return marshalErrorResult(fmt.Sprintf("decrypt: %v", err)), nil
	}

	if err := h.store.DecideSecretOffer(ctx, req.OfferID, "accepted", time.Now().UTC(), req.SaveAs); err != nil {
		// Decryption succeeded; failure to mark decided is annoying but not fatal.
		// Return plaintext anyway so the caller isn't stuck.
		return marshalToolResult(fmt.Sprintf(
			"WARNING: failed to mark offer decided (%v).\n\nPlaintext:\n%s",
			err, string(plaintext))), nil
	}

	label := offer.Name
	if req.SaveAs != "" {
		label = req.SaveAs
	}
	return marshalToolResult(fmt.Sprintf(
		"Secret accepted (offer_id=%s, name=%s, label=%s).\n\nPlaintext follows — do not echo to chat or persist in clear:\n\n%s",
		offer.OfferID, offer.Name, label, string(plaintext))), nil
}

// handleMeshRejectSecret marks a pending inbound offer as rejected. The
// ciphertext row is kept for audit but no decryption happens.
func (h *handler) handleMeshRejectSecret(ctx context.Context, args json.RawMessage) (json.RawMessage, *RPCError) {
	if h.store == nil {
		return marshalErrorResult("Store is not wired."), nil
	}
	var req struct {
		OfferID string `json:"offer_id"`
		Reason  string `json:"reason,omitempty"`
	}
	if len(args) > 0 {
		if err := json.Unmarshal(args, &req); err != nil {
			return nil, &RPCError{Code: CodeInvalidParams, Message: err.Error()}
		}
	}
	req.OfferID = strings.TrimSpace(req.OfferID)
	v := newValidator()
	v.requireStringWithHint("offer_id", req.OfferID,
		"call mesh__list_pending_secrets to see pending offer ids")
	if env, ok := v.envelope(); ok {
		return env, nil
	}

	offer, err := h.store.GetSecretOffer(ctx, req.OfferID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return marshalErrorResult(fmt.Sprintf("offer %s not found", req.OfferID)), nil
		}
		return marshalErrorResult(fmt.Sprintf("get offer: %v", err)), nil
	}
	if offer.Direction != "inbound" {
		return marshalErrorResult("only inbound offers can be rejected"), nil
	}
	if offer.Status != "pending" {
		return marshalErrorResult(fmt.Sprintf("offer is already %s", offer.Status)), nil
	}

	if err := h.store.DecideSecretOffer(ctx, req.OfferID, "rejected", time.Now().UTC(), ""); err != nil {
		return marshalErrorResult(fmt.Sprintf("decide: %v", err)), nil
	}
	msg := fmt.Sprintf("Secret offer %s rejected.", req.OfferID)
	if req.Reason != "" {
		msg += " Reason: " + req.Reason
	}
	return marshalToolResult(msg), nil
}
