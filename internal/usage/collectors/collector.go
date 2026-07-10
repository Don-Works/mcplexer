// Package collectors provides safe, injectable HTTP clients for
// gathering AI provider usage data. Each collector takes an injected
// *http.Client and secret reader so tests use httptest and no live
// credentials leak into error messages or logs.
package collectors

import (
	"context"
	"fmt"
	"net/http"
)

// SecretReader matches secrets.Manager without exposing secret values in
// usage configuration. Scope and key are both required for a lookup.
type SecretReader interface {
	Get(ctx context.Context, scopeID, key string) ([]byte, error)
}

// httpClient is the interface collectors use. *http.Client satisfies it.
type httpClient interface {
	Do(req *http.Request) (*http.Response, error)
}

func readSecret(ctx context.Context, reader SecretReader, scopeID, key string) ([]byte, error) {
	if reader == nil {
		return nil, nil
	}
	if scopeID == "" || key == "" {
		return nil, nil
	}
	return reader.Get(ctx, scopeID, key)
}

func requestClient(client httpClient) httpClient {
	if client == nil {
		return http.DefaultClient
	}
	return client
}

func numberPtr(value float64) *float64 { return &value }

func requireSecret(ctx context.Context, reader SecretReader, scopeID, key string) (string, error) {
	token, err := readSecret(ctx, reader, scopeID, key)
	if err != nil {
		return "", fmt.Errorf("secret read: %w", err)
	}
	if len(token) == 0 {
		return "", fmt.Errorf("no API key configured")
	}
	return string(token), nil
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
