package routing

import (
	"context"

	"github.com/don-works/mcplexer/internal/store"
)

// CodeIndexStore stubs for mockRouteStore. Added when migration 127's stage-0
// contract put store.CodeIndexStore into the store.Store composite. The
// routing engine never touches the code index, so these are inert no-ops.

func (m *mockRouteStore) UpsertCodeIndexedFiles(_ context.Context, _ string, _ []store.IndexedFile) error {
	return nil
}

func (m *mockRouteStore) DeleteCodeIndexFiles(_ context.Context, _ string, _ []string) error {
	return nil
}

func (m *mockRouteStore) ListCodeIndexFileStats(_ context.Context, _ string) ([]store.CodeIndexFileStat, error) {
	return nil, nil
}

func (m *mockRouteStore) GetCodeIndexFile(_ context.Context, _, _ string) (*store.CodeIndexFile, error) {
	return nil, store.ErrNotFound
}

func (m *mockRouteStore) ListCodeIndexSymbolsByPath(_ context.Context, _, _ string) ([]store.CodeIndexSymbol, error) {
	return nil, nil
}

func (m *mockRouteStore) SearchCodeIndexSymbols(_ context.Context, _ store.CodeIndexSymbolQuery) ([]store.CodeIndexSymbolHit, error) {
	return nil, nil
}

func (m *mockRouteStore) SearchCodeIndexFiles(_ context.Context, _, _ string, _ int) ([]store.CodeIndexFileHit, error) {
	return nil, nil
}

func (m *mockRouteStore) ListCodeIndexEdges(_ context.Context, _ store.CodeIndexEdgeFilter) ([]store.CodeIndexEdgeHit, error) {
	return nil, nil
}

func (m *mockRouteStore) PutCodeIndexBuild(_ context.Context, _ *store.CodeIndexBuild) error {
	return nil
}

func (m *mockRouteStore) GetCodeIndexBuild(_ context.Context, _ string) (*store.CodeIndexBuild, error) {
	return nil, store.ErrNotFound
}
