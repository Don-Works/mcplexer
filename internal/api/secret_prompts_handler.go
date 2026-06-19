package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/don-works/mcplexer/internal/secrets/ephemeral"
	"github.com/don-works/mcplexer/internal/store"
)

// secretPromptsHandler exposes the human-side of the secret-prompt flow.
// SECURITY: the GET /pending response NEVER includes file_path. Only the
// agent (via the gateway tool dispatch) ever receives the path.
type secretPromptsHandler struct {
	manager *ephemeral.Manager
	store   store.SecretPromptStore
}

// pendingDTO is the shape returned by GET /api/v1/secrets/prompts/pending.
// Mirrors store.SecretPrompt minus file_path (excluded for safety even
// though the model also tags it json:"-"; this is a defence-in-depth copy).
type pendingDTO struct {
	ID        string    `json:"id"`
	Reason    string    `json:"reason"`
	Label     string    `json:"label"`
	Requester string    `json:"requester"`
	Status    string    `json:"status"`
	ExpiresAt time.Time `json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
}

func toPendingDTO(p store.SecretPrompt) pendingDTO {
	return pendingDTO{
		ID:        p.ID,
		Reason:    p.Reason,
		Label:     p.Label,
		Requester: p.Requester,
		Status:    p.Status,
		ExpiresAt: p.ExpiresAt,
		CreatedAt: p.CreatedAt,
	}
}

// listPending returns currently-pending prompts. UI initial fetch pairs with
// the SSE stream for live updates.
func (h *secretPromptsHandler) listPending(w http.ResponseWriter, r *http.Request) {
	rows, err := h.store.ListPendingSecretPrompts(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError,
			fmt.Sprintf("list pending: %v", err))
		return
	}
	out := make([]pendingDTO, 0, len(rows))
	for _, p := range rows {
		out = append(out, toPendingDTO(p))
	}
	writeJSON(w, http.StatusOK, out)
}

// submit accepts the user-supplied secret value, writes it to the daemon-
// owned 0600 file, and unblocks the agent's tool call. The body's `value`
// field is consumed once and zeroed before the function returns.
func (h *secretPromptsHandler) submit(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}
	var body struct {
		Value string `json:"value"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if body.Value == "" {
		writeError(w, http.StatusBadRequest, "value is required")
		return
	}

	// Convert to []byte and best-effort wipe local copy after submit.
	secret := []byte(body.Value)
	body.Value = ""
	defer func() {
		for i := range secret {
			secret[i] = 0
		}
	}()

	err := h.manager.Submit(r.Context(), id, secret)
	if err != nil {
		switch {
		case errors.Is(err, ephemeral.ErrPromptNotFound),
			errors.Is(err, ephemeral.ErrPromptAlreadyResolved):
			writeError(w, http.StatusNotFound, "prompt not found or already resolved")
		default:
			writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "submitted"})
}

// cancel resolves the prompt with ErrUserCancelled.
func (h *secretPromptsHandler) cancel(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}
	if err := h.manager.Cancel(r.Context(), id); err != nil {
		if errors.Is(err, ephemeral.ErrPromptNotFound) {
			writeError(w, http.StatusNotFound, "prompt not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "cancelled"})
}

// secretPromptsSSEHandler streams Event values to the UI. file_path is
// never included — the bus never publishes the path.
type secretPromptsSSEHandler struct {
	bus *ephemeral.Bus
}

func (h *secretPromptsSSEHandler) stream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}
	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{})

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher.Flush()

	ch := h.bus.Subscribe()
	defer h.bus.Unsubscribe(ch)
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case evt, ok := <-ch:
			if !ok {
				return
			}
			data, err := json.Marshal(evt)
			if err != nil {
				continue
			}
			_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		case <-heartbeat.C:
			_, _ = fmt.Fprint(w, ":\n\n")
			flusher.Flush()
		}
	}
}
