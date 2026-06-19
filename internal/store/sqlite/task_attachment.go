// task_attachment.go — SQLite CRUD for task_attachments (migration 079).
// Mirrors task.go conventions: ULID ids, Unix epoch INTEGER timestamps,
// soft-delete via deleted_at, sentinel errors via store.ErrNotFound.
//
// The on-disk side of attachments (filesystem write/read of the actual
// bytes) lives in the gateway service layer — this file only manages
// the index row.
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

const taskAttachmentSelectCols = `id, task_id, workspace_id, filename, mime_type,
	size_bytes, sha256, storage_path, uploader_session_id, uploader_kind,
	created_at, deleted_at`

// InsertTaskAttachment inserts an attachment index row.
func (d *DB) InsertTaskAttachment(ctx context.Context, a *store.TaskAttachment) error {
	if a == nil {
		return errors.New("InsertTaskAttachment: nil attachment")
	}
	if strings.TrimSpace(a.TaskID) == "" {
		return errors.New("InsertTaskAttachment: task_id required")
	}
	if strings.TrimSpace(a.WorkspaceID) == "" {
		return errors.New("InsertTaskAttachment: workspace_id required")
	}
	if strings.TrimSpace(a.Sha256) == "" {
		return errors.New("InsertTaskAttachment: sha256 required")
	}
	if strings.TrimSpace(a.StoragePath) == "" {
		return errors.New("InsertTaskAttachment: storage_path required")
	}
	if a.SizeBytes < 0 {
		return errors.New("InsertTaskAttachment: size_bytes must be >= 0")
	}
	if a.ID == "" {
		a.ID = ulid.Make().String()
	}
	if a.CreatedAt.IsZero() {
		a.CreatedAt = time.Now().UTC()
	}
	if a.MimeType == "" {
		a.MimeType = "application/octet-stream"
	}
	if a.UploaderKind == "" {
		a.UploaderKind = store.TaskSourceAgent
	}

	_, err := d.q.ExecContext(ctx, `
		INSERT INTO task_attachments (
			id, task_id, workspace_id, filename, mime_type,
			size_bytes, sha256, storage_path, uploader_session_id, uploader_kind,
			created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ID, a.TaskID, a.WorkspaceID, a.Filename, a.MimeType,
		a.SizeBytes, a.Sha256, a.StoragePath, nullString(a.UploaderSessionID), a.UploaderKind,
		a.CreatedAt.Unix(),
	)
	if err != nil {
		return fmt.Errorf("insert task attachment: %w", mapConstraintError(err))
	}
	return nil
}

// GetTaskAttachment returns one attachment row, excluding soft-deleted.
func (d *DB) GetTaskAttachment(ctx context.Context, id string) (*store.TaskAttachment, error) {
	row := d.q.QueryRowContext(ctx, `
		SELECT `+taskAttachmentSelectCols+`
		FROM task_attachments
		WHERE id = ? AND deleted_at IS NULL`, id)
	a, err := scanTaskAttachment(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get task attachment: %w", err)
	}
	return a, nil
}

// ListTaskAttachments returns non-deleted rows for a task, newest first.
func (d *DB) ListTaskAttachments(ctx context.Context, taskID string) ([]store.TaskAttachment, error) {
	if strings.TrimSpace(taskID) == "" {
		return nil, errors.New("ListTaskAttachments: task_id required")
	}
	rows, err := d.q.QueryContext(ctx, `
		SELECT `+taskAttachmentSelectCols+`
		FROM task_attachments
		WHERE task_id = ? AND deleted_at IS NULL
		ORDER BY created_at DESC, id DESC`, taskID)
	if err != nil {
		return nil, fmt.Errorf("list task attachments: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []store.TaskAttachment
	for rows.Next() {
		a, err := scanTaskAttachment(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

// SoftDeleteTaskAttachment stamps deleted_at. ErrNotFound when already
// soft-deleted or missing.
func (d *DB) SoftDeleteTaskAttachment(ctx context.Context, id string) error {
	now := time.Now().UTC().Unix()
	res, err := d.q.ExecContext(ctx, `
		UPDATE task_attachments
		SET deleted_at = ?
		WHERE id = ? AND deleted_at IS NULL`, now, id)
	if err != nil {
		return fmt.Errorf("soft-delete task attachment: %w", err)
	}
	return checkRowsAffected(res)
}

func scanTaskAttachment(scan func(...any) error) (*store.TaskAttachment, error) {
	var (
		a            store.TaskAttachment
		filename     sql.NullString
		uploaderSess sql.NullString
		createdAt    int64
		deletedAt    sql.NullInt64
	)
	if err := scan(
		&a.ID, &a.TaskID, &a.WorkspaceID, &filename, &a.MimeType,
		&a.SizeBytes, &a.Sha256, &a.StoragePath, &uploaderSess, &a.UploaderKind,
		&createdAt, &deletedAt,
	); err != nil {
		return nil, err
	}
	if filename.Valid {
		a.Filename = filename.String
	}
	if uploaderSess.Valid {
		a.UploaderSessionID = uploaderSess.String
	}
	a.CreatedAt = time.Unix(createdAt, 0).UTC()
	if deletedAt.Valid {
		t := time.Unix(deletedAt.Int64, 0).UTC()
		a.DeletedAt = &t
	}
	return &a, nil
}
