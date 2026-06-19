package mesh

import (
	"context"
	"fmt"
	"time"
)

func (m *Manager) buildAuthSnapshot(ctx context.Context, scopeID string) (authSnapshotPlain, error) {
	scope, err := m.authSyncStore.GetAuthScope(ctx, scopeID)
	if err != nil {
		return authSnapshotPlain{}, fmt.Errorf("get auth scope: %w", err)
	}
	secretsMap, err := decryptSecretMap(m.authSyncEncryptor, scope.EncryptedData)
	if err != nil {
		return authSnapshotPlain{}, err
	}
	token, err := decryptOAuthTokenData(m.authSyncEncryptor, scope.OAuthTokenData)
	if err != nil {
		return authSnapshotPlain{}, err
	}
	snap := authSnapshotPlain{
		Schema:     authSyncSchema,
		Exported:   time.Now().UTC(),
		Secrets:    secretsMap,
		OAuthToken: token,
		Scope: authScopeSnapshot{
			ID:              scope.ID,
			Name:            scope.Name,
			DisplayName:     scope.DisplayName,
			Type:            scope.Type,
			RedactionHints:  cloneRaw(scope.RedactionHints),
			OAuthProviderID: scope.OAuthProviderID,
			Source:          scope.Source,
		},
	}
	if scope.OAuthProviderID == "" {
		return m.addLinkedConfigToSnapshot(ctx, &snap, scope.ID)
	}
	provider, err := m.authSyncStore.GetOAuthProvider(ctx, scope.OAuthProviderID)
	if err != nil {
		return authSnapshotPlain{}, fmt.Errorf("get oauth provider: %w", err)
	}
	clientSecret, err := decryptString(m.authSyncEncryptor, provider.EncryptedClientSecret)
	if err != nil {
		return authSnapshotPlain{}, err
	}
	snap.Provider = &oauthProviderSnapshot{
		ID:           provider.ID,
		Name:         provider.Name,
		TemplateID:   provider.TemplateID,
		AuthorizeURL: provider.AuthorizeURL,
		TokenURL:     provider.TokenURL,
		ClientID:     provider.ClientID,
		ClientSecret: clientSecret,
		Scopes:       cloneRaw(provider.Scopes),
		UsePKCE:      provider.UsePKCE,
		RedirectURI:  provider.RedirectURI,
		Source:       provider.Source,
	}
	return m.addLinkedConfigToSnapshot(ctx, &snap, scope.ID)
}
