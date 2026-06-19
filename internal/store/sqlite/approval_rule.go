package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

const approvalRuleCols = `id, surface, pattern, directory, ai_session_id,
    decision, priority, expires_at, hit_count, last_hit_at, created_by,
    created_at, updated_at, allow_metachars`

// CreateApprovalRule inserts one allowlist row.
func (d *DB) CreateApprovalRule(ctx context.Context, r *store.ApprovalRule) error {
	if r == nil || r.ID == "" || r.Surface == "" || r.Decision == "" {
		return errors.New("CreateApprovalRule: id + surface + decision required")
	}
	now := time.Now().UTC()
	if r.CreatedAt.IsZero() {
		r.CreatedAt = now
	}
	if r.UpdatedAt.IsZero() {
		r.UpdatedAt = now
	}
	_, err := d.q.ExecContext(ctx, `
		INSERT INTO approval_rules (`+approvalRuleCols+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.Surface, r.Pattern, r.Directory, r.AISessionID,
		r.Decision, r.Priority, formatTimePtr(r.ExpiresAt),
		r.HitCount, formatTimePtr(r.LastHitAt), r.CreatedBy,
		formatTime(r.CreatedAt), formatTime(r.UpdatedAt),
		boolToInt(r.AllowMetachars),
	)
	if err != nil {
		return fmt.Errorf("create approval_rule: %w", err)
	}
	return nil
}

// GetApprovalRule returns one rule by ID or ErrNotFound.
func (d *DB) GetApprovalRule(ctx context.Context, id string) (*store.ApprovalRule, error) {
	row := d.q.QueryRowContext(ctx,
		`SELECT `+approvalRuleCols+` FROM approval_rules WHERE id = ?`, id)
	r, err := scanApprovalRule(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get approval_rule: %w", err)
	}
	return r, nil
}

// ListApprovalRules returns every rule for a surface ordered by
// priority ASC (lower wins). Empty surface returns every rule.
func (d *DB) ListApprovalRules(
	ctx context.Context, surface string,
) ([]store.ApprovalRule, error) {
	query := `SELECT ` + approvalRuleCols + ` FROM approval_rules`
	var args []any
	if surface != "" {
		query += ` WHERE surface = ?`
		args = append(args, surface)
	}
	query += ` ORDER BY priority ASC, created_at ASC`
	rows, err := d.q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list approval_rules: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []store.ApprovalRule
	for rows.Next() {
		r, err := scanApprovalRule(rows)
		if err != nil {
			return nil, fmt.Errorf("scan approval_rule: %w", err)
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// UpdateApprovalRule rewrites every column for r and bumps updated_at.
func (d *DB) UpdateApprovalRule(ctx context.Context, r *store.ApprovalRule) error {
	if r == nil || r.ID == "" {
		return errors.New("UpdateApprovalRule: id required")
	}
	r.UpdatedAt = time.Now().UTC()
	res, err := d.q.ExecContext(ctx, `
		UPDATE approval_rules
		SET surface = ?, pattern = ?, directory = ?, ai_session_id = ?,
		    decision = ?, priority = ?, expires_at = ?,
		    hit_count = ?, last_hit_at = ?, created_by = ?, updated_at = ?,
		    allow_metachars = ?
		WHERE id = ?`,
		r.Surface, r.Pattern, r.Directory, r.AISessionID,
		r.Decision, r.Priority, formatTimePtr(r.ExpiresAt),
		r.HitCount, formatTimePtr(r.LastHitAt), r.CreatedBy,
		formatTime(r.UpdatedAt), boolToInt(r.AllowMetachars), r.ID,
	)
	if err != nil {
		return fmt.Errorf("update approval_rule: %w", err)
	}
	return checkRowsAffected(res)
}

// DeleteApprovalRule removes one row by id. Returns ErrNotFound if absent.
func (d *DB) DeleteApprovalRule(ctx context.Context, id string) error {
	res, err := d.q.ExecContext(ctx,
		`DELETE FROM approval_rules WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete approval_rule: %w", err)
	}
	return checkRowsAffected(res)
}

// IncrementHitCount bumps hit_count by 1 and sets last_hit_at = hitAt.
func (d *DB) IncrementHitCount(
	ctx context.Context, id string, hitAt time.Time,
) error {
	res, err := d.q.ExecContext(ctx, `
		UPDATE approval_rules
		SET hit_count = hit_count + 1, last_hit_at = ?, updated_at = ?
		WHERE id = ?`,
		formatTime(hitAt), formatTime(time.Now().UTC()), id,
	)
	if err != nil {
		return fmt.Errorf("increment approval_rule hit: %w", err)
	}
	return checkRowsAffected(res)
}

func scanApprovalRule(r scanner) (*store.ApprovalRule, error) {
	var (
		rule                 store.ApprovalRule
		expiresAt, lastHit   sql.NullString
		createdAt, updatedAt string
		allowMetachars       int
	)
	err := r.Scan(
		&rule.ID, &rule.Surface, &rule.Pattern, &rule.Directory, &rule.AISessionID,
		&rule.Decision, &rule.Priority, &expiresAt,
		&rule.HitCount, &lastHit, &rule.CreatedBy,
		&createdAt, &updatedAt, &allowMetachars,
	)
	if err != nil {
		return nil, err
	}
	rule.AllowMetachars = allowMetachars != 0
	if expiresAt.Valid {
		t := parseTime(expiresAt.String)
		rule.ExpiresAt = &t
	}
	if lastHit.Valid {
		t := parseTime(lastHit.String)
		rule.LastHitAt = &t
	}
	rule.CreatedAt = parseTime(createdAt)
	rule.UpdatedAt = parseTime(updatedAt)
	return &rule, nil
}
