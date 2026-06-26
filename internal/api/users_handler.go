package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/don-works/mcplexer/internal/config"
	"github.com/don-works/mcplexer/internal/store"
)

type userAdminStore interface {
	DeleteUser(ctx context.Context, userID string) error
}

type userDeviceAdminStore interface {
	GetPeer(ctx context.Context, peerID string) (*store.P2PPeer, error)
	RelinkPeerToUser(ctx context.Context, peerID, userID string) error
	UnlinkPeerFromUsers(ctx context.Context, peerID string) error
}

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

// list returns every real human user row, with self first, ordered by
// display_name. Legacy pairings may have created fallback user rows from peer
// IDs or device display names; those rows represent devices, not people, so
// they are hidden here.
// 200 + {"users":[...]} even when the table is empty (callers expect a
// stable shape).
func (h *usersHandler) list(w http.ResponseWriter, r *http.Request) {
	users, err := h.store.ListUsers(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list users: "+err.Error())
		return
	}
	people := make([]store.User, 0, len(users))
	for _, u := range users {
		synthetic, err := h.isSyntheticDeviceUser(r.Context(), u)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "classify user: "+err.Error())
			return
		}
		if !synthetic {
			people = append(people, u)
		}
	}
	if people == nil {
		people = []store.User{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": people})
}

func (h *usersHandler) isSyntheticDeviceUser(ctx context.Context, u store.User) (bool, error) {
	if u.IsSelf || u.UserID == "" {
		return false, nil
	}
	peers, err := h.store.ListPeersForUser(ctx, u.UserID)
	if err != nil {
		return false, err
	}
	if len(peers) == 0 {
		return false, nil
	}
	display := strings.TrimSpace(u.DisplayName)
	if len(peers) == 1 {
		peer := peers[0]
		if display == strings.TrimSpace(peer.DisplayName) || display == shortPeerID(peer.PeerID) {
			return true, nil
		}
	}
	for _, peer := range peers {
		if u.UserID != config.SyntheticUserIDForPeer(peer.PeerID) {
			return false, nil
		}
	}
	return true, nil
}

type createUserRequest struct {
	DisplayName string `json:"display_name"`
}

// create inserts an explicit human identity. Device rows are still created by
// pairing; this endpoint is only for people the operator wants to assign
// devices or tasks to.
func (h *usersHandler) create(w http.ResponseWriter, r *http.Request) {
	var req createUserRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "decode: "+err.Error())
		return
	}
	displayName := strings.TrimSpace(req.DisplayName)
	if displayName == "" {
		writeError(w, http.StatusBadRequest, "display_name is required")
		return
	}
	u := &store.User{
		UserID:      uuid.NewString(),
		DisplayName: displayName,
	}
	if err := h.store.CreateUser(r.Context(), u); err != nil {
		writeError(w, http.StatusInternalServerError, "create user: "+err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, u)
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

type updateUserRequest struct {
	DisplayName *string `json:"display_name"`
}

// update renames an explicit human identity. Ownership links stay unchanged.
func (h *usersHandler) update(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "missing user id")
		return
	}
	var req updateUserRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "decode: "+err.Error())
		return
	}
	if req.DisplayName == nil {
		writeError(w, http.StatusBadRequest, "display_name is required")
		return
	}
	displayName := strings.TrimSpace(*req.DisplayName)
	if displayName == "" {
		writeError(w, http.StatusBadRequest, "display_name is required")
		return
	}
	if err := h.store.UpdateUserDisplayName(r.Context(), id, displayName); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "user not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "update user: "+err.Error())
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
	writeJSON(w, http.StatusOK, u)
}

// delete removes a stale non-self human identity, but only after confirming it
// has no linked devices. Device revocation stays on /api/p2p/peers/{id}; this
// endpoint is for cleaning up orphaned people rows left by old pairing flows.
func (h *usersHandler) delete(w http.ResponseWriter, r *http.Request) {
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
	if u.IsSelf {
		writeError(w, http.StatusBadRequest, "cannot delete the local self identity")
		return
	}
	peers, err := h.store.ListPeersForUser(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list peers: "+err.Error())
		return
	}
	if len(peers) > 0 {
		writeError(w, http.StatusConflict, "identity still has linked devices")
		return
	}
	deleter, ok := h.store.(userAdminStore)
	if !ok {
		writeError(w, http.StatusNotImplemented, "user deletion is not supported by this store")
		return
	}
	if err := deleter.DeleteUser(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "user not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "delete user: "+err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// updateDeviceOwner reassigns a paired peer (device) to a human identity, or
// unlinks it when user_id is null/empty. This is deliberately scoped to the
// ownership join table; device trust/revocation stays on the p2p peer routes.
func (h *usersHandler) updateDeviceOwner(w http.ResponseWriter, r *http.Request) {
	peerID := r.PathValue("peer_id")
	if peerID == "" {
		writeError(w, http.StatusBadRequest, "missing peer id")
		return
	}
	admin, ok := h.store.(userDeviceAdminStore)
	if !ok {
		writeError(w, http.StatusNotImplemented, "device ownership is not supported by this store")
		return
	}
	peer, err := admin.GetPeer(r.Context(), peerID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "peer not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "get peer: "+err.Error())
		return
	}
	if peer == nil {
		writeError(w, http.StatusNotFound, "peer not found")
		return
	}
	var req map[string]*string
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "decode: "+err.Error())
		return
	}
	rawUserID, ok := req["user_id"]
	if !ok {
		writeError(w, http.StatusBadRequest, "user_id is required; use null or empty string to unlink")
		return
	}
	userID := ""
	if rawUserID != nil {
		userID = strings.TrimSpace(*rawUserID)
	}
	if userID == "" {
		if err := admin.UnlinkPeerFromUsers(r.Context(), peerID); err != nil {
			writeError(w, http.StatusInternalServerError, "unlink device owner: "+err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"peer_id": peerID, "user_id": nil})
		return
	}
	if _, err := h.store.GetUser(r.Context(), userID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "user not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "get user: "+err.Error())
		return
	}
	if err := admin.RelinkPeerToUser(r.Context(), peerID, userID); err != nil {
		writeError(w, http.StatusInternalServerError, "update device owner: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"peer_id": peerID, "user_id": userID})
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
