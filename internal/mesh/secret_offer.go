package mesh

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/don-works/mcplexer/internal/p2p"
	"github.com/don-works/mcplexer/internal/store"
)

// Secret-offer wire frame for v0.13.0 / mesh__send_secret.
//
// A secret offer is age-encrypted payload sent peer→peer. The plaintext
// has already been wrapped to the recipient's age recipient (looked up
// from p2p_peers.secret_transfer_recipient, populated via peer_identity
// gossip). The sender ships the ciphertext as the envelope content +
// metadata in a side-channel JSON.
//
// Wire shape:
//   Kind=event, Tags=secret_offer
//   Content = JSON {
//     "offer_id":    "01H..." (ULID),
//     "name":        "pi-ssh-key",
//     "ciphertext":  "<base64 age blob>",
//     "metadata":    {...},     // optional, never sensitive
//     "expires_at":  "2026-05-26T18:00:00Z"
//   }
//
// On receive, ingestEnvelope stages the row via the wired
// SecretOfferStager. The MCP tools (mesh__list_pending_secrets /
// accept / reject) handle the decision flow.
//
// NOT auth-bearing — sender identity = libp2p PeerID + envelope sig.
// The age wrapper proves the sender knew the recipient's public key.

const SecretOfferKind = "event"
const SecretOfferTag = "secret_offer"

// MaxCiphertextBytes caps the age blob carried on the wire. 80 KB is
// generous given the 64 KB plaintext cap in internal/secrets.
const MaxCiphertextBytes = 80 * 1024

// SecretOfferStager is the narrow store surface the bridge calls when an
// inbound secret_offer envelope arrives. Wires to a store.Store in
// production; tests pass an in-memory fake.
type SecretOfferStager interface {
	InsertSecretOffer(ctx context.Context, o *store.SecretOffer) error
}

// SetSecretOfferStager wires the inbound stager. nil-safe.
func (m *Manager) SetSecretOfferStager(s SecretOfferStager) {
	if m == nil {
		return
	}
	m.secretOfferStager = s
}

// SendSecretOffer ships an outbound secret-offer envelope targeted to a
// single paired peer. Caller is responsible for the age encryption — this
// method only handles the wire framing.
//
// Returns the envelope ID on success. When p2p is not built in, returns
// nil error and "" id (callers downgrade to local-only mode).
func (m *Manager) SendSecretOffer(ctx context.Context, toPeerID string, offer SecretOfferWire) (string, error) {
	if m == nil || m.p2p == nil {
		return "", nil
	}
	if toPeerID == "" {
		return "", errors.New("SendSecretOffer: peer ID required")
	}
	if err := offer.validate(); err != nil {
		return "", err
	}
	body, err := json.Marshal(offer)
	if err != nil {
		return "", fmt.Errorf("marshal secret offer: %w", err)
	}
	env := &p2p.MeshEnvelope{
		ID:           newULID(),
		SenderPeerID: m.selfPeerID,
		Kind:         SecretOfferKind,
		Tags:         SecretOfferTag,
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

// SecretOfferWire is the JSON payload carried in a secret_offer envelope.
// `Ciphertext` is the base64-encoded age blob. The struct is exported
// because the MCP send handler builds one before calling SendSecretOffer.
type SecretOfferWire struct {
	OfferID    string            `json:"offer_id"`
	Name       string            `json:"name"`
	Ciphertext string            `json:"ciphertext"` // base64-encoded
	Metadata   map[string]string `json:"metadata,omitempty"`
	ExpiresAt  time.Time         `json:"expires_at"`
}

func (w SecretOfferWire) validate() error {
	if w.OfferID == "" {
		return errors.New("offer_id required")
	}
	if w.Name == "" {
		return errors.New("name required")
	}
	if w.Ciphertext == "" {
		return errors.New("ciphertext required")
	}
	if len(w.Ciphertext) > MaxCiphertextBytes {
		return fmt.Errorf("ciphertext %d bytes exceeds limit %d", len(w.Ciphertext), MaxCiphertextBytes)
	}
	if w.ExpiresAt.IsZero() {
		return errors.New("expires_at required")
	}
	return nil
}

// applySecretOffer persists an inbound secret-offer row. Best-effort:
// logs + swallows errors so a flaky DB doesn't poison the bridge.
// Duplicate offer IDs (replays) are silently no-op'd by the store layer.
func (m *Manager) applySecretOffer(ctx context.Context, env p2p.MeshEnvelope) {
	if m.secretOfferStager == nil {
		return
	}
	wire, err := parseSecretOfferContent(env.Content)
	if err != nil {
		slog.Default().Debug("p2p: parse secret_offer",
			"peer", env.SenderPeerID, "err", err)
		return
	}
	ct, err := base64.StdEncoding.DecodeString(wire.Ciphertext)
	if err != nil {
		slog.Default().Debug("p2p: decode secret_offer ciphertext",
			"peer", env.SenderPeerID, "err", err)
		return
	}
	row := &store.SecretOffer{
		OfferID:    wire.OfferID,
		Direction:  "inbound",
		PeerID:     env.SenderPeerID,
		Name:       wire.Name,
		Metadata:   wire.Metadata,
		Ciphertext: ct,
		Status:     "pending",
		CreatedAt:  time.Now().UTC(),
		ExpiresAt:  wire.ExpiresAt,
	}
	if err := m.secretOfferStager.InsertSecretOffer(ctx, row); err != nil {
		// store.ErrAlreadyExists is expected on replay — silent no-op.
		if errors.Is(err, store.ErrAlreadyExists) {
			return
		}
		slog.Default().Debug("p2p: stage secret_offer",
			"peer", env.SenderPeerID, "offer_id", wire.OfferID, "err", err)
	}
}

func isSecretOffer(env p2p.MeshEnvelope) bool {
	if env.Kind != SecretOfferKind {
		return false
	}
	for _, tag := range splitTags(env.Tags) {
		if tag == SecretOfferTag {
			return true
		}
	}
	return false
}

func parseSecretOfferContent(s string) (SecretOfferWire, error) {
	var w SecretOfferWire
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
