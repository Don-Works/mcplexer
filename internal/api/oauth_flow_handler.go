package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/oauth"
	"github.com/don-works/mcplexer/internal/secrets"
	"github.com/don-works/mcplexer/internal/store"
)

type oauthFlowHandler struct {
	flow      *oauth.FlowManager
	store     store.Store
	opStore   store.OAuthProviderStore
	encryptor *secrets.AgeEncryptor
}

// GET /api/v1/auth-scopes/{id}/oauth/authorize
func (h *oauthFlowHandler) authorize(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()

	// Check if the scope needs auto-discovery first.
	scope, err := h.store.GetAuthScope(ctx, id)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	needsDiscovery := scope.OAuthProviderID == ""
	if !needsDiscovery && scope.OAuthProviderID != "" {
		// Provider linked but may lack client_id — check before proceeding.
		provider, provErr := h.opStore.GetOAuthProvider(ctx, scope.OAuthProviderID)
		if provErr == nil && provider.ClientID == "" {
			// Template-based providers (e.g. Linear, GitHub) need manual
			// credential configuration — don't attempt auto-discovery.
			if provider.TemplateID != "" {
				writeError(w, http.StatusBadRequest,
					"OAuth provider \""+provider.Name+"\" requires client credentials. "+
						"Use the Connect button on the Servers page to configure them.")
				return
			}
			needsDiscovery = true
		}
	}
	if needsDiscovery {
		if err := h.autoDiscoverProvider(ctx, scope, r); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	url, err := h.flow.AuthorizeURL(ctx, id, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"authorize_url": url})
}

// autoDiscoverProvider finds the downstream linked to a scope via route rules,
// runs MCP OAuth discovery + DCR, and links the resulting provider to the scope.
// `r` provides the inbound request used to derive the redirect URI for DCR
// (so the URI matches the origin the user is browsing on).
func (h *oauthFlowHandler) autoDiscoverProvider(
	ctx context.Context, scope *store.AuthScope, r *http.Request,
) error {
	rules, err := h.store.ListRouteRules(ctx, "")
	if err != nil {
		return fmt.Errorf("list route rules: %w", err)
	}

	var serverID string
	for _, rule := range rules {
		if rule.AuthScopeID == scope.ID {
			serverID = rule.DownstreamServerID
			break
		}
	}
	if serverID == "" {
		return fmt.Errorf("no downstream server linked to auth scope %q", scope.ID)
	}

	server, err := h.store.GetDownstreamServer(ctx, serverID)
	if err != nil {
		return fmt.Errorf("get downstream server: %w", err)
	}
	if server.Transport != "http" || server.URL == nil {
		return fmt.Errorf("downstream %q is not an HTTP server", serverID)
	}

	metadata, err := oauth.DiscoverOAuthServer(ctx, *server.URL)
	if err != nil {
		return fmt.Errorf("oauth discovery failed: %w", err)
	}
	if metadata.RegistrationEndpoint == "" {
		return fmt.Errorf("server does not support dynamic client registration")
	}

	callbackURL := h.flow.RequestCallbackURL(r)
	dcr, err := oauth.DynamicClientRegister(ctx, metadata.RegistrationEndpoint, callbackURL)
	if err != nil {
		return fmt.Errorf("dynamic client registration failed: %w", err)
	}

	usePKCE := true
	if len(metadata.CodeChallengeMethods) > 0 {
		usePKCE = false
		for _, m := range metadata.CodeChallengeMethods {
			if m == "S256" {
				usePKCE = true
				break
			}
		}
	}

	now := time.Now().UTC()
	providerName := fmt.Sprintf("%s (auto)", server.Name)
	provider := store.OAuthProvider{
		Name:         providerName,
		AuthorizeURL: metadata.AuthorizationEndpoint,
		TokenURL:     metadata.TokenEndpoint,
		ClientID:     dcr.ClientID,
		UsePKCE:      usePKCE,
		RedirectURI:  callbackURL,
		Source:       "auto-discovery",
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := h.opStore.CreateOAuthProvider(ctx, &provider); err != nil {
		if errors.Is(err, store.ErrAlreadyExists) {
			existing, lookupErr := h.opStore.GetOAuthProviderByName(ctx, providerName)
			if lookupErr != nil {
				return fmt.Errorf("provider already exists and lookup failed: %w", lookupErr)
			}
			// Heal stale registrations: if the stored redirect_uri doesn't
			// match the URL the user is browsing on, the OAuth client was
			// registered for a different origin and will fail authorize.
			// Re-register against the current origin.
			if existing.RedirectURI != callbackURL {
				existing.ClientID = dcr.ClientID
				existing.RedirectURI = callbackURL
				existing.AuthorizeURL = metadata.AuthorizationEndpoint
				existing.TokenURL = metadata.TokenEndpoint
				existing.UsePKCE = usePKCE
				existing.UpdatedAt = now
				if updErr := h.opStore.UpdateOAuthProvider(ctx, existing); updErr != nil {
					return fmt.Errorf("update stale provider: %w", updErr)
				}
			}
			provider = *existing
		} else {
			return fmt.Errorf("create provider: %w", err)
		}
	}

	scope.OAuthProviderID = provider.ID
	scope.UpdatedAt = now
	if err := h.store.UpdateAuthScope(ctx, scope); err != nil {
		return fmt.Errorf("link provider to scope: %w", err)
	}
	return nil
}

// GET /api/v1/oauth/callback?state=...&code=...
func (h *oauthFlowHandler) callback(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	code := r.URL.Query().Get("code")
	if state == "" || code == "" {
		writeError(w, http.StatusBadRequest, "missing state or code parameter")
		return
	}
	_, err := h.flow.HandleCallback(r.Context(), state, code, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Respond with an HTML page that redirects client-side rather than a 302.
	// This breaks the cross-site redirect chain so the browser treats the
	// subsequent navigation as same-origin (avoids Sec-Fetch-Site: cross-site).
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprint(w, `<!DOCTYPE html><html><head><meta http-equiv="refresh" content="0;url=/config/auth-scopes"></head>`+
		`<body><p>Authentication successful. Redirecting&hellip;</p></body></html>`)
}

// tokenStatusResponse is the JSON body for oauth token status.
type tokenStatusResponse struct {
	Status    string     `json:"status"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// GET /api/v1/auth-scopes/{id}/oauth/status
func (h *oauthFlowHandler) status(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	status, expiresAt, err := h.flow.TokenStatus(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, tokenStatusResponse{
		Status:    status,
		ExpiresAt: expiresAt,
	})
}

// POST /api/v1/auth-scopes/{id}/oauth/revoke
func (h *oauthFlowHandler) revoke(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.flow.RevokeToken(r.Context(), id); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// quickSetupRequest is the body for the one-step OAuth quick-setup endpoint.
type quickSetupRequest struct {
	Name         string `json:"name"`
	TemplateID   string `json:"template_id"`
	ProviderID   string `json:"provider_id"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
}

// quickSetupResponse is returned on successful quick-setup.
type quickSetupResponse struct {
	AuthScope    store.AuthScope       `json:"auth_scope"`
	Provider     oauthProviderResponse `json:"provider"`
	AuthorizeURL string                `json:"authorize_url"`
}

// POST /api/v1/auth-scopes/oauth-quick-setup
func (h *oauthFlowHandler) quickSetup(w http.ResponseWriter, r *http.Request) {
	var req quickSetupRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}

	ctx := r.Context()
	var providerID string

	if req.TemplateID != "" {
		// Create provider from template
		tpl := oauth.GetTemplate(req.TemplateID)
		if tpl == nil {
			writeError(w, http.StatusBadRequest, "unknown template_id")
			return
		}
		if req.ClientID == "" {
			writeError(w, http.StatusBadRequest, "client_id is required")
			return
		}

		p := store.OAuthProvider{
			Name:         req.Name,
			TemplateID:   req.TemplateID,
			AuthorizeURL: tpl.AuthorizeURL,
			TokenURL:     tpl.TokenURL,
			ClientID:     req.ClientID,
			UsePKCE:      tpl.UsePKCE,
		}
		if len(tpl.Scopes) > 0 {
			p.Scopes, _ = json.Marshal(tpl.Scopes)
		}

		if req.ClientSecret != "" {
			if h.encryptor == nil {
				writeError(w, http.StatusInternalServerError, "encryption not configured")
				return
			}
			enc, err := h.encryptor.Encrypt([]byte(strings.TrimSpace(req.ClientSecret)))
			if err != nil {
				writeError(w, http.StatusInternalServerError, "failed to encrypt client secret")
				return
			}
			p.EncryptedClientSecret = enc
		}

		now := time.Now().UTC()
		p.CreatedAt = now
		p.UpdatedAt = now
		p.Source = "api"
		if err := h.opStore.CreateOAuthProvider(ctx, &p); err != nil {
			if errors.Is(err, store.ErrAlreadyExists) {
				writeError(w, http.StatusConflict, "provider with this name already exists")
				return
			}
			writeErrorDetail(w, http.StatusBadRequest, "failed to create provider", err.Error())
			return
		}
		providerID = p.ID
	} else if req.ProviderID != "" {
		// Use existing provider
		if _, err := h.opStore.GetOAuthProvider(ctx, req.ProviderID); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeError(w, http.StatusBadRequest, "provider not found")
				return
			}
			writeError(w, http.StatusInternalServerError, "failed to get provider")
			return
		}
		providerID = req.ProviderID
	} else {
		writeError(w, http.StatusBadRequest, "template_id or provider_id is required")
		return
	}

	// Create auth scope linked to the provider
	scope := store.AuthScope{
		Name:            req.Name,
		Type:            "oauth2",
		OAuthProviderID: providerID,
		Source:          "api",
	}
	now := time.Now().UTC()
	scope.CreatedAt = now
	scope.UpdatedAt = now
	if err := h.store.CreateAuthScope(ctx, &scope); err != nil {
		if errors.Is(err, store.ErrAlreadyExists) {
			writeError(w, http.StatusConflict, "auth scope with this name already exists")
			return
		}
		writeErrorDetail(w, http.StatusBadRequest, "failed to create auth scope", err.Error())
		return
	}

	// Build authorize URL
	authorizeURL, err := h.flow.AuthorizeURL(ctx, scope.ID, r)
	if err != nil {
		// Scope was created but we can't build the URL — return what we have
		provider, _ := h.opStore.GetOAuthProvider(ctx, providerID)
		resp := quickSetupResponse{AuthScope: scope}
		if provider != nil {
			resp.Provider = newOAuthProviderResponse(provider)
		}
		writeJSON(w, http.StatusCreated, resp)
		return
	}

	provider, _ := h.opStore.GetOAuthProvider(ctx, providerID)
	resp := quickSetupResponse{
		AuthScope:    scope,
		AuthorizeURL: authorizeURL,
	}
	if provider != nil {
		resp.Provider = newOAuthProviderResponse(provider)
	}
	writeJSON(w, http.StatusCreated, resp)
}
