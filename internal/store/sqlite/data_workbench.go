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

const dataCollectionCols = `id, workspace_id, name, kind, tags_json,
	schema_json, metadata_json, row_count, doc_count, pinned, ttl_expires_at,
	source_session_id, deleted_at, created_at, updated_at`

func (d *DB) IngestDataCollection(
	ctx context.Context, c *store.DataCollection, items []store.DataItem,
) error {
	if c == nil {
		return errors.New("IngestDataCollection: nil collection")
	}
	if strings.TrimSpace(c.WorkspaceID) == "" || strings.TrimSpace(c.Name) == "" {
		return errors.New("IngestDataCollection: workspace_id and name required")
	}
	if c.Kind == "" {
		c.Kind = store.DataWorkbenchKindTable
	}
	if c.Kind != store.DataWorkbenchKindTable && c.Kind != store.DataWorkbenchKindDocs {
		return fmt.Errorf("IngestDataCollection: invalid kind %q", c.Kind)
	}
	if c.ID == "" {
		c.ID = ulid.Make().String()
	}
	now := time.Now().UTC()
	if c.CreatedAt.IsZero() {
		c.CreatedAt = now
	}
	if c.UpdatedAt.IsZero() {
		c.UpdatedAt = now
	}
	c.RowCount, c.DocCount = countDataItems(items, c.Kind)

	return d.withTx(ctx, func(q queryable) error {
		if err := dropDataCollection(ctx, q, c.WorkspaceID, c.Name, now); err != nil {
			return err
		}
		if err := insertDataCollection(ctx, q, c); err != nil {
			return err
		}
		return insertDataItems(ctx, q, c, items, now)
	})
}

func (d *DB) GetDataCollection(
	ctx context.Context, workspaceID, name string,
) (*store.DataCollection, error) {
	row := d.q.QueryRowContext(ctx, `SELECT `+dataCollectionCols+`
		FROM data_workbench_collections
		WHERE workspace_id = ? AND name = ? AND deleted_at IS NULL
		  AND (ttl_expires_at IS NULL OR ttl_expires_at > ?)
		LIMIT 1`, workspaceID, name, formatTime(time.Now().UTC()))
	return scanDataCollection(row)
}

func (d *DB) ListDataCollections(
	ctx context.Context, f store.DataCollectionFilter,
) ([]store.DataCollection, error) {
	if strings.TrimSpace(f.WorkspaceID) == "" {
		return nil, errors.New("ListDataCollections: workspace_id required")
	}
	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	where := `workspace_id = ?`
	args := []any{f.WorkspaceID}
	if !f.IncludeDeleted {
		where += ` AND deleted_at IS NULL`
	}
	if !f.IncludeExpired {
		where += ` AND (ttl_expires_at IS NULL OR ttl_expires_at > ?)`
		args = append(args, formatTime(time.Now().UTC()))
	}
	rows, err := d.q.QueryContext(ctx, `SELECT `+dataCollectionCols+`
		FROM data_workbench_collections
		WHERE `+where+`
		ORDER BY updated_at DESC LIMIT ? OFFSET ?`, append(args, limit, f.Offset)...)
	if err != nil {
		return nil, fmt.Errorf("list data collections: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []store.DataCollection
	for rows.Next() {
		c, err := scanDataCollection(rows)
		if err != nil {
			return nil, err
		}
		if tagsMatch(c.TagsJSON, f.Tags) {
			out = append(out, *c)
		}
	}
	return out, rows.Err()
}

func (d *DB) DropDataCollection(ctx context.Context, workspaceID, name string) error {
	err := d.withTx(ctx, func(q queryable) error {
		return dropDataCollection(ctx, q, workspaceID, name, time.Now().UTC())
	})
	if errors.Is(err, store.ErrNotFound) {
		return store.ErrNotFound
	}
	return err
}

func (d *DB) PruneExpiredDataCollections(ctx context.Context, now time.Time) (int, error) {
	rows, err := d.q.QueryContext(ctx, `
		SELECT workspace_id, name FROM data_workbench_collections
		WHERE deleted_at IS NULL AND pinned = 0 AND ttl_expires_at IS NOT NULL
		  AND ttl_expires_at <= ?`, formatTime(now))
	if err != nil {
		return 0, err
	}
	defer func() { _ = rows.Close() }()

	var refs [][2]string
	for rows.Next() {
		var workspaceID, name string
		if err := rows.Scan(&workspaceID, &name); err != nil {
			return 0, err
		}
		refs = append(refs, [2]string{workspaceID, name})
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, ref := range refs {
		if err := d.DropDataCollection(ctx, ref[0], ref[1]); err != nil {
			return 0, err
		}
	}
	return len(refs), nil
}

func insertDataCollection(ctx context.Context, q queryable, c *store.DataCollection) error {
	_, err := q.ExecContext(ctx, `
		INSERT INTO data_workbench_collections (`+dataCollectionCols+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ID, c.WorkspaceID, c.Name, c.Kind,
		normalizeJSON(c.TagsJSON, "[]"), normalizeJSON(c.SchemaJSON, "{}"),
		normalizeJSON(c.MetadataJSON, "{}"), c.RowCount, c.DocCount,
		boolToInt(c.Pinned), nullableTime(c.TTLExpiresAt), c.SourceSessionID,
		nullableTime(c.DeletedAt), formatTime(c.CreatedAt), formatTime(c.UpdatedAt))
	if err != nil {
		return fmt.Errorf("insert data collection: %w", err)
	}
	return nil
}

func insertDataItems(
	ctx context.Context, q queryable, c *store.DataCollection,
	items []store.DataItem, now time.Time,
) error {
	tags := normalizeJSON(c.TagsJSON, "[]")
	for i := range items {
		it := &items[i]
		if it.ID == "" {
			it.ID = ulid.Make().String()
		}
		it.CollectionID = c.ID
		if it.Kind == "" {
			it.Kind = c.Kind
		}
		it.Ordinal = i
		if it.CreatedAt.IsZero() {
			it.CreatedAt = now
		}
		payload := normalizeJSON(it.PayloadJSON, "{}")
		if strings.TrimSpace(it.Text) == "" {
			it.Text = payload
		}
		if _, err := q.ExecContext(ctx, `
			INSERT INTO data_workbench_items
				(id, collection_id, ordinal, kind, payload_json, text, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			it.ID, c.ID, it.Ordinal, it.Kind, payload, it.Text, formatTime(it.CreatedAt)); err != nil {
			return fmt.Errorf("insert data item: %w", err)
		}
		if _, err := q.ExecContext(ctx, `
			INSERT INTO data_workbench_items_fts
				(collection_id, item_id, name, payload, text, tags, workspace_id)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			c.ID, it.ID, c.Name, payload, it.Text, tags, c.WorkspaceID); err != nil {
			return fmt.Errorf("insert data item fts: %w", err)
		}
	}
	return nil
}

func dropDataCollection(
	ctx context.Context, q queryable, workspaceID, name string, now time.Time,
) error {
	var ids []string
	rows, err := q.QueryContext(ctx, `
		SELECT id FROM data_workbench_collections
		WHERE workspace_id = ? AND name = ? AND deleted_at IS NULL`, workspaceID, name)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	for _, id := range ids {
		if _, err := q.ExecContext(ctx,
			`DELETE FROM data_workbench_items_fts WHERE collection_id = ?`, id); err != nil {
			return err
		}
		if _, err := q.ExecContext(ctx,
			`DELETE FROM data_workbench_items WHERE collection_id = ?`, id); err != nil {
			return err
		}
	}
	res, err := q.ExecContext(ctx, `
		UPDATE data_workbench_collections SET deleted_at = ?, updated_at = ?
		WHERE workspace_id = ? AND name = ? AND deleted_at IS NULL`,
		formatTime(now), formatTime(now), workspaceID, name)
	if err != nil {
		return err
	}
	return checkRowsAffected(res)
}

func scanDataCollection(r scanner) (*store.DataCollection, error) {
	var c store.DataCollection
	var pinned int
	var ttl, deleted sql.NullString
	var tagsJSON, schemaJSON, metadataJSON string
	var created, updated string
	err := r.Scan(&c.ID, &c.WorkspaceID, &c.Name, &c.Kind, &tagsJSON,
		&schemaJSON, &metadataJSON, &c.RowCount, &c.DocCount, &pinned,
		&ttl, &c.SourceSessionID, &deleted, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	c.TagsJSON = []byte(tagsJSON)
	c.SchemaJSON = []byte(schemaJSON)
	c.MetadataJSON = []byte(metadataJSON)
	c.Pinned = pinned != 0
	if ttl.Valid {
		c.TTLExpiresAt = parseTimePtr(&ttl.String)
	}
	if deleted.Valid {
		c.DeletedAt = parseTimePtr(&deleted.String)
	}
	c.CreatedAt = parseTime(created)
	c.UpdatedAt = parseTime(updated)
	return &c, nil
}
