package sqlite_test

import (
	"context"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
)

// TestCreateWorkspace_WithParent verifies the parent_id column (migration
// 092) round-trips through Create + Get + List.
func TestCreateWorkspace_WithParent(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	parent := &store.Workspace{ID: "acme", Name: "Acme", RootPath: "/code/acme"}
	if err := db.CreateWorkspace(ctx, parent); err != nil {
		t.Fatalf("create parent: %v", err)
	}

	tests := []struct {
		name    string
		ws      *store.Workspace
		wantPar string
	}{
		{
			name:    "child with parent",
			ws:      &store.Workspace{ID: "acme-api", Name: "Acme API", RootPath: "/code/acme-api", ParentID: "acme"},
			wantPar: "acme",
		},
		{
			name:    "root with no parent",
			ws:      &store.Workspace{ID: "solo", Name: "Solo", RootPath: "/code/solo"},
			wantPar: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := db.CreateWorkspace(ctx, tc.ws); err != nil {
				t.Fatalf("create: %v", err)
			}
			got, err := db.GetWorkspace(ctx, tc.ws.ID)
			if err != nil {
				t.Fatalf("get: %v", err)
			}
			if got.ParentID != tc.wantPar {
				t.Errorf("ParentID = %q, want %q", got.ParentID, tc.wantPar)
			}
		})
	}
}

// TestUpdateWorkspace_ParentRoundTrip verifies parent_id survives an update
// (both set and clear).
func TestUpdateWorkspace_ParentRoundTrip(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	if err := db.CreateWorkspace(ctx, &store.Workspace{ID: "acme", Name: "Acme"}); err != nil {
		t.Fatalf("create parent: %v", err)
	}
	child := &store.Workspace{ID: "acme-web", Name: "Acme Web", RootPath: "/code/acme-web"}
	if err := db.CreateWorkspace(ctx, child); err != nil {
		t.Fatalf("create child: %v", err)
	}

	// Set parent.
	child.ParentID = "acme"
	if err := db.UpdateWorkspace(ctx, child); err != nil {
		t.Fatalf("update set parent: %v", err)
	}
	got, _ := db.GetWorkspace(ctx, child.ID)
	if got.ParentID != "acme" {
		t.Fatalf("after set: ParentID = %q, want acme", got.ParentID)
	}

	// Clear parent.
	child.ParentID = ""
	if err := db.UpdateWorkspace(ctx, child); err != nil {
		t.Fatalf("update clear parent: %v", err)
	}
	got, _ = db.GetWorkspace(ctx, child.ID)
	if got.ParentID != "" {
		t.Fatalf("after clear: ParentID = %q, want empty", got.ParentID)
	}
}

// TestListWorkspaces_ParentRoundTrip confirms ListWorkspaces carries
// parent_id on every row.
func TestListWorkspaces_ParentRoundTrip(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	_ = db.CreateWorkspace(ctx, &store.Workspace{ID: "acme", Name: "Acme"})
	_ = db.CreateWorkspace(ctx, &store.Workspace{ID: "acme-api", Name: "API", ParentID: "acme"})

	list, err := db.ListWorkspaces(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	byID := map[string]store.Workspace{}
	for _, w := range list {
		byID[w.ID] = w
	}
	if byID["acme-api"].ParentID != "acme" {
		t.Errorf("acme-api ParentID = %q, want acme", byID["acme-api"].ParentID)
	}
	if byID["acme"].ParentID != "" {
		t.Errorf("acme ParentID = %q, want empty", byID["acme"].ParentID)
	}
}

// TestIndexFile_Source verifies the index_files.source column (migration
// 092): default 'central' when unset, explicit 'repo' preserved.
func TestIndexFile_Source(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{"default central when empty", "", store.IndexSourceCentral},
		{"explicit repo", store.IndexSourceRepo, store.IndexSourceRepo},
		{"explicit central", store.IndexSourceCentral, store.IndexSourceCentral},
	}
	for i, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := "/brain/file" + string(rune('a'+i)) + ".md"
			f := &store.IndexFile{Path: path, WorkspaceID: "ws", Source: tc.in, Sha: "x"}
			if err := db.UpsertIndexFile(ctx, f); err != nil {
				t.Fatalf("upsert: %v", err)
			}
			got, err := db.GetIndexFile(ctx, path)
			if err != nil {
				t.Fatalf("get: %v", err)
			}
			if got.Source != tc.want {
				t.Errorf("Source = %q, want %q", got.Source, tc.want)
			}
		})
	}
}
