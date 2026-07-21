package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/downstream"
	"github.com/don-works/mcplexer/internal/oauth"
	"github.com/don-works/mcplexer/internal/store"
)

type downstreamOAuthHandler struct {
	store       store.Store
	flowManager *oauth.FlowManager
	manager     *downstream.Manager
}

type oauthSetupRequest struct {
	AuthScopeName string `json:"auth_scope_name"`
}

type oauthSetupResponse struct {
	AuthScope    store.AuthScope       `json:"auth_scope"`
	Provider     oauthProviderResponse `json:"provider"`
	AuthorizeURL string                `json:"authorize_url"`
}

type oauthStatusEntry struct {
	AuthScopeID   string     `json:"auth_scope_id"`
	AuthScopeName string     `json:"auth_scope_name"`
	Status        string     `json:"status"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
	WorkspaceID   string     `json:"workspace_id,omitempty"`
	RouteRuleID   string     `json:"route_rule_id,omitempty"`
}

type oauthStatusResponse struct {
	Entries []oauthStatusEntry `json:"entries"`
}

// POST /api/v1/downstreams/{id}/oauth-setup
func (h *downstreamOAuthHandler) setup(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()

	server, err := h.store.GetDownstreamServer(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "downstream server not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to get downstream server")
		return
	}

	if server.Transport != "http" || server.URL == nil {
		writeError(w, http.StatusBadRequest, "oauth setup only works for HTTP transport servers")
		return
	}

	var req oauthSetupRequest
	if err := decodeJSON(r, &req); err != nil {
		// Body is optional; use defaults if empty/invalid.
		req = oauthSetupRequest{}
	}

	scopeName := req.AuthScopeName
	if scopeName == "" {
		scopeName = server.ToolNamespace + "_oauth"
	}

	// Step 1: Discover OAuth metadata from the server.
	metadata, err := oauth.DiscoverOAuthServer(ctx, *server.URL)
	if err != nil {
		writeErrorDetail(w, http.StatusBadGateway,
			"oauth discovery failed", err.Error())
		return
	}

	// Step 2: Dynamic Client Registration if endpoint is available.
	// Use the inbound request's origin so the redirect URI matches the user's
	// browser, not the daemon's startup-time external URL.
	callbackURL := h.flowManager.RequestCallbackURL(r)
	var clientID string
	if metadata.RegistrationEndpoint != "" {
		dcr, err := oauth.DynamicClientRegister(ctx, metadata.RegistrationEndpoint, callbackURL)
		if err != nil {
			writeErrorDetail(w, http.StatusBadGateway,
				"dynamic client registration failed", err.Error())
			return
		}
		clientID = dcr.ClientID
	} else {
		writeError(w, http.StatusBadGateway,
			"server does not support dynamic client registration; "+
				"configure the OAuth provider manually")
		return
	}

	// Step 3: Create OAuthProvider from discovered metadata.
	providerName := fmt.Sprintf("%s (auto)", server.Name)
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

	provider := store.OAuthProvider{
		Name:         providerName,
		AuthorizeURL: metadata.AuthorizationEndpoint,
		TokenURL:     metadata.TokenEndpoint,
		ClientID:     clientID,
		UsePKCE:      usePKCE,
		RedirectURI:  callbackURL,
		Source:       "auto-discovery",
	}
	now := time.Now().UTC()
	provider.CreatedAt = now
	provider.UpdatedAt = now

	if err := h.store.CreateOAuthProvider(ctx, &provider); err != nil {
		if errors.Is(err, store.ErrAlreadyExists) {
			// Try to find the existing provider by name and reuse it.
			existing, lookupErr := h.store.GetOAuthProviderByName(ctx, providerName)
			if lookupErr != nil {
				writeError(w, http.StatusConflict, "oauth provider already exists")
				return
			}
			provider = *existing
		} else {
			writeErrorDetail(w, http.StatusInternalServerError,
				"failed to create oauth provider", err.Error())
			return
		}
	}

	// Step 4: Find or create AuthScope linked to the provider.
	// First, check if there's an existing OAuth scope linked via route rules.
	var scope *store.AuthScope
	rules, _ := h.store.ListRouteRules(ctx, "")
	for _, rule := range rules {
		if rule.DownstreamServerID != server.ID || rule.AuthScopeID == "" {
			continue
		}
		existing, err := h.store.GetAuthScope(ctx, rule.AuthScopeID)
		if err != nil {
			continue
		}
		if existing.Type == "oauth2" {
			scope = existing
			break
		}
		// Found an env/header scope; we'll convert it to oauth2.
		scope = existing
		scope.Type = "oauth2"
		break
	}

	if scope != nil {
		// Update existing scope to link to the auto-discovered provider.
		if scope.OAuthProviderID != provider.ID {
			scope.OAuthProviderID = provider.ID
			scope.UpdatedAt = now
			_ = h.store.UpdateAuthScope(ctx, scope)
		}
	} else {
		// Create a new scope.
		newScope := store.AuthScope{
			Name:            scopeName,
			Type:            "oauth2",
			OAuthProviderID: provider.ID,
			Source:          "auto-discovery",
			CreatedAt:       now,
			UpdatedAt:       now,
		}
		if err := h.store.CreateAuthScope(ctx, &newScope); err != nil {
			writeErrorDetail(w, http.StatusInternalServerError,
				"failed to create auth scope", err.Error())
			return
		}
		scope = &newScope
	}

	// Step 5: Build authorize URL.
	authorizeURL, err := h.flowManager.AuthorizeURL(ctx, scope.ID, r)
	if err != nil {
		writeErrorDetail(w, http.StatusInternalServerError,
			"failed to build authorize url", err.Error())
		return
	}

	// Step 6: Update route rules that reference this downstream to use the new scope.
	h.updateRouteAuthScopes(ctx, server.ID, scope.ID)

	writeJSON(w, http.StatusOK, oauthSetupResponse{
		AuthScope:    *scope,
		Provider:     newOAuthProviderResponse(&provider),
		AuthorizeURL: authorizeURL,
	})
}

// updateRouteAuthScopes updates route rules for this downstream that have
// empty or env-type auth scopes to point to the new OAuth scope.
func (h *downstreamOAuthHandler) updateRouteAuthScopes(
	ctx context.Context, serverID, authScopeID string,
) {
	rules, err := h.store.ListRouteRules(ctx, "")
	if err != nil {
		return
	}
	for _, rule := range rules {
		if rule.DownstreamServerID != serverID {
			continue
		}
		if rule.AuthScopeID != "" {
			// Check if the existing scope is an env type (not oauth2).
			existing, err := h.store.GetAuthScope(ctx, rule.AuthScopeID)
			if err == nil && existing.Type == "oauth2" {
				continue // already linked to an OAuth scope
			}
		}
		rule.AuthScopeID = authScopeID
		rule.UpdatedAt = time.Now().UTC()
		_ = h.store.UpdateRouteRule(ctx, &rule)
	}
}

// GET /api/v1/downstreams/{id}/oauth-status
func (h *downstreamOAuthHandler) status(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ctx := r.Context()

	server, err := h.store.GetDownstreamServer(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "downstream server not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to get downstream server")
		return
	}

	// Find auth scopes linked to this downstream via route rules.
	rules, err := h.store.ListRouteRules(ctx, "")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list route rules")
		return
	}

	seen := make(map[string]bool)
	var entries []oauthStatusEntry
	for _, rule := range rules {
		if rule.DownstreamServerID != id || rule.AuthScopeID == "" {
			continue
		}
		if seen[rule.AuthScopeID] {
			continue
		}
		seen[rule.AuthScopeID] = true

		scope, err := h.store.GetAuthScope(ctx, rule.AuthScopeID)
		if err != nil {
			continue
		}

		entry := oauthStatusEntry{
			AuthScopeID:   scope.ID,
			AuthScopeName: scope.Name,
			Status:        "not_configured",
			WorkspaceID:   rule.WorkspaceID,
			RouteRuleID:   rule.ID,
		}

		if scope.Type == "oauth2" {
			if h.flowManager != nil {
				status, expiresAt, err := h.flowManager.TokenStatus(ctx, scope.ID)
				if err == nil {
					switch status {
					case "valid":
						entry.Status = "authenticated"
					case "expired", "refresh_needed":
						entry.Status = "expired"
					default:
						entry.Status = "not_configured"
					}
					entry.ExpiresAt = expiresAt
				}
			}
			if entry.Status == "authenticated" &&
				shouldProbeDownstreamOAuth(server) &&
				h.manager != nil {
				h.probeOAuthStatus(ctx, id, scope.ID, &entry)
			}
		} else if len(scope.EncryptedData) > 0 {
			// Non-OAuth scopes (env/header) with stored credentials.
			entry.Status = "authenticated"
		}
		entries = append(entries, entry)
	}

	if entries == nil {
		entries = []oauthStatusEntry{}
	}
	writeJSON(w, http.StatusOK, oauthStatusResponse{Entries: entries})
}

func shouldProbeDownstreamOAuth(server *store.DownstreamServer) bool {
	if server == nil || server.Transport != "http" {
		return false
	}
	if strings.EqualFold(server.ToolNamespace, "slack") ||
		strings.EqualFold(server.ID, "slack") {
		return true
	}
	if server.URL != nil && strings.Contains(*server.URL, "mcp.slack.com") {
		return true
	}
	return false
}

func (h *downstreamOAuthHandler) probeOAuthStatus(
	ctx context.Context,
	serverID, authScopeID string,
	entry *oauthStatusEntry,
) {
	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, err := h.manager.ListTools(probeCtx, serverID, authScopeID); err == nil {
		return
	} else if errors.Is(err, downstream.ErrAuthRequired) {
		entry.Status = "not_configured"
		entry.ExpiresAt = nil
	}
}

// DownstreamOAuthStatus is a JSON-friendly status for a single downstream's OAuth state.
type DownstreamOAuthStatus struct {
	ServerID string `json:"server_id"`
	Status   string `json:"status"` // "authenticated", "expired", "not_configured", "not_applicable"
}

// GetAllDownstreamOAuthStatuses returns OAuth status for all HTTP downstreams.
func GetAllDownstreamOAuthStatuses(
	ctx context.Context, s store.Store, fm *oauth.FlowManager,
) ([]DownstreamOAuthStatus, error) {
	servers, err := s.ListDownstreamServers(ctx)
	if err != nil {
		return nil, err
	}

	var statuses []DownstreamOAuthStatus
	rules, _ := s.ListRouteRules(ctx, "")

	for _, srv := range servers {
		ds := DownstreamOAuthStatus{ServerID: srv.ID, Status: "not_applicable"}
		if srv.Transport != "http" {
			statuses = append(statuses, ds)
			continue
		}
		ds.Status = "not_configured"

		// Find auth scope for this server via route rules.
		for _, rule := range rules {
			if rule.DownstreamServerID != srv.ID || rule.AuthScopeID == "" {
				continue
			}
			scope, err := s.GetAuthScope(ctx, rule.AuthScopeID)
			if err != nil {
				continue
			}
			if scope.Type == "oauth2" {
				if fm != nil {
					status, _, _ := fm.TokenStatus(ctx, scope.ID)
					switch status {
					case "valid":
						ds.Status = "authenticated"
					case "expired", "refresh_needed":
						ds.Status = "expired"
					}
				}
			} else if len(scope.EncryptedData) > 0 {
				ds.Status = "authenticated"
			}
			break
		}
		statuses = append(statuses, ds)
	}
	return statuses, nil
}
