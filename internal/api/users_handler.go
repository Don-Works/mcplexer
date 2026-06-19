package api

import (
	"errors"
	"net/http"

	"github.com/don-works/mcplexer/internal/store"
)

// usersHandler exposes the M7.1 per-human user identity API. It is a thin
// projection over store.UserStore: list every known user (self + paired
// peers' users) and fetch a single user with the peers they own.
type usersHandler struct {
	store store.UserStore
}

// userWithPeers is the response shape for GET /api/v1/users/{id}: the
// canonical user row plus every peer linked to that user. We never return
// raw peer IDs in the user-visible UI text; the front-end uses display_name.
type userWithPeers struct {
	store.User
	Peers []store.P2PPeer `json:"peers"`
}

// list returns every user row, with self first, ordered by display_name.
// 200 + {"users":[...]} even when the table is empty (callers expect a
// stable shape).
func (h *usersHandler) list(w http.ResponseWriter, r *http.Request) {
	users, err := h.store.ListUsers(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list users: "+err.Error())
		return
	}
	if users == nil {
		users = []store.User{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": users})
}

// get returns one user with the peers they own. 404 when the user_id
// doesn't exist.
func (h *usersHandler) get(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "missing user id")
		return
	}
	u, err := h.store.GetUser(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "user not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "get user: "+err.Error())
		return
	}
	peers, err := h.store.ListPeersForUser(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list peers: "+err.Error())
		return
	}
	if peers == nil {
		peers = []store.P2PPeer{}
	}
	writeJSON(w, http.StatusOK, userWithPeers{User: *u, Peers: peers})
}

// whoamiResponse is the JSON shape of GET /api/v1/users/self. On a fresh
// install the self row may not exist yet (the boot path creates it on
// first run); we return 200 with a structured empty body so a UI
// component can detect the gap and prompt the user to bootstrap, rather
// than receiving a generic 404 and showing a "not found" error.
type whoamiResponse struct {
	User             *store.User `json:"user"`
	SelfBootstrapped bool        `json:"self_bootstrapped"`
}

// self returns the local self user row (or a structured empty when the
// row has not yet been bootstrapped). 200 in both cases so the UI can
// render the "you haven't introduced yourself yet" CTA from the
// self_bootstrapped=false branch.
func (h *usersHandler) self(w http.ResponseWriter, r *http.Request) {
	u, err := h.store.GetSelfUser(r.Context())
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeJSON(w, http.StatusOK, whoamiResponse{User: nil, SelfBootstrapped: false})
			return
		}
		writeError(w, http.StatusInternalServerError, "get self user: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, whoamiResponse{User: u, SelfBootstrapped: true})
}
