package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// fakeUserStore is a minimal in-memory store.UserStore for handler tests.
// Every call lands in either users or peerUsers; methods the handler
// doesn't call panic so accidental dependencies surface immediately.
type fakeUserStore struct {
	users     map[string]store.User
	peers     map[string]store.P2PPeer // peerID -> peer
	peerUsers map[string][]string      // userID -> []peerID
}

func newFakeUserStore() *fakeUserStore {
	return &fakeUserStore{
		users:     make(map[string]store.User),
		peers:     make(map[string]store.P2PPeer),
		peerUsers: make(map[string][]string),
	}
}

func (f *fakeUserStore) CreateUser(_ context.Context, u *store.User) error {
	f.users[u.UserID] = *u
	return nil
}

func (f *fakeUserStore) GetUser(_ context.Context, id string) (*store.User, error) {
	u, ok := f.users[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return &u, nil
}

func (f *fakeUserStore) GetSelfUser(_ context.Context) (*store.User, error) {
	for _, u := range f.users {
		if u.IsSelf {
			return &u, nil
		}
	}
	return nil, store.ErrNotFound
}

func (f *fakeUserStore) ListUsers(_ context.Context) ([]store.User, error) {
	out := make([]store.User, 0, len(f.users))
	for _, u := range f.users {
		out = append(out, u)
	}
	return out, nil
}

func (f *fakeUserStore) UpdateUserDisplayName(_ context.Context, id, name string) error {
	u, ok := f.users[id]
	if !ok {
		return store.ErrNotFound
	}
	u.DisplayName = name
	f.users[id] = u
	return nil
}

func (f *fakeUserStore) UpsertUser(_ context.Context, id, name string) error {
	u := f.users[id]
	u.UserID = id
	u.DisplayName = name
	if u.CreatedAt.IsZero() {
		u.CreatedAt = time.Now().UTC()
	}
	f.users[id] = u
	return nil
}

func (f *fakeUserStore) LinkPeerToUser(_ context.Context, peerID, userID string) error {
	for _, p := range f.peerUsers[userID] {
		if p == peerID {
			return nil
		}
	}
	f.peerUsers[userID] = append(f.peerUsers[userID], peerID)
	return nil
}

func (f *fakeUserStore) GetUserForPeer(_ context.Context, peerID string) (*store.User, error) {
	for uid, peers := range f.peerUsers {
		for _, p := range peers {
			if p == peerID {
				u := f.users[uid]
				return &u, nil
			}
		}
	}
	return nil, store.ErrNotFound
}

func (f *fakeUserStore) ListPeersForUser(_ context.Context, id string) ([]store.P2PPeer, error) {
	out := []store.P2PPeer{}
	for _, peerID := range f.peerUsers[id] {
		if p, ok := f.peers[peerID]; ok {
			out = append(out, p)
		}
	}
	return out, nil
}

// TestUsersListEmpty pins the empty-list shape: must always be {"users":[]}.
func TestUsersListEmpty(t *testing.T) {
	t.Parallel()
	h := &usersHandler{store: newFakeUserStore()}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/users", nil)
	rr := httptest.NewRecorder()
	h.list(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var got struct {
		Users []store.User `json:"users"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Users == nil || len(got.Users) != 0 {
		t.Fatalf("users = %+v, want []", got.Users)
	}
}

// TestUsersGetReturnsUserAndPeers verifies the {id} endpoint embeds the
// peers linked to that user.
func TestUsersGetReturnsUserAndPeers(t *testing.T) {
	t.Parallel()
	st := newFakeUserStore()
	st.users["u-1"] = store.User{UserID: "u-1", DisplayName: "User", IsSelf: true}
	st.peers["peer-1"] = store.P2PPeer{PeerID: "peer-1", DisplayName: "laptop"}
	st.peerUsers["u-1"] = []string{"peer-1"}

	h := &usersHandler{store: st}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/users/u-1", nil)
	req.SetPathValue("id", "u-1")
	rr := httptest.NewRecorder()
	h.get(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var got userWithPeers
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.DisplayName != "User" {
		t.Fatalf("display_name = %q, want Max", got.DisplayName)
	}
	if len(got.Peers) != 1 || got.Peers[0].PeerID != "peer-1" {
		t.Fatalf("peers = %+v", got.Peers)
	}
}

// TestUsersGetNotFound returns 404 for an unknown user id.
func TestUsersGetNotFound(t *testing.T) {
	t.Parallel()
	h := &usersHandler{store: newFakeUserStore()}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/users/ghost", nil)
	req.SetPathValue("id", "ghost")
	rr := httptest.NewRecorder()
	h.get(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
}

// TestUsersSelfBootstrapped returns the self row with the flag set.
func TestUsersSelfBootstrapped(t *testing.T) {
	t.Parallel()
	st := newFakeUserStore()
	st.users["u-1"] = store.User{UserID: "u-1", DisplayName: "User", IsSelf: true}

	h := &usersHandler{store: st}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/users/self", nil)
	rr := httptest.NewRecorder()
	h.self(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var got whoamiResponse
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !got.SelfBootstrapped {
		t.Fatalf("SelfBootstrapped = false, want true")
	}
	if got.User == nil || got.User.UserID != "u-1" {
		t.Fatalf("User = %+v, want u-1", got.User)
	}
}

// TestUsersSelfNotBootstrapped returns 200 with a structured empty body
// (user=null, self_bootstrapped=false) on a fresh DB. The UI uses the
// flag to render the "introduce yourself" CTA — NOT a 404.
func TestUsersSelfNotBootstrapped(t *testing.T) {
	t.Parallel()
	h := &usersHandler{store: newFakeUserStore()}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/users/self", nil)
	rr := httptest.NewRecorder()
	h.self(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (structured empty, not 404)", rr.Code)
	}
	var got whoamiResponse
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.SelfBootstrapped {
		t.Fatalf("SelfBootstrapped = true, want false on empty store")
	}
	if got.User != nil {
		t.Fatalf("User = %+v, want nil on empty store", got.User)
	}
}
