package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/don-works/mcplexer/internal/secrets"
	"github.com/don-works/mcplexer/internal/store"
)

// createOAuthProviderParams is the create_oauth_provider tool's input. It
// mirrors the REST create body (internal/api/oauth_provider_handler.go) plus
// an optional link_scope_id that, when set, links the freshly-created provider
// to an existing auth scope in the same MCP call — collapsing the old
// two-step "POST /oauth-providers then mcplexer__update_auth_scope" workflow.
type createOAuthProviderParams struct {
	Name         string   `json:"name"`
	AuthorizeURL string   `json:"authorize_url"`
	TokenURL     string   `json:"token_url"`
	ClientID     string   `json:"client_id"`
	ClientSecret string   `json:"client_secret"`
	Scopes       []string `json:"scopes"`
	UsePKCE      bool     `json:"use_pkce"`
	LinkScopeID  string   `json:"link_scope_id"`
}

// createOAuthProviderResult is the tool's success payload. It NEVER carries the
// plaintext client secret nor the encrypted bytes — only whether one was set.
type createOAuthProviderResult struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	HasClientSecret bool   `json:"has_client_secret"`
	LinkedScopeID   string `json:"linked_scope_id,omitempty"`
}

// handleCreateOAuthProvider builds a create_oauth_provider handler bound to the
// given AgeEncryptor. The encryptor is required: the client secret must end up
// age-encrypted at rest exactly as the REST path does, so a nil encryptor is a
// hard error rather than a silent plaintext write.
//
// Secret handling: client_secret is accepted as a plaintext string and
// encrypted here. The repo's `secret://REF` substitution runs upstream in the
// downstream Manager and resolves against the CALL's auth scope — but the
// internal "mcplexer" admin server has no auth scope, so a `secret://` ref
// would fail loudly with ErrSecretRefNoScope before reaching this handler
// rather than leak. Callers should therefore pass the plaintext directly (or
// fetch it interactively via secret__prompt); the value is encrypted before it
// touches the DB and is never logged or returned.
func handleCreateOAuthProvider(enc *secrets.AgeEncryptor) handlerFunc {
	return func(ctx context.Context, s store.Store, args json.RawMessage) (json.RawMessage, error) {
		var p createOAuthProviderParams
		if err := json.Unmarshal(args, &p); err != nil {
			return nil, fmt.Errorf("invalid params: %w", err)
		}
		if strings.TrimSpace(p.Name) == "" {
			return nil, fmt.Errorf("name is required")
		}

		provider := store.OAuthProvider{
			Name:         p.Name,
			AuthorizeURL: p.AuthorizeURL,
			TokenURL:     p.TokenURL,
			ClientID:     p.ClientID,
			UsePKCE:      p.UsePKCE,
		}
		if len(p.Scopes) > 0 {
			raw, mErr := json.Marshal(p.Scopes)
			if mErr != nil {
				return nil, fmt.Errorf("marshal scopes: %w", mErr)
			}
			provider.Scopes = raw
		}

		if p.ClientSecret != "" {
			if enc == nil {
				return nil, fmt.Errorf("encryption not configured (no age key); cannot store client secret")
			}
			sealed, err := enc.Encrypt([]byte(strings.TrimSpace(p.ClientSecret)))
			if err != nil {
				return nil, fmt.Errorf("encrypt client secret: %w", err)
			}
			provider.EncryptedClientSecret = sealed
		}

		if err := s.CreateOAuthProvider(ctx, &provider); err != nil {
			if errors.Is(err, store.ErrAlreadyExists) {
				return nil, fmt.Errorf("oauth provider %q already exists", p.Name)
			}
			return nil, fmt.Errorf("create oauth provider: %w", err)
		}

		result := createOAuthProviderResult{
			ID:              provider.ID,
			Name:            provider.Name,
			HasClientSecret: len(provider.EncryptedClientSecret) > 0,
		}

		// Optional one-call link: set the target auth scope's
		// oauth_provider_id via a PARTIAL update so no other scope field is
		// clobbered. Mirrors handleUpdateAuthScope's read-modify-write.
		if strings.TrimSpace(p.LinkScopeID) != "" {
			scope, err := s.GetAuthScope(ctx, p.LinkScopeID)
			if err != nil {
				return nil, fmt.Errorf("link scope %q: %w", p.LinkScopeID, err)
			}
			scope.OAuthProviderID = provider.ID
			if err := s.UpdateAuthScope(ctx, scope); err != nil {
				return nil, fmt.Errorf("link scope %q: %w", p.LinkScopeID, err)
			}
			result.LinkedScopeID = scope.ID
		}

		return jsonResult(result)
	}
}
