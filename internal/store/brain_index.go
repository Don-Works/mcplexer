package store

import (
	"context"
	"time"
)

// IndexFile is one row of index_files (migration 090): the brain's
// incremental fast-path bookkeeping. Path is the natural key — one row
// per on-disk Markdown/YAML file. Sha/Mtime/Size record the file's state
// at last index so the watcher can skip unchanged files cheaply.
// EntityKind/EntityID tie the file back to the DB row it materialised so
// deletes + verify can reconcile in either direction.
type IndexFile struct {
	Path        string `json:"path"`
	WorkspaceID string `json:"workspace_id,omitempty"`
	EntityKind  string `json:"entity_kind,omitempty"`
	EntityID    string `json:"entity_id,omitempty"`
	// Source records which brain materialised this row (migration 092,
	// docs/brain.md Appendix C.2): "central" (~/mcplexer-brain/) or "repo"
	// (a per-repo .mcplexer/ folder that rides the project git history).
	// Repo-local is canonical for its workspace when present. Defaults to
	// "central" on write when empty.
	Source    string    `json:"source,omitempty"`
	Sha       string    `json:"sha"`
	Mtime     int64     `json:"mtime"`
	Size      int64     `json:"size"`
	IndexedAt time.Time `json:"indexed_at"`
}

// Index file source markers (migration 092 / Appendix C.2).
const (
	IndexSourceCentral = "central"
	IndexSourceRepo    = "repo"
)

// BrainError is one row of brain_errors (migration 090): a frontmatter
// validation failure surfaced to the dashboard rather than silently
// indexing a record that lies. Path is the offending file; Field/Reason
// describe the failure (from brain.ValidationError).
type BrainError struct {
	ID         string    `json:"id"`
	Path       string    `json:"path"`
	EntityKind string    `json:"entity_kind,omitempty"`
	Field      string    `json:"field,omitempty"`
	Reason     string    `json:"reason"`
	CreatedAt  time.Time `json:"created_at"`
}

// BrainIndexStore manages the brain's derived-index bookkeeping tables
// (index_files, brain_errors — migration 090). These tables are
// index-rebuildable: a full reindex from the file tree can reconstruct
// them at any time, so they hold no authoritative state. All methods take
// context.Context first and return store.ErrNotFound for missing rows
// (sentinel error convention).
type BrainIndexStore interface {
	// UpsertIndexFile inserts or replaces the index-bookkeeping row for a
	// file (keyed on Path). IndexedAt defaults to now when zero.
	UpsertIndexFile(ctx context.Context, f *IndexFile) error

	// GetIndexFile returns the row for path, or ErrNotFound when absent.
	GetIndexFile(ctx context.Context, path string) (*IndexFile, error)

	// DeleteIndexFile removes the row for path. Deleting a missing path is
	// a no-op (not an error) so reconcile sweeps are idempotent.
	DeleteIndexFile(ctx context.Context, path string) error

	// ListIndexFiles returns all rows for a workspace. An empty
	// workspaceID returns every row (used by full-reindex/verify).
	ListIndexFiles(ctx context.Context, workspaceID string) ([]IndexFile, error)

	// RecordBrainError appends a validation-failure row. ID + CreatedAt
	// default when empty/zero.
	RecordBrainError(ctx context.Context, e *BrainError) error

	// ClearBrainErrorsForPath removes all error rows for a path (called
	// before re-recording on each reindex of that file).
	ClearBrainErrorsForPath(ctx context.Context, path string) error

	// ListBrainErrors returns all current validation errors (dashboard).
	ListBrainErrors(ctx context.Context) ([]BrainError, error)

	// SuppressCandidate records a sticky per-record proactive-memory
	// suppression (migration 093 — the "never" choice). A blank contentHash
	// suppresses ALL candidates for the record. Idempotent (re-suppressing is
	// a no-op).
	SuppressCandidate(ctx context.Context, recordID, contentHash string) error

	// IsCandidateSuppressed reports whether (recordID, contentHash) — or
	// recordID with the suppress-all marker — has been suppressed.
	IsCandidateSuppressed(ctx context.Context, recordID, contentHash string) (bool, error)
}
