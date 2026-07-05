// memory_embeddings_handler.go — REST surface for configuring semantic
// (vector) memory recall from the dashboard's Settings → Memory →
// Embeddings panel. Without a vector provider, recall degrades silently to
// keyword-only (FTS5); these endpoints make wiring one a one-click,
// in-product action with auto-detection + a visible backfill of the
// existing corpus.
//
// Routes (all under /api/v1/memory/embeddings):
//
//	GET    /api/v1/memory/embeddings/status    → provider, active, backfill progress
//	POST   /api/v1/memory/embeddings/detect    → probe localhost for a usable endpoint
//	POST   /api/v1/memory/embeddings/configure  → persist + hot-swap + backfill
//	POST   /api/v1/memory/embeddings/backfill   → (re)run the backfill now
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/don-works/mcplexer/internal/config"
	"github.com/don-works/mcplexer/internal/memory"
)

// embeddingsHandler wires the memory service (hot-swap + backfill) and the
// settings service (persisted provider config).
type embeddingsHandler struct {
	svc      *memory.Service
	settings *config.SettingsService
}

func newEmbeddingsHandler(svc *memory.Service, settings *config.SettingsService) *embeddingsHandler {
	return &embeddingsHandler{svc: svc, settings: settings}
}

// embeddingsStatus is the GET /status payload + the configure/backfill
// response: the configured provider plus live backfill progress.
type embeddingsStatus struct {
	Provider       string `json:"provider"`
	BaseURL        string `json:"base_url,omitempty"`
	Model          string `json:"model,omitempty"`
	EmbedderActive bool   `json:"embedder_active"`
	Running        bool   `json:"running"`
	Pending        int    `json:"pending"`
	Embedded       int    `json:"embedded"`
	Total          int    `json:"total"`
}

func (h *embeddingsHandler) status(ctx context.Context) embeddingsStatus {
	s := h.settings.Load(ctx)
	bf := h.svc.BackfillStatus(ctx)
	provider := strings.TrimSpace(s.MemoryEmbedProvider)
	if provider == "" {
		provider = "auto"
	}
	return embeddingsStatus{
		Provider:       provider,
		BaseURL:        s.MemoryEmbedBaseURL,
		Model:          s.MemoryEmbedModel,
		EmbedderActive: bf.EmbedderActive,
		Running:        bf.Running,
		Pending:        bf.Pending,
		Embedded:       bf.Embedded,
		Total:          bf.Total,
	}
}

// handleStatus serves GET /api/v1/memory/embeddings/status.
func (h *embeddingsHandler) handleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, h.status(r.Context()))
}

// handleDetect serves POST /api/v1/memory/embeddings/detect. Probes the
// local OpenAI-compatible servers WITHOUT applying anything, so the UI can
// pre-fill the form with a found endpoint for the user to confirm.
func (h *embeddingsHandler) handleDetect(w http.ResponseWriter, r *http.Request) {
	det, ok := memory.DetectLocalEmbedEndpoint(r.Context())
	writeJSON(w, http.StatusOK, map[string]any{
		"found":    ok,
		"base_url": det.BaseURL,
		"model":    det.Model,
	})
}

// configureRequest is the POST /configure body. openai_key is used for the
// live session + persisted nowhere (settings JSON never holds a raw key);
// re-supply it after a restart, or set MCPLEXER_OPENAI_API_KEY.
type configureRequest struct {
	Provider  string `json:"provider"` // auto | local | openai | none
	BaseURL   string `json:"base_url"`
	Model     string `json:"model"`
	OpenAIKey string `json:"openai_key,omitempty"`
}

// handleConfigure serves POST /api/v1/memory/embeddings/configure. It
// validates + builds the provider, persists the (non-secret) config, hot-
// swaps the live embedder, and kicks off a backfill of the existing corpus.
func (h *embeddingsHandler) handleConfigure(w http.ResponseWriter, r *http.Request) {
	var req configureRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	provider := strings.ToLower(strings.TrimSpace(req.Provider))
	if provider == "" {
		provider = "auto"
	}

	emb := memory.EmbedProvider(memory.NoopEmbedder{})
	baseURL, model := strings.TrimSpace(req.BaseURL), strings.TrimSpace(req.Model)
	switch provider {
	case "none":
		// keep noop
	case "local":
		e, err := memory.NewLocalEmbedder(baseURL, model, "")
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := probeEmbedder(r.Context(), e); err != nil {
			writeErrorDetail(w, http.StatusBadGateway,
				"embedding endpoint not usable", err.Error())
			return
		}
		emb = e
	case "openai":
		key := strings.TrimSpace(req.OpenAIKey)
		if key == "" {
			key = os.Getenv("MCPLEXER_OPENAI_API_KEY")
		}
		e, err := memory.NewOpenAIEmbedder(key, "", model)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := probeEmbedder(r.Context(), e); err != nil {
			writeErrorDetail(w, http.StatusBadGateway,
				"OpenAI embeddings call failed", err.Error())
			return
		}
		emb = e
	case "auto":
		if det, ok := memory.DetectLocalEmbedEndpoint(r.Context()); ok {
			if e, err := memory.NewLocalEmbedder(det.BaseURL, det.Model, ""); err == nil {
				emb = e
				baseURL, model = det.BaseURL, det.Model
			}
		}
	default:
		writeError(w, http.StatusBadRequest, "unknown provider: "+provider)
		return
	}

	// Persist the non-secret config so it survives restart.
	s := h.settings.Load(r.Context())
	s.MemoryEmbedProvider = provider
	s.MemoryEmbedBaseURL = baseURL
	s.MemoryEmbedModel = model
	if err := h.settings.Save(r.Context(), s); err != nil {
		writeErrorDetail(w, http.StatusInternalServerError, "save settings failed", err.Error())
		return
	}

	// Hot-swap the live provider + backfill the existing corpus.
	h.svc.SetEmbedder(emb)
	h.svc.StartBackfillAsync(r.Context())
	writeJSON(w, http.StatusOK, h.status(r.Context()))
}

// handleBackfill serves POST /api/v1/memory/embeddings/backfill. Re-runs
// the backfill of any un-embedded memories with the live provider.
func (h *embeddingsHandler) handleBackfill(w http.ResponseWriter, r *http.Request) {
	if !h.svc.EmbedderActive() {
		writeError(w, http.StatusBadRequest,
			"no embedding provider is active — configure one first")
		return
	}
	h.svc.StartBackfillAsync(r.Context())
	writeJSON(w, http.StatusOK, h.status(r.Context()))
}

// probeEmbedder confirms a provider actually returns an EmbedDim-wide
// vector for a tiny input, so a broken endpoint / wrong model is rejected
// at configure time instead of silently producing zero recall hits.
func probeEmbedder(ctx context.Context, emb memory.EmbedProvider) error {
	if !emb.HasModel() {
		return nil
	}
	vecs, _, err := emb.Embed(ctx, []string{"ping"})
	if err != nil {
		return err
	}
	if len(vecs) != 1 || len(vecs[0]) != memory.EmbedDim {
		return fmt.Errorf("model must emit %d-dim vectors", memory.EmbedDim)
	}
	return nil
}
