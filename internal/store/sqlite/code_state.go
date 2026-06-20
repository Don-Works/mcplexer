package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

const codeStateCols = `workspace_id, key, value_json, bytes, pinned,
	ttl_expires_at, source_session_id, created_at, updated_at`

func (d *DB) SetCodeState(ctx context.Context, e *store.CodeStateEntry) error {
	if e == nil {
		return errors.New("SetCodeState: nil entry")
	}
	if strings.TrimSpace(e.WorkspaceID) == "" || strings.TrimSpace(e.Key) == "" {
		return errors.New("SetCodeState: workspace_id and key required")
	}
	if len(e.ValueJSON) == 0 {
		return errors.New("SetCodeState: value_json required")
	}
	now := time.Now().UTC()
	if e.CreatedAt.IsZero() {
		e.CreatedAt = now
	}
	e.UpdatedAt = now
	e.Bytes = len(e.ValueJSON)

	// created_at is intentionally excluded from the UPDATE branch so it is
	// preserved across overwrites of an existing key.
	_, err := d.q.ExecContext(ctx, `
		INSERT INTO code_state (`+codeStateCols+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(workspace_id, key) DO UPDATE SET
			value_json = excluded.value_json,
			bytes = excluded.bytes,
			pinned = excluded.pinned,
			ttl_expires_at = excluded.ttl_expires_at,
			source_session_id = excluded.source_session_id,
			updated_at = excluded.updated_at`,
		e.WorkspaceID, e.Key, string(e.ValueJSON), e.Bytes,
		boolToInt(e.Pinned), nullableTime(e.TTLExpiresAt), e.SourceSessionID,
		formatTime(e.CreatedAt), formatTime(e.UpdatedAt))
	if err != nil {
		return fmt.Errorf("set code state: %w", err)
	}
	return nil
}

func (d *DB) GetCodeState(
	ctx context.Context, workspaceID, key string,
) (*store.CodeStateEntry, error) {
	row := d.q.QueryRowContext(ctx, `SELECT `+codeStateCols+`
		FROM code_state
		WHERE workspace_id = ? AND key = ?
		  AND (ttl_expires_at IS NULL OR ttl_expires_at > ?)
		LIMIT 1`, workspaceID, key, formatTime(time.Now().UTC()))
	return scanCodeState(row, true)
}

func (d *DB) ListCodeState(
	ctx context.Context, f store.CodeStateFilter,
) ([]store.CodeStateEntry, error) {
	if strings.TrimSpace(f.WorkspaceID) == "" {
		return nil, errors.New("ListCodeState: workspace_id required")
	}
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	where := `workspace_id = ?`
	args := []any{f.WorkspaceID}
	if !f.IncludeExpired {
		where += ` AND (ttl_expires_at IS NULL OR ttl_expires_at > ?)`
		args = append(args, formatTime(time.Now().UTC()))
	}
	if p := strings.TrimSpace(f.Prefix); p != "" {
		where += ` AND key LIKE ? ESCAPE '\'`
		args = append(args, escapeLike(p)+"%")
	}
	rows, err := d.q.QueryContext(ctx, `SELECT `+codeStateCols+`
		FROM code_state
		WHERE `+where+`
		ORDER BY updated_at DESC LIMIT ? OFFSET ?`, append(args, limit, f.Offset)...)
	if err != nil {
		return nil, fmt.Errorf("list code state: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []store.CodeStateEntry
	for rows.Next() {
		// List views omit the value payload to stay light.
		e, err := scanCodeState(rows, false)
		if err != nil {
			return nil, err
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}

func (d *DB) DeleteCodeState(ctx context.Context, workspaceID, key string) error {
	res, err := d.q.ExecContext(ctx,
		`DELETE FROM code_state WHERE workspace_id = ? AND key = ?`, workspaceID, key)
	if err != nil {
		return fmt.Errorf("delete code state: %w", err)
	}
	return checkRowsAffected(res)
}

func (d *DB) PruneExpiredCodeState(ctx context.Context, now time.Time) (int, error) {
	res, err := d.q.ExecContext(ctx, `
		DELETE FROM code_state
		WHERE pinned = 0 AND ttl_expires_at IS NOT NULL AND ttl_expires_at <= ?`,
		formatTime(now))
	if err != nil {
		return 0, fmt.Errorf("prune code state: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(n), nil
}

func scanCodeState(r scanner, withValue bool) (*store.CodeStateEntry, error) {
	var e store.CodeStateEntry
	var value string
	var pinned int
	var ttl sql.NullString
	var created, updated string
	err := r.Scan(&e.WorkspaceID, &e.Key, &value, &e.Bytes, &pinned,
		&ttl, &e.SourceSessionID, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if withValue {
		e.ValueJSON = []byte(value)
	}
	e.Pinned = pinned != 0
	if ttl.Valid {
		e.TTLExpiresAt = parseTimePtr(&ttl.String)
	}
	e.CreatedAt = parseTime(created)
	e.UpdatedAt = parseTime(updated)
	return &e, nil
}

// escapeLike escapes the LIKE wildcards in a user-supplied prefix so a key
// prefix containing % or _ matches literally (paired with ESCAPE '\').
func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}
