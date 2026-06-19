package api

import (
	"errors"
	"net/http"

	"github.com/don-works/mcplexer/internal/config"
	"github.com/don-works/mcplexer/internal/store"
)

// envFieldResponse is the JSON representation of a required env field.
type envFieldResponse struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Secret bool   `json:"secret"`
}

type authHandler struct {
	svc   *config.Service
	store store.AuthScopeStore
}

// authScopeResponse masks EncryptedData from API responses.
type authScopeResponse struct {
	store.AuthScope
	HasSecrets bool               `json:"has_secrets"`
	EnvFields  []envFieldResponse `json:"env_fields,omitempty"`
}

func newAuthScopeResponse(a *store.AuthScope) authScopeResponse {
	resp := authScopeResponse{
		AuthScope:  *a,
		HasSecrets: len(a.EncryptedData) > 0,
	}
	if fields := config.GetEnvFields(a.ID); len(fields) > 0 {
		resp.EnvFields = make([]envFieldResponse, len(fields))
		for i, f := range fields {
			resp.EnvFields[i] = envFieldResponse{Key: f.Key, Label: f.Label, Secret: f.Secret}
		}
	}
	return resp
}

func (h *authHandler) list(w http.ResponseWriter, r *http.Request) {
	scopes, err := h.store.ListAuthScopes(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list auth scopes")
		return
	}
	resp := make([]authScopeResponse, len(scopes))
	for i := range scopes {
		resp[i] = newAuthScopeResponse(&scopes[i])
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *authHandler) get(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	scope, err := h.store.GetAuthScope(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "auth scope not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to get auth scope")
		return
	}
	writeJSON(w, http.StatusOK, newAuthScopeResponse(scope))
}

func (h *authHandler) create(w http.ResponseWriter, r *http.Request) {
	var a store.AuthScope
	if err := decodeJSON(r, &a); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if err := h.svc.CreateAuthScope(r.Context(), &a); err != nil {
		if errors.Is(err, store.ErrAlreadyExists) {
			writeError(w, http.StatusConflict, "auth scope already exists")
			return
		}
		writeErrorDetail(w, http.StatusBadRequest, "failed to create auth scope", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, newAuthScopeResponse(&a))
}

func (h *authHandler) update(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var a store.AuthScope
	if err := decodeJSON(r, &a); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	a.ID = id
	if err := h.svc.UpdateAuthScope(r.Context(), &a); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "auth scope not found")
			return
		}
		writeErrorDetail(w, http.StatusBadRequest, "failed to update auth scope", err.Error())
		return
	}
	// Re-read from store to get accurate encrypted_data / has_secrets status.
	updated, err := h.store.GetAuthScope(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusOK, newAuthScopeResponse(&a))
		return
	}
	writeJSON(w, http.StatusOK, newAuthScopeResponse(updated))
}

func (h *authHandler) delete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := h.store.DeleteAuthScope(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "auth scope not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to delete auth scope")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
