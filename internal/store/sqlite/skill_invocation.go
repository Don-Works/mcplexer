package sqlite

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// InsertSkillInvocation persists a single skill->tool dispatch attempt.
// id is auto-assigned by SQLite; ts defaults to now(UTC) when zero.
func (d *DB) InsertSkillInvocation(
	ctx context.Context, inv *store.SkillInvocation,
) error {
	if inv == nil {
		return fmt.Errorf("nil invocation")
	}
	if inv.Timestamp.IsZero() {
		inv.Timestamp = time.Now().UTC()
	}
	allowed := 0
	if inv.Allowed {
		allowed = 1
	}
	res, err := d.q.ExecContext(ctx, `
		INSERT INTO skill_invocations (skill_name, tool_name, namespace, allowed, ts)
		VALUES (?, ?, ?, ?, ?)`,
		inv.SkillName, inv.ToolName, inv.Namespace, allowed,
		inv.Timestamp.Unix(),
	)
	if err != nil {
		return fmt.Errorf("insert skill_invocations: %w", err)
	}
	id, err := res.LastInsertId()
	if err == nil {
		inv.ID = id
	}
	return nil
}

// ListSkillInvocations returns recent skill invocations matching the filter.
// Results are ordered newest first.
func (d *DB) ListSkillInvocations(
	ctx context.Context, f store.SkillInvocationFilter,
) ([]store.SkillInvocation, error) {
	var (
		conds []string
		args  []any
	)
	if f.SkillName != nil {
		conds = append(conds, "skill_name = ?")
		args = append(args, *f.SkillName)
	}
	if f.Allowed != nil {
		v := 0
		if *f.Allowed {
			v = 1
		}
		conds = append(conds, "allowed = ?")
		args = append(args, v)
	}

	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}

	q := `SELECT id, skill_name, tool_name, namespace, allowed, ts
		FROM skill_invocations`
	if len(conds) > 0 {
		q += " WHERE " + strings.Join(conds, " AND ")
	}
	q += " ORDER BY ts DESC, id DESC LIMIT ? OFFSET ?"
	args = append(args, limit, f.Offset)

	rows, err := d.q.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("query skill_invocations: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []store.SkillInvocation
	for rows.Next() {
		var inv store.SkillInvocation
		var allowed int
		var tsUnix int64
		if err := rows.Scan(&inv.ID, &inv.SkillName, &inv.ToolName,
			&inv.Namespace, &allowed, &tsUnix); err != nil {
			return nil, fmt.Errorf("scan skill_invocations row: %w", err)
		}
		inv.Allowed = allowed != 0
		inv.Timestamp = time.Unix(tsUnix, 0).UTC()
		out = append(out, inv)
	}
	return out, rows.Err()
}
