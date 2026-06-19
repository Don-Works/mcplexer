package oauth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// tokenResponse is the JSON response from an OAuth2 token endpoint.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	Scope        string `json:"scope"`
	OK           *bool  `json:"ok,omitempty"`
	Error        string `json:"error,omitempty"`
	ErrorDesc    string `json:"error_description,omitempty"`
}

func (fm *FlowManager) exchangeCode(
	ctx context.Context, p *store.OAuthProvider,
	clientSecret, code, codeVerifier, redirectURI string,
) (*store.OAuthTokenData, error) {
	if redirectURI == "" {
		redirectURI = fm.RedirectURIFor(p, nil)
	}
	form := url.Values{
		"grant_type":   {"authorization_code"},
		"code":         {code},
		"redirect_uri": {redirectURI},
		"client_id":    {p.ClientID},
	}
	if clientSecret != "" {
		form.Set("client_secret", clientSecret)
	}
	if codeVerifier != "" {
		form.Set("code_verifier", codeVerifier)
	}
	return fm.postToken(ctx, p.TokenURL, form)
}

// RefreshToken refreshes an expired access token using the stored refresh token.
func (fm *FlowManager) RefreshToken(
	ctx context.Context, authScopeID string,
) (*store.OAuthTokenData, error) {
	scope, err := fm.store.GetAuthScope(ctx, authScopeID)
	if err != nil {
		return nil, fmt.Errorf("get auth scope: %w", err)
	}

	provider, err := fm.store.GetOAuthProvider(ctx, scope.OAuthProviderID)
	if err != nil {
		return nil, fmt.Errorf("get oauth provider: %w", err)
	}

	existing, err := fm.decryptTokenData(scope.OAuthTokenData)
	if err != nil {
		return nil, fmt.Errorf("decrypt stored tokens: %w", err)
	}
	if existing.RefreshToken == "" {
		return nil, fmt.Errorf("no refresh token available")
	}

	clientSecret, err := fm.decryptClientSecret(provider)
	if err != nil {
		return nil, err
	}

	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {existing.RefreshToken},
		"client_id":     {provider.ClientID},
	}
	if clientSecret != "" {
		form.Set("client_secret", clientSecret)
	}

	td, err := fm.postToken(ctx, provider.TokenURL, form)
	if err != nil {
		return nil, fmt.Errorf("refresh token: %w", err)
	}

	// Preserve refresh token if the new response doesn't include one.
	if td.RefreshToken == "" {
		td.RefreshToken = existing.RefreshToken
	}

	encrypted, err := fm.encryptTokenData(td)
	if err != nil {
		return nil, err
	}
	if err := fm.store.UpdateAuthScopeTokenData(ctx, authScopeID, encrypted); err != nil {
		return nil, fmt.Errorf("store refreshed tokens: %w", err)
	}
	fm.emitTokenChange(ctx, authScopeID)
	return td, nil
}

// TokenStatus returns the current status of an auth scope's OAuth token.
// Returns "none", "valid", "expired", or "refresh_needed".
func (fm *FlowManager) TokenStatus(
	ctx context.Context, authScopeID string,
) (status string, expiresAt *time.Time, err error) {
	scope, err := fm.store.GetAuthScope(ctx, authScopeID)
	if err != nil {
		return "", nil, fmt.Errorf("get auth scope: %w", err)
	}
	if len(scope.OAuthTokenData) == 0 {
		return "none", nil, nil
	}

	td, err := fm.decryptTokenData(scope.OAuthTokenData)
	if err != nil {
		return "", nil, fmt.Errorf("decrypt token data: %w", err)
	}

	exp := td.ExpiresAt

	// Zero ExpiresAt means the provider didn't supply expires_in (e.g. GitHub).
	// Treat these tokens as non-expiring.
	if exp.IsZero() {
		return "valid", nil, nil
	}

	if time.Now().After(exp) {
		return "expired", &exp, nil
	}

	// Consider tokens expiring within 5 minutes as needing refresh.
	if time.Until(exp) < 5*time.Minute && td.RefreshToken != "" {
		return "refresh_needed", &exp, nil
	}
	return "valid", &exp, nil
}

// GetValidToken returns a valid access token, refreshing if needed.
func (fm *FlowManager) GetValidToken(
	ctx context.Context, authScopeID string,
) (string, error) {
	scope, err := fm.store.GetAuthScope(ctx, authScopeID)
	if err != nil {
		return "", fmt.Errorf("get auth scope: %w", err)
	}
	if len(scope.OAuthTokenData) == 0 {
		return "", fmt.Errorf("no oauth token data for scope %q", authScopeID)
	}

	td, err := fm.decryptTokenData(scope.OAuthTokenData)
	if err != nil {
		return "", fmt.Errorf("decrypt token data: %w", err)
	}

	// Zero ExpiresAt means the provider didn't supply expires_in (e.g. GitHub).
	// Treat these tokens as non-expiring.
	if td.ExpiresAt.IsZero() || time.Until(td.ExpiresAt) > 5*time.Minute {
		return td.AccessToken, nil
	}

	refreshed, err := fm.RefreshToken(ctx, authScopeID)
	if err != nil {
		return "", fmt.Errorf("auto-refresh: %w", err)
	}
	return refreshed.AccessToken, nil
}

// HasValidCachedToken reports whether an OAuth scope already has a usable
// cached access token without attempting a refresh. Startup warm-up uses this
// to avoid turning daemon boot into an auth side effect; refresh remains on
// demand when a user actually calls the downstream.
func (fm *FlowManager) HasValidCachedToken(
	ctx context.Context, authScopeID string, minTTL time.Duration,
) (bool, error) {
	scope, err := fm.store.GetAuthScope(ctx, authScopeID)
	if err != nil {
		return false, fmt.Errorf("get auth scope: %w", err)
	}
	if len(scope.OAuthTokenData) == 0 {
		return false, nil
	}
	td, err := fm.decryptTokenData(scope.OAuthTokenData)
	if err != nil {
		return false, fmt.Errorf("decrypt token data: %w", err)
	}
	if td.AccessToken == "" {
		return false, nil
	}
	return td.ExpiresAt.IsZero() || time.Until(td.ExpiresAt) > minTTL, nil
}

// RevokeToken clears the stored OAuth token data for an auth scope.
func (fm *FlowManager) RevokeToken(ctx context.Context, authScopeID string) error {
	if err := fm.store.UpdateAuthScopeTokenData(ctx, authScopeID, nil); err != nil {
		return err
	}
	fm.emitTokenChange(ctx, authScopeID)
	return nil
}

func (fm *FlowManager) postToken(
	ctx context.Context, tokenURL string, form url.Values,
) (*store.OAuthTokenData, error) {
	encoded := form.Encode()
	req, err := http.NewRequestWithContext(
		ctx, http.MethodPost, tokenURL, strings.NewReader(encoded),
	)
	if err != nil {
		return nil, fmt.Errorf("build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read token response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("token endpoint returned %d: %s", resp.StatusCode, body)
	}

	var tr tokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return nil, fmt.Errorf("parse token response: %w", err)
	}
	if tr.OK != nil && !*tr.OK {
		if tr.ErrorDesc != "" {
			return nil, fmt.Errorf("token endpoint returned oauth error %q: %s",
				tr.Error, tr.ErrorDesc)
		}
		return nil, fmt.Errorf("token endpoint returned oauth error %q", tr.Error)
	}
	if tr.AccessToken == "" {
		return nil, fmt.Errorf("token endpoint response missing access_token")
	}

	td := &store.OAuthTokenData{
		AccessToken:  tr.AccessToken,
		RefreshToken: tr.RefreshToken,
		TokenType:    tr.TokenType,
	}
	if tr.ExpiresIn > 0 {
		td.ExpiresAt = time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)
	}
	if tr.Scope != "" {
		td.Scopes = splitScopes(tr.Scope)
	}
	return td, nil
}

func splitScopes(s string) []string {
	var out []string
	for _, part := range strings.Fields(s) {
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
