package mesh

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/don-works/mcplexer/internal/p2p"
	"github.com/don-works/mcplexer/internal/secrets"
)

func isAuthSync(env p2p.MeshEnvelope) bool {
	if env.Kind != AuthSyncKind {
		return false
	}
	for _, tag := range splitTags(env.Tags) {
		if tag == AuthSyncTag {
			return true
		}
	}
	return false
}

func (m *Manager) applyAuthSync(ctx context.Context, env p2p.MeshEnvelope) {
	if m == nil || m.authSyncStore == nil || m.authSyncEncryptor == nil || m.authSyncTransferKey == nil {
		return
	}
	ok, err := m.authSyncStore.HasPeerScope(ctx, env.SenderPeerID, AuthSyncScopeName)
	if err != nil {
		slog.Default().Warn("p2p: auth_sync scope check", "peer", env.SenderPeerID, "err", err)
		return
	}
	if !ok {
		// Expected drop for a peer without the grant (or revoked) — not a
		// failure, so it stays at Debug.
		slog.Default().Debug("p2p: auth_sync dropped, peer lacks grant", "peer", env.SenderPeerID)
		return
	}
	wire, snap, err := m.decodeAuthSnapshot(env.Content)
	if err != nil {
		slog.Default().Warn("p2p: decode auth_sync", "peer", env.SenderPeerID, "err", err)
		return
	}
	if !m.authSyncAcceptSnapshot(env.SenderPeerID, snap.Scope.Name, wire.SnapshotID, wire.ExportedAt) {
		slog.Default().Warn("p2p: auth_sync dropped replayed or stale snapshot",
			"peer", env.SenderPeerID, "scope", snap.Scope.Name,
			"snapshot", wire.SnapshotID, "exported_at", wire.ExportedAt)
		return
	}
	if err := m.applyAuthSnapshot(ctx, env.SenderPeerID, snap); err != nil {
		slog.Default().Warn("p2p: apply auth_sync",
			"peer", env.SenderPeerID, "scope", snap.Scope.Name, "err", err)
		return
	}
	if len(snap.Routes) > 0 && m.authSyncRefreshHook != nil {
		m.authSyncRefreshHook()
	}
}

func (m *Manager) decodeAuthSnapshot(content string) (AuthSnapshotWire, authSnapshotPlain, error) {
	wire, err := parseAuthSnapshotWire(content)
	if err != nil {
		return wire, authSnapshotPlain{}, err
	}
	ct, err := base64.StdEncoding.DecodeString(wire.Ciphertext)
	if err != nil {
		return wire, authSnapshotPlain{}, fmt.Errorf("decode ciphertext: %w", err)
	}
	plain, err := secrets.DecryptWithIdentity(m.authSyncTransferKey, ct)
	if err != nil {
		return wire, authSnapshotPlain{}, err
	}
	var snap authSnapshotPlain
	if err := json.Unmarshal(plain, &snap); err != nil {
		return wire, authSnapshotPlain{}, fmt.Errorf("unmarshal auth snapshot: %w", err)
	}
	if snap.Schema != authSyncSchema || snap.Scope.Name == "" {
		return wire, authSnapshotPlain{}, errors.New("invalid auth snapshot")
	}
	// Bind the ciphertext payload to the plaintext envelope: a mismatched
	// scope name signals a tampered or misrouted wire and must not apply.
	if wire.ScopeName != snap.Scope.Name {
		return wire, authSnapshotPlain{}, fmt.Errorf(
			"wire scope %q does not match payload scope %q", wire.ScopeName, snap.Scope.Name)
	}
	return wire, snap, nil
}

func parseAuthSnapshotWire(s string) (AuthSnapshotWire, error) {
	var w AuthSnapshotWire
	if s == "" {
		return w, errors.New("empty content")
	}
	if err := json.Unmarshal([]byte(s), &w); err != nil {
		return w, err
	}
	if w.SnapshotID == "" || w.ScopeName == "" || w.Ciphertext == "" {
		return w, errors.New("snapshot_id, scope_name and ciphertext are required")
	}
	if len(w.Ciphertext) > maxAuthSyncCiphertextChars {
		return w, fmt.Errorf("ciphertext %d chars exceeds limit %d",
			len(w.Ciphertext), maxAuthSyncCiphertextChars)
	}
	return w, nil
}
