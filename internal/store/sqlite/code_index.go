package sqlite

import (
	"context"

	"github.com/don-works/mcplexer/internal/store"
)

// STAGE 0 CONTRACTS PLACEHOLDER.
//
// These stub implementations exist only so *DB satisfies the store.Store
// composite (which gained store.CodeIndexStore in migration 127's stage-0
// contract) and `go build ./...` stays green before the real write/read
// paths land. Stage 1 Agent B REPLACES this file wholesale — the real
// UpsertCodeIndexedFiles / DeleteCodeIndexFiles / PutCodeIndexBuild (write
// path) plus a sibling code_index_query.go (read path) per plan §2/§4/P2.
//
// Behavior of the stubs: writes are no-ops; list/search return empty; the
// getters return store.ErrNotFound (i.e. "never built"), which lets the
// index service compile and degrade cleanly until the real impl arrives.

// UpsertCodeIndexedFiles is a stage-0 no-op. Replaced by Agent B.
func (d *DB) UpsertCodeIndexedFiles(ctx context.Context, workspaceID string, files []store.IndexedFile) error {
	return nil
}

// DeleteCodeIndexFiles is a stage-0 no-op. Replaced by Agent B.
func (d *DB) DeleteCodeIndexFiles(ctx context.Context, workspaceID string, paths []string) error {
	return nil
}

// ListCodeIndexFileStats is a stage-0 stub returning no rows. Replaced by Agent B.
func (d *DB) ListCodeIndexFileStats(ctx context.Context, workspaceID string) ([]store.CodeIndexFileStat, error) {
	return nil, nil
}

// GetCodeIndexFile is a stage-0 stub. Replaced by Agent B.
func (d *DB) GetCodeIndexFile(ctx context.Context, workspaceID, path string) (*store.CodeIndexFile, error) {
	return nil, store.ErrNotFound
}

// ListCodeIndexSymbolsByPath is a stage-0 stub returning no rows. Replaced by Agent B.
func (d *DB) ListCodeIndexSymbolsByPath(ctx context.Context, workspaceID, path string) ([]store.CodeIndexSymbol, error) {
	return nil, nil
}

// SearchCodeIndexSymbols is a stage-0 stub returning no hits. Replaced by Agent B.
func (d *DB) SearchCodeIndexSymbols(ctx context.Context, q store.CodeIndexSymbolQuery) ([]store.CodeIndexSymbolHit, error) {
	return nil, nil
}

// SearchCodeIndexFiles is a stage-0 stub returning no hits. Replaced by Agent B.
func (d *DB) SearchCodeIndexFiles(ctx context.Context, workspaceID, query string, limit int) ([]store.CodeIndexFileHit, error) {
	return nil, nil
}

// ListCodeIndexEdges is a stage-0 stub returning no edges. Replaced by Agent B.
func (d *DB) ListCodeIndexEdges(ctx context.Context, f store.CodeIndexEdgeFilter) ([]store.CodeIndexEdgeHit, error) {
	return nil, nil
}

// PutCodeIndexBuild is a stage-0 no-op. Replaced by Agent B.
func (d *DB) PutCodeIndexBuild(ctx context.Context, b *store.CodeIndexBuild) error {
	return nil
}

// GetCodeIndexBuild is a stage-0 stub reporting "never built". Replaced by Agent B.
func (d *DB) GetCodeIndexBuild(ctx context.Context, workspaceID string) (*store.CodeIndexBuild, error) {
	return nil, store.ErrNotFound
}
