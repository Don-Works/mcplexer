package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/config"
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

func (f *fakeUserStore) DeleteUser(_ context.Context, id string) error {
	if _, ok := f.users[id]; !ok {
		return store.ErrNotFound
	}
	delete(f.users, id)
	delete(f.peerUsers, id)
	return nil
}

func (f *fakeUserStore) GetPeer(_ context.Context, id string) (*store.P2PPeer, error) {
	p, ok := f.peers[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return &p, nil
}

func (f *fakeUserStore) RelinkPeerToUser(_ context.Context, peerID, userID string) error {
	if _, ok := f.peers[peerID]; !ok {
		return store.ErrNotFound
	}
	if _, ok := f.users[userID]; !ok {
		return store.ErrNotFound
	}
	for uid, peers := range f.peerUsers {
		filtered := peers[:0]
		for _, p := range peers {
			if p != peerID {
				filtered = append(filtered, p)
			}
		}
		f.peerUsers[uid] = filtered
	}
	f.peerUsers[userID] = append(f.peerUsers[userID], peerID)
	return nil
}

func (f *fakeUserStore) UnlinkPeerFromUsers(_ context.Context, peerID string) error {
	for uid, peers := range f.peerUsers {
		filtered := peers[:0]
		for _, p := range peers {
			if p != peerID {
				filtered = append(filtered, p)
			}
		}
		f.peerUsers[uid] = filtered
	}
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

func TestUsersListHidesSyntheticDeviceIdentities(t *testing.T) {
	t.Parallel()
	st := newFakeUserStore()
	syntheticID := config.SyntheticUserIDForPeer("peer-legacy")
	st.users["u-self"] = store.User{UserID: "u-self", DisplayName: "Max", IsSelf: true}
	st.users[syntheticID] = store.User{UserID: syntheticID, DisplayName: "peer-legacy"}
	st.users["u-morgan"] = store.User{UserID: "u-morgan", DisplayName: "Morgan"}
	st.peers["peer-legacy"] = store.P2PPeer{PeerID: "peer-legacy", DisplayName: "my-air"}
	st.peers["peer-morgan"] = store.P2PPeer{PeerID: "peer-morgan", DisplayName: "morgan-mbp"}
	st.peerUsers[syntheticID] = []string{"peer-legacy"}
	st.peerUsers["u-morgan"] = []string{"peer-morgan"}

	h := &usersHandler{store: st}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/users", nil)
	rr := httptest.NewRecorder()
	h.list(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var got struct {
		Users []store.User `json:"users"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Users) != 2 {
		t.Fatalf("users = %+v, want self + morgan only", got.Users)
	}
	for _, u := range got.Users {
		if u.UserID == syntheticID {
			t.Fatalf("synthetic device identity leaked into people list: %+v", got.Users)
		}
	}
}

func TestUsersListHidesLegacyDeviceNamedIdentities(t *testing.T) {
	t.Parallel()
	st := newFakeUserStore()
	st.users["u-self"] = store.User{UserID: "u-self", DisplayName: "Max", IsSelf: true}
	st.users["u-device-name"] = store.User{UserID: "u-device-name", DisplayName: "my-air"}
	st.users["u-peer-label"] = store.User{UserID: "u-peer-label", DisplayName: "peer-abcdef12"}
	st.users["u-person"] = store.User{UserID: "u-person", DisplayName: "Morgan"}
	st.peers["peer-device"] = store.P2PPeer{PeerID: "peer-device", DisplayName: "my-air"}
	st.peers["12D3KooWabcdef12"] = store.P2PPeer{PeerID: "12D3KooWabcdef12"}
	st.peers["peer-person"] = store.P2PPeer{PeerID: "peer-person", DisplayName: "morgan-mbp"}
	st.peerUsers["u-device-name"] = []string{"peer-device"}
	st.peerUsers["u-peer-label"] = []string{"12D3KooWabcdef12"}
	st.peerUsers["u-person"] = []string{"peer-person"}

	h := &usersHandler{store: st}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/users", nil)
	rr := httptest.NewRecorder()
	h.list(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var got struct {
		Users []store.User `json:"users"`
	}
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	ids := map[string]bool{}
	for _, u := range got.Users {
		ids[u.UserID] = true
	}
	if !ids["u-self"] || !ids["u-person"] {
		t.Fatalf("real people missing from users list: %+v", got.Users)
	}
	if ids["u-device-name"] || ids["u-peer-label"] {
		t.Fatalf("legacy device identity leaked into people list: %+v", got.Users)
	}
}

func TestUsersCreatePerson(t *testing.T) {
	t.Parallel()
	st := newFakeUserStore()
	h := &usersHandler{store: st}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/users",
		strings.NewReader(`{"display_name":"  Morgan  "}`))
	rr := httptest.NewRecorder()
	h.create(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	var got store.User
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.UserID == "" || got.DisplayName != "Morgan" || got.IsSelf {
		t.Fatalf("created user = %+v", got)
	}
	if _, ok := st.users[got.UserID]; !ok {
		t.Fatalf("created user not persisted: %+v", st.users)
	}
}

func TestUsersCreatePersonRejectsBlankName(t *testing.T) {
	t.Parallel()
	h := &usersHandler{store: newFakeUserStore()}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/users",
		strings.NewReader(`{"display_name":"  "}`))
	rr := httptest.NewRecorder()
	h.create(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
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

func TestUsersUpdateDisplayName(t *testing.T) {
	t.Parallel()
	st := newFakeUserStore()
	st.users["u-1"] = store.User{UserID: "u-1", DisplayName: "Old"}
	h := &usersHandler{store: st}
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/users/u-1",
		strings.NewReader(`{"display_name":"  New Name  "}`))
	req.SetPathValue("id", "u-1")
	rr := httptest.NewRecorder()
	h.update(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var got store.User
	if err := json.NewDecoder(rr.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.DisplayName != "New Name" || st.users["u-1"].DisplayName != "New Name" {
		t.Fatalf("display_name not updated: got=%+v stored=%+v", got, st.users["u-1"])
	}
}

func TestUsersUpdateDisplayNameRejectsBlank(t *testing.T) {
	t.Parallel()
	st := newFakeUserStore()
	st.users["u-1"] = store.User{UserID: "u-1", DisplayName: "Old"}
	h := &usersHandler{store: st}
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/users/u-1",
		strings.NewReader(`{"display_name":""}`))
	req.SetPathValue("id", "u-1")
	rr := httptest.NewRecorder()
	h.update(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
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

func TestUsersDeleteStaleNonSelfIdentity(t *testing.T) {
	t.Parallel()
	st := newFakeUserStore()
	st.users["u-stale"] = store.User{UserID: "u-stale", DisplayName: "ai-gateway"}

	h := &usersHandler{store: st}
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/users/u-stale", nil)
	req.SetPathValue("id", "u-stale")
	rr := httptest.NewRecorder()
	h.delete(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", rr.Code, rr.Body.String())
	}
	if _, ok := st.users["u-stale"]; ok {
		t.Fatal("stale user still present after delete")
	}
}

func TestUsersDeleteRefusesSelf(t *testing.T) {
	t.Parallel()
	st := newFakeUserStore()
	st.users["u-self"] = store.User{UserID: "u-self", DisplayName: "Max", IsSelf: true}

	h := &usersHandler{store: st}
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/users/u-self", nil)
	req.SetPathValue("id", "u-self")
	rr := httptest.NewRecorder()
	h.delete(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestUsersDeleteRefusesLinkedDevices(t *testing.T) {
	t.Parallel()
	st := newFakeUserStore()
	st.users["u-remote"] = store.User{UserID: "u-remote", DisplayName: "Remote"}
	st.peers["peer-1"] = store.P2PPeer{PeerID: "peer-1", DisplayName: "laptop"}
	st.peerUsers["u-remote"] = []string{"peer-1"}

	h := &usersHandler{store: st}
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/users/u-remote", nil)
	req.SetPathValue("id", "u-remote")
	rr := httptest.NewRecorder()
	h.delete(rr, req)
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rr.Code)
	}
}

func TestUsersUpdateDeviceOwnerRelinksPeer(t *testing.T) {
	t.Parallel()
	st := newFakeUserStore()
	st.users["u-old"] = store.User{UserID: "u-old", DisplayName: "Old"}
	st.users["u-new"] = store.User{UserID: "u-new", DisplayName: "New", IsSelf: true}
	st.peers["peer-1"] = store.P2PPeer{PeerID: "peer-1", DisplayName: "laptop"}
	st.peerUsers["u-old"] = []string{"peer-1"}

	h := &usersHandler{store: st}
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/users/devices/peer-1",
		strings.NewReader(`{"user_id":"u-new"}`))
	req.SetPathValue("peer_id", "peer-1")
	rr := httptest.NewRecorder()
	h.updateDeviceOwner(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if len(st.peerUsers["u-old"]) != 0 {
		t.Fatalf("old owner still has peer: %+v", st.peerUsers["u-old"])
	}
	if got := st.peerUsers["u-new"]; len(got) != 1 || got[0] != "peer-1" {
		t.Fatalf("new owner peers = %+v, want peer-1", got)
	}
}

func TestUsersUpdateDeviceOwnerUnlinksPeer(t *testing.T) {
	t.Parallel()
	st := newFakeUserStore()
	st.users["u-old"] = store.User{UserID: "u-old", DisplayName: "Old"}
	st.peers["peer-1"] = store.P2PPeer{PeerID: "peer-1", DisplayName: "laptop"}
	st.peerUsers["u-old"] = []string{"peer-1"}

	h := &usersHandler{store: st}
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/users/devices/peer-1",
		strings.NewReader(`{"user_id":null}`))
	req.SetPathValue("peer_id", "peer-1")
	rr := httptest.NewRecorder()
	h.updateDeviceOwner(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if len(st.peerUsers["u-old"]) != 0 {
		t.Fatalf("old owner still has peer: %+v", st.peerUsers["u-old"])
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
