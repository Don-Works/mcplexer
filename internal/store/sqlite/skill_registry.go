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

// PublishSkillRegistryEntry inserts a new version row for
// (entry.WorkspaceID, entry.Name). Race-safe: runs inside a transaction
// so concurrent publishes receive contiguous versions through the
// COALESCE-based UNIQUE index.
func (d *DB) PublishSkillRegistryEntry(
	ctx context.Context, entry *store.SkillRegistryEntry,
) (bool, error) {
	if entry == nil {
		return false, errors.New("PublishSkillRegistryEntry: nil entry")
	}
	if entry.Name == "" {
		return false, errors.New("PublishSkillRegistryEntry: name required")
	}
	if entry.ContentHash == "" {
		return false, errors.New("PublishSkillRegistryEntry: content_hash required")
	}
	if entry.Body == "" {
		return false, errors.New("PublishSkillRegistryEntry: body required")
	}
	if entry.SourceType == "" {
		entry.SourceType = "inline"
	}

	wsArg, wsClause, wsParams := workspaceClause(entry.WorkspaceID)

	var dedup bool
	err := d.withTx(ctx, func(q queryable) error {
		// Dedup against the latest active row in the same scope.
		var (
			existVer  int
			existID   string
			existHash string
		)
		dedupQuery := `
			SELECT id, version, content_hash
			FROM skill_registry_entries
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

		// Next version within this scope.
		var nextVer int
		row = q.QueryRowContext(ctx, `
			SELECT COALESCE(MAX(version), 0) + 1
			FROM skill_registry_entries
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

		metadataRaw, manifestExtra := splitManifestExtra(entry.MetadataJSON)
		metadata := normalizeJSON(metadataRaw, "{}")
		tags := normalizeJSON(entry.TagsJSON, "[]")
		var parent any
		if entry.ParentVersion != nil {
			parent = *entry.ParentVersion
		}
		var createdBy any
		if entry.CreatedByAgentID != "" {
			createdBy = entry.CreatedByAgentID
		}
		var sourcePath any
		if entry.SourcePath != "" {
			sourcePath = entry.SourcePath
		}
		var bundleArg any
		var bundleSHA any
		if len(entry.Bundle) > 0 {
			bundleArg = entry.Bundle
			bundleSHA = entry.BundleSHA256
		}

		_, err := q.ExecContext(ctx, `
			INSERT INTO skill_registry_entries
				(id, name, version, content_hash, description, body,
				 metadata_json, tags_json, author, parent_version,
				 published_at, created_by_agent_id,
				 workspace_id, source_type, source_path, payload_type,
				 bundle, bundle_sha256, manifest_extra)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'skill', ?, ?, ?)`,
			entry.ID, entry.Name, entry.Version, entry.ContentHash,
			entry.Description, entry.Body, metadata, tags, entry.Author,
			parent, entry.PublishedAt.Unix(), createdBy,
			wsArg, entry.SourceType, sourcePath, bundleArg, bundleSHA,
			manifestExtra,
		)
		if err != nil {
			return fmt.Errorf("insert skill_registry_entry: %w", mapConstraintError(err))
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	return dedup, nil
}

// GetSkillRegistryEntry returns a specific (workspace, name, version) row.
func (d *DB) GetSkillRegistryEntry(
	ctx context.Context, workspaceID *string, name string, version int,
) (*store.SkillRegistryEntry, error) {
	_, wsClause, wsParams := workspaceClause(workspaceID)
	q := `
		SELECT ` + skillRegSelectCols + `
		FROM skill_registry_entries
		WHERE name = ? AND version = ? AND deleted_at IS NULL ` + wsClause
	row := d.q.QueryRowContext(ctx, q, append([]any{name, version}, wsParams...)...)
	e, err := scanSkillRegistryEntry(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get skill_registry_entry: %w", err)
	}
	return e, nil
}

// GetSkillRegistryHead returns the highest active version for name in
// the given scope. Workspace rows shadow global rows of the same name.
func (d *DB) GetSkillRegistryHead(
	ctx context.Context, scope store.SkillScope, name string,
) (*store.SkillRegistryEntry, error) {
	clause, params := scopeWhereClause(scope)
	q := `
		SELECT ` + skillRegSelectCols + `
		FROM skill_registry_entries
		WHERE name = ? AND deleted_at IS NULL ` + clause + `
		ORDER BY (workspace_id IS NULL) ASC, version DESC
		LIMIT 1`
	row := d.q.QueryRowContext(ctx, q, append([]any{name}, params...)...)
	e, err := scanSkillRegistryEntry(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get skill_registry_head: %w", err)
	}
	return e, nil
}

// ListSkillRegistryHeads returns one row per name visible in scope.
// Workspace rows shadow global rows; if the same name exists in both,
// the workspace row wins. Ordered by name.
func (d *DB) ListSkillRegistryHeads(
	ctx context.Context, scope store.SkillScope, limit int,
) ([]store.SkillRegistryEntry, error) {
	clause, params := scopeWhereClause(scope)
	// Two-step pick: for each name, find the "best" row — workspace
	// version takes precedence over global, then highest version wins.
	q := `
		WITH ranked AS (
			SELECT *, ROW_NUMBER() OVER (
				PARTITION BY name
				ORDER BY (workspace_id IS NULL) ASC, version DESC
			) AS rn
			FROM skill_registry_entries
			WHERE deleted_at IS NULL ` + clause + `
		)
		SELECT ` + skillRegSelectColsPrefixed("") + `
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
		return nil, fmt.Errorf("list skill_registry_heads: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	var out []store.SkillRegistryEntry
	for rows.Next() {
		e, err := scanSkillRegistryEntry(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("scan skill_registry_entry: %w", err)
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}

// ListSkillRegistryVersions returns every version for name in scope.
func (d *DB) ListSkillRegistryVersions(
	ctx context.Context, scope store.SkillScope, name string, includeDeleted bool,
) ([]store.SkillRegistryEntry, error) {
	clause, params := scopeWhereClause(scope)
	q := `
		SELECT ` + skillRegSelectCols + `
		FROM skill_registry_entries
		WHERE name = ? ` + clause
	if !includeDeleted {
		q += ` AND deleted_at IS NULL`
	}
	q += ` ORDER BY (workspace_id IS NULL) ASC, version DESC`
	rows, err := d.q.QueryContext(ctx, q, append([]any{name}, params...)...)
	if err != nil {
		return nil, fmt.Errorf("list skill_registry_versions: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	var out []store.SkillRegistryEntry
	for rows.Next() {
		e, err := scanSkillRegistryEntry(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("scan skill_registry_entry: %w", err)
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}

// SoftDeleteSkillRegistryEntry sets deleted_at on the matching row(s).
// version=0 deletes every active row for (workspace, name).
func (d *DB) SoftDeleteSkillRegistryEntry(
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
			UPDATE skill_registry_entries
			SET deleted_at = ?
			WHERE name = ? AND deleted_at IS NULL `+wsClause, args...)
	} else {
		args := append([]any{now, name, version}, wsParams...)
		res, err = d.q.ExecContext(ctx, `
			UPDATE skill_registry_entries
			SET deleted_at = ?
			WHERE name = ? AND version = ? AND deleted_at IS NULL `+wsClause, args...)
	}
	if err != nil {
		return fmt.Errorf("soft delete skill_registry_entry: %w", err)
	}
	return checkRowsAffected(res)
}

// SetSkillRegistryTag upserts a (name, tag) → version pointer.
func (d *DB) SetSkillRegistryTag(
	ctx context.Context, t *store.SkillRegistryTag,
) error {
	if t == nil || t.Name == "" || t.Tag == "" {
		return errors.New("SetSkillRegistryTag: name and tag required")
	}
	if t.Tag == "@latest" {
		return errors.New("SetSkillRegistryTag: @latest is derived, not stored")
	}
	if t.SetAt.IsZero() {
		t.SetAt = time.Now().UTC()
	}
	_, err := d.q.ExecContext(ctx, `
		INSERT INTO skill_registry_tags (name, tag, version, set_at, set_by)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(name, tag) DO UPDATE SET
			version = excluded.version,
			set_at  = excluded.set_at,
			set_by  = excluded.set_by`,
		t.Name, t.Tag, t.Version, t.SetAt.Unix(), t.SetBy)
	if err != nil {
		return fmt.Errorf("set skill_registry_tag: %w", err)
	}
	return nil
}

// GetSkillRegistryTag returns the (name, tag) row or store.ErrNotFound.
func (d *DB) GetSkillRegistryTag(
	ctx context.Context, name, tag string,
) (*store.SkillRegistryTag, error) {
	row := d.q.QueryRowContext(ctx, `
		SELECT name, tag, version, set_at, set_by
		FROM skill_registry_tags
		WHERE name = ? AND tag = ?`, name, tag)
	var (
		t     store.SkillRegistryTag
		setAt int64
	)
	err := row.Scan(&t.Name, &t.Tag, &t.Version, &setAt, &t.SetBy)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get skill_registry_tag: %w", err)
	}
	t.SetAt = time.Unix(setAt, 0).UTC()
	return &t, nil
}

// DeleteSkillRegistryTag drops a (name, tag) row.
func (d *DB) DeleteSkillRegistryTag(
	ctx context.Context, name, tag string,
) error {
	res, err := d.q.ExecContext(ctx, `
		DELETE FROM skill_registry_tags WHERE name = ? AND tag = ?`,
		name, tag)
	if err != nil {
		return fmt.Errorf("delete skill_registry_tag: %w", err)
	}
	return checkRowsAffected(res)
}

// skillRegSelectCols intentionally excludes the bundle BLOB column —
// list / head / version reads must not pay the cost of loading up to
// 25 MiB of tarball per row. bundle_sha256 is included (it's tiny) so
// callers can see at a glance whether a row has a bundle attached;
// fetch the bytes with GetSkillRegistryBundle.
const skillRegSelectCols = `id, name, version, content_hash, description, body,
		metadata_json, tags_json, author, parent_version,
		deleted_at, published_at, created_by_agent_id,
		workspace_id, source_type, source_path, bundle_sha256,
		manifest_extra`

func skillRegSelectColsPrefixed(p string) string {
	if p == "" {
		return skillRegSelectCols
	}
	cols := strings.Split(skillRegSelectCols, ",")
	for i, c := range cols {
		cols[i] = p + "." + strings.TrimSpace(c)
	}
	return strings.Join(cols, ", ")
}

// workspaceClause produces ("?", " AND workspace_id = ?", ["wsID"]) for
// scope-pinning a single workspace, or (nil, " AND workspace_id IS NULL", [])
// for global. Used by single-row writes.
func workspaceClause(workspaceID *string) (any, string, []any) {
	if workspaceID == nil {
		return nil, " AND workspace_id IS NULL", nil
	}
	return *workspaceID, " AND workspace_id = ?", []any{*workspaceID}
}

// scopeWhereClause returns " AND (...)" + params expressing the visible
// rows for a SkillScope (workspaces ∪ global). Empty scope = global only.
// IncludeAll bypasses scoping entirely.
func scopeWhereClause(scope store.SkillScope) (string, []any) {
	if scope.IncludeAll {
		return "", nil
	}
	if len(scope.WorkspaceIDs) == 0 {
		return " AND workspace_id IS NULL", nil
	}
	placeholders := make([]string, len(scope.WorkspaceIDs))
	params := make([]any, 0, len(scope.WorkspaceIDs))
	for i, id := range scope.WorkspaceIDs {
		placeholders[i] = "?"
		params = append(params, id)
	}
	clause := " AND (workspace_id IS NULL OR workspace_id IN (" + strings.Join(placeholders, ",") + "))"
	return clause, params
}

func scanSkillRegistryEntry(scan func(...any) error) (*store.SkillRegistryEntry, error) {
	var (
		e             store.SkillRegistryEntry
		metadata      string
		tags          string
		parentVer     sql.NullInt64
		deletedAt     sql.NullInt64
		publishedAt   int64
		createdByID   sql.NullString
		workspaceID   sql.NullString
		sourceType    sql.NullString
		sourcePath    sql.NullString
		bundleSHA256  sql.NullString
		manifestExtra sql.NullString
	)
	if err := scan(
		&e.ID, &e.Name, &e.Version, &e.ContentHash, &e.Description,
		&e.Body, &metadata, &tags, &e.Author, &parentVer,
		&deletedAt, &publishedAt, &createdByID,
		&workspaceID, &sourceType, &sourcePath, &bundleSHA256,
		&manifestExtra,
	); err != nil {
		return nil, err
	}
	e.MetadataJSON = mergeManifestExtra(json.RawMessage(metadata), manifestExtra)
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
	if sourceType.Valid {
		e.SourceType = sourceType.String
	} else {
		e.SourceType = "inline"
	}
	if sourcePath.Valid {
		e.SourcePath = sourcePath.String
	}
	if bundleSHA256.Valid {
		e.BundleSHA256 = bundleSHA256.String
	}
	return &e, nil
}

// GetSkillRegistryBundle fetches the raw tar.gz bytes for one entry.
// Returns (nil, "", nil) when the row exists but has no bundle attached,
// and store.ErrNotFound when the row itself is missing or soft-deleted.
// Pulled out of the default reads so list / head queries don't carry the
// 25 MiB blob through Go memory for every row.
func (d *DB) GetSkillRegistryBundle(
	ctx context.Context, workspaceID *string, name string, version int,
) ([]byte, string, error) {
	_, wsClause, wsParams := workspaceClause(workspaceID)
	q := `
		SELECT bundle, bundle_sha256
		FROM skill_registry_entries
		WHERE name = ? AND version = ? AND deleted_at IS NULL ` + wsClause
	row := d.q.QueryRowContext(ctx, q, append([]any{name, version}, wsParams...)...)
	var (
		bundle []byte
		sha    sql.NullString
	)
	if err := row.Scan(&bundle, &sha); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, "", store.ErrNotFound
		}
		return nil, "", fmt.Errorf("get skill_registry bundle: %w", err)
	}
	return bundle, sha.String, nil
}
