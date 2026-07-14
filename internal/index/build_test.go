package index

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testService(t *testing.T) (*Service, *memStore) {
	t.Helper()
	ms := newMemStore()
	svc := NewService(ms, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return svc, ms
}

func writeWorkspaceFile(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

const goFileA = `package a

// Alpha does a thing.
func Alpha() int { return 1 }

func beta() {}
`

const tsFileB = `export function widget(): number {
  return 2;
}
`

func newWorkspace(t *testing.T) string {
	dir := t.TempDir()
	writeWorkspaceFile(t, dir, "a.go", goFileA)
	writeWorkspaceFile(t, dir, "web/b.ts", tsFileB)
	return dir
}

func TestBuildCold(t *testing.T) {
	svc, ms := testService(t)
	dir := newWorkspace(t)
	indexID := indexIDForRoot(dir)
	res, err := svc.Build(context.Background(), BuildRequest{WorkspaceID: "ws", Root: dir})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if res.FilesIndexed != 2 {
		t.Errorf("FilesIndexed = %d, want 2", res.FilesIndexed)
	}
	if res.SymbolCount != 3 { // Alpha, beta, widget
		t.Errorf("SymbolCount = %d, want 3", res.SymbolCount)
	}
	if !res.Complete {
		t.Fatalf("cold build unexpectedly incomplete: %+v", res.Warnings)
	}
	build, err := ms.GetCodeIndexBuild(context.Background(), indexID)
	if err != nil {
		t.Fatalf("build row missing: %v", err)
	}
	if build.FileCount != 2 || build.SymbolCount != 3 {
		t.Errorf("build row counts = files %d symbols %d, want 2/3", build.FileCount, build.SymbolCount)
	}
}

func TestBuildWallGuardMarksResultIncomplete(t *testing.T) {
	svc, _ := testService(t)
	br := &buildRun{
		svc:      svc,
		res:      &BuildResult{},
		deadline: time.Now().Add(-time.Second),
	}
	if err := br.processAll(context.Background(), []string{"never-read.go"}); err != nil {
		t.Fatal(err)
	}
	if !br.incomplete || len(br.res.Warnings) == 0 || !strings.Contains(br.res.Warnings[0], "partial") {
		t.Fatalf("wall guard did not mark partial build: incomplete=%v warnings=%v", br.incomplete, br.res.Warnings)
	}
}

func TestBuildIncrementalUnchanged(t *testing.T) {
	svc, _ := testService(t)
	dir := newWorkspace(t)
	ctx := context.Background()
	if _, err := svc.Build(ctx, BuildRequest{WorkspaceID: "ws", Root: dir}); err != nil {
		t.Fatal(err)
	}
	res, err := svc.Build(ctx, BuildRequest{WorkspaceID: "ws", Root: dir})
	if err != nil {
		t.Fatal(err)
	}
	if res.FilesIndexed != 0 || res.FilesUnchanged != 2 {
		t.Errorf("rebuild = indexed %d unchanged %d, want 0/2", res.FilesIndexed, res.FilesUnchanged)
	}
}

func TestBuildHashChangeReindex(t *testing.T) {
	svc, ms := testService(t)
	dir := newWorkspace(t)
	indexID := indexIDForRoot(dir)
	ctx := context.Background()
	if _, err := svc.Build(ctx, BuildRequest{WorkspaceID: "ws", Root: dir}); err != nil {
		t.Fatal(err)
	}
	writeWorkspaceFile(t, dir, "a.go", goFileA+"\n// Gamma added\nfunc Gamma() {}\n")
	res, err := svc.Build(ctx, BuildRequest{WorkspaceID: "ws", Root: dir})
	if err != nil {
		t.Fatal(err)
	}
	if res.FilesIndexed != 1 {
		t.Errorf("FilesIndexed = %d, want 1", res.FilesIndexed)
	}
	syms, _ := ms.ListCodeIndexSymbolsByPath(ctx, indexID, "a.go")
	if _, ok := symByName(syms, "Gamma"); !ok {
		t.Error("new symbol Gamma should be indexed after the edit")
	}
	build, _ := ms.GetCodeIndexBuild(ctx, indexID)
	if build.SymbolCount != 4 { // Alpha, beta, widget, Gamma
		t.Errorf("carried symbol total = %d, want 4", build.SymbolCount)
	}
}

func TestBuildDeletion(t *testing.T) {
	svc, ms := testService(t)
	dir := newWorkspace(t)
	indexID := indexIDForRoot(dir)
	ctx := context.Background()
	if _, err := svc.Build(ctx, BuildRequest{WorkspaceID: "ws", Root: dir}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "web/b.ts")); err != nil {
		t.Fatal(err)
	}
	res, err := svc.Build(ctx, BuildRequest{WorkspaceID: "ws", Root: dir})
	if err != nil {
		t.Fatal(err)
	}
	if res.FilesRemoved != 1 {
		t.Errorf("FilesRemoved = %d, want 1", res.FilesRemoved)
	}
	if _, err := ms.GetCodeIndexFile(ctx, indexID, "web/b.ts"); err == nil {
		t.Error("deleted file should be gone from the store")
	}
}

// TestBuildScopedPreservesOutOfScope guards against the scoped-deletion bug:
// a paths-restricted build must never prune rows outside its scope, and its
// build row must still report whole-index totals.
func TestBuildScopedPreservesOutOfScope(t *testing.T) {
	svc, ms := testService(t)
	dir := newWorkspace(t)
	indexID := indexIDForRoot(dir)
	ctx := context.Background()
	if _, err := svc.Build(ctx, BuildRequest{WorkspaceID: "ws", Root: dir}); err != nil {
		t.Fatal(err)
	}
	res, err := svc.Build(ctx, BuildRequest{WorkspaceID: "ws", Root: dir, Paths: []string{"web"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.FilesRemoved != 0 {
		t.Errorf("scoped build removed %d out-of-scope files, want 0", res.FilesRemoved)
	}
	if _, err := ms.GetCodeIndexFile(ctx, indexID, "a.go"); err != nil {
		t.Errorf("out-of-scope a.go pruned by scoped build: %v", err)
	}
	build, err := ms.GetCodeIndexBuild(ctx, indexID)
	if err != nil {
		t.Fatal(err)
	}
	if build.FileCount != 2 {
		t.Errorf("scoped build row FileCount = %d, want whole-index 2", build.FileCount)
	}

	// A file deleted inside the scope is still pruned by a scoped build.
	if err := os.Remove(filepath.Join(dir, "web/b.ts")); err != nil {
		t.Fatal(err)
	}
	res, err = svc.Build(ctx, BuildRequest{WorkspaceID: "ws", Root: dir, Paths: []string{"web"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.FilesRemoved != 1 {
		t.Errorf("FilesRemoved = %d, want 1", res.FilesRemoved)
	}
	if _, err := ms.GetCodeIndexFile(ctx, indexID, "a.go"); err != nil {
		t.Errorf("out-of-scope a.go pruned on second scoped build: %v", err)
	}
}

func TestBuildScopedForceReportsWholeSymbolCount(t *testing.T) {
	svc, ms := testService(t)
	dir := newWorkspace(t)
	indexID := indexIDForRoot(dir)
	ctx := context.Background()
	if _, err := svc.Build(ctx, BuildRequest{WorkspaceID: "ws", Root: dir}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Build(ctx, BuildRequest{
		WorkspaceID: "ws", Root: dir, Paths: []string{"web"}, Force: true,
	}); err != nil {
		t.Fatal(err)
	}
	build, err := ms.GetCodeIndexBuild(ctx, indexID)
	if err != nil {
		t.Fatal(err)
	}
	if build.SymbolCount != 3 {
		t.Errorf("scoped force SymbolCount = %d, want whole-index 3", build.SymbolCount)
	}
	if build.FileCount != 2 {
		t.Errorf("scoped force FileCount = %d, want whole-index 2", build.FileCount)
	}
}

func TestBuildForce(t *testing.T) {
	svc, _ := testService(t)
	dir := newWorkspace(t)
	ctx := context.Background()
	if _, err := svc.Build(ctx, BuildRequest{WorkspaceID: "ws", Root: dir}); err != nil {
		t.Fatal(err)
	}
	res, err := svc.Build(ctx, BuildRequest{WorkspaceID: "ws", Root: dir, Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.FilesIndexed != 2 || res.FilesUnchanged != 0 {
		t.Errorf("force = indexed %d unchanged %d, want 2/0", res.FilesIndexed, res.FilesUnchanged)
	}
}

func TestBuildSkipsLargeAndBinary(t *testing.T) {
	svc, ms := testService(t)
	dir := t.TempDir()
	indexID := indexIDForRoot(dir)
	writeWorkspaceFile(t, dir, "big.txt", strings.Repeat("x", (1<<20)+10))
	writeWorkspaceFile(t, dir, "blob.dat", "abc\x00def")
	ctx := context.Background()
	res, err := svc.Build(ctx, BuildRequest{WorkspaceID: "ws", Root: dir})
	if err != nil {
		t.Fatal(err)
	}
	if res.FilesSkipped != 2 {
		t.Fatalf("FilesSkipped = %d, want 2", res.FilesSkipped)
	}
	big, _ := ms.GetCodeIndexFile(ctx, indexID, "big.txt")
	if big == nil || !strings.Contains(big.SkippedReason, "1 MiB") {
		t.Errorf("big file skip reason = %v", big)
	}
	blob, _ := ms.GetCodeIndexFile(ctx, indexID, "blob.dat")
	if blob == nil || blob.SkippedReason != "binary file" {
		t.Errorf("binary skip reason = %v", blob)
	}
}

func TestBuildSingleFlight(t *testing.T) {
	svc, _ := testService(t)
	dir := newWorkspace(t)
	indexID := indexIDForRoot(dir)
	svc.buildWait = 20 * time.Millisecond
	svc.guard.Lock()
	svc.inflight[indexID] = true // simulate an in-flight build for this physical repo index
	svc.guard.Unlock()
	_, err := svc.Build(context.Background(), BuildRequest{WorkspaceID: "ws", Root: dir})
	if !errors.Is(err, ErrBuildInProgress) {
		t.Fatalf("concurrent build: got %v, want ErrBuildInProgress", err)
	}
}

func TestBuildRootUnsafe(t *testing.T) {
	svc, _ := testService(t)
	for _, root := range []string{"", "/", filepath.Join(t.TempDir(), "does-not-exist")} {
		if _, err := svc.Build(context.Background(), BuildRequest{WorkspaceID: "ws", Root: root}); !errors.Is(err, ErrRootUnsafe) {
			t.Errorf("Build(root=%q) = %v, want ErrRootUnsafe", root, err)
		}
	}
}
