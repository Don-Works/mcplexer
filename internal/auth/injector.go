package auth

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/don-works/mcplexer/internal/oauth"
	"github.com/don-works/mcplexer/internal/secrets"
	"github.com/don-works/mcplexer/internal/store"
)

// Well-known secret keys for client_credentials auth scopes.
const (
	keyClientID     = "AIKIDO_CLIENT_ID"
	keyClientSecret = "AIKIDO_CLIENT_SECRET"
	keyTokenURL     = "AIKIDO_TOKEN_URL"
	keyTokenScopes  = "AIKIDO_SCOPES"

	defaultAikidoTokenURL    = "https://app.aikido.dev/api/oauth/token"
	defaultAikidoTokenScopes = "issues:read issues:write repositories:read repositories:write containers:read teams:read reports:read research:read task_tracking:read task_tracking:write licenses:write basics:read"
)

// Injector resolves credentials from the secrets manager and provides them
// as environment variables or HTTP headers for downstream servers.
type Injector struct {
	secrets     *secrets.Manager
	flowManager *oauth.FlowManager   // may be nil
	store       store.AuthScopeStore // may be nil
	ccCache     *clientCredentialsCache
}

// NewInjector creates a credential Injector.
func NewInjector(sm *secrets.Manager, fm *oauth.FlowManager, as store.AuthScopeStore) *Injector {
	return &Injector{
		secrets:     sm,
		flowManager: fm,
		store:       as,
		ccCache:     newClientCredentialsCache(),
	}
}

// GetSecret returns the plaintext value of a single secret. Used by the
// downstream tool-call dispatch path to resolve `secret://<key>` references
// in tool arguments without taking a direct dependency on the secrets
// manager. Emits a secret.read audit row (via the secrets manager) carrying
// scope_id + key only — the plaintext value never reaches audit.
func (inj *Injector) GetSecret(ctx context.Context, authScopeID, key string) ([]byte, error) {
	if inj.secrets == nil {
		return nil, fmt.Errorf("no secrets manager configured")
	}
	return inj.secrets.Get(ctx, authScopeID, key)
}

// EnvForDownstream decrypts all secrets for the given auth scope and returns
// them as a string map suitable for use as environment variables.
// For OAuth2 scopes, it returns a valid access token instead.
func (inj *Injector) EnvForDownstream(ctx context.Context, authScopeID string) (map[string]string, error) {
	if authScopeID == "" {
		return nil, nil
	}
	if inj.store == nil {
		return nil, fmt.Errorf("auth store not configured")
	}

	scope, err := inj.store.GetAuthScope(ctx, authScopeID)
	if err == nil {
		switch scope.Type {
		case "oauth2":
			// A recognised oauth2 scope MUST resolve via the flow
			// manager. If none is configured we return an explicit
			// error rather than falling through to the generic
			// env-dump path below — that path would inject the raw
			// stored secrets (client_id / client_secret /
			// refresh_token) as env vars, leaking long-lived OAuth
			// credentials to the downstream process.
			if inj.flowManager == nil {
				return nil, fmt.Errorf("oauth2 scope %s has no flow manager configured", authScopeID)
			}
			token, err := inj.flowManager.GetValidToken(ctx, authScopeID)
			if err != nil {
				return nil, fmt.Errorf("get oauth token for scope %s: %w", authScopeID, err)
			}
			return map[string]string{"ACCESS_TOKEN": token}, nil
		case "client_credentials":
			token, err := inj.clientCredentialsToken(ctx, authScopeID)
			if err != nil {
				return nil, err
			}
			return map[string]string{"ACCESS_TOKEN": token}, nil
		case "hawk":
			return nil, fmt.Errorf("hawk scope %s requires HTTP request signing", authScopeID)
		case "env", "generic":
			// fall through to generic secret dump for legacy/known non-special scopes
		default:
			if scope.Type == "" {
				return nil, fmt.Errorf("auth scope %s has empty type; refusing to dump raw credentials", authScopeID)
			}
			return nil, fmt.Errorf("unknown auth scope type %q for scope %s; refusing to dump raw credentials", scope.Type, authScopeID)
		}
	}

	// Existing env-based flow (reached for "env"/"generic" or when GetAuthScope failed)
	if inj.secrets == nil {
		return nil, nil
	}

	keys, err := inj.secrets.List(ctx, authScopeID)
	if err != nil {
		return nil, fmt.Errorf("list secrets for scope %s: %w", authScopeID, err)
	}

	env := make(map[string]string, len(keys))
	for _, key := range keys {
		val, err := inj.secrets.Get(ctx, authScopeID, key)
		if err != nil {
			return nil, fmt.Errorf("get secret %s/%s: %w", authScopeID, key, err)
		}
		env[key] = string(val)
	}
	return env, nil
}

// HeadersForDownstream decrypts all secrets for the given auth scope and
// returns them as HTTP headers (for future HTTP remote transports).
// For OAuth2 scopes, it returns an Authorization bearer header.
// For client_credentials scopes, it exchanges credentials for a bearer token.
func (inj *Injector) HeadersForDownstream(ctx context.Context, authScopeID string) (http.Header, error) {
	if authScopeID == "" {
		return nil, nil
	}
	if inj.store == nil {
		return nil, fmt.Errorf("auth store not configured")
	}

	scope, err := inj.store.GetAuthScope(ctx, authScopeID)
	if err == nil {
		switch scope.Type {
		case "oauth2":
			// As in EnvForDownstream: a recognised oauth2 scope MUST
			// resolve via the flow manager. Returning an error when it
			// is nil prevents the env/header-dump fall-through from
			// leaking the raw stored OAuth credentials downstream.
			if inj.flowManager == nil {
				return nil, fmt.Errorf("oauth2 scope %s has no flow manager configured", authScopeID)
			}
			token, err := inj.flowManager.GetValidToken(ctx, authScopeID)
			if err != nil {
				return nil, fmt.Errorf("get oauth token for scope %s: %w", authScopeID, err)
			}
			h := make(http.Header)
			h.Set("Authorization", "Bearer "+token)
			return h, nil
		case "client_credentials":
			token, err := inj.clientCredentialsToken(ctx, authScopeID)
			if err != nil {
				return nil, err
			}
			h := make(http.Header)
			h.Set("Authorization", "Bearer "+token)
			return h, nil
		case "hawk":
			return nil, fmt.Errorf("hawk scope %s requires HTTP request signing", authScopeID)
		case "env", "generic":
			// fall through to generic secret dump for legacy/known non-special scopes
		default:
			if scope.Type == "" {
				return nil, fmt.Errorf("auth scope %s has empty type; refusing to dump raw credentials", authScopeID)
			}
			return nil, fmt.Errorf("unknown auth scope type %q for scope %s; refusing to dump raw credentials", scope.Type, authScopeID)
		}
	}

	// Existing env-based flow (reached for "env"/"generic" or when GetAuthScope failed)
	if inj.secrets == nil {
		return nil, nil
	}

	keys, err := inj.secrets.List(ctx, authScopeID)
	if err != nil {
		return nil, fmt.Errorf("list secrets for scope %s: %w", authScopeID, err)
	}

	headers := make(http.Header, len(keys))
	for _, key := range keys {
		val, err := inj.secrets.Get(ctx, authScopeID, key)
		if err != nil {
			return nil, fmt.Errorf("get secret %s/%s: %w", authScopeID, key, err)
		}
		headers.Set(key, string(val))
	}
	return headers, nil
}

// clientCredentialsToken returns a cached bearer token or exchanges
// client credentials for a new one.
func (inj *Injector) clientCredentialsToken(ctx context.Context, authScopeID string) (string, error) {
	if token, ok := inj.ccCache.get(authScopeID); ok {
		return token, nil
	}

	if inj.secrets == nil {
		return "", fmt.Errorf("no secrets manager for client_credentials scope %s", authScopeID)
	}

	clientID, err := inj.secrets.Get(ctx, authScopeID, keyClientID)
	if err != nil {
		return "", fmt.Errorf("get %s for scope %s: %w", keyClientID, authScopeID, err)
	}
	clientSecret, err := inj.secrets.Get(ctx, authScopeID, keyClientSecret)
	if err != nil {
		return "", fmt.Errorf("get %s for scope %s: %w", keyClientSecret, authScopeID, err)
	}

	tokenURL := defaultAikidoTokenURL
	if stored, err := inj.secrets.Get(ctx, authScopeID, keyTokenURL); err == nil && len(stored) > 0 {
		tokenURL = string(stored)
	}

	scopes := defaultAikidoTokenScopes
	if stored, err := inj.secrets.Get(ctx, authScopeID, keyTokenScopes); err == nil && len(stored) > 0 {
		scopes = string(stored)
	}

	slog.Debug("exchanging client credentials", "scope", authScopeID, "token_url", tokenURL)

	token, expiresAt, err := exchangeClientCredentials(ctx, tokenURL, string(clientID), string(clientSecret), scopes)
	if err != nil {
		return "", fmt.Errorf("client_credentials exchange for scope %s: %w", authScopeID, err)
	}

	inj.ccCache.set(authScopeID, token, expiresAt)
	return token, nil
}
