package sqlite_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// addPairedPeer is a tiny helper for tests that need a peer row before
// they can link a user to it.
func addPairedPeer(t *testing.T, ctx context.Context, db userStoreLike, peerID, name string) {
	t.Helper()
	if err := db.AddPeer(ctx, &store.P2PPeer{
		PeerID: peerID, DisplayName: name,
		PairedAt: time.Now().UTC(), Scopes: []string{},
	}); err != nil {
		t.Fatalf("add peer %s: %v", peerID, err)
	}
}

// userStoreLike is the subset of *sqlite.DB the test helpers exercise.
type userStoreLike interface {
	store.P2PPeerStore
	store.UserStore
}

// TestUserCRUD exercises the basic User row life cycle.
func TestUserCRUD(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	u := &store.User{
		UserID:      "u-user",
		DisplayName: "User",
		IsSelf:      true,
	}
	if err := db.CreateUser(ctx, u); err != nil {
		t.Fatalf("create: %v", err)
	}

	got, err := db.GetUser(ctx, "u-user")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !got.IsSelf || got.DisplayName != "User" {
		t.Fatalf("got = %+v", got)
	}

	self, err := db.GetSelfUser(ctx)
	if err != nil {
		t.Fatalf("get self: %v", err)
	}
	if self.UserID != "u-user" {
		t.Fatalf("self user_id = %q", self.UserID)
	}

	// Cannot create a second self user.
	err = db.CreateUser(ctx, &store.User{
		UserID: "u-other", DisplayName: "Other", IsSelf: true,
	})
	if !errors.Is(err, store.ErrAlreadyExists) {
		t.Fatalf("second self user err = %v, want ErrAlreadyExists", err)
	}

	// Update display_name.
	if err := db.UpdateUserDisplayName(ctx, "u-user", "User Prime"); err != nil {
		t.Fatalf("update name: %v", err)
	}
	got, _ = db.GetUser(ctx, "u-user")
	if got.DisplayName != "User Prime" {
		t.Fatalf("display_name after update = %q", got.DisplayName)
	}

	// Upsert (display_name change on existing).
	if err := db.UpsertUser(ctx, "u-user", "User v2"); err != nil {
		t.Fatalf("upsert existing: %v", err)
	}
	got, _ = db.GetUser(ctx, "u-user")
	if got.DisplayName != "User v2" {
		t.Fatalf("display_name after upsert = %q", got.DisplayName)
	}
}

// TestPeerUserLinkPairWithOnePeer covers the single-machine pairing case:
// pair with one peer → exactly 1 user row exists for that peer.
func TestPeerUserLinkPairWithOnePeer(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	addPairedPeer(t, ctx, db, "peer-1", "alice-laptop")

	if err := db.UpsertUser(ctx, "u-alice", "Alice"); err != nil {
		t.Fatalf("upsert alice: %v", err)
	}
	if err := db.LinkPeerToUser(ctx, "peer-1", "u-alice"); err != nil {
		t.Fatalf("link: %v", err)
	}

	u, err := db.GetUserForPeer(ctx, "peer-1")
	if err != nil {
		t.Fatalf("get user for peer: %v", err)
	}
	if u.UserID != "u-alice" {
		t.Fatalf("user = %+v", u)
	}

	peers, err := db.ListPeersForUser(ctx, "u-alice")
	if err != nil {
		t.Fatalf("list peers for user: %v", err)
	}
	if len(peers) != 1 || peers[0].PeerID != "peer-1" {
		t.Fatalf("peers = %+v", peers)
	}

	// LinkPeerToUser is idempotent.
	if err := db.LinkPeerToUser(ctx, "peer-1", "u-alice"); err != nil {
		t.Fatalf("re-link: %v", err)
	}
	peers, _ = db.ListPeersForUser(ctx, "u-alice")
	if len(peers) != 1 {
		t.Fatalf("re-link duplicated rows: %d", len(peers))
	}
}

func TestPeerUserRelinkMovesDeviceBetweenUsers(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	addPairedPeer(t, ctx, db, "peer-1", "laptop")
	if err := db.UpsertUser(ctx, "u-old", "Old"); err != nil {
		t.Fatalf("upsert old: %v", err)
	}
	if err := db.UpsertUser(ctx, "u-new", "New"); err != nil {
		t.Fatalf("upsert new: %v", err)
	}
	if err := db.LinkPeerToUser(ctx, "peer-1", "u-old"); err != nil {
		t.Fatalf("link old: %v", err)
	}

	if err := db.RelinkPeerToUser(ctx, "peer-1", "u-new"); err != nil {
		t.Fatalf("relink: %v", err)
	}

	oldPeers, err := db.ListPeersForUser(ctx, "u-old")
	if err != nil {
		t.Fatalf("list old peers: %v", err)
	}
	if len(oldPeers) != 0 {
		t.Fatalf("old owner peers = %+v, want none", oldPeers)
	}
	newPeers, err := db.ListPeersForUser(ctx, "u-new")
	if err != nil {
		t.Fatalf("list new peers: %v", err)
	}
	if len(newPeers) != 1 || newPeers[0].PeerID != "peer-1" {
		t.Fatalf("new owner peers = %+v, want peer-1", newPeers)
	}
}

func TestPeerUserLinkReplacesExistingOwner(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	addPairedPeer(t, ctx, db, "peer-1", "laptop")
	if err := db.UpsertUser(ctx, "u-synthetic", "peer-1"); err != nil {
		t.Fatalf("upsert synthetic: %v", err)
	}
	if err := db.UpsertUser(ctx, "u-real", "Elliot"); err != nil {
		t.Fatalf("upsert real: %v", err)
	}
	if err := db.LinkPeerToUser(ctx, "peer-1", "u-synthetic"); err != nil {
		t.Fatalf("link synthetic: %v", err)
	}
	if err := db.LinkPeerToUser(ctx, "peer-1", "u-real"); err != nil {
		t.Fatalf("link real: %v", err)
	}

	u, err := db.GetUserForPeer(ctx, "peer-1")
	if err != nil {
		t.Fatalf("get user for peer: %v", err)
	}
	if u.UserID != "u-real" {
		t.Fatalf("owner = %q, want u-real", u.UserID)
	}
	oldPeers, err := db.ListPeersForUser(ctx, "u-synthetic")
	if err != nil {
		t.Fatalf("list synthetic peers: %v", err)
	}
	if len(oldPeers) != 0 {
		t.Fatalf("synthetic owner retained peers = %+v", oldPeers)
	}
}

func TestPeerUserUnlinkClearsDeviceOwner(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	addPairedPeer(t, ctx, db, "peer-1", "laptop")
	if err := db.UpsertUser(ctx, "u-old", "Old"); err != nil {
		t.Fatalf("upsert old: %v", err)
	}
	if err := db.LinkPeerToUser(ctx, "peer-1", "u-old"); err != nil {
		t.Fatalf("link old: %v", err)
	}

	if err := db.UnlinkPeerFromUsers(ctx, "peer-1"); err != nil {
		t.Fatalf("unlink: %v", err)
	}
	peers, err := db.ListPeersForUser(ctx, "u-old")
	if err != nil {
		t.Fatalf("list old peers: %v", err)
	}
	if len(peers) != 0 {
		t.Fatalf("owner peers = %+v, want none", peers)
	}
}

// TestPeerUserLinkMultiMachine is the M7.1 hero scenario: the same human
// (Max) operates two laptops. Pairing the second laptop should NOT create
// a second user row — the new peer just gets linked to Max's existing
// user, so users count stays at 1 while peer count is 2.
func TestPeerUserLinkMultiMachine(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	addPairedPeer(t, ctx, db, "peer-laptop-a", "workstation-a")
	addPairedPeer(t, ctx, db, "peer-laptop-b", "workstation-b")

	// First pairing — user with laptop A.
	if err := db.UpsertUser(ctx, "u-user", "User"); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	if err := db.LinkPeerToUser(ctx, "peer-laptop-a", "u-user"); err != nil {
		t.Fatalf("first link: %v", err)
	}

	// Second pairing — same user_id, different peer.
	if err := db.UpsertUser(ctx, "u-user", "User"); err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if err := db.LinkPeerToUser(ctx, "peer-laptop-b", "u-user"); err != nil {
		t.Fatalf("second link: %v", err)
	}

	users, err := db.ListUsers(ctx)
	if err != nil {
		t.Fatalf("list users: %v", err)
	}
	if len(users) != 1 {
		t.Fatalf("expected 1 user, got %d (%+v)", len(users), users)
	}

	peers, err := db.ListPeersForUser(ctx, "u-user")
	if err != nil {
		t.Fatalf("list peers for user: %v", err)
	}
	if len(peers) != 2 {
		t.Fatalf("expected 2 peers for Max, got %d (%+v)", len(peers), peers)
	}

	// Both peers resolve back to the same user.
	for _, pid := range []string{"peer-laptop-a", "peer-laptop-b"} {
		u, err := db.GetUserForPeer(ctx, pid)
		if err != nil {
			t.Fatalf("get user for %s: %v", pid, err)
		}
		if u.UserID != "u-user" {
			t.Fatalf("peer %s -> user %q, want u-user", pid, u.UserID)
		}
	}
}

// TestPeerUserLinkUnknownPeerFails verifies the FK constraint prevents
// linking a peer that does not exist in p2p_peers.
func TestPeerUserLinkUnknownPeerFails(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	if err := db.UpsertUser(ctx, "u-x", "X"); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	err := db.LinkPeerToUser(ctx, "ghost-peer", "u-x")
	if err == nil {
		t.Fatal("expected FK violation linking unknown peer, got nil")
	}
}

// TestPeerUserCascadeDelete verifies deleting a peer also drops its
// peer_users link (FK ON DELETE CASCADE).
func TestPeerUserCascadeDelete(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	addPairedPeer(t, ctx, db, "peer-cascade", "doomed")
	if err := db.UpsertUser(ctx, "u-c", "C"); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := db.LinkPeerToUser(ctx, "peer-cascade", "u-c"); err != nil {
		t.Fatalf("link: %v", err)
	}

	// Revoke is what we use day-to-day, but a hard delete is the
	// FK-cascade contract we want to verify. Use raw SQL via the store
	// helpers' tx-or-direct connection.
	peers, _ := db.ListPeersForUser(ctx, "u-c")
	if len(peers) != 1 {
		t.Fatalf("pre-delete peers = %d", len(peers))
	}

	// Hard delete via raw connection (Revoke would leave the row in place).
	if _, err := db.Raw().ExecContext(ctx, `DELETE FROM p2p_peers WHERE peer_id = ?`, "peer-cascade"); err != nil {
		t.Fatalf("hard delete: %v", err)
	}
	peers, _ = db.ListPeersForUser(ctx, "u-c")
	if len(peers) != 0 {
		t.Fatalf("expected cascade to clear peer_users, got %d rows", len(peers))
	}
}
