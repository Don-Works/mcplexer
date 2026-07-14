package store

import (
	"context"
	"time"
)

// CodeIndexStore is the derived-index read/write surface backing the builtin
// `index__*` tools (migration 127). A workspace's repo is enumerated and its
// Go + TS/JS files have symbols and file-level import edges extracted into a
// searchable map. The index is a cache of the repo on disk — never a durable
// conclusion (use MemoryStore for those) — and is rebuilt per-file on demand.
//
// Write path is batched: the index service flushes ~100 files per
// UpsertCodeIndexedFiles call, each running in one transaction. Each file's
// children (symbols, edges, chunks) are replaced wholesale keyed by the
// stable file_id, so a re-index never orphans FTS rows. Read path is FTS5 BM25
// over the mirror tables plus vec0 KNN over chunk embeddings.
type CodeIndexStore interface {
	// UpsertCodeIndexedFiles inserts-or-updates each file row (preserving its
	// existing id on conflict of (workspace_id, path)) and fully replaces that
	// file's symbols + edges + chunks, all inside one transaction.
	UpsertCodeIndexedFiles(ctx context.Context, workspaceID string, files []IndexedFile) error

	// DeleteCodeIndexFiles removes the named files (and their children + FTS
	// mirrors + vec rows) for a workspace. Used for stored-but-no-longer-enumerated paths.
	DeleteCodeIndexFiles(ctx context.Context, workspaceID string, paths []string) error

	// ListCodeIndexFileStats returns the lightweight per-file freshness tuples
	// (path, size, mtime, hash, chunk_version) the incremental build diffs against.
	ListCodeIndexFileStats(ctx context.Context, workspaceID string) ([]CodeIndexFileStat, error)

	// GetCodeIndexFile returns one file row. ErrNotFound when absent.
	GetCodeIndexFile(ctx context.Context, workspaceID, path string) (*CodeIndexFile, error)

	// ListCodeIndexSymbolsByPath returns every symbol declared in one file,
	// in source order (start_line ASC).
	ListCodeIndexSymbolsByPath(ctx context.Context, workspaceID, path string) ([]CodeIndexSymbol, error)

	// SearchCodeIndexSymbols runs an FTS5 BM25 query over the symbol mirror,
	// joined back to the owning file path. Honors kind + exported filters.
	SearchCodeIndexSymbols(ctx context.Context, q CodeIndexSymbolQuery) ([]CodeIndexSymbolHit, error)

	// SearchCodeIndexFiles runs an FTS5 BM25 query over the file mirror
	// (path/package/doc_summary). Score is negated BM25: higher = better.
	SearchCodeIndexFiles(ctx context.Context, workspaceID, query string, limit int) ([]CodeIndexFileHit, error)

	// SearchCodeIndexChunks runs a weighted FTS5 BM25 query over source chunks.
	// Score is negated BM25: higher = better.
	SearchCodeIndexChunks(ctx context.Context, q CodeIndexChunkQuery) ([]CodeIndexChunkHit, error)

	// VectorSearchCodeIndexChunks runs vec0 KNN over chunk embeddings, enforcing
	// workspace scope and exact embed_model + embed_version.
	VectorSearchCodeIndexChunks(
		ctx context.Context, workspaceID, embedModel string, embedVersion int,
		vector []float32, k int,
	) ([]CodeIndexChunkHit, error)

	// ListCodeIndexChunksNeedingEmbedding returns chunks whose stored embedding
	// meta does not match the requested model/version (including empty model).
	ListCodeIndexChunksNeedingEmbedding(
		ctx context.Context, workspaceID, embedModel string, embedVersion, limit int,
	) ([]CodeIndexEmbedTarget, error)

	// CountCodeIndexEmbeddingProgress returns (pending, total) chunk rows for a
	// workspace using the same stale-embedding predicate as the backfill selector.
	CountCodeIndexEmbeddingProgress(
		ctx context.Context, workspaceID, embedModel string, embedVersion int,
	) (pending, total int, err error)

	// UpsertCodeIndexChunkEmbeddings atomically replaces vectors for a batch of
	// chunks and stamps embed_model/embed_version on each row.
	UpsertCodeIndexChunkEmbeddings(
		ctx context.Context, workspaceID, embedModel string, embedVersion int,
		rows []CodeIndexChunkEmbedding,
	) error

	// ListCodeIndexEdges returns import edges. A FromPath filter yields the
	// imports-of that file; a ToPath filter yields the importers-of it.
	ListCodeIndexEdges(ctx context.Context, f CodeIndexEdgeFilter) ([]CodeIndexEdgeHit, error)

	// PutCodeIndexBuild upserts the per-workspace build row (freshness +
	// counters). One row per workspace, keyed by workspace_id.
	PutCodeIndexBuild(ctx context.Context, b *CodeIndexBuild) error

	// GetCodeIndexBuild returns the build row. ErrNotFound when never built.
	GetCodeIndexBuild(ctx context.Context, workspaceID string) (*CodeIndexBuild, error)

	// CountCodeIndexSymbols returns how many symbols are indexed for a workspace.
	CountCodeIndexSymbols(ctx context.Context, workspaceID string) (int, error)

	// CountCodeIndexChunks returns how many source chunks are indexed for a workspace.
	CountCodeIndexChunks(ctx context.Context, workspaceID string) (int, error)
}

// CodeIndexFile is one indexed source file, root-relative to the workspace.
type CodeIndexFile struct {
	ID            int64     `json:"id"`
	WorkspaceID   string    `json:"workspace_id"`
	Path          string    `json:"path"`
	PathTokens    string    `json:"path_tokens"`
	Language      string    `json:"language"`
	Package       string    `json:"package"`
	SizeBytes     int       `json:"size_bytes"`
	LineCount     int       `json:"line_count"`
	MtimeUnix     int64     `json:"mtime_unix"`
	ContentHash   string    `json:"content_hash"`
	DocSummary    string    `json:"doc_summary"`
	IsTest        bool      `json:"is_test"`
	SkippedReason string    `json:"skipped_reason"`
	ChunkVersion  int       `json:"chunk_version"`
	IndexedAt     time.Time `json:"indexed_at"`
}

// CodeIndexSymbol is one declaration (func/method/type/const/var/class/...)
// extracted from a file.
type CodeIndexSymbol struct {
	ID          int64  `json:"id"`
	FileID      int64  `json:"file_id"`
	WorkspaceID string `json:"workspace_id"`
	Name        string `json:"name"`
	NameTokens  string `json:"name_tokens"`
	Kind        string `json:"kind"`
	Receiver    string `json:"receiver"`
	Signature   string `json:"signature"`
	Doc         string `json:"doc"`
	StartLine   int    `json:"start_line"`
	EndLine     int    `json:"end_line"`
	Exported    bool   `json:"exported"`
}

// CodeIndexChunk is one searchable source slice from an indexed file.
type CodeIndexChunk struct {
	ID           int64     `json:"id"`
	WorkspaceID  string    `json:"workspace_id"`
	FileID       int64     `json:"file_id"`
	Path         string    `json:"path"`
	PathTokens   string    `json:"path_tokens"`
	Ordinal      int       `json:"ordinal"`
	Kind         string    `json:"kind"`
	SymbolName   string    `json:"symbol_name"`
	SymbolTokens string    `json:"symbol_tokens"`
	CodeTokens   string    `json:"code_tokens"`
	StartLine    int       `json:"start_line"`
	EndLine      int       `json:"end_line"`
	Content      string    `json:"content"`
	ContentHash  string    `json:"content_hash"`
	EmbedModel   string    `json:"embed_model"`
	EmbedVersion int       `json:"embed_version"`
	IndexedAt    time.Time `json:"indexed_at"`
}

// CodeIndexEdge is one file-level import edge (NOT a call edge).
type CodeIndexEdge struct {
	ID          int64  `json:"id"`
	FromFileID  int64  `json:"from_file_id"`
	WorkspaceID string `json:"workspace_id"`
	Kind        string `json:"kind"`
	ToPath      string `json:"to_path"`
	ToModule    string `json:"to_module"`
}

// CodeIndexBuild is the per-workspace freshness + counter row.
type CodeIndexBuild struct {
	WorkspaceID  string    `json:"workspace_id"`
	RootPath     string    `json:"root_path"`
	GitHead      string    `json:"git_head"`
	DirtyCount   int       `json:"dirty_count"`
	BuiltAt      time.Time `json:"built_at"`
	DurationMS   int       `json:"duration_ms"`
	FileCount    int       `json:"file_count"`
	SymbolCount  int       `json:"symbol_count"`
	ChunkCount   int       `json:"chunk_count"`
	Complete     bool      `json:"complete"`
	WarningsJSON string    `json:"warnings_json"`
}

// IndexedFile is one file's full replacement payload for UpsertCodeIndexedFiles.
type IndexedFile struct {
	File    CodeIndexFile     `json:"file"`
	Symbols []CodeIndexSymbol `json:"symbols"`
	Edges   []CodeIndexEdge   `json:"edges"`
	Chunks  []CodeIndexChunk  `json:"chunks"`
}

// CodeIndexFileStat is the lightweight freshness tuple the incremental build
// diffs against, avoiding loading full file rows.
type CodeIndexFileStat struct {
	Path         string `json:"path"`
	SizeBytes    int    `json:"size_bytes"`
	MtimeUnix    int64  `json:"mtime_unix"`
	ContentHash  string `json:"content_hash"`
	ChunkVersion int    `json:"chunk_version"`
	ChunkCount   int    `json:"chunk_count"`
}

// CodeIndexSymbolQuery parameterizes a symbol FTS search.
type CodeIndexSymbolQuery struct {
	WorkspaceID  string `json:"workspace_id"`
	Query        string `json:"query"`
	Kind         string `json:"kind"`
	ExportedOnly bool   `json:"exported_only"`
	Limit        int    `json:"limit"`
}

// CodeIndexChunkQuery parameterizes a chunk FTS search.
type CodeIndexChunkQuery struct {
	WorkspaceID string `json:"workspace_id"`
	Query       string `json:"query"`
	Kind        string `json:"kind"`
	Limit       int    `json:"limit"`
}

// CodeIndexSymbolHit is one scored symbol result joined to its file path.
type CodeIndexSymbolHit struct {
	Symbol CodeIndexSymbol `json:"symbol"`
	Path   string          `json:"path"`
	Score  float64         `json:"score"`
}

// CodeIndexChunkHit is one scored chunk result with a citation source string.
type CodeIndexChunkHit struct {
	Chunk  CodeIndexChunk `json:"chunk"`
	Path   string         `json:"path"`
	Score  float64        `json:"score"`
	Source string         `json:"source"`
}

// CodeIndexFileHit is one scored file result.
type CodeIndexFileHit struct {
	File  CodeIndexFile `json:"file"`
	Score float64       `json:"score"`
}

// CodeIndexEmbedTarget is one chunk awaiting embedding backfill.
type CodeIndexEmbedTarget struct {
	ChunkID   int64  `json:"chunk_id"`
	Path      string `json:"path"`
	EmbedText string `json:"embed_text"`
}

// CodeIndexChunkEmbedding is one vector write in a batch upsert.
type CodeIndexChunkEmbedding struct {
	ChunkID int64     `json:"chunk_id"`
	Vector  []float32 `json:"vector"`
}

// CodeIndexEdgeFilter scopes a ListCodeIndexEdges call. FromPath = imports-of;
// ToPath = importers-of. Exactly one of them is normally set.
type CodeIndexEdgeFilter struct {
	WorkspaceID string `json:"workspace_id"`
	FromPath    string `json:"from_path"`
	ToPath      string `json:"to_path"`
	Limit       int    `json:"limit"`
}

// CodeIndexEdgeHit is one import edge in path terms (file ids resolved away).
type CodeIndexEdgeHit struct {
	FromPath string `json:"from_path"`
	Kind     string `json:"kind"`
	ToPath   string `json:"to_path"`
	ToModule string `json:"to_module"`
}
