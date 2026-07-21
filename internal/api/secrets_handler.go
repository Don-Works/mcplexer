package api

import (
	"errors"
	"net/http"
	"sort"

	"github.com/don-works/mcplexer/internal/secrets"
	"github.com/don-works/mcplexer/internal/store"
)

type secretsHandler struct {
	manager *secrets.Manager
	store   store.AuthScopeStore
}

// listKeys returns secret key names (not values) for an auth scope.
func (h *secretsHandler) listKeys(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	keys, err := h.manager.List(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "auth scope not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to list secrets")
		return
	}
	sort.Strings(keys)
	writeJSON(w, http.StatusOK, map[string][]string{"keys": keys})
}

// put stores a secret key-value pair in the auth scope.
func (h *secretsHandler) put(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}
	if err := decodeJSON(r, &body); err != nil || body.Key == "" || body.Value == "" {
		writeError(w, http.StatusBadRequest, "key and value are required")
		return
	}

	if err := h.manager.Put(r.Context(), id, body.Key, []byte(body.Value)); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "auth scope not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to store secret")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// remove deletes a secret key from the auth scope.
func (h *secretsHandler) remove(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	key := r.PathValue("key")
	if key == "" {
		writeError(w, http.StatusBadRequest, "key is required")
		return
	}

	if err := h.manager.Delete(r.Context(), id, key); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "secret not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to delete secret")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
