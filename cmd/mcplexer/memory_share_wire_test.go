// memory_share_wire_test.go — coverage for the cmd-layer glue that
// translates a paired-peer's scope slice into a workspace allowlist for
// GetMemoryForPeer. Locks down the three grant flavours that the
// peerscope registry recognises today plus the wildcard escape hatch.
//
// This helper is the choke-point that turns "this peer holds X scopes"
// into "this peer sees these workspaces" — getting it wrong re-opens
// the JTAC65 side-channel even if the SQL filter is correct.
package main

import (
	"reflect"
	"sort"
	"testing"
)

func TestAllowedWorkspacesFromScopesBareBoolean(t *testing.T) {
	ids, global, wildcard := allowedWorkspacesFromScopes(
		[]string{"mesh.memory_request"})
	if wildcard {
		t.Errorf("bare boolean must NOT trigger wildcard branch")
	}
	if !global {
		t.Errorf("bare boolean should admit global memories")
	}
	if len(ids) != 0 {
		t.Errorf("bare boolean should produce no per-workspace ids, got %v", ids)
	}
}

func TestAllowedWorkspacesFromScopesPerWorkspace(t *testing.T) {
	ids, global, wildcard := allowedWorkspacesFromScopes(
		[]string{"mesh.memory_request:alpha-shared"})
	if wildcard {
		t.Errorf("per-workspace grant must NOT trigger wildcard branch")
	}
	if global {
		t.Errorf("per-workspace grant alone must NOT admit global rows")
	}
	if !reflect.DeepEqual(ids, []string{"alpha-shared"}) {
		t.Errorf("per-workspace ids: got %v want [alpha-shared]", ids)
	}
}

func TestAllowedWorkspacesFromScopesWildcard(t *testing.T) {
	ids, global, wildcard := allowedWorkspacesFromScopes(
		[]string{"mesh.memory_request:*"})
	if !wildcard {
		t.Errorf("'mesh.memory_request:*' must trigger wildcard branch")
	}
	if !global {
		t.Errorf("wildcard must admit global rows too")
	}
	if len(ids) != 0 {
		t.Errorf("wildcard branch must NOT populate per-workspace ids "+
			"(the SQL helper falls back to unfiltered GetMemory): got %v", ids)
	}
}

func TestAllowedWorkspacesFromScopesCompositeBoolPlusWorkspace(t *testing.T) {
	// A peer that holds BOTH the bare grant AND a per-workspace grant
	// sees BOTH global rows AND the named workspace's rows.
	ids, global, wildcard := allowedWorkspacesFromScopes([]string{
		"mesh.memory_request",
		"mesh.memory_request:alpha-shared",
		"mesh.memory_request:alpha-second",
	})
	if wildcard {
		t.Errorf("composite scopes must NOT trigger wildcard")
	}
	if !global {
		t.Errorf("bare grant present → global should be admitted")
	}
	sort.Strings(ids)
	want := []string{"alpha-shared", "alpha-second"}
	sort.Strings(want)
	if !reflect.DeepEqual(ids, want) {
		t.Errorf("composite ids: got %v want %v", ids, want)
	}
}

func TestAllowedWorkspacesFromScopesNoMemoryGrant(t *testing.T) {
	// Peer holds unrelated scopes — the helper must return the
	// (nil, false, false) shape that triggers the SQL short-circuit
	// to ErrNotFound. Defensive: the stream-level gate should already
	// have rejected the peer, but if it somehow reaches the provider
	// this is the second-line block.
	ids, global, wildcard := allowedWorkspacesFromScopes(
		[]string{"trigger_worker:foo", "task_offer:bar"})
	if wildcard {
		t.Errorf("unrelated scopes must NOT trigger wildcard")
	}
	if global {
		t.Errorf("unrelated scopes must NOT admit global")
	}
	if len(ids) != 0 {
		t.Errorf("unrelated scopes must NOT populate ids, got %v", ids)
	}
}

func TestAllowedWorkspacesFromScopesEmpty(t *testing.T) {
	ids, global, wildcard := allowedWorkspacesFromScopes(nil)
	if wildcard || global || len(ids) != 0 {
		t.Errorf("nil scopes must yield (nil, false, false), got (%v, %v, %v)",
			ids, global, wildcard)
	}
}

// TestAllowedWorkspacesFromScopesWildcardPrecedence — when the wildcard
// is present alongside other grants, the wildcard branch wins (returns
// early with wildcard=true). The SQL caller uses this to fall back to
// the unscoped GetMemory, so we MUST short-circuit cleanly rather than
// also populating a per-workspace IN list that the caller will ignore.
func TestAllowedWorkspacesFromScopesWildcardPrecedence(t *testing.T) {
	ids, global, wildcard := allowedWorkspacesFromScopes([]string{
		"mesh.memory_request",
		"mesh.memory_request:alpha-shared",
		"mesh.memory_request:*", // wildcard wins
		"mesh.memory_request:alpha-second",
	})
	if !wildcard {
		t.Errorf("wildcard presence must dominate, got wildcard=%v", wildcard)
	}
	if !global {
		t.Errorf("wildcard branch must admit global")
	}
	if len(ids) != 0 {
		t.Errorf("wildcard branch must NOT populate per-workspace ids, got %v", ids)
	}
}
