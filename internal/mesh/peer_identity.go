package mesh

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/don-works/mcplexer/internal/p2p"
)

// Peer-identity gossip for v0.13.0 / mesh__send_secret.
//
// Each daemon owns a dedicated age X25519 transfer keypair (see
// internal/secrets/transfer_key.go). The public *recipient* string
// (age1...) is broadcast on a `Kind=event` + tag `peer_identity` envelope
// at startup and whenever the local key rotates. Receivers persist the
// recipient on p2p_peers.secret_transfer_recipient via the wired
// PeerIdentityUpdater.
//
// Identity gossip is NOT auth-bearing — the cryptographic identity is
// still the libp2p PeerID + envelope signature. The recipient is a UX
// label that lets `mesh__send_secret` know which key to wrap to.
//
// Wire shape mirrors display_name_change exactly so the two evolve in
// lockstep.

// PeerIdentityChangedKind is the mesh envelope kind for identity gossip.
const PeerIdentityChangedKind = "event"

// PeerIdentityChangedTag flags the envelope so the inbound bridge can
// dispatch without parsing content.
const PeerIdentityChangedTag = "peer_identity"

// PeerIdentityUpdater is the narrow store surface the bridge uses when a
// peer broadcasts a peer_identity event. Wires to P2PPeerStore.
type PeerIdentityUpdater interface {
	UpdateSecretTransferRecipient(ctx context.Context, peerID, recipient string) error
}

// SetPeerIdentityUpdater wires the store-side update hook used by the
// peer_identity event handler. nil-safe; absent updater means identity
// events are logged + ignored.
func (m *Manager) SetPeerIdentityUpdater(u PeerIdentityUpdater) {
	if m == nil {
		return
	}
	m.peerIdentityUpdater = u
}

// SetTransferRecipientProvider wires a fn that returns the local age
// recipient string. Called by BroadcastPeerIdentity to fill in the
// envelope content. Returning "" yields no broadcast (no key wired yet).
func (m *Manager) SetTransferRecipientProvider(fn func() string) {
	if m == nil {
		return
	}
	m.transferRecipientFn = fn
}

// BroadcastPeerIdentity ships a `peer_identity` mesh event carrying the
// local age recipient string to every paired peer. Receivers update
// p2p_peers.secret_transfer_recipient for THIS host. nil-safe + idempotent.
// Returns nil (not an error) when no recipient is wired — boot-time call
// sites can fire this unconditionally and the first real broadcast happens
// once the key is loaded.
func (m *Manager) BroadcastPeerIdentity(ctx context.Context) error {
	if m == nil || m.p2p == nil {
		return nil
	}
	if m.transferRecipientFn == nil {
		return nil
	}
	recipient := m.transferRecipientFn()
	if recipient == "" {
		return nil
	}
	body, err := json.Marshal(map[string]string{
		"secret_transfer_recipient": recipient,
	})
	if err != nil {
		return err
	}
	env := &p2p.MeshEnvelope{
		ID:           newULID(),
		SenderPeerID: m.selfPeerID,
		Kind:         PeerIdentityChangedKind,
		Tags:         PeerIdentityChangedTag,
		Content:      string(body),
		Recipient:    p2p.Recipient{Kind: "audience", Value: "*"},
		TS:           time.Now().UnixMilli(),
	}
	if name := m.localDisplayName(); name != "" {
		env.SenderDisplayName = name
	}
	if _, err := m.p2p.SendBroadcast(ctx, env); err != nil {
		if errors.Is(err, p2p.ErrP2PNotBuiltIn) {
			return nil
		}
		return err
	}
	return nil
}

// applyPeerIdentity persists a peer's announced age recipient. Best-effort:
// logs and swallows errors so a flaky DB doesn't poison the inbound bridge.
func (m *Manager) applyPeerIdentity(ctx context.Context, env p2p.MeshEnvelope) {
	if m.peerIdentityUpdater == nil {
		return
	}
	recipient := parsePeerIdentityContent(env.Content)
	if recipient == "" {
		return
	}
	if err := m.peerIdentityUpdater.UpdateSecretTransferRecipient(ctx, env.SenderPeerID, recipient); err != nil {
		slog.Default().Debug("p2p: persist peer identity",
			"peer", env.SenderPeerID, "err", err)
	}
	if m.authSyncStore != nil {
		go func() {
			syncCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			defer cancel()
			if err := m.SendAllAuthScopesToPeer(syncCtx, env.SenderPeerID); err != nil {
				slog.Default().Debug("p2p: auth_sync after peer identity",
					"peer", env.SenderPeerID, "err", err)
			}
		}()
	}
}

// isPeerIdentity returns true when env carries the identity signal
// (kind=event + tag=peer_identity).
func isPeerIdentity(env p2p.MeshEnvelope) bool {
	if env.Kind != PeerIdentityChangedKind {
		return false
	}
	for _, tag := range splitTags(env.Tags) {
		if tag == PeerIdentityChangedTag {
			return true
		}
	}
	return false
}

// parsePeerIdentityContent extracts the recipient from envelope content.
// Tolerant of malformed input — returns "" so the caller no-ops.
func parsePeerIdentityContent(s string) string {
	if s == "" {
		return ""
	}
	var body struct {
		Recipient string `json:"secret_transfer_recipient"`
	}
	if err := json.Unmarshal([]byte(s), &body); err != nil {
		return ""
	}
	return body.Recipient
}
