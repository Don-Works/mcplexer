package config

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

func newBootstrapDB(t *testing.T) *sqlite.DB {
	t.Helper()
	db, err := sqlite.New(context.Background(), t.TempDir()+"/bs.db")
	if err != nil {
		t.Fatalf("new db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// TestBootstrapSelfUserCreatesRow verifies a fresh DB ends up with exactly
// one users.is_self=1 row after BootstrapSelfUser.
func TestBootstrapSelfUserCreatesRow(t *testing.T) {
	db := newBootstrapDB(t)
	ctx := context.Background()

	// Seed a display_name in settings so we can confirm it is mirrored.
	_ = db.UpdateSettings(ctx, json.RawMessage(`{"display_name":"Max"}`))

	u, err := BootstrapSelfUser(ctx, db, db)
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if !u.IsSelf {
		t.Fatalf("expected IsSelf=true, got %+v", u)
	}
	if u.DisplayName != "Max" {
		t.Fatalf("display_name = %q, want Max", u.DisplayName)
	}
	if u.UserID == "" {
		t.Fatal("user_id empty")
	}

	// Idempotent: a second call returns the same row.
	u2, err := BootstrapSelfUser(ctx, db, db)
	if err != nil {
		t.Fatalf("bootstrap (idempotent): %v", err)
	}
	if u2.UserID != u.UserID {
		t.Fatalf("non-idempotent: first %q, second %q", u.UserID, u2.UserID)
	}

	// Only one self row exists.
	users, err := db.ListUsers(ctx)
	if err != nil {
		t.Fatalf("list users: %v", err)
	}
	selfCount := 0
	for _, x := range users {
		if x.IsSelf {
			selfCount++
		}
	}
	if selfCount != 1 {
		t.Fatalf("expected exactly 1 self user, got %d (%+v)", selfCount, users)
	}
}

// TestBootstrapSelfUserHonorsEnvUserID pins the bulletproof e2e contract:
// MCPLEXER_SELF_USER_ID seeds the user_id on first boot so two daemons
// in the same docker-compose project end up with the SAME user_id and
// form a Tier 1 same-user pair. Without this, every node generates a
// fresh UUID and tier resolution can never see them as same-user.
func TestBootstrapSelfUserHonorsEnvUserID(t *testing.T) {
	db := newBootstrapDB(t)
	ctx := context.Background()

	t.Setenv(SelfUserIDEnvVar, "user-alice")
	t.Setenv(SelfDisplayNameEnvVar, "Alice (test)")

	u, err := BootstrapSelfUser(ctx, db, db)
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if u.UserID != "user-alice" {
		t.Errorf("UserID = %q, want user-alice (from env)", u.UserID)
	}
	if u.DisplayName != "Alice (test)" {
		t.Errorf("DisplayName = %q, want Alice (test) (from env)", u.DisplayName)
	}

	// Idempotent — second call returns the same row even if env changes
	// (the row is already persisted with the original value).
	t.Setenv(SelfUserIDEnvVar, "user-bob")
	u2, err := BootstrapSelfUser(ctx, db, db)
	if err != nil {
		t.Fatalf("bootstrap (idempotent): %v", err)
	}
	if u2.UserID != "user-alice" {
		t.Errorf("idempotent UserID changed: %q, want user-alice", u2.UserID)
	}
}

// TestBootstrapSelfUserHostnameFallback verifies that with no settings
// display_name we land on os.Hostname() (or "user"). We don't pin the
// exact hostname value — just that it's non-empty.
func TestBootstrapSelfUserHostnameFallback(t *testing.T) {
	db := newBootstrapDB(t)
	ctx := context.Background()

	u, err := BootstrapSelfUser(ctx, db, db)
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}
	if u.DisplayName == "" {
		t.Fatalf("display_name empty, expected hostname or 'user' fallback")
	}
}

// TestSyntheticUserIDForPeerStable verifies the synthesized ID is stable
// across calls and shaped as a 36-char UUID-style string.
func TestSyntheticUserIDForPeerStable(t *testing.T) {
	a := SyntheticUserIDForPeer("12D3KooWAaaa")
	b := SyntheticUserIDForPeer("12D3KooWAaaa")
	if a != b {
		t.Fatalf("synthetic IDs differ: %q vs %q", a, b)
	}
	if len(a) != 36 {
		t.Fatalf("len = %d, want 36 (got %q)", len(a), a)
	}
	c := SyntheticUserIDForPeer("12D3KooWBbbb")
	if c == a {
		t.Fatalf("collision: same ID for different peers (%q)", a)
	}
}

// Compile-time assertion: *sqlite.DB satisfies store.UserStore so callers
// don't need to wrap it.
var _ store.UserStore = (*sqlite.DB)(nil)
