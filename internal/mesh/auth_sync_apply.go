package mesh

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/don-works/mcplexer/internal/secrets"
	"github.com/don-works/mcplexer/internal/store"
)

func (m *Manager) applyAuthSnapshot(ctx context.Context, senderPeerID string, snap authSnapshotPlain) error {
	providerID := ""
	if snap.Provider != nil {
		id, err := m.upsertAuthProvider(ctx, senderPeerID, snap.Provider)
		if err != nil {
			return err
		}
		providerID = id
	}
	encryptedSecrets, err := encryptSecretMap(m.authSyncEncryptor, snap.Secrets)
	if err != nil {
		return err
	}
	tokenData, err := encryptOAuthTokenData(m.authSyncEncryptor, snap.OAuthToken)
	if err != nil {
		return err
	}
	scopeID, err := m.upsertAuthScope(ctx, senderPeerID, snap.Scope, providerID, encryptedSecrets, tokenData)
	if err != nil {
		return err
	}
	return m.upsertAuthLinkedConfig(ctx, senderPeerID, snap, scopeID)
}

func (m *Manager) upsertAuthProvider(ctx context.Context, senderPeerID string, snap *oauthProviderSnapshot) (string, error) {
	if snap == nil || snap.Name == "" {
		return "", nil
	}
	clientSecret, err := encryptString(m.authSyncEncryptor, snap.ClientSecret)
	if err != nil {
		return "", err
	}
	p := &store.OAuthProvider{
		ID:                    snap.ID,
		Name:                  snap.Name,
		TemplateID:            snap.TemplateID,
		AuthorizeURL:          snap.AuthorizeURL,
		TokenURL:              snap.TokenURL,
		ClientID:              snap.ClientID,
		EncryptedClientSecret: clientSecret,
		Scopes:                cloneRaw(snap.Scopes),
		UsePKCE:               snap.UsePKCE,
		RedirectURI:           snap.RedirectURI,
		Source:                meshImportSource(senderPeerID),
	}
	existing, err := m.findExistingProvider(ctx, snap)
	if err != nil {
		return "", err
	}
	if existing != nil {
		if !importClobberOK(existing.Source, senderPeerID) {
			slog.Default().Warn("p2p: auth_sync preserving local oauth provider, skipping import overwrite",
				"name", existing.Name, "peer", senderPeerID, "existing_source", existing.Source)
			return existing.ID, nil
		}
		p.ID = existing.ID
		return p.ID, m.authSyncStore.UpdateOAuthProvider(ctx, p)
	}
	if err := m.authSyncStore.CreateOAuthProvider(ctx, p); err != nil {
		return "", err
	}
	return p.ID, nil
}

func (m *Manager) findExistingProvider(ctx context.Context, snap *oauthProviderSnapshot) (*store.OAuthProvider, error) {
	existing, err := m.authSyncStore.GetOAuthProviderByName(ctx, snap.Name)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	if snap.ID == "" {
		return nil, nil
	}
	existing, err = m.authSyncStore.GetOAuthProvider(ctx, snap.ID)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	return nil, nil
}

func (m *Manager) upsertAuthScope(
	ctx context.Context,
	senderPeerID string,
	snap authScopeSnapshot,
	providerID string,
	encryptedSecrets []byte,
	tokenData []byte,
) (string, error) {
	scope := &store.AuthScope{
		ID:              snap.ID,
		Name:            snap.Name,
		DisplayName:     snap.DisplayName,
		Type:            snap.Type,
		EncryptedData:   encryptedSecrets,
		RedactionHints:  cloneRaw(snap.RedactionHints),
		OAuthProviderID: providerID,
		OAuthTokenData:  tokenData,
		Source:          meshImportSource(senderPeerID),
	}
	existing, err := m.findExistingScope(ctx, snap)
	if err != nil {
		return "", err
	}
	if existing != nil {
		if !importClobberOK(existing.Source, senderPeerID) {
			slog.Default().Warn("p2p: auth_sync preserving local auth scope, skipping import overwrite",
				"name", existing.Name, "peer", senderPeerID, "existing_source", existing.Source)
			return existing.ID, nil
		}
		scope.ID = existing.ID
		return scope.ID, m.updateAuthScopeWithSecrets(ctx, scope)
	}
	if err := m.authSyncStore.CreateAuthScope(ctx, scope); err != nil {
		return "", err
	}
	return scope.ID, nil
}

func (m *Manager) findExistingScope(ctx context.Context, snap authScopeSnapshot) (*store.AuthScope, error) {
	existing, err := m.authSyncStore.GetAuthScopeByName(ctx, snap.Name)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	if snap.ID == "" {
		return nil, nil
	}
	existing, err = m.authSyncStore.GetAuthScope(ctx, snap.ID)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	return nil, nil
}

func (m *Manager) updateAuthScopeWithSecrets(ctx context.Context, scope *store.AuthScope) error {
	if err := m.authSyncStore.UpdateAuthScope(ctx, scope); err != nil {
		return err
	}
	if err := m.authSyncStore.UpdateAuthScopeEncryptedData(ctx, scope.ID, scope.EncryptedData); err != nil {
		return err
	}
	return m.authSyncStore.UpdateAuthScopeTokenData(ctx, scope.ID, scope.OAuthTokenData)
}

func decryptSecretMap(enc *secrets.AgeEncryptor, data []byte) (map[string]string, error) {
	if len(data) == 0 {
		return map[string]string{}, nil
	}
	plain, err := decryptBytes(enc, data, "secrets")
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	if err := json.Unmarshal(plain, &out); err != nil {
		return nil, fmt.Errorf("unmarshal secrets: %w", err)
	}
	return out, nil
}

func encryptSecretMap(enc *secrets.AgeEncryptor, vals map[string]string) ([]byte, error) {
	if len(vals) == 0 {
		return nil, nil
	}
	return encryptJSON(enc, vals, "secrets")
}

func decryptOAuthTokenData(enc *secrets.AgeEncryptor, data []byte) (*store.OAuthTokenData, error) {
	if len(data) == 0 {
		return nil, nil
	}
	plain, err := decryptBytes(enc, data, "oauth token data")
	if err != nil {
		return nil, err
	}
	var td store.OAuthTokenData
	if err := json.Unmarshal(plain, &td); err != nil {
		return nil, fmt.Errorf("unmarshal oauth token data: %w", err)
	}
	return &td, nil
}

func encryptOAuthTokenData(enc *secrets.AgeEncryptor, td *store.OAuthTokenData) ([]byte, error) {
	if td == nil {
		return nil, nil
	}
	return encryptJSON(enc, td, "oauth token data")
}

func decryptString(enc *secrets.AgeEncryptor, data []byte) (string, error) {
	if len(data) == 0 {
		return "", nil
	}
	plain, err := decryptBytes(enc, data, "secret string")
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

func encryptString(enc *secrets.AgeEncryptor, value string) ([]byte, error) {
	if value == "" {
		return nil, nil
	}
	return encryptBytes(enc, []byte(value), "secret string")
}

func decryptBytes(enc *secrets.AgeEncryptor, data []byte, label string) ([]byte, error) {
	if enc == nil {
		return nil, errors.New("auth sync encryptor is not configured")
	}
	plain, err := enc.Decrypt(data)
	if err != nil {
		return nil, fmt.Errorf("decrypt %s: %w", label, err)
	}
	return plain, nil
}

func encryptJSON(enc *secrets.AgeEncryptor, v any, label string) ([]byte, error) {
	plain, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("marshal %s: %w", label, err)
	}
	return encryptBytes(enc, plain, label)
}

func encryptBytes(enc *secrets.AgeEncryptor, data []byte, label string) ([]byte, error) {
	if enc == nil {
		return nil, errors.New("auth sync encryptor is not configured")
	}
	encrypted, err := enc.Encrypt(data)
	if err != nil {
		return nil, fmt.Errorf("encrypt %s: %w", label, err)
	}
	return encrypted, nil
}

func cloneRaw(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	out := make([]byte, len(raw))
	copy(out, raw)
	return out
}
