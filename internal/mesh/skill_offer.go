package mesh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/don-works/mcplexer/internal/p2p"
	"github.com/don-works/mcplexer/internal/store"
)

// Skill-offer wire frame for mesh__push_skill.
//
// A skill offer is a metadata-only PUSH notification sent peer→peer. Unlike
// a secret offer, it carries NO payload: the SKILL.md body and tar.gz bundle
// would blow the 1 MiB envelope cap (p2p.MaxEnvelopeBytes), so the offer
// ships identifiers only. On accept, the receiver pulls the full content
// from the sender over /mcplexer/skill-registry/1.0.0 (no size cap) and
// publishes it into the local registry.
//
// Wire shape:
//   Kind=event, Tags=skill_offer
//   Content = JSON {
//     "offer_id":     "01H..." (ULID),
//     "name":         "pdf",
//     "version":      4,
//     "content_hash": "...",
//     "bundle_sha256":"...",        // optional
//     "description":  "...",        // optional, for the receiver's inbox view
//     "metadata":     {...},        // optional, non-sensitive
//     "expires_at":   "2026-06-18T18:00:00Z"
//   }
//
// On receive, ingestEnvelope stages the row via the wired SkillOfferStager.
// The MCP tools (mesh__list_pending_skills / accept / reject) drive the
// decision flow. NOT auth-bearing — sender identity = libp2p PeerID +
// envelope sig.

const SkillOfferKind = "event"
const SkillOfferTag = "skill_offer"

// SkillOfferStager is the narrow store surface the bridge calls when an
// inbound skill_offer envelope arrives. Wires to a store.Store in
// production; tests pass an in-memory fake.
type SkillOfferStager interface {
	InsertSkillOffer(ctx context.Context, o *store.SkillOffer) error
}

// SetSkillOfferStager wires the inbound stager. nil-safe.
func (m *Manager) SetSkillOfferStager(s SkillOfferStager) {
	if m == nil {
		return
	}
	m.skillOfferStager = s
}

// SendSkillOffer ships an outbound skill-offer notification targeted to a
// single paired peer. Returns the envelope ID on success. When p2p is not
// built in, returns nil error and "" id (callers downgrade to local-only).
func (m *Manager) SendSkillOffer(ctx context.Context, toPeerID string, offer SkillOfferWire) (string, error) {
	if m == nil || m.p2p == nil {
		return "", nil
	}
	if toPeerID == "" {
		return "", errors.New("SendSkillOffer: peer ID required")
	}
	if err := offer.validate(); err != nil {
		return "", err
	}
	body, err := json.Marshal(offer)
	if err != nil {
		return "", fmt.Errorf("marshal skill offer: %w", err)
	}
	env := &p2p.MeshEnvelope{
		ID:           newULID(),
		SenderPeerID: m.selfPeerID,
		Kind:         SkillOfferKind,
		Tags:         SkillOfferTag,
		Content:      string(body),
		Recipient:    p2p.Recipient{Kind: "peer", Value: toPeerID},
		TS:           time.Now().UnixMilli(),
	}
	if name := m.localDisplayName(); name != "" {
		env.SenderDisplayName = name
	}
	if err := m.p2p.SendToPeer(ctx, toPeerID, env); err != nil {
		if errors.Is(err, p2p.ErrP2PNotBuiltIn) {
			return "", nil
		}
		return "", fmt.Errorf("p2p send: %w", err)
	}
	return env.ID, nil
}

// SkillOfferWire is the JSON payload carried in a skill_offer envelope.
// Exported because the MCP push handler builds one before calling
// SendSkillOffer.
type SkillOfferWire struct {
	OfferID      string            `json:"offer_id"`
	Name         string            `json:"name"`
	Version      int               `json:"version"`
	ContentHash  string            `json:"content_hash,omitempty"`
	BundleSHA256 string            `json:"bundle_sha256,omitempty"`
	Description  string            `json:"description,omitempty"`
	Metadata     map[string]string `json:"metadata,omitempty"`
	ExpiresAt    time.Time         `json:"expires_at"`
}

func (w SkillOfferWire) validate() error {
	if w.OfferID == "" {
		return errors.New("offer_id required")
	}
	if w.Name == "" {
		return errors.New("name required")
	}
	if w.ExpiresAt.IsZero() {
		return errors.New("expires_at required")
	}
	return nil
}

// applySkillOffer persists an inbound skill-offer row. Best-effort: logs +
// swallows errors so a flaky DB doesn't poison the bridge. Duplicate offer
// IDs (replays) are silently no-op'd by the store layer.
func (m *Manager) applySkillOffer(ctx context.Context, env p2p.MeshEnvelope) {
	if m.skillOfferStager == nil {
		return
	}
	wire, err := parseSkillOfferContent(env.Content)
	if err != nil {
		slog.Default().Debug("p2p: parse skill_offer",
			"peer", env.SenderPeerID, "err", err)
		return
	}
	row := &store.SkillOffer{
		OfferID:      wire.OfferID,
		Direction:    "inbound",
		PeerID:       env.SenderPeerID,
		Name:         wire.Name,
		Version:      wire.Version,
		ContentHash:  wire.ContentHash,
		BundleSHA256: wire.BundleSHA256,
		Description:  wire.Description,
		Metadata:     wire.Metadata,
		Status:       "pending",
		CreatedAt:    time.Now().UTC(),
		ExpiresAt:    wire.ExpiresAt,
	}
	if err := m.skillOfferStager.InsertSkillOffer(ctx, row); err != nil {
		if errors.Is(err, store.ErrAlreadyExists) {
			return
		}
		slog.Default().Debug("p2p: stage skill_offer",
			"peer", env.SenderPeerID, "offer_id", wire.OfferID, "err", err)
	}
}

func isSkillOffer(env p2p.MeshEnvelope) bool {
	if env.Kind != SkillOfferKind {
		return false
	}
	for _, tag := range splitTags(env.Tags) {
		if tag == SkillOfferTag {
			return true
		}
	}
	return false
}

func parseSkillOfferContent(s string) (SkillOfferWire, error) {
	var w SkillOfferWire
	if s == "" {
		return w, errors.New("empty content")
	}
	if err := json.Unmarshal([]byte(s), &w); err != nil {
		return w, err
	}
	if err := w.validate(); err != nil {
		return w, err
	}
	return w, nil
}
