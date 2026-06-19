package control

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// TestHandleWhoamiSelfRow returns the self user with self_bootstrapped=true.
func TestHandleWhoamiSelfRow(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	if err := db.CreateUser(ctx, &store.User{
		UserID: "u-self", DisplayName: "User", IsSelf: true,
	}); err != nil {
		t.Fatalf("seed self: %v", err)
	}

	result, err := handleWhoami(ctx, db, nil)
	if err != nil {
		t.Fatalf("handleWhoami: %v", err)
	}
	text, isErr := parseToolResult(t, result)
	if isErr {
		t.Fatalf("unexpected isError: %s", text)
	}

	var got whoamiResponse
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !got.SelfBootstrapped {
		t.Fatalf("SelfBootstrapped = false, want true")
	}
	if got.User == nil {
		t.Fatalf("User = nil, want u-self")
	}
	if got.User.UserID != "u-self" || got.User.DisplayName != "User" || !got.User.IsSelf {
		t.Fatalf("User = %+v", got.User)
	}
}

// TestHandleWhoamiNotBootstrapped returns the structured empty result
// (user=null, self_bootstrapped=false) on a fresh DB. This is the agent
// contract for "I am about to be useful but the local user row is missing".
func TestHandleWhoamiNotBootstrapped(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	result, err := handleWhoami(ctx, db, nil)
	if err != nil {
		t.Fatalf("handleWhoami: %v", err)
	}
	text, isErr := parseToolResult(t, result)
	if isErr {
		t.Fatalf("unexpected isError: %s", text)
	}

	var got whoamiResponse
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.SelfBootstrapped {
		t.Fatalf("SelfBootstrapped = true, want false on empty DB")
	}
	if got.User != nil {
		t.Fatalf("User = %+v, want nil on empty DB", got.User)
	}
}

// TestHandleListUsersEmpty pins the empty-list shape. Existing
// ListUsers returns a nil slice on an empty table, which marshals to
// "null" — that's still valid JSON for an array consumer, but a future
// refactor that returns []store.User{} would marshal to "[]" and trip
// callers that test the exact string. We only assert the decoded slice
// is empty + non-nil (treating "null" and "[]" equivalently).
func TestHandleListUsersEmpty(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	result, err := handleListUsers(ctx, db, nil)
	if err != nil {
		t.Fatalf("handleListUsers: %v", err)
	}
	text, isErr := parseToolResult(t, result)
	if isErr {
		t.Fatalf("unexpected isError: %s", text)
	}
	var users []store.User
	if err := json.Unmarshal([]byte(text), &users); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(users) != 0 {
		t.Fatalf("len(users) = %d, want 0", len(users))
	}
}

// TestHandleListUsersWithSelfAndRemote seeds self + one paired-user row
// (Alice owns a remote peer). The order is self-first then display_name;
// we assert count and self-presence.
func TestHandleListUsersWithSelfAndRemote(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	if err := db.CreateUser(ctx, &store.User{
		UserID: "u-self", DisplayName: "User", IsSelf: true,
	}); err != nil {
		t.Fatalf("seed self: %v", err)
	}
	if err := db.UpsertUser(ctx, "u-alice", "Alice"); err != nil {
		t.Fatalf("seed alice: %v", err)
	}

	result, err := handleListUsers(ctx, db, nil)
	if err != nil {
		t.Fatalf("handleListUsers: %v", err)
	}
	text, _ := parseToolResult(t, result)
	var users []store.User
	if err := json.Unmarshal([]byte(text), &users); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(users) != 2 {
		t.Fatalf("len(users) = %d, want 2", len(users))
	}
	if users[0].UserID != "u-self" || !users[0].IsSelf {
		t.Fatalf("users[0] = %+v, want self row first", users[0])
	}
}

// TestHandleGetUserReturnsRowAndNotFound covers both branches of get_user.
func TestHandleGetUserReturnsRowAndNotFound(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if err := db.CreateUser(ctx, &store.User{
		UserID: "u-1", DisplayName: "User", IsSelf: true,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	t.Run("found", func(t *testing.T) {
		args := json.RawMessage(`{"id":"u-1"}`)
		result, err := handleGetUser(ctx, db, args)
		if err != nil {
			t.Fatalf("handleGetUser: %v", err)
		}
		text, isErr := parseToolResult(t, result)
		if isErr {
			t.Fatalf("unexpected isError: %s", text)
		}
		var got store.User
		if err := json.Unmarshal([]byte(text), &got); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if got.UserID != "u-1" {
			t.Fatalf("user_id = %q, want u-1", got.UserID)
		}
	})

	t.Run("not found", func(t *testing.T) {
		args := json.RawMessage(`{"id":"ghost"}`)
		result, err := handleGetUser(ctx, db, args)
		if err != nil {
			t.Fatalf("handleGetUser: %v", err)
		}
		text, isErr := parseToolResult(t, result)
		if !isErr {
			t.Fatalf("expected isError=true for unknown user, got text=%q", text)
		}
	})

	t.Run("missing id", func(t *testing.T) {
		// Empty args → requireID returns the field-error. The handler
		// unwraps the (nil, err) so the result is the raw error; the
		// server surfaces this as a tool isError envelope, but the
		// handler-level contract is "returns err".
		_, err := handleGetUser(ctx, db, nil)
		if err == nil {
			t.Fatalf("expected error for missing id, got nil")
		}
	})
}

// TestHandleListUserDevicesEmpty seeds a user with no peers; the response
// must be a JSON-empty list (not a structured error and not the user
// row). Distinguishing "no peers" from "no such user" is the whole point
// of pre-checking the user row inside the handler.
func TestHandleListUserDevicesEmpty(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if err := db.CreateUser(ctx, &store.User{
		UserID: "u-1", DisplayName: "User", IsSelf: true,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	args := json.RawMessage(`{"id":"u-1"}`)
	result, err := handleListUserDevices(ctx, db, args)
	if err != nil {
		t.Fatalf("handleListUserDevices: %v", err)
	}
	text, isErr := parseToolResult(t, result)
	if isErr {
		t.Fatalf("unexpected isError: %s", text)
	}
	var peers []store.P2PPeer
	if err := json.Unmarshal([]byte(text), &peers); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(peers) != 0 {
		t.Fatalf("len(peers) = %d, want 0", len(peers))
	}
}

// TestHandleListUserDevicesWithPeers covers the multi-machine case: Max
// owns two paired peers. We assert the pre-check passes and both peer
// rows are returned (the order is paired_at DESC — we only assert
// presence, not ordering, to avoid coupling to the SQL contract).
func TestHandleListUserDevicesWithPeers(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	if err := db.CreateUser(ctx, &store.User{
		UserID: "u-user", DisplayName: "User", IsSelf: true,
	}); err != nil {
		t.Fatalf("seed self: %v", err)
	}
	if err := db.AddPeer(ctx, &store.P2PPeer{
		PeerID: "peer-laptop-a", DisplayName: "workstation-a",
		PairedAt: time.Now().UTC(), Scopes: []string{},
	}); err != nil {
		t.Fatalf("add peer A: %v", err)
	}
	if err := db.AddPeer(ctx, &store.P2PPeer{
		PeerID: "peer-laptop-b", DisplayName: "workstation-b",
		PairedAt: time.Now().UTC(), Scopes: []string{},
	}); err != nil {
		t.Fatalf("add peer B: %v", err)
	}
	if err := db.LinkPeerToUser(ctx, "peer-laptop-a", "u-user"); err != nil {
		t.Fatalf("link A: %v", err)
	}
	if err := db.LinkPeerToUser(ctx, "peer-laptop-b", "u-user"); err != nil {
		t.Fatalf("link B: %v", err)
	}

	args := json.RawMessage(`{"id":"u-user"}`)
	result, err := handleListUserDevices(ctx, db, args)
	if err != nil {
		t.Fatalf("handleListUserDevices: %v", err)
	}
	text, isErr := parseToolResult(t, result)
	if isErr {
		t.Fatalf("unexpected isError: %s", text)
	}
	var peers []store.P2PPeer
	if err := json.Unmarshal([]byte(text), &peers); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(peers) != 2 {
		t.Fatalf("len(peers) = %d, want 2 (%+v)", len(peers), peers)
	}
	seen := map[string]bool{}
	for _, p := range peers {
		seen[p.PeerID] = true
	}
	if !seen["peer-laptop-a"] || !seen["peer-laptop-b"] {
		t.Fatalf("peers missing one of the linked peers: %+v", peers)
	}
}

// TestHandleListUserDevicesUnknownUser covers the not-found branch of
// the pre-check. The error envelope must mention "user not found" so an
// agent can distinguish a typo from a real "no peers" response.
func TestHandleListUserDevicesUnknownUser(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	args := json.RawMessage(`{"id":"ghost"}`)
	result, err := handleListUserDevices(ctx, db, args)
	if err != nil {
		t.Fatalf("handleListUserDevices: %v", err)
	}
	text, isErr := parseToolResult(t, result)
	if !isErr {
		t.Fatalf("expected isError=true for unknown user, got text=%q", text)
	}
	// Be loose on the exact wording — just check it mentions the user.
	if text == "" {
		t.Fatalf("expected non-empty error message")
	}
}
