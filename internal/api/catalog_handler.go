package api

import (
	"net/http"

	"github.com/don-works/mcplexer/internal/config"
)

type catalogHandler struct {
	svc *config.CatalogService
}

func (h *catalogHandler) list(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, h.svc.Get())
}
