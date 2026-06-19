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

type downstreamConnectHandler struct {
	store       store.Store
	flowManager *oauth.FlowManager
	encryptor   *secrets.AgeEncryptor
}

type connectRequest struct {
	WorkspaceID  string `json:"workspace_id"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	ScopeName    string `json:"scope_name"`
	AccountLabel string `json:"account_label"`
}

type connectResponse struct {
	AuthScope    store.AuthScope       `json:"auth_scope"`
	Provider     oauthProviderResponse `json:"provider"`
	RouteRule    store.RouteRule       `json:"route_rule"`
	AuthorizeURL string                `json:"authorize_url"`
}

// POST /api/v1/downstreams/{id}/connect
func (h *downstreamConnectHandler) connect(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()

	var req connectRequest
	if err := decodeJSON(r, &req); err != nil {
		req = connectRequest{}
	}
	if req.WorkspaceID == "" {
		req.WorkspaceID = "global"
	}

	server, err := h.store.GetDownstreamServer(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "downstream server not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to get server")
		return
	}

	if server.Transport != "http" || server.URL == nil {
		writeError(w, http.StatusBadRequest,
			"connect only works for HTTP transport servers")
		return
	}

	// Verify workspace exists.
	if _, err := h.store.GetWorkspace(ctx, req.WorkspaceID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusBadRequest,
				"workspace \""+req.WorkspaceID+"\" not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to get workspace")
		return
	}

	var (
		provider *store.OAuthProvider
		scope    *store.AuthScope
		rule     *store.RouteRule
		authURL  string
	)

	txErr := h.store.Tx(ctx, func(tx store.Store) error {
		var txErr error

		// Step 1: Find or configure the OAuth provider.
		provider, txErr = h.findOrConfigureProvider(ctx, tx, server, &req, r)
		if txErr != nil {
			return txErr
		}

		// Step 2: Create or find auth scope.
		scopeName := req.ScopeName
		if scopeName == "" {
			if req.AccountLabel != "" {
				scopeName = server.ToolNamespace + "_oauth_" + sanitizeLabel(req.AccountLabel)
			} else {
				scopeName = server.ToolNamespace + "_oauth"
			}
		}
		scope, txErr = h.findOrCreateScope(ctx, tx, scopeName, provider.ID)
		if txErr != nil {
			return txErr
		}

		// Step 3: Create route rule (idempotent).
		routeName := ""
		if req.AccountLabel != "" {
			routeName = server.Name + " (" + req.AccountLabel + ")"
		}
		rule, txErr = h.findOrCreateRoute(
			ctx, tx, req.WorkspaceID, server.ID, scope.ID, routeName)
		if txErr != nil {
			return txErr
		}

		return nil
	})
	if txErr != nil {
		writeError(w, http.StatusBadRequest, txErr.Error())
		return
	}

	// Build authorize URL (outside tx, uses FlowManager).
	authURL, err = h.flowManager.AuthorizeURL(ctx, scope.ID, r)
	if err != nil {
		writeErrorDetail(w, http.StatusInternalServerError,
			"failed to build authorize URL", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, connectResponse{
		AuthScope:    *scope,
		Provider:     newOAuthProviderResponse(provider),
		RouteRule:    *rule,
		AuthorizeURL: authURL,
	})
}

// findOrConfigureProvider finds a seeded template provider and updates it
// with credentials, or falls back to auto-discovery + DCR.
// `r` is plumbed through so auto-discovery DCR uses the user's actual origin
// for the callback URL.
func (h *downstreamConnectHandler) findOrConfigureProvider(
	ctx context.Context,
	tx store.Store,
	server *store.DownstreamServer,
	req *connectRequest,
	r *http.Request,
) (*store.OAuthProvider, error) {
	// Look for a seeded provider whose template_id matches the server's
	// OAuth template/provider key. Downstream row IDs can be user-defined,
	// but tool_namespace is what maps a server to a built-in provider.
	providers, err := tx.ListOAuthProviders(ctx)
	if err != nil {
		return nil, fmt.Errorf("list providers: %w", err)
	}

	var tmplProvider *store.OAuthProvider
	for i := range providers {
		if providers[i].TemplateID == server.ToolNamespace {
			tmplProvider = &providers[i]
			break
		}
	}

	if tmplProvider != nil {
		tmpl := oauth.GetTemplate(server.ToolNamespace)
		// If credentials were provided, refresh the seeded template provider.
		if req.ClientID != "" {
			if tmpl != nil && tmpl.NeedsSecret &&
				strings.TrimSpace(req.ClientSecret) == "" &&
				len(tmplProvider.EncryptedClientSecret) == 0 {
				return nil, fmt.Errorf("client_secret is required for %s", server.Name)
			}
			tmplProvider.ClientID = req.ClientID
			tmplProvider.UpdatedAt = time.Now().UTC()

			if req.ClientSecret != "" {
				if h.encryptor == nil {
					return nil, fmt.Errorf("encryption not configured")
				}
				enc, err := h.encryptor.Encrypt(
					[]byte(strings.TrimSpace(req.ClientSecret)))
				if err != nil {
					return nil, fmt.Errorf("encrypt client secret: %w", err)
				}
				tmplProvider.EncryptedClientSecret = enc
			}

			if err := tx.UpdateOAuthProvider(ctx, tmplProvider); err != nil {
				return nil, fmt.Errorf("update provider: %w", err)
			}
			return tmplProvider, nil
		}

		// Re-auth path: if a template provider already has app credentials,
		// reuse it to build a fresh authorize URL without asking the operator
		// to paste client credentials again.
		if tmplProvider.ClientID != "" {
			return tmplProvider, nil
		}
	}

	// No credentials — try auto-discovery + DCR for HTTP servers.
	if server.Transport == "http" && server.URL != nil {
		discovered, discErr := h.autoDiscoverAndRegister(ctx, tx, server, r)
		if discErr == nil {
			return discovered, nil
		}
		// Auto-discovery failed — give a helpful error if template exists.
		if tmplProvider != nil {
			tmpl := oauth.GetTemplate(server.ToolNamespace)
			hint := ""
			if tmpl != nil && tmpl.SetupURL != "" {
				hint = fmt.Sprintf(
					"; provide client_id/secret from %s", tmpl.SetupURL)
			}
			return nil, fmt.Errorf(
				"auto-discovery failed for %s%s: %w",
				server.Name, hint, discErr)
		}
		return nil, discErr
	}

	// Non-HTTP server with template but no credentials.
	if tmplProvider != nil {
		return nil, fmt.Errorf(
			"client_id is required for %s (template-based provider)",
			server.Name)
	}
	return nil, fmt.Errorf(
		"no OAuth provider configured for %s", server.Name)
}

// autoDiscoverAndRegister runs MCP OAuth discovery and DCR for the server.
// If a provider with the same auto-discovery name already exists but was
// registered for a different redirect URI (typical when the daemon host
// changed, or for legacy records from before redirect_uri was persisted),
// the client is re-registered against the current origin and the stored
// record is updated. This is what lets a user move the daemon between
// hostnames (or between ports) without manually wiping providers.
func (h *downstreamConnectHandler) autoDiscoverAndRegister(
	ctx context.Context,
	tx store.Store,
	server *store.DownstreamServer,
	r *http.Request,
) (*store.OAuthProvider, error) {
	metadata, err := oauth.DiscoverOAuthServer(ctx, *server.URL)
	if err != nil {
		return nil, fmt.Errorf("OAuth discovery failed for %s: %w",
			server.Name, err)
	}

	if metadata.RegistrationEndpoint == "" {
		return nil, fmt.Errorf(
			"server %s does not support dynamic client registration; "+
				"configure OAuth provider manually", server.Name)
	}

	callbackURL := h.flowManager.RequestCallbackURL(r)

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

	existing, lookupErr := tx.GetOAuthProviderByName(ctx, providerName)
	if lookupErr == nil && existing != nil {
		// Provider already registered. If the redirect URI matches the
		// current origin we can reuse the existing client_id; otherwise
		// the OAuth server has it bound to the wrong URI and we need a
		// fresh registration.
		if existing.RedirectURI == callbackURL && existing.ClientID != "" {
			return existing, nil
		}
		dcr, dcrErr := oauth.DynamicClientRegister(
			ctx, metadata.RegistrationEndpoint, callbackURL)
		if dcrErr != nil {
			return nil, fmt.Errorf("re-register stale client: %w", dcrErr)
		}
		existing.ClientID = dcr.ClientID
		existing.RedirectURI = callbackURL
		existing.AuthorizeURL = metadata.AuthorizationEndpoint
		existing.TokenURL = metadata.TokenEndpoint
		existing.UsePKCE = usePKCE
		existing.UpdatedAt = now
		if updErr := tx.UpdateOAuthProvider(ctx, existing); updErr != nil {
			return nil, fmt.Errorf("update stale provider: %w", updErr)
		}
		return existing, nil
	}

	dcr, err := oauth.DynamicClientRegister(
		ctx, metadata.RegistrationEndpoint, callbackURL)
	if err != nil {
		return nil, fmt.Errorf("dynamic client registration failed: %w", err)
	}
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
	if err := tx.CreateOAuthProvider(ctx, &provider); err != nil {
		// Race: another caller created the same provider between our
		// lookup and this insert. Fetch and reuse.
		if errors.Is(err, store.ErrAlreadyExists) {
			raced, raceErr := tx.GetOAuthProviderByName(ctx, providerName)
			if raceErr != nil {
				return nil, fmt.Errorf("provider exists, lookup failed: %w", raceErr)
			}
			return raced, nil
		}
		return nil, fmt.Errorf("create provider: %w", err)
	}
	return &provider, nil
}

// findOrCreateScope creates an auth scope or returns an existing one.
func (h *downstreamConnectHandler) findOrCreateScope(
	ctx context.Context,
	tx store.Store,
	name, providerID string,
) (*store.AuthScope, error) {
	now := time.Now().UTC()
	scope := store.AuthScope{
		Name:            name,
		Type:            "oauth2",
		OAuthProviderID: providerID,
		Source:          "api",
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := tx.CreateAuthScope(ctx, &scope); err != nil {
		if errors.Is(err, store.ErrAlreadyExists) {
			existing, lookupErr := tx.GetAuthScopeByName(ctx, name)
			if lookupErr != nil {
				return nil, fmt.Errorf("scope exists, lookup failed: %w", lookupErr)
			}
			// Update provider link if changed.
			if existing.OAuthProviderID != providerID {
				existing.OAuthProviderID = providerID
				existing.UpdatedAt = now
				if err := tx.UpdateAuthScope(ctx, existing); err != nil {
					return nil, fmt.Errorf("update scope: %w", err)
				}
			}
			return existing, nil
		}
		return nil, fmt.Errorf("create scope: %w", err)
	}
	return &scope, nil
}

// findOrCreateRoute creates a route rule or returns an existing one.
// Matches on (workspace_id, server_id, scope_id) so multiple accounts
// for the same server in the same workspace each get their own route.
func (h *downstreamConnectHandler) findOrCreateRoute(
	ctx context.Context,
	tx store.Store,
	workspaceID, serverID, scopeID, routeName string,
) (*store.RouteRule, error) {
	rules, err := tx.ListRouteRules(ctx, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list routes: %w", err)
	}
	for i := range rules {
		if rules[i].DownstreamServerID == serverID &&
			rules[i].WorkspaceID == workspaceID &&
			rules[i].AuthScopeID == scopeID {
			return &rules[i], nil
		}
	}

	now := time.Now().UTC()
	rule := store.RouteRule{
		Name:               routeName,
		Priority:           100,
		WorkspaceID:        workspaceID,
		PathGlob:           "**",
		ToolMatch:          json.RawMessage(`["*"]`),
		DownstreamServerID: serverID,
		AuthScopeID:        scopeID,
		Policy:             "allow",
		LogLevel:           "info",
		Source:             "api",
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	if err := tx.CreateRouteRule(ctx, &rule); err != nil {
		return nil, fmt.Errorf("create route: %w", err)
	}
	return &rule, nil
}

// sanitizeLabel converts an account label to a safe scope name suffix.
func sanitizeLabel(label string) string {
	label = strings.ToLower(strings.TrimSpace(label))
	label = strings.ReplaceAll(label, " ", "_")
	var b strings.Builder
	for _, r := range label {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

type oauthCapabilities struct {
	HasTemplate           bool                    `json:"has_template"`
	Template              *oauth.ProviderTemplate `json:"template,omitempty"`
	SupportsAutoDiscovery bool                    `json:"supports_auto_discovery"`
	NeedsCredentials      bool                    `json:"needs_credentials"`
}

// GET /api/v1/downstreams/{id}/oauth-capabilities
func (h *downstreamConnectHandler) capabilities(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()

	server, err := h.store.GetDownstreamServer(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "downstream server not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to get server")
		return
	}

	caps := oauthCapabilities{NeedsCredentials: true}

	// Check for a built-in template.
	if tmpl := oauth.GetTemplate(server.ToolNamespace); tmpl != nil {
		template := *tmpl
		template.CallbackURL = h.flowManager.RequestCallbackURL(r)
		caps.HasTemplate = true
		caps.Template = &template
		caps.SupportsAutoDiscovery = template.SupportsAutoDiscovery
		caps.NeedsCredentials = template.NeedsSecret && !template.SupportsAutoDiscovery
		if caps.NeedsCredentials {
			if providers, err := h.store.ListOAuthProviders(ctx); err == nil {
				for i := range providers {
					if providers[i].TemplateID == server.ToolNamespace &&
						providers[i].ClientID != "" {
						caps.NeedsCredentials = false
						break
					}
				}
			}
		}
	} else if server.Transport == "http" && server.URL != nil {
		// No template — probe the server for OAuth discovery support.
		metadata, discErr := oauth.DiscoverOAuthServer(ctx, *server.URL)
		if discErr == nil && metadata.RegistrationEndpoint != "" {
			caps.SupportsAutoDiscovery = true
			caps.NeedsCredentials = false
		}
	}

	writeJSON(w, http.StatusOK, caps)
}
