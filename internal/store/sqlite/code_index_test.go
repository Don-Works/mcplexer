package sqlite

import (
	"context"
	"errors"
	"strings"
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
	payload.File.ChunkVersion = 2
	payload.Chunks = []store.CodeIndexChunk{testChunk(0, "HandleKVSet", "func Set(key string)")}

	if err := db.UpsertCodeIndexedFiles(ctx, "ws-1", []store.IndexedFile{payload}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got, err := db.GetCodeIndexFile(ctx, "ws-1", "internal/kv/handler.go")
	if err != nil {
		t.Fatalf("get file: %v", err)
	}
	if got.Path != payload.File.Path || got.ContentHash != "abc123" || got.ChunkVersion != 2 {
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
	if len(stats) != 1 || stats[0].ContentHash != "abc123" || stats[0].ChunkVersion != 2 {
		t.Fatalf("stats: %+v", stats)
	}

	n, err := db.CountCodeIndexChunks(ctx, "ws-1")
	if err != nil {
		t.Fatalf("count chunks: %v", err)
	}
	if n != 1 {
		t.Fatalf("chunk count = %d, want 1", n)
	}
}

func TestCodeIndexUpsertPreservesFileIDAndReplacesChildren(t *testing.T) {
	ctx := context.Background()
	db := newCodeIndexTestDB(t)
	path := "pkg/a.go"
	at := time.Now().UTC()

	first := testIndexedFile(path, "OldSym", at)
	first.Chunks = []store.CodeIndexChunk{testChunk(0, "OldSym", "zzxqobsolete99001")}
	if err := db.UpsertCodeIndexedFiles(ctx, "ws-1", []store.IndexedFile{first}); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	before, err := db.GetCodeIndexFile(ctx, "ws-1", path)
	if err != nil {
		t.Fatalf("get before: %v", err)
	}

	second := testIndexedFile(path, "NewSym", at)
	second.File.ContentHash = "def456"
	second.Edges = []store.CodeIndexEdge{{Kind: "import", ToPath: "pkg/b.go"}}
	second.Chunks = []store.CodeIndexChunk{testChunk(0, "NewSym", "new body")}
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

	hits, err := db.SearchCodeIndexChunks(ctx, store.CodeIndexChunkQuery{
		WorkspaceID: "ws-1", Query: "zzxqobsolete99001",
	})
	if err != nil {
		t.Fatalf("search old chunk: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("stale FTS chunk should be gone, got %+v", hits)
	}
	newHits, err := db.SearchCodeIndexChunks(ctx, store.CodeIndexChunkQuery{
		WorkspaceID: "ws-1", Query: "new body",
	})
	if err != nil {
		t.Fatalf("search new chunk: %v", err)
	}
	if len(newHits) != 1 || newHits[0].Chunk.SymbolName != "NewSym" {
		t.Fatalf("new chunk hit: %+v", newHits)
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

func TestCodeIndexChunkFTSRankingAndCitation(t *testing.T) {
	ctx := context.Background()
	db := newCodeIndexTestDB(t)
	at := time.Now().UTC()

	symbolHit := testIndexedFile("pkg/auth.go", "Authenticate", at)
	symbolHit.Chunks = []store.CodeIndexChunk{{
		Ordinal: 0, Kind: "func", SymbolName: "Authenticate",
		SymbolTokens: "authenticate user login", CodeTokens: "token verify",
		Content: "misc unrelated prose about databases",
	}}

	contentHit := testIndexedFile("pkg/db.go", "Query", at)
	contentHit.Chunks = []store.CodeIndexChunk{{
		Ordinal: 0, Kind: "func", SymbolName: "Query",
		SymbolTokens: "query rows", CodeTokens: "select from",
		Content: "authenticate user login flow details here",
	}}

	if err := db.UpsertCodeIndexedFiles(ctx, "ws-1", []store.IndexedFile{symbolHit, contentHit}); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	hits, err := db.SearchCodeIndexChunks(ctx, store.CodeIndexChunkQuery{
		WorkspaceID: "ws-1", Query: "authenticate user login", Limit: 10,
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) < 2 {
		t.Fatalf("want at least 2 hits, got %+v", hits)
	}
	if hits[0].Chunk.SymbolName != "Authenticate" {
		t.Fatalf("symbol-token match should rank first, got %+v", hits)
	}
	if hits[0].Score <= 0 {
		t.Fatalf("expected positive negated-bm25 score, got %v", hits[0].Score)
	}
	if !strings.Contains(hits[0].Source, "pkg/auth.go") || !strings.Contains(hits[0].Source, "Authenticate") {
		t.Fatalf("citation source missing path/symbol: %q", hits[0].Source)
	}
}

func TestCodeIndexChunkVectorModelVersionIsolation(t *testing.T) {
	ctx := context.Background()
	db := newCodeIndexTestDB(t)
	at := time.Now().UTC()

	payload := testIndexedFile("pkg/a.go", "Alpha", at)
	payload.Chunks = []store.CodeIndexChunk{testChunk(0, "Alpha", "alpha chunk")}
	if err := db.UpsertCodeIndexedFiles(ctx, "ws-1", []store.IndexedFile{payload}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	targets, err := db.ListCodeIndexChunksNeedingEmbedding(ctx, "ws-1", "model-a", 1, 10)
	if err != nil || len(targets) != 1 {
		t.Fatalf("need embedding: targets=%+v err=%v", targets, err)
	}
	if !strings.Contains(targets[0].EmbedText, "pkg/a.go") || !strings.Contains(targets[0].EmbedText, "Alpha") {
		t.Fatalf("embed text: %q", targets[0].EmbedText)
	}

	vNear := makeVec(memoryVecDim, 0.1)
	vFar := makeVec(memoryVecDim, 0.9)
	if err := db.UpsertCodeIndexChunkEmbeddings(ctx, "ws-1", "model-a", 1, []store.CodeIndexChunkEmbedding{
		{ChunkID: targets[0].ChunkID, Vector: vNear},
	}); err != nil {
		t.Fatalf("upsert embedding: %v", err)
	}

	pending, total, err := db.CountCodeIndexEmbeddingProgress(ctx, "ws-1", "model-a", 1)
	if err != nil {
		t.Fatalf("progress: %v", err)
	}
	if pending != 0 || total != 1 {
		t.Fatalf("progress pending=%d total=%d, want 0/1", pending, total)
	}

	wrongModel, err := db.VectorSearchCodeIndexChunks(ctx, "ws-1", "model-b", 1, vNear, 5)
	if err != nil {
		t.Fatalf("wrong model search: %v", err)
	}
	if len(wrongModel) != 0 {
		t.Fatalf("model-b should find nothing, got %+v", wrongModel)
	}

	wrongVersion, err := db.VectorSearchCodeIndexChunks(ctx, "ws-1", "model-a", 2, vNear, 5)
	if err != nil {
		t.Fatalf("wrong version search: %v", err)
	}
	if len(wrongVersion) != 0 {
		t.Fatalf("version 2 should find nothing, got %+v", wrongVersion)
	}

	query := makeVec(memoryVecDim, 0.09)
	hits, err := db.VectorSearchCodeIndexChunks(ctx, "ws-1", "model-a", 1, query, 5)
	if err != nil {
		t.Fatalf("vector search: %v", err)
	}
	if len(hits) != 1 || hits[0].Chunk.SymbolName != "Alpha" {
		t.Fatalf("vector hits: %+v", hits)
	}
	if !strings.Contains(hits[0].Source, "alpha chunk") {
		t.Fatalf("vector citation: %q", hits[0].Source)
	}

	// Stale when version bumps — chunk should re-enter backfill queue.
	if err := db.UpsertCodeIndexChunkEmbeddings(ctx, "ws-1", "model-a", 2, []store.CodeIndexChunkEmbedding{
		{ChunkID: targets[0].ChunkID, Vector: vFar},
	}); err != nil {
		t.Fatalf("re-embed v2: %v", err)
	}
	stale, err := db.ListCodeIndexChunksNeedingEmbedding(ctx, "ws-1", "model-a", 3, 10)
	if err != nil || len(stale) != 1 {
		t.Fatalf("version mismatch backfill: %+v err=%v", stale, err)
	}
}

func TestCodeIndexChunkEmbeddingBatchValidation(t *testing.T) {
	ctx := context.Background()
	db := newCodeIndexTestDB(t)
	payload := testIndexedFile("pkg/a.go", "A", time.Now().UTC())
	payload.Chunks = []store.CodeIndexChunk{testChunk(0, "A", "body")}
	if err := db.UpsertCodeIndexedFiles(ctx, "ws-1", []store.IndexedFile{payload}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	targets, _ := db.ListCodeIndexChunksNeedingEmbedding(ctx, "ws-1", "m", 1, 1)
	badVec := makeVec(4, 0.5)

	cases := []struct {
		name string
		err  error
		call func() error
	}{
		{
			name: "empty model",
			call: func() error {
				return db.UpsertCodeIndexChunkEmbeddings(ctx, "ws-1", "", 1, nil)
			},
		},
		{
			name: "bad vector dim",
			call: func() error {
				return db.UpsertCodeIndexChunkEmbeddings(ctx, "ws-1", "m", 1, []store.CodeIndexChunkEmbedding{
					{ChunkID: targets[0].ChunkID, Vector: badVec},
				})
			},
		},
		{
			name: "missing chunk",
			call: func() error {
				return db.UpsertCodeIndexChunkEmbeddings(ctx, "ws-1", "m", 1, []store.CodeIndexChunkEmbedding{
					{ChunkID: 99999, Vector: makeVec(memoryVecDim, 0.5)},
				})
			},
		},
		{
			name: "workspace mismatch",
			call: func() error {
				return db.UpsertCodeIndexChunkEmbeddings(ctx, "ws-other", "m", 1, []store.CodeIndexChunkEmbedding{
					{ChunkID: targets[0].ChunkID, Vector: makeVec(memoryVecDim, 0.5)},
				})
			},
		},
		{
			name: "vector search bad dim",
			call: func() error {
				_, err := db.VectorSearchCodeIndexChunks(ctx, "ws-1", "m", 1, badVec, 5)
				return err
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.call(); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestCodeIndexReplacementRemovesOldVectorRows(t *testing.T) {
	ctx := context.Background()
	db := newCodeIndexTestDB(t)
	path := "pkg/a.go"
	at := time.Now().UTC()

	first := testIndexedFile(path, "Old", at)
	first.Chunks = []store.CodeIndexChunk{testChunk(0, "Old", "old")}
	if err := db.UpsertCodeIndexedFiles(ctx, "ws-1", []store.IndexedFile{first}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	targets, _ := db.ListCodeIndexChunksNeedingEmbedding(ctx, "ws-1", "m", 1, 1)
	oldID := targets[0].ChunkID
	if err := db.UpsertCodeIndexChunkEmbeddings(ctx, "ws-1", "m", 1, []store.CodeIndexChunkEmbedding{
		{ChunkID: oldID, Vector: makeVec(memoryVecDim, 0.5)},
	}); err != nil {
		t.Fatalf("embed: %v", err)
	}

	second := testIndexedFile(path, "New", at)
	second.Chunks = []store.CodeIndexChunk{testChunk(0, "New", "new")}
	if err := db.UpsertCodeIndexedFiles(ctx, "ws-1", []store.IndexedFile{second}); err != nil {
		t.Fatalf("reindex: %v", err)
	}

	hits, err := db.VectorSearchCodeIndexChunks(ctx, "ws-1", "m", 1, makeVec(memoryVecDim, 0.5), 5)
	if err != nil {
		t.Fatalf("vector search: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("old vector row should be gone, got %+v", hits)
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

func TestCodeIndexDeleteRemovesFTSAndVectorRows(t *testing.T) {
	ctx := context.Background()
	db := newCodeIndexTestDB(t)
	path := "internal/kv/handler.go"

	file := testIndexedFile(path, "HandleKVSet", time.Now().UTC())
	file.Symbols[0].NameTokens = "handle kv set"
	file.Chunks = []store.CodeIndexChunk{testChunk(0, "HandleKVSet", "kv set body")}
	if err := db.UpsertCodeIndexedFiles(ctx, "ws-1", []store.IndexedFile{file}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	targets, _ := db.ListCodeIndexChunksNeedingEmbedding(ctx, "ws-1", "m", 1, 1)
	if err := db.UpsertCodeIndexChunkEmbeddings(ctx, "ws-1", "m", 1, []store.CodeIndexChunkEmbedding{
		{ChunkID: targets[0].ChunkID, Vector: makeVec(memoryVecDim, 0.5)},
	}); err != nil {
		t.Fatalf("embed: %v", err)
	}

	if err := db.DeleteCodeIndexFiles(ctx, "ws-1", []string{path}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := db.GetCodeIndexFile(ctx, "ws-1", path); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("file should be gone: %v", err)
	}

	symHits, err := db.SearchCodeIndexSymbols(ctx, store.CodeIndexSymbolQuery{
		WorkspaceID: "ws-1", Query: "kv set",
	})
	if err != nil {
		t.Fatalf("symbol search after delete: %v", err)
	}
	if len(symHits) != 0 {
		t.Fatalf("symbol FTS rows should be gone, got %+v", symHits)
	}

	chunkHits, err := db.SearchCodeIndexChunks(ctx, store.CodeIndexChunkQuery{
		WorkspaceID: "ws-1", Query: "kv set",
	})
	if err != nil {
		t.Fatalf("chunk search after delete: %v", err)
	}
	if len(chunkHits) != 0 {
		t.Fatalf("chunk FTS rows should be gone, got %+v", chunkHits)
	}

	vecHits, err := db.VectorSearchCodeIndexChunks(ctx, "ws-1", "m", 1, makeVec(memoryVecDim, 0.5), 5)
	if err != nil {
		t.Fatalf("vector search after delete: %v", err)
	}
	if len(vecHits) != 0 {
		t.Fatalf("vector rows should be gone, got %+v", vecHits)
	}

	n, err := db.CountCodeIndexChunks(ctx, "ws-1")
	if err != nil {
		t.Fatalf("count chunks: %v", err)
	}
	if n != 0 {
		t.Fatalf("chunk count = %d, want 0", n)
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
		FileCount: 3, SymbolCount: 10, ChunkCount: 25, WarningsJSON: `["w1"]`,
	}
	if err := db.PutCodeIndexBuild(ctx, first); err != nil {
		t.Fatalf("put first: %v", err)
	}

	got, err := db.GetCodeIndexBuild(ctx, "ws-1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.DirtyCount != 2 || got.FileCount != 3 || got.ChunkCount != 25 {
		t.Fatalf("build row: %+v", got)
	}

	second := &store.CodeIndexBuild{
		WorkspaceID: "ws-1", RootPath: "/proj", GitHead: "bbb",
		DirtyCount: 0, BuiltAt: builtAt.Add(time.Hour), DurationMS: 50,
		FileCount: 4, SymbolCount: 12, ChunkCount: 30, WarningsJSON: `[]`,
	}
	if err := db.PutCodeIndexBuild(ctx, second); err != nil {
		t.Fatalf("put second: %v", err)
	}
	got2, err := db.GetCodeIndexBuild(ctx, "ws-1")
	if err != nil {
		t.Fatalf("get after upsert: %v", err)
	}
	if got2.GitHead != "bbb" || got2.DirtyCount != 0 || got2.FileCount != 4 || got2.ChunkCount != 30 {
		t.Fatalf("upserted build: %+v", got2)
	}
}

func TestCodeIndexMaliciousFTSQueries(t *testing.T) {
	ctx := context.Background()
	db := newCodeIndexTestDB(t)

	file := testIndexedFile("pkg/a.go", "Foo", time.Now().UTC())
	file.Symbols[0].NameTokens = "foo bar"
	file.Chunks = []store.CodeIndexChunk{testChunk(0, "Foo", "foo bar content")}
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
		if _, err := db.SearchCodeIndexChunks(ctx, store.CodeIndexChunkQuery{
			WorkspaceID: "ws-1", Query: q,
		}); err != nil {
			t.Fatalf("chunk search %q errored: %v", q, err)
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

func testChunk(ordinal int, symbolName, content string) store.CodeIndexChunk {
	return store.CodeIndexChunk{
		Ordinal: ordinal, Kind: "func", SymbolName: symbolName,
		SymbolTokens: strings.ToLower(symbolName), CodeTokens: "code tokens",
		StartLine: 10, EndLine: 20, Content: content, ContentHash: "hash",
	}
}
