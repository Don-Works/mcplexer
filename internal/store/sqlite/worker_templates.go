// worker_templates.go — SQLite implementation of store.WorkerTemplateStore
// (migration 057). Mirrors the shape of skill_registry.go intentionally:
// the scoping rules, dedup-on-content-hash, and shadowing-by-workspace
// behaviour are identical, so callers can reason about both tables the
// same way.
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/google/uuid"
)

// PublishWorkerTemplate inserts a new version for (entry.WorkspaceID,
// entry.Name). Race-safe via withTx — concurrent publishes serialize on
// the COALESCE-based UNIQUE index.
func (d *DB) PublishWorkerTemplate(
	ctx context.Context, entry *store.WorkerTemplateEntry,
) (bool, error) {
	if entry == nil {
		return false, errors.New("PublishWorkerTemplate: nil entry")
	}
	if entry.Name == "" {
		return false, errors.New("PublishWorkerTemplate: name required")
	}
	if entry.ContentHash == "" {
		return false, errors.New("PublishWorkerTemplate: content_hash required")
	}
	if entry.Body == "" {
		return false, errors.New("PublishWorkerTemplate: body required")
	}

	wsArg, wsClause, wsParams := workspaceClause(entry.WorkspaceID)

	var dedup bool
	err := d.withTx(ctx, func(q queryable) error {
		var (
			existVer  int
			existID   string
			existHash string
		)
		dedupQuery := `
			SELECT id, version, content_hash
			FROM worker_templates
			WHERE name = ? AND deleted_at IS NULL ` + wsClause + `
			ORDER BY version DESC
			LIMIT 1`
		row := q.QueryRowContext(ctx, dedupQuery, append([]any{entry.Name}, wsParams...)...)
		switch err := row.Scan(&existID, &existVer, &existHash); {
		case errors.Is(err, sql.ErrNoRows):
			// First publish in this scope.
		case err != nil:
			return fmt.Errorf("dedup lookup: %w", err)
		default:
			if existHash == entry.ContentHash {
				entry.ID = existID
				entry.Version = existVer
				dedup = true
				return nil
			}
		}

		var nextVer int
		row = q.QueryRowContext(ctx, `
			SELECT COALESCE(MAX(version), 0) + 1
			FROM worker_templates
			WHERE name = ? `+wsClause,
			append([]any{entry.Name}, wsParams...)...)
		if err := row.Scan(&nextVer); err != nil {
			return fmt.Errorf("next version: %w", err)
		}

		if entry.ID == "" {
			entry.ID = uuid.NewString()
		}
		if entry.PublishedAt.IsZero() {
			entry.PublishedAt = time.Now().UTC()
		}
		entry.Version = nextVer

		metadata := normalizeJSON(entry.MetadataJSON, "{}")
		tags := normalizeJSON(entry.TagsJSON, "[]")
		var parent any
		if entry.ParentVersion != nil {
			parent = *entry.ParentVersion
		}
		var createdBy any
		if entry.CreatedByAgentID != "" {
			createdBy = entry.CreatedByAgentID
		}

		_, err := q.ExecContext(ctx, `
			INSERT INTO worker_templates
				(id, name, version, content_hash, description, body,
				 metadata_json, tags_json, author, parent_version,
				 published_at, created_by_agent_id, workspace_id)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			entry.ID, entry.Name, entry.Version, entry.ContentHash,
			entry.Description, entry.Body, metadata, tags, entry.Author,
			parent, entry.PublishedAt.Unix(), createdBy, wsArg,
		)
		if err != nil {
			return fmt.Errorf("insert worker_template: %w", mapConstraintError(err))
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	return dedup, nil
}

// GetWorkerTemplate returns a specific (workspace, name, version) row.
func (d *DB) GetWorkerTemplate(
	ctx context.Context, workspaceID *string, name string, version int,
) (*store.WorkerTemplateEntry, error) {
	_, wsClause, wsParams := workspaceClause(workspaceID)
	q := `
		SELECT ` + workerTplSelectCols + `
		FROM worker_templates
		WHERE name = ? AND version = ? AND deleted_at IS NULL ` + wsClause
	row := d.q.QueryRowContext(ctx, q, append([]any{name, version}, wsParams...)...)
	e, err := scanWorkerTemplate(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get worker_template: %w", err)
	}
	return e, nil
}

// GetWorkerTemplateHead returns the highest active version for name in scope.
// Workspace rows shadow global rows of the same name.
func (d *DB) GetWorkerTemplateHead(
	ctx context.Context, scope store.SkillScope, name string,
) (*store.WorkerTemplateEntry, error) {
	clause, params := scopeWhereClause(scope)
	q := `
		SELECT ` + workerTplSelectCols + `
		FROM worker_templates
		WHERE name = ? AND deleted_at IS NULL ` + clause + `
		ORDER BY (workspace_id IS NULL) ASC, version DESC
		LIMIT 1`
	row := d.q.QueryRowContext(ctx, q, append([]any{name}, params...)...)
	e, err := scanWorkerTemplate(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get worker_template_head: %w", err)
	}
	return e, nil
}

// ListWorkerTemplateHeads returns one row per name in scope. Workspace
// rows shadow global rows.
func (d *DB) ListWorkerTemplateHeads(
	ctx context.Context, scope store.SkillScope, limit int,
) ([]store.WorkerTemplateEntry, error) {
	clause, params := scopeWhereClause(scope)
	q := `
		WITH ranked AS (
			SELECT *, ROW_NUMBER() OVER (
				PARTITION BY name
				ORDER BY (workspace_id IS NULL) ASC, version DESC
			) AS rn
			FROM worker_templates
			WHERE deleted_at IS NULL ` + clause + `
		)
		SELECT ` + workerTplSelectColsPrefixed("") + `
		FROM ranked
		WHERE rn = 1
		ORDER BY name ASC`
	args := params
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := d.q.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list worker_template_heads: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	var out []store.WorkerTemplateEntry
	for rows.Next() {
		e, err := scanWorkerTemplate(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("scan worker_template: %w", err)
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}

// ListWorkerTemplateVersions returns every version for name in scope, desc.
func (d *DB) ListWorkerTemplateVersions(
	ctx context.Context, scope store.SkillScope, name string, includeDeleted bool,
) ([]store.WorkerTemplateEntry, error) {
	clause, params := scopeWhereClause(scope)
	q := `
		SELECT ` + workerTplSelectCols + `
		FROM worker_templates
		WHERE name = ? ` + clause
	if !includeDeleted {
		q += ` AND deleted_at IS NULL`
	}
	q += ` ORDER BY (workspace_id IS NULL) ASC, version DESC`
	rows, err := d.q.QueryContext(ctx, q, append([]any{name}, params...)...)
	if err != nil {
		return nil, fmt.Errorf("list worker_template_versions: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	var out []store.WorkerTemplateEntry
	for rows.Next() {
		e, err := scanWorkerTemplate(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("scan worker_template: %w", err)
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}

// SoftDeleteWorkerTemplate sets deleted_at on matching rows.
// version=0 deletes every active row for (workspace, name).
func (d *DB) SoftDeleteWorkerTemplate(
	ctx context.Context, workspaceID *string, name string, version int,
) error {
	now := time.Now().Unix()
	_, wsClause, wsParams := workspaceClause(workspaceID)
	var (
		res sql.Result
		err error
	)
	if version == 0 {
		args := append([]any{now, name}, wsParams...)
		res, err = d.q.ExecContext(ctx, `
			UPDATE worker_templates
			SET deleted_at = ?
			WHERE name = ? AND deleted_at IS NULL `+wsClause, args...)
	} else {
		args := append([]any{now, name, version}, wsParams...)
		res, err = d.q.ExecContext(ctx, `
			UPDATE worker_templates
			SET deleted_at = ?
			WHERE name = ? AND version = ? AND deleted_at IS NULL `+wsClause, args...)
	}
	if err != nil {
		return fmt.Errorf("soft delete worker_template: %w", err)
	}
	return checkRowsAffected(res)
}

const workerTplSelectCols = `id, name, version, content_hash, description, body,
		metadata_json, tags_json, author, parent_version,
		deleted_at, published_at, created_by_agent_id, workspace_id`

func workerTplSelectColsPrefixed(p string) string {
	if p == "" {
		return workerTplSelectCols
	}
	cols := strings.Split(workerTplSelectCols, ",")
	for i, c := range cols {
		cols[i] = p + "." + strings.TrimSpace(c)
	}
	return strings.Join(cols, ", ")
}

func scanWorkerTemplate(scan func(...any) error) (*store.WorkerTemplateEntry, error) {
	var (
		e           store.WorkerTemplateEntry
		metadata    string
		tags        string
		parentVer   sql.NullInt64
		deletedAt   sql.NullInt64
		publishedAt int64
		createdByID sql.NullString
		workspaceID sql.NullString
	)
	if err := scan(
		&e.ID, &e.Name, &e.Version, &e.ContentHash, &e.Description,
		&e.Body, &metadata, &tags, &e.Author, &parentVer,
		&deletedAt, &publishedAt, &createdByID, &workspaceID,
	); err != nil {
		return nil, err
	}
	e.MetadataJSON = json.RawMessage(metadata)
	e.TagsJSON = json.RawMessage(tags)
	if parentVer.Valid {
		v := int(parentVer.Int64)
		e.ParentVersion = &v
	}
	if deletedAt.Valid {
		t := time.Unix(deletedAt.Int64, 0).UTC()
		e.DeletedAt = &t
	}
	e.PublishedAt = time.Unix(publishedAt, 0).UTC()
	if createdByID.Valid {
		e.CreatedByAgentID = createdByID.String
	}
	if workspaceID.Valid {
		ws := workspaceID.String
		e.WorkspaceID = &ws
	}
	return &e, nil
}
