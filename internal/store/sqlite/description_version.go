package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/google/uuid"
)

const descCols = `id, tool_name, description, source, status, session_id,
	model, workspace_id, rationale, reviewed_by, review_note,
	created_at, reviewed_at`

func (d *DB) CreateToolDescriptionVersion(
	ctx context.Context, v *store.ToolDescriptionVersion,
) error {
	if v.ID == "" {
		v.ID = uuid.NewString()
	}
	if v.CreatedAt.IsZero() {
		v.CreatedAt = time.Now().UTC()
	}
	if v.Status == "" {
		v.Status = "pending"
	}

	_, err := d.q.ExecContext(ctx, `
		INSERT INTO tool_description_versions
			(id, tool_name, description, source, status, session_id,
			 model, workspace_id, rationale, reviewed_by, review_note,
			 created_at, reviewed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		v.ID, v.ToolName, v.Description, v.Source, v.Status, v.SessionID,
		v.Model, v.WorkspaceID, v.Rationale, v.ReviewedBy, v.ReviewNote,
		formatTime(v.CreatedAt), formatTimePtr(v.ReviewedAt),
	)
	return err
}

func (d *DB) GetToolDescriptionVersion(
	ctx context.Context, id string,
) (*store.ToolDescriptionVersion, error) {
	row := d.q.QueryRowContext(ctx,
		`SELECT `+descCols+` FROM tool_description_versions WHERE id = ?`, id)
	return scanDescVersion(row)
}

func (d *DB) ListToolDescriptionVersions(
	ctx context.Context, f store.ToolDescriptionFilter,
) ([]store.ToolDescriptionVersion, int, error) {
	where, args := buildDescFilter(f)

	var total int
	countQ := `SELECT COUNT(*) FROM tool_description_versions` + where
	if err := d.q.QueryRowContext(ctx, countQ, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	q := `SELECT ` + descCols + ` FROM tool_description_versions` +
		where + ` ORDER BY created_at DESC LIMIT ? OFFSET ?`
	args = append(args, limit, f.Offset)

	rows, err := d.q.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()

	var out []store.ToolDescriptionVersion
	for rows.Next() {
		v, err := scanDescVersionRow(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *v)
	}
	return out, total, rows.Err()
}

func (d *DB) GetActiveDescriptions(
	ctx context.Context,
) (map[string]string, error) {
	rows, err := d.q.QueryContext(ctx, `
		SELECT tool_name, description
		FROM tool_description_versions
		WHERE status = 'active' AND source != 'original'`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	m := make(map[string]string)
	for rows.Next() {
		var name, desc string
		if err := rows.Scan(&name, &desc); err != nil {
			return nil, err
		}
		m[name] = desc
	}
	return m, rows.Err()
}

func (d *DB) ActivateVersion(
	ctx context.Context, id, reviewedBy, reviewNote string,
) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	// Find the tool_name for this version.
	var toolName, status string
	err = tx.QueryRowContext(ctx,
		`SELECT tool_name, status FROM tool_description_versions WHERE id = ?`, id,
	).Scan(&toolName, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return store.ErrNotFound
	}
	if err != nil {
		return err
	}
	if status != "pending" && status != "superseded" && status != "active" {
		return fmt.Errorf("cannot activate version with status %q", status)
	}

	now := formatTime(time.Now().UTC())

	// Supersede current active version for this tool.
	_, err = tx.ExecContext(ctx, `
		UPDATE tool_description_versions
		SET status = 'superseded'
		WHERE tool_name = ? AND status = 'active' AND id != ?`,
		toolName, id)
	if err != nil {
		return err
	}

	// Activate the target version.
	res, err := tx.ExecContext(ctx, `
		UPDATE tool_description_versions
		SET status = 'active', reviewed_by = ?, review_note = ?, reviewed_at = ?
		WHERE id = ?`,
		reviewedBy, reviewNote, now, id)
	if err != nil {
		return err
	}
	if err := checkRowsAffected(res); err != nil {
		return err
	}
	return tx.Commit()
}

func (d *DB) RejectVersion(
	ctx context.Context, id, reviewedBy, reviewNote string,
) error {
	now := formatTime(time.Now().UTC())
	res, err := d.q.ExecContext(ctx, `
		UPDATE tool_description_versions
		SET status = 'rejected', reviewed_by = ?, review_note = ?, reviewed_at = ?
		WHERE id = ? AND status = 'pending'`,
		reviewedBy, reviewNote, now, id)
	if err != nil {
		return err
	}
	return checkRowsAffected(res)
}

func (d *DB) HasPendingForToolBySession(
	ctx context.Context, toolName, sessionID string,
) (bool, error) {
	var count int
	err := d.q.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM tool_description_versions
		WHERE tool_name = ? AND session_id = ? AND status = 'pending'`,
		toolName, sessionID).Scan(&count)
	return count > 0, err
}

func buildDescFilter(f store.ToolDescriptionFilter) (string, []any) {
	var clauses []string
	var args []any
	if f.ToolName != nil {
		clauses = append(clauses, "tool_name = ?")
		args = append(args, *f.ToolName)
	}
	if f.Status != nil {
		clauses = append(clauses, "status = ?")
		args = append(args, *f.Status)
	}
	if f.Source != nil {
		clauses = append(clauses, "source = ?")
		args = append(args, *f.Source)
	}
	if len(clauses) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}

func scanDescVersion(row *sql.Row) (*store.ToolDescriptionVersion, error) {
	var v store.ToolDescriptionVersion
	var createdAt string
	var reviewedAt *string
	err := row.Scan(
		&v.ID, &v.ToolName, &v.Description, &v.Source, &v.Status,
		&v.SessionID, &v.Model, &v.WorkspaceID, &v.Rationale,
		&v.ReviewedBy, &v.ReviewNote, &createdAt, &reviewedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	v.CreatedAt = parseTime(createdAt)
	v.ReviewedAt = parseTimePtr(reviewedAt)
	return &v, nil
}

func scanDescVersionRow(row rowScanner) (*store.ToolDescriptionVersion, error) {
	var v store.ToolDescriptionVersion
	var createdAt string
	var reviewedAt *string
	err := row.Scan(
		&v.ID, &v.ToolName, &v.Description, &v.Source, &v.Status,
		&v.SessionID, &v.Model, &v.WorkspaceID, &v.Rationale,
		&v.ReviewedBy, &v.ReviewNote, &createdAt, &reviewedAt,
	)
	if err != nil {
		return nil, err
	}
	v.CreatedAt = parseTime(createdAt)
	v.ReviewedAt = parseTimePtr(reviewedAt)
	return &v, nil
}
