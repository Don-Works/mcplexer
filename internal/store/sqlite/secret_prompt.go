package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

func (d *DB) CreateSecretPrompt(ctx context.Context, p *store.SecretPrompt) error {
	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Now().UTC()
	}
	if p.Status == "" {
		p.Status = "pending"
	}
	deleteOnRead := 0
	if p.DeleteOnRead {
		deleteOnRead = 1
	}
	var filePath *string
	if p.FilePath != "" {
		fp := p.FilePath
		filePath = &fp
	}
	_, err := d.q.ExecContext(ctx, `
		INSERT INTO secret_prompts
			(id, reason, label, requester, status, file_path,
			 expires_at, created_at, completed_at, delete_on_read)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.Reason, p.Label, p.Requester, p.Status, filePath,
		formatTime(p.ExpiresAt), formatTime(p.CreatedAt),
		formatTimePtr(p.CompletedAt), deleteOnRead,
	)
	return err
}

func (d *DB) GetSecretPrompt(ctx context.Context, id string) (*store.SecretPrompt, error) {
	row := d.q.QueryRowContext(ctx, `
		SELECT id, reason, label, requester, status, file_path,
		       expires_at, created_at, completed_at, delete_on_read
		FROM secret_prompts WHERE id = ?`, id)
	return scanSecretPrompt(row)
}

func (d *DB) ListPendingSecretPrompts(ctx context.Context) ([]store.SecretPrompt, error) {
	rows, err := d.q.QueryContext(ctx, `
		SELECT id, reason, label, requester, status, file_path,
		       expires_at, created_at, completed_at, delete_on_read
		FROM secret_prompts
		WHERE status = 'pending'
		ORDER BY created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []store.SecretPrompt
	for rows.Next() {
		p, err := scanSecretPromptRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// CompleteSecretPrompt transitions a pending row to a terminal state. status
// is one of submitted|cancelled|timeout. filePath is "" for non-submitted
// completions; sqlite stores it as NULL when empty.
func (d *DB) CompleteSecretPrompt(
	ctx context.Context, id, status, filePath string, completedAt time.Time,
) error {
	var fp *string
	if filePath != "" {
		fp = &filePath
	}
	res, err := d.q.ExecContext(ctx, `
		UPDATE secret_prompts
		SET status = ?, file_path = ?, completed_at = ?
		WHERE id = ? AND status = 'pending'`,
		status, fp, formatTime(completedAt), id,
	)
	if err != nil {
		return err
	}
	return checkRowsAffected(res)
}

// ListExpiredSecretPrompts returns every row (any status) whose expires_at
// is at or before the given cutoff. Used by the sweeper to hard-delete files.
func (d *DB) ListExpiredSecretPrompts(
	ctx context.Context, before time.Time,
) ([]store.SecretPrompt, error) {
	rows, err := d.q.QueryContext(ctx, `
		SELECT id, reason, label, requester, status, file_path,
		       expires_at, created_at, completed_at, delete_on_read
		FROM secret_prompts
		WHERE expires_at <= ?`, formatTime(before))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []store.SecretPrompt
	for rows.Next() {
		p, err := scanSecretPromptRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

func scanSecretPrompt(row *sql.Row) (*store.SecretPrompt, error) {
	var p store.SecretPrompt
	var filePath, completedAt *string
	var expiresAt, createdAt string
	var deleteOnRead int
	err := row.Scan(
		&p.ID, &p.Reason, &p.Label, &p.Requester, &p.Status, &filePath,
		&expiresAt, &createdAt, &completedAt, &deleteOnRead,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if filePath != nil {
		p.FilePath = *filePath
	}
	p.ExpiresAt = parseTime(expiresAt)
	p.CreatedAt = parseTime(createdAt)
	p.CompletedAt = parseTimePtr(completedAt)
	p.DeleteOnRead = deleteOnRead != 0
	return &p, nil
}

func scanSecretPromptRow(row rowScanner) (*store.SecretPrompt, error) {
	var p store.SecretPrompt
	var filePath, completedAt *string
	var expiresAt, createdAt string
	var deleteOnRead int
	err := row.Scan(
		&p.ID, &p.Reason, &p.Label, &p.Requester, &p.Status, &filePath,
		&expiresAt, &createdAt, &completedAt, &deleteOnRead,
	)
	if err != nil {
		return nil, err
	}
	if filePath != nil {
		p.FilePath = *filePath
	}
	p.ExpiresAt = parseTime(expiresAt)
	p.CreatedAt = parseTime(createdAt)
	p.CompletedAt = parseTimePtr(completedAt)
	p.DeleteOnRead = deleteOnRead != 0
	return &p, nil
}
