package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

func TestCodeIndexCountSymbols(t *testing.T) {
	ctx := context.Background()
	db := newCodeIndexTestDB(t)
	payload := testIndexedFile("internal/kv/handler.go", "HandleKVSet", time.Now().UTC())
	payload.Symbols = append(payload.Symbols, store.CodeIndexSymbol{
		Name: "HandleKVGet", NameTokens: "handle kv get", Kind: "func", StartLine: 20,
	})
	if err := db.UpsertCodeIndexedFiles(ctx, "ws-1", []store.IndexedFile{payload}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	n, err := db.CountCodeIndexSymbols(ctx, "ws-1")
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 2 {
		t.Fatalf("symbol count = %d, want 2", n)
	}
}

func TestCodeIndexRoundTrip(t *testing.T) {
	ctx := context.Background()
	db := newCodeIndexTestDB(t)

	indexedAt := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	payload := testIndexedFile("internal/kv/handler.go", "HandleKVSet", indexedAt)

	if err := db.UpsertCodeIndexedFiles(ctx, "ws-1", []store.IndexedFile{payload}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got, err := db.GetCodeIndexFile(ctx, "ws-1", "internal/kv/handler.go")
	if err != nil {
		t.Fatalf("get file: %v", err)
	}
	if got.Path != payload.File.Path || got.ContentHash != "abc123" {
		t.Fatalf("file mismatch: %+v", got)
	}

	syms, err := db.ListCodeIndexSymbolsByPath(ctx, "ws-1", "internal/kv/handler.go")
	if err != nil {
		t.Fatalf("list symbols: %v", err)
	}
	if len(syms) != 1 || syms[0].Name != "HandleKVSet" {
		t.Fatalf("symbols: %+v", syms)
	}

	stats, err := db.ListCodeIndexFileStats(ctx, "ws-1")
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if len(stats) != 1 || stats[0].ContentHash != "abc123" {
		t.Fatalf("stats: %+v", stats)
	}
}

func TestCodeIndexUpsertPreservesFileIDAndReplacesChildren(t *testing.T) {
	ctx := context.Background()
	db := newCodeIndexTestDB(t)
	path := "pkg/a.go"
	at := time.Now().UTC()

	first := testIndexedFile(path, "OldSym", at)
	if err := db.UpsertCodeIndexedFiles(ctx, "ws-1", []store.IndexedFile{first}); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	before, err := db.GetCodeIndexFile(ctx, "ws-1", path)
	if err != nil {
		t.Fatalf("get before: %v", err)
	}

	second := testIndexedFile(path, "NewSym", at)
	second.File.ContentHash = "def456"
	second.Edges = []store.CodeIndexEdge{{
		Kind: "import", ToPath: "pkg/b.go",
	}}
	if err := db.UpsertCodeIndexedFiles(ctx, "ws-1", []store.IndexedFile{second}); err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	after, err := db.GetCodeIndexFile(ctx, "ws-1", path)
	if err != nil {
		t.Fatalf("get after: %v", err)
	}
	if after.ID != before.ID {
		t.Fatalf("file id changed: %d -> %d", before.ID, after.ID)
	}
	if after.ContentHash != "def456" {
		t.Fatalf("hash not updated: %q", after.ContentHash)
	}

	syms, err := db.ListCodeIndexSymbolsByPath(ctx, "ws-1", path)
	if err != nil {
		t.Fatalf("symbols: %v", err)
	}
	if len(syms) != 1 || syms[0].Name != "NewSym" {
		t.Fatalf("want NewSym only, got %+v", syms)
	}
	for _, s := range syms {
		if s.Name == "OldSym" {
			t.Fatalf("stale symbol OldSym still present")
		}
	}

	edges, err := db.ListCodeIndexEdges(ctx, store.CodeIndexEdgeFilter{
		WorkspaceID: "ws-1", FromPath: path,
	})
	if err != nil {
		t.Fatalf("edges: %v", err)
	}
	if len(edges) != 1 || edges[0].ToPath != "pkg/b.go" {
		t.Fatalf("edges: %+v", edges)
	}
}

func TestCodeIndexFTSSymbolSearch(t *testing.T) {
	ctx := context.Background()
	db := newCodeIndexTestDB(t)

	file := testIndexedFile("internal/kv/handler.go", "HandleKVSet", time.Now().UTC())
	file.Symbols[0].NameTokens = "handle kv set"
	if err := db.UpsertCodeIndexedFiles(ctx, "ws-1", []store.IndexedFile{file}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	hits, err := db.SearchCodeIndexSymbols(ctx, store.CodeIndexSymbolQuery{
		WorkspaceID: "ws-1", Query: "kv set", Limit: 10,
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) == 0 || hits[0].Symbol.Name != "HandleKVSet" {
		t.Fatalf("want HandleKVSet hit, got %+v", hits)
	}
	if hits[0].Score <= 0 {
		t.Fatalf("expected positive negated-bm25 score, got %v", hits[0].Score)
	}
}

func TestCodeIndexDeleteRemovesFTSRows(t *testing.T) {
	ctx := context.Background()
	db := newCodeIndexTestDB(t)
	path := "internal/kv/handler.go"

	file := testIndexedFile(path, "HandleKVSet", time.Now().UTC())
	file.Symbols[0].NameTokens = "handle kv set"
	if err := db.UpsertCodeIndexedFiles(ctx, "ws-1", []store.IndexedFile{file}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	if err := db.DeleteCodeIndexFiles(ctx, "ws-1", []string{path}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := db.GetCodeIndexFile(ctx, "ws-1", path); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("file should be gone: %v", err)
	}

	hits, err := db.SearchCodeIndexSymbols(ctx, store.CodeIndexSymbolQuery{
		WorkspaceID: "ws-1", Query: "kv set",
	})
	if err != nil {
		t.Fatalf("search after delete: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("FTS rows should be gone, got %+v", hits)
	}
}

func TestCodeIndexEdgesBothDirections(t *testing.T) {
	ctx := context.Background()
	db := newCodeIndexTestDB(t)
	at := time.Now().UTC()

	a := testIndexedFile("pkg/a.go", "A", at)
	a.Edges = []store.CodeIndexEdge{{Kind: "import", ToPath: "pkg/b.go"}}
	b := testIndexedFile("pkg/b.go", "B", at)
	b.Edges = []store.CodeIndexEdge{{Kind: "import", ToPath: "pkg/c.go"}}
	if err := db.UpsertCodeIndexedFiles(ctx, "ws-1", []store.IndexedFile{a, b}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	imports, err := db.ListCodeIndexEdges(ctx, store.CodeIndexEdgeFilter{
		WorkspaceID: "ws-1", FromPath: "pkg/a.go",
	})
	if err != nil {
		t.Fatalf("imports: %v", err)
	}
	if len(imports) != 1 || imports[0].ToPath != "pkg/b.go" {
		t.Fatalf("imports-of: %+v", imports)
	}

	importers, err := db.ListCodeIndexEdges(ctx, store.CodeIndexEdgeFilter{
		WorkspaceID: "ws-1", ToPath: "pkg/b.go",
	})
	if err != nil {
		t.Fatalf("importers: %v", err)
	}
	if len(importers) != 1 || importers[0].FromPath != "pkg/a.go" {
		t.Fatalf("importers-of: %+v", importers)
	}
}

func TestCodeIndexBuildUpsert(t *testing.T) {
	ctx := context.Background()
	db := newCodeIndexTestDB(t)

	builtAt := time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC)
	first := &store.CodeIndexBuild{
		WorkspaceID: "ws-1", RootPath: "/proj", GitHead: "aaa",
		DirtyCount: 2, BuiltAt: builtAt, DurationMS: 100,
		FileCount: 3, SymbolCount: 10, WarningsJSON: `["w1"]`,
	}
	if err := db.PutCodeIndexBuild(ctx, first); err != nil {
		t.Fatalf("put first: %v", err)
	}

	got, err := db.GetCodeIndexBuild(ctx, "ws-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.DirtyCount != 2 || got.FileCount != 3 {
		t.Fatalf("build row: %+v", got)
	}

	second := &store.CodeIndexBuild{
		WorkspaceID: "ws-1", RootPath: "/proj", GitHead: "bbb",
		DirtyCount: 0, BuiltAt: builtAt.Add(time.Hour), DurationMS: 50,
		FileCount: 4, SymbolCount: 12, WarningsJSON: `[]`,
	}
	if err := db.PutCodeIndexBuild(ctx, second); err != nil {
		t.Fatalf("put second: %v", err)
	}
	got2, err := db.GetCodeIndexBuild(ctx, "ws-1")
	if err != nil {
		t.Fatalf("get after upsert: %v", err)
	}
	if got2.GitHead != "bbb" || got2.DirtyCount != 0 || got2.FileCount != 4 {
		t.Fatalf("upserted build: %+v", got2)
	}
}

func TestCodeIndexMaliciousFTSQueries(t *testing.T) {
	ctx := context.Background()
	db := newCodeIndexTestDB(t)

	file := testIndexedFile("pkg/a.go", "Foo", time.Now().UTC())
	file.Symbols[0].NameTokens = "foo bar"
	if err := db.UpsertCodeIndexedFiles(ctx, "ws-1", []store.IndexedFile{file}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	queries := []string{`'a) OR (b`, `'"unclosed`}
	for _, q := range queries {
		if _, err := db.SearchCodeIndexSymbols(ctx, store.CodeIndexSymbolQuery{
			WorkspaceID: "ws-1", Query: q,
		}); err != nil {
			t.Fatalf("symbol search %q errored: %v", q, err)
		}
		if _, err := db.SearchCodeIndexFiles(ctx, "ws-1", q, 10); err != nil {
			t.Fatalf("file search %q errored: %v", q, err)
		}
	}
}

func TestCodeIndexGetNotFound(t *testing.T) {
	ctx := context.Background()
	db := newCodeIndexTestDB(t)

	if _, err := db.GetCodeIndexFile(ctx, "ws-1", "missing.go"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("file: %v", err)
	}
	if _, err := db.GetCodeIndexBuild(ctx, "ws-1"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("build: %v", err)
	}
}

func newCodeIndexTestDB(t *testing.T) *DB {
	t.Helper()
	ctx := context.Background()
	db, err := New(ctx, ":memory:")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func testIndexedFile(path, symName string, indexedAt time.Time) store.IndexedFile {
	return store.IndexedFile{
		File: store.CodeIndexFile{
			Path: path, PathTokens: "internal kv handler",
			Language: "go", Package: "kv", SizeBytes: 100, LineCount: 10,
			MtimeUnix: indexedAt.Unix(), ContentHash: "abc123",
			DocSummary: "kv handlers", IndexedAt: indexedAt,
		},
		Symbols: []store.CodeIndexSymbol{{
			Name: symName, NameTokens: symName, Kind: "func",
			Signature: "func() error", StartLine: 10, Exported: true,
		}},
	}
}