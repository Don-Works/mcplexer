package index

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
)

// fakeCodeIndexStore is a minimal store.CodeIndexStore that reports the
// workspace was never built. It proves the stage-0 contracts compile and
// wire together end to end without a real DB.
type fakeCodeIndexStore struct{}

func (fakeCodeIndexStore) UpsertCodeIndexedFiles(ctx context.Context, workspaceID string, files []store.IndexedFile) error {
	return nil
}

func (fakeCodeIndexStore) DeleteCodeIndexFiles(ctx context.Context, workspaceID string, paths []string) error {
	return nil
}

func (fakeCodeIndexStore) ListCodeIndexFileStats(ctx context.Context, workspaceID string) ([]store.CodeIndexFileStat, error) {
	return nil, nil
}

func (fakeCodeIndexStore) GetCodeIndexFile(ctx context.Context, workspaceID, path string) (*store.CodeIndexFile, error) {
	return nil, store.ErrNotFound
}

func (fakeCodeIndexStore) ListCodeIndexSymbolsByPath(ctx context.Context, workspaceID, path string) ([]store.CodeIndexSymbol, error) {
	return nil, nil
}

func (fakeCodeIndexStore) SearchCodeIndexSymbols(ctx context.Context, q store.CodeIndexSymbolQuery) ([]store.CodeIndexSymbolHit, error) {
	return nil, nil
}

func (fakeCodeIndexStore) SearchCodeIndexFiles(ctx context.Context, workspaceID, query string, limit int) ([]store.CodeIndexFileHit, error) {
	return nil, nil
}

func (fakeCodeIndexStore) ListCodeIndexEdges(ctx context.Context, f store.CodeIndexEdgeFilter) ([]store.CodeIndexEdgeHit, error) {
	return nil, nil
}

func (fakeCodeIndexStore) PutCodeIndexBuild(ctx context.Context, b *store.CodeIndexBuild) error {
	return nil
}

func (fakeCodeIndexStore) GetCodeIndexBuild(ctx context.Context, workspaceID string) (*store.CodeIndexBuild, error) {
	return nil, store.ErrNotFound
}

func (fakeCodeIndexStore) CountCodeIndexSymbols(ctx context.Context, workspaceID string) (int, error) {
	return 0, nil
}

// Compile-time proof the fake satisfies the frozen interface.
var _ store.CodeIndexStore = fakeCodeIndexStore{}

// TestServiceStatusNotBuilt is the P7 smoke test: Status on a never-built
// workspace returns the ErrNotBuilt sentinel.
func TestServiceStatusNotBuilt(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := NewService(fakeCodeIndexStore{}, logger)

	_, err := svc.Status(context.Background(), "ws1", "/tmp/x")
	if !errors.Is(err, ErrNotBuilt) {
		t.Fatalf("Status on never-built workspace: got %v, want ErrNotBuilt", err)
	}
}
