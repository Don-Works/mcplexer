package api

import (
	"errors"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/don-works/mcplexer/internal/googlechat"
	"github.com/don-works/mcplexer/internal/secrets"
	"github.com/don-works/mcplexer/internal/store"
)

// googleChatHandler exposes HTTP endpoints for managing the Google Chat
// integration: token (service account JSON), spaces CRUD, test sends, and
// the webhook receiver Google posts events to.
type googleChatHandler struct {
	manager     *googlechat.Manager
	store       store.Store
	secrets     *secrets.Manager
	jwtVerifier *googlechat.JWTVerifier
}

// status reports whether a service account JSON is configured and whether the
// client is live. GET /api/v1/googlechat/status
func (h *googleChatHandler) status(w http.ResponseWriter, r *http.Request) {
	tokenSet := false
	if scope, err := h.store.GetAuthScopeByName(r.Context(), "googlechat"); err == nil && scope != nil {
		if h.secrets != nil {
			if _, err := h.secrets.Get(r.Context(), scope.ID, "service_account_json"); err == nil {
				tokenSet = true
			}
		}
	}
	clientActive := h.manager != nil && h.manager.HasClient()
	writeJSON(w, http.StatusOK, map[string]any{
		"token_set":     tokenSet,
		"client_active": clientActive,
	})
}

// storeToken writes the Google Chat service account JSON to the secrets store.
// POST /api/v1/googlechat/token  body:{service_account_json}
func (h *googleChatHandler) storeToken(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ServiceAccountJSON string `json:"service_account_json"`
	}
	if err := decodeJSON(r, &body); err != nil || body.ServiceAccountJSON == "" {
		writeError(w, http.StatusBadRequest, "service_account_json is required")
		return
	}
	if h.secrets == nil {
		writeError(w, http.StatusServiceUnavailable, "secrets manager not configured")
		return
	}
	// Validate the JSON parses + has the expected fields before persisting.
	if _, err := googlechat.ParseServiceAccountKey([]byte(body.ServiceAccountJSON)); err != nil {
		writeError(w, http.StatusBadRequest, "invalid service account JSON: "+err.Error())
		return
	}
	scope, err := ensureGoogleChatScope(r.Context(), h.store)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to ensure auth scope: "+err.Error())
		return
	}
	if err := h.secrets.Put(r.Context(), scope.ID, "service_account_json", []byte(body.ServiceAccountJSON)); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to store token")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"status":   "ok",
		"scope_id": scope.ID,
		"hint":     "restart MCPlexer so the client picks up the new credentials",
	})
}

// listSpaces returns every GoogleChatSpace row.
// GET /api/v1/googlechat/spaces
func (h *googleChatHandler) listSpaces(w http.ResponseWriter, r *http.Request) {
	spaces, err := h.store.ListGoogleChatSpaces(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list spaces")
		return
	}
	if spaces == nil {
		spaces = []store.GoogleChatSpace{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"spaces": spaces})
}

// updateSpace patches a space's min_priority and/or listen_mode.
// PATCH /api/v1/googlechat/spaces/{id}  body:{min_priority?, listen_mode?}
func (h *googleChatHandler) updateSpace(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		MinPriority string `json:"min_priority"`
		ListenMode  string `json:"listen_mode"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.MinPriority == "" && body.ListenMode == "" {
		writeError(w, http.StatusBadRequest, "at least one of min_priority|listen_mode required")
		return
	}
	if body.MinPriority != "" {
		if !validPriority(body.MinPriority) {
			writeError(w, http.StatusBadRequest, "min_priority must be one of critical|high|normal|low")
			return
		}
		if err := h.store.UpdateGoogleChatSpaceMinPriority(r.Context(), id, body.MinPriority); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to update space")
			return
		}
	}
	if body.ListenMode != "" {
		if body.ListenMode != "mention" && body.ListenMode != "all" {
			writeError(w, http.StatusBadRequest, "listen_mode must be mention|all")
			return
		}
		if err := h.store.UpdateGoogleChatSpaceListenMode(r.Context(), id, body.ListenMode); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to update space")
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// deleteSpace marks a space inactive.
// DELETE /api/v1/googlechat/spaces/{id}
func (h *googleChatHandler) deleteSpace(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}
	if err := h.store.DeactivateGoogleChatSpace(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "space not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to deactivate space")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// testMessage sends a one-off message to a specific space.
// POST /api/v1/googlechat/test-message  body:{space_id, text}
func (h *googleChatHandler) testMessage(w http.ResponseWriter, r *http.Request) {
	if h.manager == nil || !h.manager.HasClient() {
		writeError(w, http.StatusServiceUnavailable,
			"googlechat client not active — set a service account and restart MCPlexer")
		return
	}
	var body struct {
		SpaceID string `json:"space_id"`
		Text    string `json:"text"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.SpaceID == "" || body.Text == "" {
		writeError(w, http.StatusBadRequest, "space_id and text are required")
		return
	}
	if err := h.manager.SendBySpaceID(r.Context(), body.SpaceID, body.Text, "normal"); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// createPairing generates a pairing code scoped to a workspace.
// POST /api/v1/googlechat/pairings  body:{workspace_id}
func (h *googleChatHandler) createPairing(w http.ResponseWriter, r *http.Request) {
	if h.manager == nil {
		writeError(w, http.StatusServiceUnavailable, "googlechat manager not configured")
		return
	}
	var body struct {
		WorkspaceID string `json:"workspace_id"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.WorkspaceID == "" {
		writeError(w, http.StatusBadRequest, "workspace_id is required")
		return
	}
	p, err := h.manager.CreatePairing(r.Context(), body.WorkspaceID, "", 0)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create pairing")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"code":       p.Code,
		"expires_at": p.ExpiresAt,
	})
}

// events is the Google Chat webhook endpoint. Validates the Bearer JWT (when
// the GOOGLECHAT_REQUIRE_JWT_VALIDATION env var is set), parses the event
// payload, and pushes it onto the manager's inbound channel.
// POST /api/v1/googlechat/events
func (h *googleChatHandler) events(w http.ResponseWriter, r *http.Request) {
	if h.manager == nil {
		writeError(w, http.StatusServiceUnavailable, "googlechat manager not configured")
		return
	}
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, "failed to read body")
		return
	}
	if requireJWTValidation() {
		auth := r.Header.Get("Authorization")
		token := strings.TrimPrefix(auth, "Bearer ")
		if token == "" || token == auth {
			writeError(w, http.StatusUnauthorized, "missing Bearer token")
			return
		}
		if h.jwtVerifier == nil {
			writeError(w, http.StatusServiceUnavailable, "JWT verifier not configured")
			return
		}
		if _, err := h.jwtVerifier.Verify(r.Context(), token); err != nil {
			writeError(w, http.StatusUnauthorized, "JWT verification failed: "+err.Error())
			return
		}
	}
	botName := ""
	if h.manager.HasClient() {
		// botName isn't exposed via Manager — fetch via env var fallback.
		botName = os.Getenv("GOOGLECHAT_BOT_NAME")
	}
	msg, ok := googlechat.ParseEvent(raw, botName)
	if !ok {
		// Echo a 200 to Google so it doesn't retry on opaque events; just
		// log and move on.
		writeJSON(w, http.StatusOK, map[string]string{"status": "ignored"})
		return
	}
	h.manager.PushInbound(msg)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// requireJWTValidation reports whether incoming events must carry a valid
// Google-signed bearer JWT. Default ON (fail-closed). Operators set
// `GOOGLECHAT_DISABLE_JWT_VALIDATION=true` to opt out for local dev without
// a configured Google Chat bot project.
func requireJWTValidation() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("GOOGLECHAT_DISABLE_JWT_VALIDATION")))
	return v != "1" && v != "true" && v != "yes"
}
