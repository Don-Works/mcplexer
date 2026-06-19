// Package api — memory_offers_handler.go exposes the peer-offered memory
// surface. Incoming offers from libp2p peers are recorded via
// /mcplexer/memory/1.0.0 and stored in the memory_offers table; the
// dashboard lists them, then the human accepts (importing into local
// memories) or declines.
//
// Routes (registered alongside memory_handler.go when the memory service
// is wired):
//
//	GET    /api/v1/memory/offers                  → list (filters in querystring)
//	POST   /api/v1/memory/offers/{id}/accept      → accept (body: local_memory_id)
//	POST   /api/v1/memory/offers/{id}/decline     → decline
package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/don-works/mcplexer/internal/store"
)

// handleListOffers serves GET /api/v1/memory/offers.
//
// Querystring: pending_only=1, peer_id=<id>, limit=<n>.
func (h *memoryHandler) handleListOffers(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.MemoryOfferFilter{
		PeerID:      strings.TrimSpace(q.Get("peer_id")),
		PendingOnly: parseBoolQ(q.Get("pending_only")),
	}
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			f.Limit = n
		}
	}
	// When pending_only is false default to including done rows so the UI
	// can render a "history" view. Callers can still set pending_only=1
	// to suppress the noise.
	if !f.PendingOnly {
		f.IncludeDone = true
	}
	offers, err := h.store.ListMemoryOffers(r.Context(), f)
	if err != nil {
		writeErrorDetail(w, http.StatusInternalServerError,
			"failed to list memory offers", err.Error())
		return
	}
	if offers == nil {
		offers = []store.MemoryOffer{}
	}
	writeJSON(w, http.StatusOK, offers)
}

// acceptOfferRequest is the POST /api/v1/memory/offers/{id}/accept body.
type acceptOfferRequest struct {
	LocalMemoryID string `json:"local_memory_id"`
}

// handleAcceptOffer serves POST /api/v1/memory/offers/{id}/accept.
//
// local_memory_id is REQUIRED — it's the ID of the local memories row
// the imported content was written to. The handler does NOT itself fetch
// the remote content; that's the p2p layer's job. The dashboard hits this
// endpoint AFTER the import worker has landed the row, to flip the offer
// row's accepted_at + accepted_as_id pointer.
func (h *memoryHandler) handleAcceptOffer(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}
	var body acceptOfferRequest
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(body.LocalMemoryID) == "" {
		writeError(w, http.StatusBadRequest, "local_memory_id is required")
		return
	}
	if err := h.store.AcceptMemoryOffer(r.Context(), id, body.LocalMemoryID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "offer not found")
			return
		}
		writeErrorDetail(w, http.StatusInternalServerError,
			"accept failed", err.Error())
		return
	}
	// Fan an offer_accepted event so the dashboard drops the row from
	// the pending list + the /memory page surfaces the import. peer_id
	// is best-effort: looking it up here would require another store
	// round-trip and the dashboard reconciles via the offers list anyway.
	h.svc.NotifyOfferAccepted(r.Context(), id, "", body.LocalMemoryID)
	w.WriteHeader(http.StatusNoContent)
}

// handleDeclineOffer serves POST /api/v1/memory/offers/{id}/decline.
func (h *memoryHandler) handleDeclineOffer(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}
	if err := h.store.DeclineMemoryOffer(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "offer not found")
			return
		}
		writeErrorDetail(w, http.StatusInternalServerError,
			"decline failed", err.Error())
		return
	}
	// Fan an offer_declined event so the pending offers tile updates
	// without a manual reload.
	h.svc.NotifyOfferDeclined(r.Context(), id, "")
	w.WriteHeader(http.StatusNoContent)
}
