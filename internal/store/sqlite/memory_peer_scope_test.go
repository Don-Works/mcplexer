// memory_peer_scope_test.go — coverage for GetMemoryForPeer, the
// cross-peer scope-aware single-row lookup that closes the JTAC65
// side-channel.
//
// The load-bearing claim being tested: the un-granted workspace's row
// is EXCLUDED in the SQL WHERE clause — it never loads into the Go
// scanMemory path. We can't directly observe "did the row hit Go" from
// a test, but we CAN verify the contract: GetMemoryForPeer returns
// store.ErrNotFound (same sentinel as a genuinely missing id)
// whenever the row's workspace_id falls outside the peer's grant set.
// Combined with the deny-envelope test in internal/p2p, the wire
// reply is then bytewise-identical for "not granted" and "not
// exists" — which closes the channel.
package sqlite

import (
	"context"
	"errors"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
)

// seedMemoryWithWS writes one memory in workspaceID (or global when
// workspaceID==""). Returns the assigned ID for cross-checks.
func seedMemoryWithWS(t *testing.T, d *DB, name, workspaceID string) string {
	t.Helper()
	ctx := context.Background()
	e := &store.MemoryEntry{
		Name:    name,
		Kind:    store.MemoryKindNote,
		Content: name + " content body",
	}
	if workspaceID != "" {
		ws := workspaceID
		e.WorkspaceID = &ws
	}
	if err := d.WriteMemory(ctx, e); err != nil {
		t.Fatalf("seed %s: %v", name, err)
	}
	return e.ID
}

// TestGetMemoryForPeerGlobalAllowed asserts the (allowGlobal=true,
// allowedWorkspaceIDs=[]) shape returns global memories.
func TestGetMemoryForPeerGlobalAllowed(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	id := seedMemoryWithWS(t, d, "g1", "")

	got, err := d.GetMemoryForPeer(ctx, id, nil, true)
	if err != nil {
		t.Fatalf("GetMemoryForPeer: %v", err)
	}
	if got.ID != id {
		t.Errorf("id mismatch: got %q, want %q", got.ID, id)
	}
}

// TestGetMemoryForPeerWorkspaceScopedDeniedWhenGlobalOnly asserts the
// load-bearing case: a workspace-scoped memory returns ErrNotFound to
// a peer who only holds the bare boolean grant (admits global only).
// This is THE leak the D7.3 scenario tests on the cross-peer path.
func TestGetMemoryForPeerWorkspaceScopedDeniedWhenGlobalOnly(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	id := seedMemoryWithWS(t, d, "alpha-private-canary", "alpha-private")

	got, err := d.GetMemoryForPeer(ctx, id, nil, true)
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got err=%v row=%+v", err, got)
	}
	if got != nil {
		t.Errorf("returned row should be nil when scope check fails, got %+v", got)
	}
}

// TestGetMemoryForPeerWorkspaceScopedAllowedWithMatchingGrant verifies
// the happy path: explicit workspace in the allow-list surfaces the row.
func TestGetMemoryForPeerWorkspaceScopedAllowedWithMatchingGrant(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	id := seedMemoryWithWS(t, d, "alpha-shared-canary", "alpha-shared")

	got, err := d.GetMemoryForPeer(ctx, id,
		[]string{"alpha-shared"}, false)
	if err != nil {
		t.Fatalf("GetMemoryForPeer: %v", err)
	}
	if got.ID != id {
		t.Errorf("id mismatch: got %q, want %q", got.ID, id)
	}
}

// TestGetMemoryForPeerOtherWorkspaceDenied asserts a grant for "X"
// does NOT admit a row in workspace "Y" — adjacency check.
func TestGetMemoryForPeerOtherWorkspaceDenied(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	id := seedMemoryWithWS(t, d, "alpha-private-canary", "alpha-private")

	_, err := d.GetMemoryForPeer(ctx, id,
		[]string{"alpha-shared"}, false)
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for cross-workspace lookup, got %v", err)
	}
}

// TestGetMemoryForPeerMissingIDReturnsErrNotFound — same-sentinel guard.
// The cross-peer caller maps ErrNotFound to the constant-shape deny
// envelope; this test confirms a genuinely-missing id surfaces the same
// sentinel as the not-granted case above. The two cases are
// indistinguishable to the wire, which is the whole point.
func TestGetMemoryForPeerMissingIDReturnsErrNotFound(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	_ = seedMemoryWithWS(t, d, "existing-global", "")

	_, err := d.GetMemoryForPeer(ctx, "01HZ-MISSING-ID-NEVER-WRITTEN",
		nil, true)
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for missing id, got %v", err)
	}
}

// TestGetMemoryForPeerEmptyScopeShortCircuits — defensive guard: a
// caller that passes (allowGlobal=false, allowedWorkspaceIDs=nil) gets
// ErrNotFound without a SQL roundtrip. Closes the door on a degenerate
// caller silently widening the query to "all rows".
func TestGetMemoryForPeerEmptyScopeShortCircuits(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	id := seedMemoryWithWS(t, d, "g2", "")

	_, err := d.GetMemoryForPeer(ctx, id, nil, false)
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for empty scope, got %v", err)
	}
}

// TestGetMemoryForPeerSoftDeletedExcluded verifies the soft-delete
// filter still applies on the peer path. A soft-deleted row in a
// granted workspace must NOT surface — the deleted_at IS NULL clause
// is preserved.
func TestGetMemoryForPeerSoftDeletedExcluded(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	id := seedMemoryWithWS(t, d, "to-delete", "alpha-shared")
	if err := d.SoftDeleteMemory(ctx, id); err != nil {
		t.Fatalf("soft delete: %v", err)
	}
	_, err := d.GetMemoryForPeer(ctx, id,
		[]string{"alpha-shared"}, false)
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for soft-deleted row, got %v", err)
	}
}

// TestGetMemoryForPeerMultipleWorkspaceGrants asserts the IN clause
// expands correctly when the peer holds grants on several workspaces.
// One row in workspace A is returnable; one row in workspace C (not
// granted) is hidden; the peer holds grants for A + B.
func TestGetMemoryForPeerMultipleWorkspaceGrants(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	idA := seedMemoryWithWS(t, d, "a-row", "alpha-shared")
	idC := seedMemoryWithWS(t, d, "c-row", "alpha-other")

	gotA, err := d.GetMemoryForPeer(ctx, idA,
		[]string{"alpha-shared", "alpha-second"}, false)
	if err != nil {
		t.Fatalf("granted workspace lookup: %v", err)
	}
	if gotA.ID != idA {
		t.Errorf("granted lookup mismatch: got %q want %q", gotA.ID, idA)
	}

	_, err = d.GetMemoryForPeer(ctx, idC,
		[]string{"alpha-shared", "alpha-second"}, false)
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("un-granted workspace must return ErrNotFound, got %v", err)
	}
}
