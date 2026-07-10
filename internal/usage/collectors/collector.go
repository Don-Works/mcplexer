// Package collectors provides safe, injectable HTTP clients for
// gathering AI provider usage data. Each collector takes an injected
// *http.Client and secret reader so tests use httptest and no live
// credentials leak into error messages or logs.
package collectors

import (
	"context"
	"net/http"
)

// SecretReader retrieves a secret value by key. Returns empty string
// when the secret is not configured (not an error).
type SecretReader interface {
	Get(ctx context.Context, key string) (string, error)
}

// httpClient is the interface collectors use. *http.Client satisfies it.
type httpClient interface {
	Do(req *http.Request) (*http.Response, error)
}

// newBearerRequest builds an HTTP GET with Authorization: Bearer <token>.
func newBearerRequest(ctx context.Context, url, token string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	return req, nil
}

// newRawAuthRequest builds an HTTP GET with a raw Authorization header
// (no "Bearer " prefix). Used by Z.AI.
func newRawAuthRequest(ctx context.Context, url, token string) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", token)
	req.Header.Set("Accept", "application/json")
	return req, nil
}
