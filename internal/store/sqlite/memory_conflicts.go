// memory_conflicts.go — persistence for the memory conflict queue
// (migration 116). A note write's neighbour scan surfaces possible
// duplicate/conflict pairs; these are recorded here so the dashboard can
// offer an explicit review + resolution flow instead of the contradiction
// signal evaporating when the save call returns.
package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/oklog/ulid/v2"
)

// RecordMemoryConflicts inserts conflict rows, ignoring any whose open
// (memory_id, candidate_id) pair already exists (the partial unique index).
// Empty IDs are filled with a fresh ULID.
func (d *DB) RecordMemoryConflicts(ctx context.Context, conflicts []store.MemoryConflict) error {
	if len(conflicts) == 0 {
		return nil
	}
	return d.withTx(ctx, func(q queryable) error {
		for i := range conflicts {
			c := &conflicts[i]
			if c.ID == "" {
				c.ID = ulid.Make().String()
			}
			created := c.CreatedAt
			if created.IsZero() {
				created = time.Now()
			}
			var wsID any
			if c.WorkspaceID != "" {
				wsID = c.WorkspaceID
			}
			if _, err := q.ExecContext(ctx, `
				INSERT OR IGNORE INTO memory_conflicts(
					id, memory_id, memory_name, candidate_id, candidate_name,
					candidate_preview, kind, reason, workspace_id, created_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				c.ID, c.MemoryID, c.MemoryName, c.CandidateID, c.CandidateName,
				c.CandidatePreview, c.Kind, c.Reason, wsID, created.Unix(),
			); err != nil {
				return fmt.Errorf("record memory conflict: %w", err)
			}
		}
		return nil
	})
}

// ListOpenMemoryConflicts returns unresolved conflicts newest-first.
func (d *DB) ListOpenMemoryConflicts(ctx context.Context, limit int) ([]store.MemoryConflict, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := d.q.QueryContext(ctx, `
		SELECT id, memory_id, memory_name, candidate_id, candidate_name,
		       candidate_preview, kind, reason, workspace_id, created_at
		FROM memory_conflicts
		WHERE resolved_at IS NULL
		ORDER BY created_at DESC
		LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list open memory conflicts: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	var out []store.MemoryConflict
	for rows.Next() {
		var c store.MemoryConflict
		var wsID sql.NullString
		var created int64
		if err := rows.Scan(&c.ID, &c.MemoryID, &c.MemoryName, &c.CandidateID,
			&c.CandidateName, &c.CandidatePreview, &c.Kind, &c.Reason, &wsID, &created); err != nil {
			return nil, fmt.Errorf("scan memory conflict: %w", err)
		}
		c.WorkspaceID = wsID.String
		c.CreatedAt = time.Unix(created, 0).UTC()
		out = append(out, c)
	}
	return out, rows.Err()
}

// ResolveMemoryConflict marks one open conflict resolved. A no-op (nil error)
// when the id is unknown or already resolved.
func (d *DB) ResolveMemoryConflict(ctx context.Context, id, resolution string) error {
	if id == "" {
		return fmt.Errorf("ResolveMemoryConflict: id required")
	}
	_, err := d.q.ExecContext(ctx, `
		UPDATE memory_conflicts
		SET resolved_at = ?, resolution = ?
		WHERE id = ? AND resolved_at IS NULL`,
		time.Now().Unix(), resolution, id)
	if err != nil {
		return fmt.Errorf("resolve memory conflict: %w", err)
	}
	return nil
}
