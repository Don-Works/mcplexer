package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// tokenEntry caches an access token with its expiry.
type tokenEntry struct {
	accessToken string
	expiresAt   time.Time
}

// clientCredentialsCache stores tokens keyed by auth scope ID.
type clientCredentialsCache struct {
	mu      sync.Mutex
	entries map[string]*tokenEntry
}

func newClientCredentialsCache() *clientCredentialsCache {
	return &clientCredentialsCache{entries: make(map[string]*tokenEntry)}
}

// get returns a cached token if it is still valid (with 60s buffer).
func (c *clientCredentialsCache) get(scopeID string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[scopeID]
	if !ok {
		return "", false
	}
	if time.Until(e.expiresAt) < 60*time.Second {
		delete(c.entries, scopeID)
		return "", false
	}
	return e.accessToken, true
}

// set stores a token with its expiry.
func (c *clientCredentialsCache) set(scopeID, token string, expiresAt time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[scopeID] = &tokenEntry{accessToken: token, expiresAt: expiresAt}
}

// ccTokenResponse is the OAuth2 token response for client_credentials.
type ccTokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
}

// exchangeClientCredentials performs an OAuth2 client_credentials grant.
// It POSTs to tokenURL with Basic auth (clientID:clientSecret).
// If scopes is non-empty, it is included as the scope parameter.
func exchangeClientCredentials(
	ctx context.Context,
	tokenURL, clientID, clientSecret, scopes string,
) (token string, expiresAt time.Time, err error) {
	form := url.Values{"grant_type": {"client_credentials"}}
	if scopes != "" {
		form.Set("scope", scopes)
	}
	req, err := http.NewRequestWithContext(
		ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()),
	)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("build token request: %w", err)
	}

	creds := base64.StdEncoding.EncodeToString([]byte(clientID + ":" + clientSecret))
	req.Header.Set("Authorization", "Basic "+creds)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("token request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("read token response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", time.Time{}, fmt.Errorf("token endpoint returned %d: %s", resp.StatusCode, body)
	}

	var tr ccTokenResponse
	if err := json.Unmarshal(body, &tr); err != nil {
		return "", time.Time{}, fmt.Errorf("parse token response: %w", err)
	}
	if tr.AccessToken == "" {
		return "", time.Time{}, fmt.Errorf("empty access_token in response")
	}

	exp := time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)
	return tr.AccessToken, exp, nil
}
