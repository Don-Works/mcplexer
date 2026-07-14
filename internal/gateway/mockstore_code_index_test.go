package gateway

import (
	"context"

	"github.com/don-works/mcplexer/internal/store"
)

// CodeIndexStore stubs for mockStore. Added when migration 127's stage-0
// contract put store.CodeIndexStore into the store.Store composite; the
// gateway index handlers (Stage 1 Agent C) get their own richer test double,
// so these are inert no-ops here.

func (m *mockStore) UpsertCodeIndexedFiles(_ context.Context, _ string, _ []store.IndexedFile) error {
	return nil
}

func (m *mockStore) DeleteCodeIndexFiles(_ context.Context, _ string, _ []string) error {
	return nil
}

func (m *mockStore) ListCodeIndexFileStats(_ context.Context, _ string) ([]store.CodeIndexFileStat, error) {
	return nil, nil
}

func (m *mockStore) GetCodeIndexFile(_ context.Context, _, _ string) (*store.CodeIndexFile, error) {
	return nil, store.ErrNotFound
}

func (m *mockStore) ListCodeIndexSymbolsByPath(_ context.Context, _, _ string) ([]store.CodeIndexSymbol, error) {
	return nil, nil
}

func (m *mockStore) SearchCodeIndexSymbols(_ context.Context, _ store.CodeIndexSymbolQuery) ([]store.CodeIndexSymbolHit, error) {
	return nil, nil
}

func (m *mockStore) SearchCodeIndexFiles(_ context.Context, _, _ string, _ int) ([]store.CodeIndexFileHit, error) {
	return nil, nil
}

func (m *mockStore) SearchCodeIndexChunks(_ context.Context, _ store.CodeIndexChunkQuery) ([]store.CodeIndexChunkHit, error) {
	return nil, nil
}
func (m *mockStore) VectorSearchCodeIndexChunks(_ context.Context, _, _ string, _ int, _ []float32, _ int) ([]store.CodeIndexChunkHit, error) {
	return nil, nil
}
func (m *mockStore) ListCodeIndexChunksNeedingEmbedding(_ context.Context, _, _ string, _, _ int) ([]store.CodeIndexEmbedTarget, error) {
	return nil, nil
}
func (m *mockStore) CountCodeIndexEmbeddingProgress(_ context.Context, _, _ string, _ int) (int, int, error) {
	return 0, 0, nil
}
func (m *mockStore) UpsertCodeIndexChunkEmbeddings(_ context.Context, _, _ string, _ int, _ []store.CodeIndexChunkEmbedding) error {
	return nil
}

func (m *mockStore) ListCodeIndexEdges(_ context.Context, _ store.CodeIndexEdgeFilter) ([]store.CodeIndexEdgeHit, error) {
	return nil, nil
}

func (m *mockStore) PutCodeIndexBuild(_ context.Context, _ *store.CodeIndexBuild) error {
	return nil
}

func (m *mockStore) GetCodeIndexBuild(_ context.Context, _ string) (*store.CodeIndexBuild, error) {
	return nil, store.ErrNotFound
}

func (m *mockStore) CountCodeIndexSymbols(_ context.Context, _ string) (int, error) {
	return 0, nil
}

func (m *mockStore) CountCodeIndexChunks(_ context.Context, _ string) (int, error) { return 0, nil }
