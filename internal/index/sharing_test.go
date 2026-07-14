package index

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestIndexSharedAcrossWorkspaceIDsForSameRoot(t *testing.T) {
	dir := newWorkspace(t)
	svc, ms := testService(t)
	ctx := context.Background()

	first, err := svc.Build(ctx, BuildRequest{WorkspaceID: "workspace-a", Root: dir})
	if err != nil {
		t.Fatal(err)
	}
	if first.IndexID == "" || first.IndexID == "workspace-a" {
		t.Fatalf("physical index id = %q", first.IndexID)
	}
	hits, err := svc.Symbols(ctx, SymbolsRequest{
		WorkspaceID: "workspace-b", Root: dir, Query: "Alpha",
	})
	if err != nil || len(hits) == 0 {
		t.Fatalf("second shared workspace did not reuse index: hits=%v err=%v", hits, err)
	}
	if len(ms.builds) != 1 {
		t.Fatalf("same root created %d physical indexes, want 1", len(ms.builds))
	}
}

func TestIndexRootSymlinkUsesSamePhysicalNamespace(t *testing.T) {
	dir := newWorkspace(t)
	alias := filepath.Join(t.TempDir(), "repo-link")
	if err := os.Symlink(dir, alias); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}
	if got, want := indexIDForRoot(alias), indexIDForRoot(dir); got != want {
		t.Fatalf("symlink index id = %q, want %q", got, want)
	}
}

func TestDifferentRootsNeverSharePhysicalIndex(t *testing.T) {
	if a, b := indexIDForRoot(newWorkspace(t)), indexIDForRoot(newWorkspace(t)); a == b {
		t.Fatalf("different roots shared index id %q", a)
	}
}
