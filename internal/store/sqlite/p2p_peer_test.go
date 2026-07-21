package sqlite_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// TestP2PPeerCRUD exercises the AddPeer/GetPeer/ListPeers/RevokePeer/
// UpdateLastSeen path end-to-end against a freshly-migrated SQLite DB.
func TestP2PPeerCRUD(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	p := &store.P2PPeer{
		PeerID:      "12D3KooWAaaa",
		DisplayName: "alice-laptop",
		PairedAt:    time.Now().UTC(),
		TrustLevel:  1,
		Scopes:      []string{"read", "write"},
	}
	if err := db.AddPeer(ctx, p); err != nil {
		t.Fatalf("AddPeer: %v", err)
	}
	if err := db.AddPeer(ctx, p); !errors.Is(err, store.ErrAlreadyExists) {
		t.Fatalf("duplicate AddPeer err = %v, want ErrAlreadyExists", err)
	}

	got, err := db.GetPeer(ctx, p.PeerID)
	if err != nil {
		t.Fatalf("GetPeer: %v", err)
	}
	if got.DisplayName != "alice-laptop" || got.TrustLevel != 1 {
		t.Fatalf("got = %+v", got)
	}
	if len(got.Scopes) != 2 || got.Scopes[0] != "read" {
		t.Fatalf("scopes round-trip failed: %+v", got.Scopes)
	}

	now := time.Now().UTC().Truncate(time.Second)
	if err := db.UpdateLastSeen(ctx, p.PeerID, now); err != nil {
		t.Fatalf("UpdateLastSeen: %v", err)
	}
	got, _ = db.GetPeer(ctx, p.PeerID)
	if got.LastSeen == nil || !got.LastSeen.Equal(now) {
		t.Fatalf("last_seen = %v, want %v", got.LastSeen, now)
	}

	peers, err := db.ListPeers(ctx)
	if err != nil {
		t.Fatalf("ListPeers: %v", err)
	}
	if len(peers) != 1 {
		t.Fatalf("ListPeers len = %d, want 1", len(peers))
	}

	if err := db.RevokePeer(ctx, p.PeerID); err != nil {
		t.Fatalf("RevokePeer: %v", err)
	}
	got, _ = db.GetPeer(ctx, p.PeerID)
	if got.RevokedAt == nil {
		t.Fatal("RevokedAt should be set after revoke")
	}
	// Re-revoke is a no-op surfaced as ErrNotFound (the WHERE clause
	// excludes already-revoked rows).
	if err := db.RevokePeer(ctx, p.PeerID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("re-revoke err = %v, want ErrNotFound", err)
	}
}

func TestP2PPeerGrantScopeConcurrent(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	const peerID = "12D3KooWScopeRace"
	if err := db.AddPeer(ctx, &store.P2PPeer{
		PeerID:      peerID,
		DisplayName: "scope-race",
		PairedAt:    time.Now().UTC(),
		Scopes:      []string{"existing"},
	}); err != nil {
		t.Fatalf("AddPeer: %v", err)
	}

	const grants = 64
	start := make(chan struct{})
	errs := make(chan error, grants)
	var wg sync.WaitGroup
	for i := 0; i < grants; i++ {
		scope := fmt.Sprintf("scope:%02d", i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- db.GrantPeerScope(ctx, peerID, scope)
		}()
	}

	close(start)
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("GrantPeerScope: %v", err)
		}
	}

	got, err := db.GetPeer(ctx, peerID)
	if err != nil {
		t.Fatalf("GetPeer: %v", err)
	}
	seen := make(map[string]bool, len(got.Scopes))
	for _, scope := range got.Scopes {
		seen[scope] = true
	}
	if len(seen) != grants+1 {
		t.Fatalf("scope count = %d, want %d; scopes=%v", len(seen), grants+1, got.Scopes)
	}
	if !seen["existing"] {
		t.Fatal("existing scope was lost")
	}
	for i := 0; i < grants; i++ {
		scope := fmt.Sprintf("scope:%02d", i)
		if !seen[scope] {
			t.Fatalf("missing granted scope %q; scopes=%v", scope, got.Scopes)
		}
	}
}

// TestP2PPeerRememberLoadAddrs round-trips the last_known_addrs column added
// by migration 033. Covers:
//   - empty default for never-persisted peers
//   - basic round-trip
//   - overwrite (subsequent calls replace, not append)
//   - empty-slice clears
//   - revoked peers are not visible to LoadPeerAddrs
func TestP2PPeerRememberLoadAddrs(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	const peerID = "12D3KooWAddrs"
	if err := db.AddPeer(ctx, &store.P2PPeer{
		PeerID:      peerID,
		DisplayName: "addrs-test",
		PairedAt:    time.Now().UTC(),
	}); err != nil {
		t.Fatalf("AddPeer: %v", err)
	}

	got, err := db.LoadPeerAddrs(ctx, peerID)
	if err != nil {
		t.Fatalf("LoadPeerAddrs (default): %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("default addrs = %v, want empty", got)
	}

	addrs := []string{
		"/ip4/192.0.2.10/tcp/4001",
		"/ip4/100.64.0.7/udp/4001/quic-v1",
	}
	if err := db.RememberPeerAddrs(ctx, peerID, addrs); err != nil {
		t.Fatalf("RememberPeerAddrs: %v", err)
	}
	got, err = db.LoadPeerAddrs(ctx, peerID)
	if err != nil {
		t.Fatalf("LoadPeerAddrs: %v", err)
	}
	if len(got) != len(addrs) || got[0] != addrs[0] || got[1] != addrs[1] {
		t.Fatalf("round-trip mismatch: got %v want %v", got, addrs)
	}

	// Overwrite (not append).
	updated := []string{"/ip4/198.51.100.5/tcp/4001"}
	if err := db.RememberPeerAddrs(ctx, peerID, updated); err != nil {
		t.Fatalf("RememberPeerAddrs (overwrite): %v", err)
	}
	got, _ = db.LoadPeerAddrs(ctx, peerID)
	if len(got) != 1 || got[0] != updated[0] {
		t.Fatalf("post-overwrite addrs = %v, want %v", got, updated)
	}

	// Empty clears.
	if err := db.RememberPeerAddrs(ctx, peerID, []string{}); err != nil {
		t.Fatalf("RememberPeerAddrs (clear): %v", err)
	}
	got, _ = db.LoadPeerAddrs(ctx, peerID)
	if len(got) != 0 {
		t.Fatalf("post-clear addrs = %v, want empty", got)
	}

	// Repopulate, then revoke — LoadPeerAddrs filters out revoked rows.
	if err := db.RememberPeerAddrs(ctx, peerID, addrs); err != nil {
		t.Fatalf("RememberPeerAddrs (repop): %v", err)
	}
	if err := db.RevokePeer(ctx, peerID); err != nil {
		t.Fatalf("RevokePeer: %v", err)
	}
	got, _ = db.LoadPeerAddrs(ctx, peerID)
	if len(got) != 0 {
		t.Fatalf("post-revoke addrs = %v, want empty", got)
	}

	// Unknown peer = empty.
	got, err = db.LoadPeerAddrs(ctx, "12D3KooWUnknown")
	if err != nil {
		t.Fatalf("LoadPeerAddrs (unknown): %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("unknown addrs = %v, want empty", got)
	}
}

// TestP2PPendingPairLifecycle covers create / fetch / consume / sweep on
// the pending-pair table.
func TestP2PPendingPairLifecycle(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	p := &store.P2PPendingPair{
		Code:       "123456",
		PeerID:     "12D3KooWBbbb",
		Multiaddrs: []string{"/ip4/127.0.0.1/tcp/4001"},
		ExpiresAt:  time.Now().Add(5 * time.Minute).UTC(),
	}
	if err := db.CreatePendingPair(ctx, p); err != nil {
		t.Fatalf("CreatePendingPair: %v", err)
	}

	got, err := db.GetPendingPair(ctx, "123456")
	if err != nil {
		t.Fatalf("GetPendingPair: %v", err)
	}
	if got.PeerID != p.PeerID || len(got.Multiaddrs) != 1 {
		t.Fatalf("round-trip mismatch: %+v", got)
	}

	if err := db.DeletePendingPair(ctx, "123456"); err != nil {
		t.Fatalf("DeletePendingPair: %v", err)
	}
	if _, err := db.GetPendingPair(ctx, "123456"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("post-delete err = %v, want ErrNotFound", err)
	}

	// Sweep removes expired rows; fresh rows survive.
	expired := &store.P2PPendingPair{
		Code: "999999", PeerID: "p", Multiaddrs: nil,
		ExpiresAt: time.Now().Add(-time.Minute).UTC(),
	}
	fresh := &store.P2PPendingPair{
		Code: "777777", PeerID: "q", Multiaddrs: nil,
		ExpiresAt: time.Now().Add(time.Minute).UTC(),
	}
	if err := db.CreatePendingPair(ctx, expired); err != nil {
		t.Fatalf("create expired: %v", err)
	}
	if err := db.CreatePendingPair(ctx, fresh); err != nil {
		t.Fatalf("create fresh: %v", err)
	}
	n, err := db.SweepExpiredPendingPairs(ctx, time.Now().UTC())
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if n != 1 {
		t.Fatalf("sweep removed %d, want 1", n)
	}
	if _, err := db.GetPendingPair(ctx, "777777"); err != nil {
		t.Fatalf("fresh row gone: %v", err)
	}
}
