package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"
)

type oidcDiscoverHandler struct{}

type oidcDiscoverRequest struct {
	IssuerURL string `json:"issuer_url"`
}

type oidcDiscoverResponse struct {
	AuthorizeURL string   `json:"authorize_url"`
	TokenURL     string   `json:"token_url"`
	Scopes       []string `json:"scopes"`
	UsePKCE      bool     `json:"use_pkce"`
	Issuer       string   `json:"issuer"`
}

// oidcConfig represents the subset of OpenID Connect Discovery we care about.
type oidcConfig struct {
	Issuer                string   `json:"issuer"`
	AuthorizationEndpoint string   `json:"authorization_endpoint"`
	TokenEndpoint         string   `json:"token_endpoint"`
	ScopesSupported       []string `json:"scopes_supported"`
	CodeChallengeSupport  []string `json:"code_challenge_methods_supported"`
}

func (h *oidcDiscoverHandler) discover(w http.ResponseWriter, r *http.Request) {
	var req oidcDiscoverRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	issuer := strings.TrimRight(req.IssuerURL, "/")
	if issuer == "" {
		writeError(w, http.StatusBadRequest, "issuer_url is required")
		return
	}
	if !strings.HasPrefix(issuer, "https://") {
		writeError(w, http.StatusBadRequest, "issuer_url must use https")
		return
	}

	discoveryURL := issuer + "/.well-known/openid-configuration"

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, discoveryURL, nil)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid issuer URL")
		return
	}
	httpReq.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		writeError(w, http.StatusBadGateway, fmt.Sprintf("failed to fetch discovery document: %v", err))
		return
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		writeError(w, http.StatusBadGateway,
			fmt.Sprintf("discovery endpoint returned %d", resp.StatusCode))
		return
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 256*1024))
	if err != nil {
		writeError(w, http.StatusBadGateway, "failed to read discovery response")
		return
	}

	var cfg oidcConfig
	if err := json.Unmarshal(body, &cfg); err != nil {
		writeError(w, http.StatusBadGateway, "invalid discovery document JSON")
		return
	}

	if cfg.AuthorizationEndpoint == "" || cfg.TokenEndpoint == "" {
		writeError(w, http.StatusBadGateway,
			"discovery document missing authorization_endpoint or token_endpoint")
		return
	}

	usePKCE := slices.Contains(cfg.CodeChallengeSupport, "S256")

	writeJSON(w, http.StatusOK, oidcDiscoverResponse{
		AuthorizeURL: cfg.AuthorizationEndpoint,
		TokenURL:     cfg.TokenEndpoint,
		Scopes:       cfg.ScopesSupported,
		UsePKCE:      usePKCE,
		Issuer:       cfg.Issuer,
	})
}
