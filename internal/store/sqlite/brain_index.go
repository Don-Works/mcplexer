// brain_index.go — SQLite CRUD for the brain derived-index bookkeeping
// tables (index_files, brain_errors — migration 090). Mirrors task.go /
// task_attachment.go conventions: Unix-epoch INTEGER timestamps,
// nullString for optional text, store.ErrNotFound sentinel.
//
// These tables are index-rebuildable — pure functions of the on-disk
// Markdown tree — so this layer holds no authoritative state; a full
// reindex can reconstruct every row.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/oklog/ulid/v2"
)

const indexFileSelectCols = `path, workspace_id, entity_kind, entity_id,
	source, sha, mtime, size, indexed_at`

// UpsertIndexFile inserts or replaces the bookkeeping row for a file.
func (d *DB) UpsertIndexFile(ctx context.Context, f *store.IndexFile) error {
	if f == nil {
		return errors.New("UpsertIndexFile: nil index file")
	}
	if strings.TrimSpace(f.Path) == "" {
		return errors.New("UpsertIndexFile: path required")
	}
	if strings.TrimSpace(f.Sha) == "" {
		return errors.New("UpsertIndexFile: sha required")
	}
	if f.IndexedAt.IsZero() {
		f.IndexedAt = time.Now().UTC()
	}
	source := f.Source
	if strings.TrimSpace(source) == "" {
		source = store.IndexSourceCentral
	}
	_, err := d.q.ExecContext(ctx, `
		INSERT INTO index_files (
			path, workspace_id, entity_kind, entity_id,
			source, sha, mtime, size, indexed_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(path) DO UPDATE SET
			workspace_id = excluded.workspace_id,
			entity_kind  = excluded.entity_kind,
			entity_id    = excluded.entity_id,
			source       = excluded.source,
			sha          = excluded.sha,
			mtime        = excluded.mtime,
			size         = excluded.size,
			indexed_at   = excluded.indexed_at`,
		f.Path, nullString(f.WorkspaceID), nullString(f.EntityKind), nullString(f.EntityID),
		source, f.Sha, f.Mtime, f.Size, f.IndexedAt.Unix(),
	)
	if err != nil {
		return fmt.Errorf("upsert index file: %w", err)
	}
	return nil
}

// GetIndexFile returns the bookkeeping row for path, or ErrNotFound.
func (d *DB) GetIndexFile(ctx context.Context, path string) (*store.IndexFile, error) {
	row := d.q.QueryRowContext(ctx, `
		SELECT `+indexFileSelectCols+`
		FROM index_files WHERE path = ?`, path)
	f, err := scanIndexFile(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get index file: %w", err)
	}
	return f, nil
}

// DeleteIndexFile removes the row for path. Missing path is a no-op.
func (d *DB) DeleteIndexFile(ctx context.Context, path string) error {
	_, err := d.q.ExecContext(ctx, `DELETE FROM index_files WHERE path = ?`, path)
	if err != nil {
		return fmt.Errorf("delete index file: %w", err)
	}
	return nil
}

// ListIndexFiles returns rows for a workspace; empty workspaceID = all.
func (d *DB) ListIndexFiles(ctx context.Context, workspaceID string) ([]store.IndexFile, error) {
	var (
		rows *sql.Rows
		err  error
	)
	if strings.TrimSpace(workspaceID) == "" {
		rows, err = d.q.QueryContext(ctx, `
			SELECT `+indexFileSelectCols+`
			FROM index_files ORDER BY path`)
	} else {
		rows, err = d.q.QueryContext(ctx, `
			SELECT `+indexFileSelectCols+`
			FROM index_files WHERE workspace_id = ? ORDER BY path`, workspaceID)
	}
	if err != nil {
		return nil, fmt.Errorf("list index files: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []store.IndexFile
	for rows.Next() {
		f, err := scanIndexFile(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *f)
	}
	return out, rows.Err()
}

// RecordBrainError appends a validation-failure row.
func (d *DB) RecordBrainError(ctx context.Context, e *store.BrainError) error {
	if e == nil {
		return errors.New("RecordBrainError: nil error")
	}
	if strings.TrimSpace(e.Path) == "" {
		return errors.New("RecordBrainError: path required")
	}
	if e.ID == "" {
		e.ID = ulid.Make().String()
	}
	if e.CreatedAt.IsZero() {
		e.CreatedAt = time.Now().UTC()
	}
	_, err := d.q.ExecContext(ctx, `
		INSERT INTO brain_errors (id, path, entity_kind, field, reason, created_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		e.ID, e.Path, nullString(e.EntityKind), nullString(e.Field), e.Reason, e.CreatedAt.Unix(),
	)
	if err != nil {
		return fmt.Errorf("record brain error: %w", err)
	}
	return nil
}

// ClearBrainErrorsForPath removes all error rows for a path.
func (d *DB) ClearBrainErrorsForPath(ctx context.Context, path string) error {
	_, err := d.q.ExecContext(ctx, `DELETE FROM brain_errors WHERE path = ?`, path)
	if err != nil {
		return fmt.Errorf("clear brain errors: %w", err)
	}
	return nil
}

// ListBrainErrors returns all current validation errors, newest first.
func (d *DB) ListBrainErrors(ctx context.Context) ([]store.BrainError, error) {
	rows, err := d.q.QueryContext(ctx, `
		SELECT id, path, entity_kind, field, reason, created_at
		FROM brain_errors ORDER BY created_at DESC, id DESC`)
	if err != nil {
		return nil, fmt.Errorf("list brain errors: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []store.BrainError
	for rows.Next() {
		e, err := scanBrainError(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}

// SuppressCandidate records a sticky per-record proactive-memory
// suppression (migration 093). Idempotent: re-suppressing the same
// (record, hash) is a no-op via INSERT OR IGNORE.
func (d *DB) SuppressCandidate(ctx context.Context, recordID, contentHash string) error {
	if strings.TrimSpace(recordID) == "" {
		return errors.New("SuppressCandidate: record id required")
	}
	_, err := d.q.ExecContext(ctx, `
		INSERT OR IGNORE INTO brain_candidate_suppressions
			(record_id, content_hash, created_at)
		VALUES (?, ?, ?)`,
		recordID, contentHash, time.Now().UTC().Unix(),
	)
	if err != nil {
		return fmt.Errorf("suppress candidate: %w", err)
	}
	return nil
}

// IsCandidateSuppressed reports whether the exact (record, hash) was
// suppressed, OR the record carries the suppress-all marker (blank hash).
func (d *DB) IsCandidateSuppressed(ctx context.Context, recordID, contentHash string) (bool, error) {
	var n int
	err := d.q.QueryRowContext(ctx, `
		SELECT COUNT(1) FROM brain_candidate_suppressions
		WHERE record_id = ? AND (content_hash = ? OR content_hash = '')`,
		recordID, contentHash,
	).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("is candidate suppressed: %w", err)
	}
	return n > 0, nil
}

func scanIndexFile(scan func(...any) error) (*store.IndexFile, error) {
	var (
		f          store.IndexFile
		workspace  sql.NullString
		entityKind sql.NullString
		entityID   sql.NullString
		source     sql.NullString
		mtime      sql.NullInt64
		size       sql.NullInt64
		indexedAt  sql.NullInt64
	)
	if err := scan(
		&f.Path, &workspace, &entityKind, &entityID,
		&source, &f.Sha, &mtime, &size, &indexedAt,
	); err != nil {
		return nil, err
	}
	if workspace.Valid {
		f.WorkspaceID = workspace.String
	}
	if entityKind.Valid {
		f.EntityKind = entityKind.String
	}
	if entityID.Valid {
		f.EntityID = entityID.String
	}
	if source.Valid {
		f.Source = source.String
	} else {
		f.Source = store.IndexSourceCentral
	}
	if mtime.Valid {
		f.Mtime = mtime.Int64
	}
	if size.Valid {
		f.Size = size.Int64
	}
	if indexedAt.Valid {
		f.IndexedAt = time.Unix(indexedAt.Int64, 0).UTC()
	}
	return &f, nil
}

func scanBrainError(scan func(...any) error) (*store.BrainError, error) {
	var (
		e          store.BrainError
		entityKind sql.NullString
		field      sql.NullString
		createdAt  int64
	)
	if err := scan(&e.ID, &e.Path, &entityKind, &field, &e.Reason, &createdAt); err != nil {
		return nil, err
	}
	if entityKind.Valid {
		e.EntityKind = entityKind.String
	}
	if field.Valid {
		e.Field = field.String
	}
	e.CreatedAt = time.Unix(createdAt, 0).UTC()
	return &e, nil
}
