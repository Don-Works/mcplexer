package oauth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var discoveryHTTPClient = &http.Client{Timeout: 30 * time.Second}

// ProtectedResourceMetadata from /.well-known/oauth-protected-resource.
type ProtectedResourceMetadata struct {
	Resource             string   `json:"resource"`
	AuthorizationServers []string `json:"authorization_servers"`
	ScopesSupported      []string `json:"scopes_supported"`
}

// AuthServerMetadata from /.well-known/oauth-authorization-server.
type AuthServerMetadata struct {
	Issuer                string   `json:"issuer"`
	AuthorizationEndpoint string   `json:"authorization_endpoint"`
	TokenEndpoint         string   `json:"token_endpoint"`
	RegistrationEndpoint  string   `json:"registration_endpoint,omitempty"`
	CodeChallengeMethods  []string `json:"code_challenge_methods_supported,omitempty"`
}

// DCRResponse from Dynamic Client Registration (POST /register).
type DCRResponse struct {
	ClientID   string `json:"client_id"`
	ClientName string `json:"client_name,omitempty"`
}

// DiscoverOAuthServer performs MCP OAuth discovery for the given server URL.
// It fetches the protected resource metadata to find the authorization server,
// then fetches the authorization server metadata.
func DiscoverOAuthServer(ctx context.Context, serverURL string) (*AuthServerMetadata, error) {
	parsed, err := url.Parse(serverURL)
	if err != nil {
		return nil, fmt.Errorf("parse server url: %w", err)
	}
	origin := parsed.Scheme + "://" + parsed.Host

	// Step 1: try /.well-known/oauth-protected-resource to find auth server.
	authServerURL := origin
	prURL := origin + "/.well-known/oauth-protected-resource"
	pr, err := fetchJSON[ProtectedResourceMetadata](ctx, prURL)
	if err == nil && len(pr.AuthorizationServers) > 0 {
		authServerURL = strings.TrimRight(pr.AuthorizationServers[0], "/")
	}

	// Step 2: fetch /.well-known/oauth-authorization-server from auth server.
	asURL := authServerURL + "/.well-known/oauth-authorization-server"
	as, err := fetchJSON[AuthServerMetadata](ctx, asURL)
	if err != nil {
		return nil, fmt.Errorf("fetch auth server metadata from %s: %w", asURL, err)
	}

	if as.AuthorizationEndpoint == "" || as.TokenEndpoint == "" {
		return nil, fmt.Errorf(
			"incomplete auth server metadata: missing authorization or token endpoint",
		)
	}

	return as, nil
}

// DynamicClientRegister performs OAuth 2.0 Dynamic Client Registration (DCR).
// It registers MCPlexer as a public client with the given callback URL.
func DynamicClientRegister(
	ctx context.Context, registrationEndpoint, callbackURL string,
) (*DCRResponse, error) {
	body := map[string]any{
		"client_name":                "MCPlexer",
		"redirect_uris":              []string{callbackURL},
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none",
	}

	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal dcr request: %w", err)
	}

	req, err := http.NewRequestWithContext(
		ctx, http.MethodPost, registrationEndpoint,
		bytes.NewReader(bodyBytes),
	)
	if err != nil {
		return nil, fmt.Errorf("build dcr request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := discoveryHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("dcr request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read dcr response: %w", err)
	}

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("dcr failed (%d): %s", resp.StatusCode, respBody)
	}

	var dcr DCRResponse
	if err := json.Unmarshal(respBody, &dcr); err != nil {
		return nil, fmt.Errorf("parse dcr response: %w", err)
	}

	if dcr.ClientID == "" {
		return nil, fmt.Errorf("dcr response missing client_id")
	}

	return &dcr, nil
}

// fetchJSON performs a GET request and decodes the JSON response into T.
func fetchJSON[T any](ctx context.Context, url string) (*T, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := discoveryHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch %s: status %d", url, resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	var result T
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	return &result, nil
}
