// Package index is the local codebase indexer that backs the builtin
// index__* tools. It enumerates a workspace's repo, extracts Go + TS/JS
// symbols and file-level import edges into a store.CodeIndexStore, and
// answers symbol/dep/test/summary/failure/context queries over that map.
//
// The structs in this file ARE the wire contract: the gateway handler
// marshals them verbatim, so their snake_case json tags are what the
// calling agent sees. Field sets are frozen at stage 0 (plan §5); B/C
// rebase on any change.
package index

import (
	"errors"
	"time"
)

// Sentinel errors surfaced to the gateway handler, which maps them to
// structured tool errors for the agent.
var (
	// ErrNotBuilt means no index build row exists for the workspace yet and
	// no auto-build ran (or a query ran before the first build completed).
	ErrNotBuilt = errors.New("code index not built")
	// ErrBuildInProgress means a build is already running for this workspace
	// (single-flight) and did not finish within the caller's wait window.
	ErrBuildInProgress = errors.New("code index build in progress")
	// ErrRootUnsafe means the resolved workspace root is empty, "/", or does
	// not exist — the index refuses to run from the seeded global workspace.
	ErrRootUnsafe = errors.New("code index root unsafe: run from a project workspace")
	// ErrQueryRequired prevents an empty search from degenerating into a large
	// unranked source dump.
	ErrQueryRequired = errors.New("code index query required")
)

// BuildRequest asks for a build or incremental refresh. Paths restricts the
// build to the given root-relative prefixes; Force drops and rebuilds.
type BuildRequest struct {
	WorkspaceID string   `json:"workspace_id"`
	Root        string   `json:"root"`
	Paths       []string `json:"paths,omitempty"`
	Force       bool     `json:"force,omitempty"`
}

// BuildResult reports what an incremental build changed.
type BuildResult struct {
	IndexID        string          `json:"index_id"`
	FilesIndexed   int             `json:"files_indexed"`
	FilesUnchanged int             `json:"files_unchanged"`
	FilesSkipped   int             `json:"files_skipped"`
	FilesRemoved   int             `json:"files_removed"`
	SymbolCount    int             `json:"symbol_count"`
	ChunkCount     int             `json:"chunk_count"`
	Complete       bool            `json:"complete"`
	DurationMS     int             `json:"duration_ms"`
	GitHead        string          `json:"git_head"`
	Warnings       []string        `json:"warnings,omitempty"`
	Embeddings     EmbeddingStatus `json:"embeddings"`
}

// SearchRequest asks for ranked source chunks. Kind optionally restricts the
// declaration kind recorded for a chunk (func, method, type, class, source…).
type SearchRequest struct {
	WorkspaceID string `json:"workspace_id"`
	Root        string `json:"root"`
	Query       string `json:"query"`
	Kind        string `json:"kind,omitempty"`
	Limit       int    `json:"limit,omitempty"`
}

// CodeSnippet is a bounded, line-addressable slice of source. Citation is
// always root-relative and can be fed straight to a file reader.
type CodeSnippet struct {
	Path       string `json:"path"`
	StartLine  int    `json:"start_line"`
	EndLine    int    `json:"end_line"`
	Citation   string `json:"citation"`
	Kind       string `json:"kind,omitempty"`
	SymbolName string `json:"symbol_name,omitempty"`
	Content    string `json:"content"`
}

// ChunkHit is one fused lexical/semantic source result. Sources contains
// "lexical", "semantic", or both; callers never have to compare BM25 with
// vector distance themselves.
type ChunkHit struct {
	CodeSnippet
	Score   float64  `json:"score"`
	Sources []string `json:"sources"`
}

// EmbeddingStatus makes vector availability explicit. Lexical retrieval is
// still active in every state, including disabled and error.
type EmbeddingStatus struct {
	Enabled   bool   `json:"enabled"`
	Model     string `json:"model,omitempty"`
	State     string `json:"state"`
	Embedded  int    `json:"embedded"`
	Pending   int    `json:"pending"`
	Total     int    `json:"total"`
	LastError string `json:"last_error,omitempty"`
}

// SearchResult is the complete index__search response.
type SearchResult struct {
	IndexID    string          `json:"index_id"`
	Query      string          `json:"query"`
	Mode       string          `json:"mode"`
	Hits       []ChunkHit      `json:"hits"`
	Embeddings EmbeddingStatus `json:"embeddings"`
}

// SymbolsRequest parameterizes a symbol search.
type SymbolsRequest struct {
	WorkspaceID  string `json:"workspace_id"`
	Root         string `json:"root"`
	Query        string `json:"query"`
	Kind         string `json:"kind,omitempty"`
	ExportedOnly bool   `json:"exported_only,omitempty"`
	Limit        int    `json:"limit,omitempty"`
}

// SymbolHit is one ranked symbol result (file:line + signature).
type SymbolHit struct {
	Name      string  `json:"name"`
	Kind      string  `json:"kind"`
	Receiver  string  `json:"receiver,omitempty"`
	Path      string  `json:"path"`
	Line      int     `json:"line"`
	EndLine   int     `json:"end_line,omitempty"`
	Signature string  `json:"signature,omitempty"`
	Doc       string  `json:"doc,omitempty"`
	Exported  bool    `json:"exported"`
	Score     float64 `json:"score"`
}

// DepsRequest parameterizes an import-graph query. Direction is one of
// imports|importers|both (default imports).
type DepsRequest struct {
	WorkspaceID string `json:"workspace_id"`
	Root        string `json:"root"`
	File        string `json:"file"`
	Direction   string `json:"direction,omitempty"`
	Limit       int    `json:"limit,omitempty"`
}

// DepEntry is one edge endpoint. External flags a dependency outside the
// indexed tree (Module holds the raw specifier).
type DepEntry struct {
	Path     string `json:"path,omitempty"`
	External bool   `json:"external"`
	Module   string `json:"module,omitempty"`
}

// DepsResult holds the imports-of and/or importers-of a file.
type DepsResult struct {
	File      string     `json:"file"`
	Imports   []DepEntry `json:"imports"`
	Importers []DepEntry `json:"importers"`
}

// TestOwner is one test file that owns a source file, with a confidence band.
type TestOwner struct {
	Path       string `json:"path"`
	Confidence string `json:"confidence"`
	Reason     string `json:"reason"`
}

// TestsForResult lists the tests that own a source file.
type TestsForResult struct {
	File  string      `json:"file"`
	Tests []TestOwner `json:"tests"`
}

// FileSummary is a heuristic one-file orientation card built without reading
// the file's full contents.
type FileSummary struct {
	Path            string      `json:"path"`
	Language        string      `json:"language"`
	Package         string      `json:"package,omitempty"`
	DocSummary      string      `json:"doc_summary,omitempty"`
	LineCount       int         `json:"line_count"`
	SizeBytes       int         `json:"size_bytes"`
	IsTest          bool        `json:"is_test"`
	ExportedSymbols []SymbolHit `json:"exported_symbols,omitempty"`
	ImportCount     int         `json:"import_count"`
	ImporterCount   int         `json:"importer_count"`
	Tests           []string    `json:"tests,omitempty"`
}

// RecentChangesRequest parameterizes a git-log churn query.
type RecentChangesRequest struct {
	WorkspaceID string `json:"workspace_id"`
	Root        string `json:"root"`
	Path        string `json:"path,omitempty"`
	Days        int    `json:"days,omitempty"`
	Limit       int    `json:"limit,omitempty"`
}

// CommitRef is one commit from git log.
type CommitRef struct {
	Hash    string   `json:"hash"`
	Author  string   `json:"author"`
	Date    string   `json:"date"`
	Subject string   `json:"subject"`
	Files   []string `json:"files,omitempty"`
}

// RecentChangesResult holds recent commits plus per-file churn counts.
type RecentChangesResult struct {
	Commits     []CommitRef    `json:"commits"`
	ChurnByFile map[string]int `json:"churn_by_file"`
}

// FailureCandidate is one ranked file to inspect for a pasted failure.
type FailureCandidate struct {
	Path    string   `json:"path"`
	Score   float64  `json:"score"`
	Reasons []string `json:"reasons"`
}

// ContextRequest asks for a token-budgeted context pack. BudgetTokens
// defaults to 4000 and caps at 16000.
type ContextRequest struct {
	WorkspaceID  string `json:"workspace_id"`
	Root         string `json:"root"`
	Query        string `json:"query"`
	BudgetTokens int    `json:"budget_tokens,omitempty"`
}

// ContextFile is one ranked file in a context pack, with the reasons it was
// selected and the assembled orientation payload.
type ContextFile struct {
	Path          string        `json:"path"`
	Score         float64       `json:"score"`
	Why           []string      `json:"why,omitempty"`
	Summary       string        `json:"summary,omitempty"`
	Symbols       []SymbolHit   `json:"symbols,omitempty"`
	Tests         []string      `json:"tests,omitempty"`
	RecentCommits []CommitRef   `json:"recent_commits,omitempty"`
	Snippets      []CodeSnippet `json:"snippets,omitempty"`
}

// ContextPack is the whole-task context bundle returned by index__context.
type ContextPack struct {
	Query        string          `json:"query"`
	BudgetTokens int             `json:"budget_tokens"`
	UsedTokens   int             `json:"used_tokens"`
	Stale        bool            `json:"stale"`
	BuiltAt      time.Time       `json:"built_at"`
	Files        []ContextFile   `json:"files"`
	Embeddings   EmbeddingStatus `json:"embeddings"`
}

// Status is the freshness verdict returned by index__status.
type Status struct {
	IndexID     string          `json:"index_id"`
	Built       bool            `json:"built"`
	BuiltAt     time.Time       `json:"built_at"`
	GitHead     string          `json:"git_head"`
	CurrentHead string          `json:"current_head"`
	Stale       bool            `json:"stale"`
	DirtyFiles  int             `json:"dirty_files"`
	FileCount   int             `json:"file_count"`
	SymbolCount int             `json:"symbol_count"`
	ChunkCount  int             `json:"chunk_count"`
	Complete    bool            `json:"complete"`
	DurationMS  int             `json:"duration_ms"`
	Warnings    []string        `json:"warnings,omitempty"`
	Embeddings  EmbeddingStatus `json:"embeddings"`
}
