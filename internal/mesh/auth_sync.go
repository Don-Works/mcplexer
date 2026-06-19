package mesh

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"filippo.io/age"
	"github.com/don-works/mcplexer/internal/p2p"
	"github.com/don-works/mcplexer/internal/secrets"
	"github.com/don-works/mcplexer/internal/store"
)

const (
	// AuthSyncScopeName is the explicit same-user peer grant required before
	// this daemon sends or imports auth scope material.
	AuthSyncScopeName = "mesh.auth_sync"
	AuthSyncKind      = "event"
	AuthSyncTag       = "auth_sync"

	authSyncSchema = 1

	// Base64-encoded age ciphertext. Plain snapshots are capped by
	// secrets.EncryptToRecipient at 64 KiB; base64 + age framing needs slack.
	maxAuthSyncCiphertextChars = 128 * 1024
)

type authSyncStore interface {
	CreateAuthScope(ctx context.Context, a *store.AuthScope) error
	GetAuthScope(ctx context.Context, id string) (*store.AuthScope, error)
	GetAuthScopeByName(ctx context.Context, name string) (*store.AuthScope, error)
	ListAuthScopes(ctx context.Context) ([]store.AuthScope, error)
	UpdateAuthScope(ctx context.Context, a *store.AuthScope) error
	UpdateAuthScopeTokenData(ctx context.Context, id string, data []byte) error
	UpdateAuthScopeEncryptedData(ctx context.Context, id string, data []byte) error

	CreateOAuthProvider(ctx context.Context, p *store.OAuthProvider) error
	GetOAuthProvider(ctx context.Context, id string) (*store.OAuthProvider, error)
	GetOAuthProviderByName(ctx context.Context, name string) (*store.OAuthProvider, error)
	UpdateOAuthProvider(ctx context.Context, p *store.OAuthProvider) error

	CreateDownstreamServer(ctx context.Context, d *store.DownstreamServer) error
	GetDownstreamServer(ctx context.Context, id string) (*store.DownstreamServer, error)
	GetDownstreamServerByName(ctx context.Context, name string) (*store.DownstreamServer, error)
	UpdateDownstreamServer(ctx context.Context, d *store.DownstreamServer) error

	CreateRouteRule(ctx context.Context, r *store.RouteRule) error
	GetRouteRule(ctx context.Context, id string) (*store.RouteRule, error)
	ListRouteRules(ctx context.Context, workspaceID string) ([]store.RouteRule, error)
	UpdateRouteRule(ctx context.Context, r *store.RouteRule) error
	GetWorkspacePeerBinding(ctx context.Context, peerID, remoteWorkspaceID string) (*store.WorkspacePeerBinding, error)

	GetPeer(ctx context.Context, peerID string) (*store.P2PPeer, error)
	ListPeers(ctx context.Context) ([]store.P2PPeer, error)
	HasPeerScope(ctx context.Context, peerID, scope string) (bool, error)
}

// AuthSnapshotWire is the JSON payload carried in a mesh.auth_sync envelope.
// The ciphertext is a base64 age blob addressed to the receiving peer's
// secret-transfer recipient.
type AuthSnapshotWire struct {
	SnapshotID  string    `json:"snapshot_id"`
	ScopeName   string    `json:"scope_name"`
	Ciphertext  string    `json:"ciphertext"`
	ExportedAt  time.Time `json:"exported_at"`
	GeneratedBy string    `json:"generated_by,omitempty"`
}

// SetAuthSync wires the credential mirroring subsystem. Passing nil values
// disables the corresponding half: nil store/encryptor disables all sync; nil
// transfer key only disables inbound decrypt.
func (m *Manager) SetAuthSync(
	s authSyncStore,
	enc *secrets.AgeEncryptor,
	transferKey *age.X25519Identity,
) {
	if m == nil {
		return
	}
	m.authSyncStore = s
	m.authSyncEncryptor = enc
	m.authSyncTransferKey = transferKey
}

// SetAuthSyncRefreshHook installs an optional callback fired after inbound
// auth sync imports route/server config.
func (m *Manager) SetAuthSyncRefreshHook(fn func()) {
	if m == nil {
		return
	}
	m.authSyncRefreshHook = fn
}

// SendAuthScopeSnapshotToTrustedPeers mirrors one auth scope to every active
// peer that has been granted mesh.auth_sync on this daemon.
func (m *Manager) SendAuthScopeSnapshotToTrustedPeers(ctx context.Context, scopeID string) error {
	if !m.authSyncOutboundReady() || scopeID == "" {
		return nil
	}
	peers, err := m.authSyncStore.ListPeers(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for i := range peers {
		if peers[i].RevokedAt != nil {
			continue
		}
		if err := m.sendAuthScopeSnapshot(ctx, &peers[i], scopeID); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// SendAllAuthScopesToPeer sends the current auth inventory to one trusted peer.
func (m *Manager) SendAllAuthScopesToPeer(ctx context.Context, peerID string) error {
	if !m.authSyncOutboundReady() || peerID == "" {
		return nil
	}
	peer, err := m.authSyncStore.GetPeer(ctx, peerID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		return err
	}
	scopes, err := m.authSyncStore.ListAuthScopes(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for i := range scopes {
		if err := m.sendAuthScopeSnapshot(ctx, peer, scopes[i].ID); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// SendAuthScopeSnapshotToPeer mirrors a single scope to one trusted peer.
func (m *Manager) SendAuthScopeSnapshotToPeer(ctx context.Context, peerID, scopeID string) error {
	if !m.authSyncOutboundReady() || peerID == "" || scopeID == "" {
		return nil
	}
	peer, err := m.authSyncStore.GetPeer(ctx, peerID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		return err
	}
	return m.sendAuthScopeSnapshot(ctx, peer, scopeID)
}

func (m *Manager) sendAuthScopeSnapshot(
	ctx context.Context,
	peer *store.P2PPeer,
	scopeID string,
) error {
	if peer == nil || peer.RevokedAt != nil || peer.SecretTransferRecipient == "" {
		return nil
	}
	ok, err := m.authSyncStore.HasPeerScope(ctx, peer.PeerID, AuthSyncScopeName)
	if err != nil || !ok {
		return err
	}
	plain, err := m.buildAuthSnapshot(ctx, scopeID)
	if err != nil {
		return err
	}
	data, err := json.Marshal(plain)
	if err != nil {
		return fmt.Errorf("marshal auth snapshot: %w", err)
	}
	ct, err := secrets.EncryptToRecipient(peer.SecretTransferRecipient, data)
	if err != nil {
		return fmt.Errorf("encrypt auth snapshot: %w", err)
	}
	wire := AuthSnapshotWire{
		SnapshotID:  newULID(),
		ScopeName:   plain.Scope.Name,
		Ciphertext:  base64.StdEncoding.EncodeToString(ct),
		ExportedAt:  time.Now().UTC(),
		GeneratedBy: "mcplexer",
	}
	content, err := json.Marshal(wire)
	if err != nil {
		return fmt.Errorf("marshal auth sync wire: %w", err)
	}
	env := &p2p.MeshEnvelope{
		ID:           newULID(),
		SenderPeerID: m.selfPeerID,
		Kind:         AuthSyncKind,
		Tags:         AuthSyncTag,
		Content:      string(content),
		Recipient:    p2p.Recipient{Kind: "peer", Value: peer.PeerID},
		TS:           time.Now().UnixMilli(),
	}
	if name := m.localDisplayName(); name != "" {
		env.SenderDisplayName = name
	}
	if err := m.p2p.SendToPeer(ctx, peer.PeerID, env); err != nil {
		if errors.Is(err, p2p.ErrP2PNotBuiltIn) {
			return nil
		}
		return fmt.Errorf("p2p send auth snapshot: %w", err)
	}
	return nil
}

func (m *Manager) authSyncOutboundReady() bool {
	return m != nil && m.p2p != nil && m.authSyncStore != nil && m.authSyncEncryptor != nil
}
