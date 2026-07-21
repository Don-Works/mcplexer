package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"time"
	"unicode"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/google/uuid"
)

func (d *DB) UpsertRecipe(ctx context.Context, r *store.Recipe) error {
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now().UTC()
	}
	if r.UpdatedAt.IsZero() {
		r.UpdatedAt = time.Now().UTC()
	}

	params := normalizeJSON(r.ParamsPattern, "null")
	tags := normalizeJSON(r.Tags, "null")
	sourceIDs := normalizeJSON(r.SourceAuditIDs, "null")

	// Try INSERT; on conflict (tool_name UNIQUE), update.
	_, err := d.q.ExecContext(ctx, `
		INSERT INTO recipes
			(id, created_at, updated_at, tool_name, namespace, description,
			 params_pattern, success_count, total_count, avg_latency_ms,
			 error_rate, score, session_count, last_used_at, tags, source_audit_ids)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(tool_name) DO UPDATE SET
			updated_at      = excluded.updated_at,
			description     = excluded.description,
			params_pattern  = excluded.params_pattern,
			success_count   = excluded.success_count,
			total_count     = excluded.total_count,
			avg_latency_ms  = excluded.avg_latency_ms,
			error_rate      = excluded.error_rate,
			score           = excluded.score,
			session_count   = excluded.session_count,
			last_used_at    = excluded.last_used_at,
			tags            = excluded.tags,
			source_audit_ids = excluded.source_audit_ids`,
		uuid.NewString(), formatTime(r.CreatedAt), formatTime(r.UpdatedAt),
		r.ToolName, r.Namespace, r.Description,
		params, r.SuccessCount, r.TotalCount, r.AvgLatencyMs,
		r.ErrorRate, r.Score, r.SessionCount,
		nullableTime(r.LastUsedAt), tags, sourceIDs,
	)
	if err != nil {
		return err
	}

	// Retrieve the stored ID (preserved on conflict).
	row := d.q.QueryRowContext(ctx,
		`SELECT id FROM recipes WHERE tool_name = ?`, r.ToolName)
	if err := row.Scan(&r.ID); err != nil {
		return err
	}
	return nil
}

func (d *DB) GetRecipe(ctx context.Context, id string) (*store.Recipe, error) {
	r, err := scanRecipe(d.q.QueryRowContext(ctx, `
		SELECT id, created_at, updated_at, tool_name, namespace, description,
		       params_pattern, success_count, total_count, avg_latency_ms,
		       error_rate, score, session_count, last_used_at, tags, source_audit_ids
		FROM recipes WHERE id = ?`, id))
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, store.ErrNotFound
		}
		return nil, err
	}
	return r, nil
}

func (d *DB) GetRecipeByToolName(ctx context.Context, toolName string) (*store.Recipe, error) {
	r, err := scanRecipe(d.q.QueryRowContext(ctx, `
		SELECT id, created_at, updated_at, tool_name, namespace, description,
		       params_pattern, success_count, total_count, avg_latency_ms,
		       error_rate, score, session_count, last_used_at, tags, source_audit_ids
		FROM recipes WHERE tool_name = ?`, toolName))
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, store.ErrNotFound
		}
		return nil, err
	}
	return r, nil
}

func (d *DB) ListRecipes(ctx context.Context, f store.RecipeFilter) ([]store.Recipe, error) {
	where, args := buildRecipeWhere(f)
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	q := `SELECT id, created_at, updated_at, tool_name, namespace, description,
	             params_pattern, success_count, total_count, avg_latency_ms,
	             error_rate, score, session_count, last_used_at, tags, source_audit_ids
	      FROM recipes` + where + ` ORDER BY score DESC LIMIT ? OFFSET ?`
	dataArgs := append(args, limit, f.Offset)

	rows, err := d.q.QueryContext(ctx, q, dataArgs...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []store.Recipe
	for rows.Next() {
		r, err := scanRecipe(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

func (d *DB) SearchRecipes(ctx context.Context, f store.RecipeFilter) ([]store.Recipe, error) {
	if f.Query == "" {
		return d.ListRecipes(ctx, f)
	}

	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}

	// FTS5 search joined back to the main table.
	q := `SELECT r.id, r.created_at, r.updated_at, r.tool_name, r.namespace, r.description,
	             r.params_pattern, r.success_count, r.total_count, r.avg_latency_ms,
	             r.error_rate, r.score, r.session_count, r.last_used_at, r.tags, r.source_audit_ids
	      FROM recipes_fts f
	      JOIN recipes r ON r.rowid = f.rowid
	      WHERE recipes_fts MATCH ?
	      ORDER BY rank
	      LIMIT ? OFFSET ?`

	safeQuery := recipeFTSQuery(f.Query)
	if safeQuery == "" {
		return nil, nil
	}

	rows, err := d.q.QueryContext(ctx, q, safeQuery, limit, f.Offset)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []store.Recipe
	for rows.Next() {
		r, err := scanRecipe(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

func recipeFTSQuery(query string) string {
	terms := strings.FieldsFunc(query, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	quoted := make([]string, 0, len(terms))
	for _, term := range terms {
		term = strings.TrimSpace(strings.ToLower(term))
		if term == "" {
			continue
		}
		quoted = append(quoted, `"`+strings.ReplaceAll(term, `"`, `""`)+`"`)
	}
	return strings.Join(quoted, " ")
}

func (d *DB) DeleteRecipe(ctx context.Context, id string) error {
	res, err := d.q.ExecContext(ctx, `DELETE FROM recipes WHERE id = ?`, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return store.ErrNotFound
	}
	return nil
}

func buildRecipeWhere(f store.RecipeFilter) (string, []any) {
	var conds []string
	var args []any
	if f.ToolName != nil {
		conds = append(conds, "tool_name = ?")
		args = append(args, *f.ToolName)
	}
	if f.Namespace != nil {
		conds = append(conds, "namespace = ?")
		args = append(args, *f.Namespace)
	}
	if f.MinScore != nil {
		conds = append(conds, "score >= ?")
		args = append(args, *f.MinScore)
	}
	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

// nullableTime returns nil when t is nil or zero, otherwise the formatted string.
func nullableTime(t *time.Time) any {
	if t == nil || t.IsZero() {
		return nil
	}
	return formatTime(*t)
}

func scanRecipe(row rowScanner) (*store.Recipe, error) {
	var r store.Recipe
	var createdAt, updatedAt string
	var paramsPattern, tags, sourceIDs sql.NullString
	var lastUsedAt sql.NullString

	err := row.Scan(
		&r.ID, &createdAt, &updatedAt,
		&r.ToolName, &r.Namespace, &r.Description,
		&paramsPattern, &r.SuccessCount, &r.TotalCount,
		&r.AvgLatencyMs, &r.ErrorRate, &r.Score,
		&r.SessionCount, &lastUsedAt, &tags, &sourceIDs,
	)
	if err != nil {
		return nil, err
	}

	r.CreatedAt = parseTime(createdAt)
	r.UpdatedAt = parseTime(updatedAt)

	if paramsPattern.Valid && paramsPattern.String != "null" {
		r.ParamsPattern = json.RawMessage(paramsPattern.String)
	}
	if tags.Valid && tags.String != "null" {
		r.Tags = json.RawMessage(tags.String)
	}
	if sourceIDs.Valid && sourceIDs.String != "null" {
		r.SourceAuditIDs = json.RawMessage(sourceIDs.String)
	}
	if lastUsedAt.Valid {
		t := parseTime(lastUsedAt.String)
		r.LastUsedAt = &t
	}
	return &r, nil
}
