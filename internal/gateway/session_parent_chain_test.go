package gateway

import (
	"testing"

	"github.com/don-works/mcplexer/internal/store"
)

// TestResolveChain_IncludesParent verifies the brain hierarchy scope fusion
// (docs/brain.md Appendix C.1): a session rooted at a child workspace
// resolves its parent (client/org tier) into the chain even when the parent
// is NOT a path ancestor, so recall/list span workspace ∪ parent ∪ global.
func TestResolveChain_IncludesParent(t *testing.T) {
	// acme-api (root /code/acme-api) has parent acme, which has parent
	// acme-group. acme has NO rootPath that is a path-ancestor of the
	// client root — it is reachable ONLY via parent_id.
	sm := &sessionManager{
		store: &mockStore{
			workspaces: []mockWorkspace{
				{id: "acme-api", rootPath: "/code/acme-api", parentID: "acme"},
				{id: "acme", rootPath: "", parentID: "acme-group"},
				{id: "acme-group", rootPath: ""},
				{id: "unrelated", rootPath: "/other"},
			},
		},
	}

	chain := sm.resolveChainForPath(t.Context(), "/code/acme-api/src")

	ids := make([]string, len(chain))
	for i, w := range chain {
		ids[i] = w.ID
	}

	want := []string{"acme-api", "acme", "acme-group"}
	if len(ids) != len(want) {
		t.Fatalf("chain = %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("chain = %v, want %v (order matters: child first, parents after)", ids, want)
		}
	}
}

// TestResolveChain_DanglingParentDegrades verifies a parent_id that points at
// a non-existent workspace degrades to "no parent" rather than erroring.
func TestResolveChain_DanglingParentDegrades(t *testing.T) {
	sm := &sessionManager{
		store: &mockStore{
			workspaces: []mockWorkspace{
				{id: "acme-api", rootPath: "/code/acme-api", parentID: "ghost"},
			},
		},
	}
	chain := sm.resolveChainForPath(t.Context(), "/code/acme-api")
	if len(chain) != 1 || chain[0].ID != "acme-api" {
		t.Fatalf("chain = %+v, want just acme-api", chain)
	}
}

// TestAppendParentChain_CycleGuard verifies a parent_id cycle does not loop
// forever and each workspace appears once.
func TestAppendParentChain_CycleGuard(t *testing.T) {
	all := []store.Workspace{
		{ID: "a", ParentID: "b"},
		{ID: "b", ParentID: "a"}, // cycle
	}
	resolved := []store.Workspace{all[0]}
	out := appendParentChain(resolved, all)

	seen := map[string]int{}
	for _, w := range out {
		seen[w.ID]++
	}
	if seen["a"] != 1 || seen["b"] != 1 {
		t.Fatalf("cycle produced duplicates/loop: %+v", out)
	}
}

// TestAppendParentChain_NoParents is the today's-behaviour case: workspaces
// with no parent_id leave the chain unchanged.
func TestAppendParentChain_NoParents(t *testing.T) {
	all := []store.Workspace{{ID: "a"}, {ID: "b"}}
	resolved := []store.Workspace{all[0]}
	out := appendParentChain(resolved, all)
	if len(out) != 1 || out[0].ID != "a" {
		t.Fatalf("no-parent chain changed: %+v", out)
	}
}
