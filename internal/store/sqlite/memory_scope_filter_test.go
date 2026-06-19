// memory_scope_filter_test.go — coverage for scopeFilteredWhereClauseAlias
// and the MemoryFilter.ScopeFilter field. Exercises the three valid values
// ("global_only", "workspace_only", "") and confirms invalid scope values
// are caught by the handler (tested at the gateway level).
package sqlite

import (
	"context"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
)

// writeGlobal writes a memory with no workspace (global scope).
func writeGlobal(t *testing.T, d *DB, name string) string {
	t.Helper()
	return mustWrite(t, d, name, "")
}

// writeWorkspace writes a memory scoped to wsID.
func writeWorkspace(t *testing.T, d *DB, name, wsID string) string {
	t.Helper()
	return mustWrite(t, d, name, wsID)
}

// TestScopeFilter_GlobalOnly confirms that ScopeFilter="global_only" returns
// only rows with workspace_id IS NULL, even when the session scope carries
// workspace IDs.
func TestScopeFilter_GlobalOnly(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)

	wsID := "ws-scope-test"
	writeGlobal(t, d, "global-note-1")
	writeGlobal(t, d, "global-note-2")
	writeWorkspace(t, d, "ws-note-1", wsID)
	writeWorkspace(t, d, "ws-note-2", wsID)

	got, err := d.ListMemories(ctx, store.MemoryFilter{
		Scope:       store.SkillScope{WorkspaceIDs: []string{wsID}},
		ScopeFilter: "global_only",
	})
	if err != nil {
		t.Fatalf("ListMemories global_only: %v", err)
	}
	for _, e := range got {
		if e.WorkspaceID != nil {
			t.Errorf("global_only returned workspace-scoped row: id=%s ws=%s",
				e.ID, *e.WorkspaceID)
		}
	}
	if len(got) < 2 {
		t.Errorf("global_only returned %d rows, want at least 2 global rows", len(got))
	}
}

// TestScopeFilter_WorkspaceOnly confirms that ScopeFilter="workspace_only"
// returns only rows with the session's workspace ID, excluding global rows.
func TestScopeFilter_WorkspaceOnly(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)

	wsID := "ws-scope-wsonly"
	writeGlobal(t, d, "global-should-be-hidden")
	writeWorkspace(t, d, "ws-should-appear", wsID)
	writeWorkspace(t, d, "ws-should-appear-2", wsID)

	got, err := d.ListMemories(ctx, store.MemoryFilter{
		Scope:       store.SkillScope{WorkspaceIDs: []string{wsID}},
		ScopeFilter: "workspace_only",
	})
	if err != nil {
		t.Fatalf("ListMemories workspace_only: %v", err)
	}
	for _, e := range got {
		if e.WorkspaceID == nil {
			t.Errorf("workspace_only returned global row: id=%s name=%s", e.ID, e.Name)
		} else if *e.WorkspaceID != wsID {
			t.Errorf("workspace_only returned row from wrong workspace: %s", *e.WorkspaceID)
		}
	}
	if len(got) != 2 {
		t.Errorf("workspace_only returned %d rows, want 2 workspace rows", len(got))
	}
}

// TestScopeFilter_WorkspaceOnly_EmptyWorkspaceIDs confirms that
// ScopeFilter="workspace_only" with no workspace IDs in scope short-circuits
// to "0 = 1" and returns no rows rather than silently widening to global.
func TestScopeFilter_WorkspaceOnly_EmptyWorkspaceIDs(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)

	writeGlobal(t, d, "global-not-returned")
	writeWorkspace(t, d, "ws-not-returned", "ws-other")

	got, err := d.ListMemories(ctx, store.MemoryFilter{
		Scope:       store.SkillScope{WorkspaceIDs: nil}, // no workspace IDs
		ScopeFilter: "workspace_only",
	})
	if err != nil {
		t.Fatalf("ListMemories workspace_only no-scope: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("workspace_only with no scope should return 0 rows, got %d", len(got))
	}
}

// TestScopeFilter_Default confirms that ScopeFilter="" (or "any") preserves
// the existing workspaces ∪ global behavior — both global and workspace rows
// are returned.
func TestScopeFilter_Default(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)

	wsID := "ws-scope-default"
	writeGlobal(t, d, "global-visible")
	writeWorkspace(t, d, "ws-visible", wsID)

	got, err := d.ListMemories(ctx, store.MemoryFilter{
		Scope:       store.SkillScope{WorkspaceIDs: []string{wsID}},
		ScopeFilter: "", // default behavior
	})
	if err != nil {
		t.Fatalf("ListMemories default: %v", err)
	}
	names := map[string]bool{}
	for _, e := range got {
		names[e.Name] = true
	}
	if !names["global-visible"] {
		t.Error("default scope should include global rows")
	}
	if !names["ws-visible"] {
		t.Error("default scope should include workspace rows")
	}
}

// TestScopeFilter_IncludeAll confirms that SkillScope.IncludeAll bypasses
// all workspace filtering regardless of ScopeFilter.
func TestScopeFilter_IncludeAll(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)

	ws1 := "ws-all-1"
	ws2 := "ws-all-2"
	writeGlobal(t, d, "global-all")
	writeWorkspace(t, d, "ws1-row", ws1)
	writeWorkspace(t, d, "ws2-row", ws2)

	// IncludeAll with ScopeFilter="workspace_only" — IncludeAll wins; all rows returned.
	got, err := d.ListMemories(ctx, store.MemoryFilter{
		Scope:       store.SkillScope{IncludeAll: true},
		ScopeFilter: "workspace_only",
	})
	if err != nil {
		t.Fatalf("ListMemories IncludeAll: %v", err)
	}
	if len(got) < 3 {
		t.Errorf("IncludeAll should return all rows, got %d", len(got))
	}
}
