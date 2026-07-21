package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/don-works/mcplexer/internal/config"
	"github.com/don-works/mcplexer/internal/oauth"
	"github.com/don-works/mcplexer/internal/secrets"
	"github.com/don-works/mcplexer/internal/store"
)

type oauthProviderHandler struct {
	svc       *config.Service
	store     store.OAuthProviderStore
	encryptor *secrets.AgeEncryptor
}

// oauthProviderRequest is the API request body for create/update.
// It accepts client_secret as plaintext (unlike the model which stores encrypted bytes).
type oauthProviderRequest struct {
	Name         string   `json:"name"`
	TemplateID   string   `json:"template_id"`
	AuthorizeURL string   `json:"authorize_url"`
	TokenURL     string   `json:"token_url"`
	ClientID     string   `json:"client_id"`
	ClientSecret string   `json:"client_secret"`
	Scopes       []string `json:"scopes"`
	UsePKCE      bool     `json:"use_pkce"`
}

// oauthProviderResponse masks EncryptedClientSecret from API responses.
type oauthProviderResponse struct {
	store.OAuthProvider
	HasClientSecret bool `json:"has_client_secret"`
}

func newOAuthProviderResponse(p *store.OAuthProvider) oauthProviderResponse {
	resp := oauthProviderResponse{
		OAuthProvider:   *p,
		HasClientSecret: len(p.EncryptedClientSecret) > 0,
	}
	resp.EncryptedClientSecret = nil
	return resp
}

func (h *oauthProviderHandler) list(w http.ResponseWriter, r *http.Request) {
	providers, err := h.store.ListOAuthProviders(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list oauth providers")
		return
	}
	resp := make([]oauthProviderResponse, len(providers))
	for i := range providers {
		resp[i] = newOAuthProviderResponse(&providers[i])
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *oauthProviderHandler) get(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	provider, err := h.store.GetOAuthProvider(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "oauth provider not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to get oauth provider")
		return
	}
	writeJSON(w, http.StatusOK, newOAuthProviderResponse(provider))
}

func (h *oauthProviderHandler) create(w http.ResponseWriter, r *http.Request) {
	var req oauthProviderRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	p := store.OAuthProvider{
		Name:         req.Name,
		TemplateID:   req.TemplateID,
		AuthorizeURL: req.AuthorizeURL,
		TokenURL:     req.TokenURL,
		ClientID:     req.ClientID,
		UsePKCE:      req.UsePKCE,
	}
	if len(req.Scopes) > 0 {
		p.Scopes, _ = json.Marshal(req.Scopes)
	}

	if req.ClientSecret != "" {
		enc, err := h.encryptSecret(req.ClientSecret)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to encrypt client secret")
			return
		}
		p.EncryptedClientSecret = enc
	}

	if err := h.svc.CreateOAuthProvider(r.Context(), &p); err != nil {
		if errors.Is(err, store.ErrAlreadyExists) {
			writeError(w, http.StatusConflict, "oauth provider already exists")
			return
		}
		writeErrorDetail(w, http.StatusBadRequest, "failed to create oauth provider", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, newOAuthProviderResponse(&p))
}

func (h *oauthProviderHandler) update(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req oauthProviderRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	p := store.OAuthProvider{
		ID:           id,
		Name:         req.Name,
		TemplateID:   req.TemplateID,
		AuthorizeURL: req.AuthorizeURL,
		TokenURL:     req.TokenURL,
		ClientID:     req.ClientID,
		UsePKCE:      req.UsePKCE,
	}
	if len(req.Scopes) > 0 {
		p.Scopes, _ = json.Marshal(req.Scopes)
	}

	if req.ClientSecret != "" {
		enc, err := h.encryptSecret(req.ClientSecret)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to encrypt client secret")
			return
		}
		p.EncryptedClientSecret = enc
	} else {
		// Preserve existing encrypted secret
		existing, err := h.store.GetOAuthProvider(r.Context(), id)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				writeError(w, http.StatusNotFound, "oauth provider not found")
				return
			}
			writeError(w, http.StatusInternalServerError, "failed to get oauth provider")
			return
		}
		p.EncryptedClientSecret = existing.EncryptedClientSecret
	}

	if err := h.svc.UpdateOAuthProvider(r.Context(), &p); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "oauth provider not found")
			return
		}
		writeErrorDetail(w, http.StatusBadRequest, "failed to update oauth provider", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, newOAuthProviderResponse(&p))
}

func (h *oauthProviderHandler) delete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.store.DeleteOAuthProvider(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "oauth provider not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to delete oauth provider")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *oauthProviderHandler) listTemplates(w http.ResponseWriter, r *http.Request) {
	tpls := oauth.ListTemplates()

	// Replace callback_url placeholder with the direct request origin. The
	// OAuth flow manager deliberately ignores forwarded headers unless a
	// trusted proxy is configured; this dashboard helper keeps the same safe
	// default posture.
	callbackURL := "http"
	if r.TLS != nil {
		callbackURL = "https"
	}
	host := r.Host
	cb := callbackURL + "://" + host + "/api/v1/oauth/callback"

	for i := range tpls {
		tpls[i].CallbackURL = cb
	}

	writeJSON(w, http.StatusOK, tpls)
}

func (h *oauthProviderHandler) encryptSecret(plaintext string) ([]byte, error) {
	if h.encryptor == nil {
		return nil, errors.New("encryption not configured (no age key)")
	}
	return h.encryptor.Encrypt([]byte(strings.TrimSpace(plaintext)))
}
