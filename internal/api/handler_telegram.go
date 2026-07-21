package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/don-works/mcplexer/internal/secrets"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/telegram"
)

// telegramHandler exposes HTTP endpoints for managing the Telegram integration
// from the dashboard: token, pairings, chats, test sends.
type telegramHandler struct {
	manager *telegram.Manager
	store   store.Store
	secrets *secrets.Manager
}

// createPairing generates a pairing code scoped to a workspace.
// POST /api/v1/telegram/pairings  body:{workspace_id}
func (h *telegramHandler) createPairing(w http.ResponseWriter, r *http.Request) {
	if h.manager == nil || !h.manager.HasClient() {
		writeError(w, http.StatusServiceUnavailable,
			"telegram client not active — set a bot token and restart MCPlexer")
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
	p, err := h.manager.CreatePairing(r.Context(), "telegram", body.WorkspaceID, "", 0)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create pairing")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"code":       p.Code,
		"expires_at": p.ExpiresAt,
	})
}

// listChats returns every TelegramChat row.
// GET /api/v1/telegram/chats
func (h *telegramHandler) listChats(w http.ResponseWriter, r *http.Request) {
	chats, err := h.store.ListTelegramChats(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list chats")
		return
	}
	if chats == nil {
		chats = []store.TelegramChat{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"chats": chats})
}

// deleteChat marks a chat inactive.
// DELETE /api/v1/telegram/chats/{id}
func (h *telegramHandler) deleteChat(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}
	if err := h.store.DeactivateTelegramChat(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "chat not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to deactivate chat")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// updateChatPriority patches a chat's min_priority filter.
// PATCH /api/v1/telegram/chats/{id}  body:{min_priority}
func (h *telegramHandler) updateChatPriority(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var body struct {
		MinPriority string `json:"min_priority"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if !validPriority(body.MinPriority) {
		writeError(w, http.StatusBadRequest, "min_priority must be one of critical|high|normal|low")
		return
	}
	if err := h.store.UpdateTelegramChatMinPriority(r.Context(), id, body.MinPriority); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update chat")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// storeToken writes the Telegram bot token to the secrets store.
// POST /api/v1/telegram/token  body:{token}
func (h *telegramHandler) storeToken(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token string `json:"token"`
	}
	if err := decodeJSON(r, &body); err != nil || body.Token == "" {
		writeError(w, http.StatusBadRequest, "token is required")
		return
	}
	if h.secrets == nil {
		writeError(w, http.StatusServiceUnavailable, "secrets manager not configured")
		return
	}
	scope, err := ensureTelegramScope(r.Context(), h.store)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to ensure auth scope: "+err.Error())
		return
	}
	if err := h.secrets.Put(r.Context(), scope.ID, "bot_token", []byte(body.Token)); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to store token")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"status":   "ok",
		"scope_id": scope.ID,
		"hint":     "restart MCPlexer so the client picks up the new token",
	})
}

// testMessage sends a one-off message to a specific chat.
// POST /api/v1/telegram/test-message  body:{chat_id, text}
func (h *telegramHandler) testMessage(w http.ResponseWriter, r *http.Request) {
	if h.manager == nil || !h.manager.HasClient() {
		writeError(w, http.StatusServiceUnavailable,
			"telegram client not active — set a bot token and restart MCPlexer")
		return
	}
	var body struct {
		ChatID string `json:"chat_id"`
		Text   string `json:"text"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.ChatID == "" || body.Text == "" {
		writeError(w, http.StatusBadRequest, "chat_id and text are required")
		return
	}
	if err := h.manager.SendByChatID(r.Context(), body.ChatID, body.Text, "normal"); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// status reports whether a bot token is configured and whether the client is live.
// GET /api/v1/telegram/status
func (h *telegramHandler) status(w http.ResponseWriter, r *http.Request) {
	tokenSet := false
	if scope, err := h.store.GetAuthScopeByName(r.Context(), "telegram"); err == nil && scope != nil {
		if h.secrets != nil {
			if _, err := h.secrets.Get(r.Context(), scope.ID, "bot_token"); err == nil {
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

func validPriority(p string) bool {
	switch p {
	case "critical", "high", "normal", "low":
		return true
	}
	return false
}

// ensureTelegramScope returns (creating if missing) the AuthScope used to store
// the Telegram bot token.
func ensureTelegramScope(ctx context.Context, s store.Store) (*store.AuthScope, error) {
	existing, err := s.GetAuthScopeByName(ctx, "telegram")
	if err == nil && existing != nil {
		return existing, nil
	}
	if !errors.Is(err, store.ErrNotFound) && err != nil {
		return nil, fmt.Errorf("look up scope: %w", err)
	}
	now := time.Now().UTC()
	scope := &store.AuthScope{
		ID:        ulid.Make().String(),
		Name:      "telegram",
		Type:      "telegram",
		Source:    "telegram",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.CreateAuthScope(ctx, scope); err != nil {
		return nil, fmt.Errorf("create scope: %w", err)
	}
	return scope, nil
}

// TelegramTokenFromSecrets reads the current stored bot token. Returns "" if
// none configured. Used by serve.go to decide whether to boot the client.
func TelegramTokenFromSecrets(ctx context.Context, s store.Store, sm *secrets.Manager) (string, error) {
	if sm == nil {
		return "", nil
	}
	scope, err := s.GetAuthScopeByName(ctx, "telegram")
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return "", nil
		}
		return "", err
	}
	// Prefer new "bot_token" key; fall back to legacy "telegram_token" so
	// tokens written before this refactor still work.
	for _, key := range []string{"bot_token", "telegram_token"} {
		val, err := sm.Get(ctx, scope.ID, key)
		if err == nil {
			return string(val), nil
		}
		if !errors.Is(err, store.ErrNotFound) {
			return "", err
		}
	}
	return "", nil
}
