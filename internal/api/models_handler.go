package api

import (
	"net/http"

	"github.com/don-works/mcplexer/internal/models"
)

// modelsHandler serves the live model catalog: per enabled provider, the
// models it currently offers, whether that list was live-probed or is a
// static fallback, when it was last refreshed, and the auth state observed.
// This is the surface that makes the catalog "constantly updated" and
// visible — the operator (and the UI) can see freshness and never trust a
// stale list silently.
type modelsHandler struct {
	catalog models.CatalogReader
}

// list handles GET /api/v1/models. It returns the last cached snapshot and
// never triggers a live probe, so it is cheap and cannot hang on a wedged
// provider. The response embeds refreshed_at (and per-provider
// last_refreshed) so freshness is observable.
func (h *modelsHandler) list(w http.ResponseWriter, r *http.Request) {
	cat := h.catalog.Catalog()
	if cat.Providers == nil {
		cat.Providers = []models.ProviderCatalog{}
	}
	writeJSON(w, http.StatusOK, cat)
}
