// memory_entities.go — entity-link operations for store.MemoryStore
// (migration 076). Entities are the "what is this memory about" axis,
// orthogonal to the structural scope (workspace / worker / run / user)
// already on the memories table. See migration 076's header for the
// kind vocabulary, role semantics, and cross-peer identity rules.
package sqlite

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/oklog/ulid/v2"
)

// LinkMemoryEntity inserts (or no-ops on duplicate) a memory_entities row.
// Idempotency comes from the unique index on
// (memory_id, entity_kind, entity_id, role).
func (d *DB) LinkMemoryEntity(
	ctx context.Context, memoryID string, e store.EntityRef, createdBy string,
) error {
	norm, err := normalizeEntityRef(e, true)
	if err != nil {
		return err
	}
	if strings.TrimSpace(memoryID) == "" {
		return errors.New("LinkMemoryEntity: memoryID required")
	}
	id := ulid.Make().String()
	now := time.Now().Unix()
	_, err = d.q.ExecContext(ctx, `
		INSERT INTO memory_entities(
			id, memory_id, entity_kind, entity_id, role, created_at, created_by
		) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(memory_id, entity_kind, entity_id, role) DO NOTHING`,
		id, memoryID, norm.Kind, norm.ID, norm.Role, now, nullString(createdBy),
	)
	if err != nil {
		return fmt.Errorf("link memory entity: %w", err)
	}
	return nil
}

// UnlinkMemoryEntity deletes the matching memory_entities row(s). Empty
// role on the EntityRef means "any role" — useful for removing all link
// flavours of a (memory, kind, id) triple in one call. Idempotent.
func (d *DB) UnlinkMemoryEntity(
	ctx context.Context, memoryID string, e store.EntityRef,
) error {
	norm, err := normalizeEntityRef(e, false)
	if err != nil {
		return err
	}
	if strings.TrimSpace(memoryID) == "" {
		return errors.New("UnlinkMemoryEntity: memoryID required")
	}
	if norm.Role == "" {
		_, err = d.q.ExecContext(ctx, `
			DELETE FROM memory_entities
			WHERE memory_id = ? AND entity_kind = ? AND entity_id = ?`,
			memoryID, norm.Kind, norm.ID,
		)
	} else {
		_, err = d.q.ExecContext(ctx, `
			DELETE FROM memory_entities
			WHERE memory_id = ? AND entity_kind = ? AND entity_id = ?
			  AND role = ?`,
			memoryID, norm.Kind, norm.ID, norm.Role,
		)
	}
	if err != nil {
		return fmt.Errorf("unlink memory entity: %w", err)
	}
	return nil
}

// ListMemoryEntities returns every link for one memory, ordered by
// created_at ASC so the UI can render them in the order they were added.
func (d *DB) ListMemoryEntities(
	ctx context.Context, memoryID string,
) ([]store.MemoryEntityRow, error) {
	rows, err := d.q.QueryContext(ctx, `
		SELECT id, memory_id, entity_kind, entity_id, role,
		       created_at, COALESCE(created_by, '')
		FROM memory_entities
		WHERE memory_id = ?
		ORDER BY created_at ASC, id ASC`, memoryID,
	)
	if err != nil {
		return nil, fmt.Errorf("list memory entities: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	var out []store.MemoryEntityRow
	for rows.Next() {
		var r store.MemoryEntityRow
		var createdAt int64
		if err := rows.Scan(
			&r.ID, &r.MemoryID, &r.EntityKind, &r.EntityID, &r.Role,
			&createdAt, &r.CreatedBy,
		); err != nil {
			return nil, fmt.Errorf("scan memory entity: %w", err)
		}
		r.CreatedAt = time.Unix(createdAt, 0).UTC()
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListEntities returns distinct entities ranked by memory_count DESC then
// last_linked_at DESC. Scope is enforced by joining memories and applying
// the standard scope clause — entities derive visibility from the
// memories they link to, so a workspace boundary on memories implies the
// same boundary on entity surfacing.
//
// Deleted + invalidated memories are excluded so the entity-picker
// autocomplete doesn't surface dead links. derived_from links are
// included in the count since the consolidator legitimately produced
// them; if that becomes noisy the UI can request only role=subject via
// a later filter extension.
func (d *DB) ListEntities(
	ctx context.Context, f store.EntityFilter,
) ([]store.EntitySummary, error) {
	scopeClause, scopeArgs := scopeWhereClauseAlias(f.Scope, "m")
	var b strings.Builder
	args := []any{}
	b.WriteString(`
		SELECT me.entity_kind, me.entity_id,
		       COUNT(DISTINCT me.memory_id) AS memory_count,
		       MAX(me.created_at) AS last_linked_at
		FROM memory_entities me
		JOIN memories m ON m.id = me.memory_id
		WHERE m.deleted_at IS NULL
		  AND m.t_valid_end IS NULL`)
	b.WriteString(scopeClause)
	args = append(args, scopeArgs...)
	if f.Kind != "" {
		b.WriteString(` AND me.entity_kind = ?`)
		args = append(args, f.Kind)
	}
	b.WriteString(`
		GROUP BY me.entity_kind, me.entity_id
		ORDER BY memory_count DESC, last_linked_at DESC, me.entity_id ASC`)
	if f.Limit > 0 {
		b.WriteString(` LIMIT ?`)
		args = append(args, f.Limit)
		if f.Offset > 0 {
			b.WriteString(` OFFSET ?`)
			args = append(args, f.Offset)
		}
	}
	rows, err := d.q.QueryContext(ctx, b.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("list entities: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	var out []store.EntitySummary
	for rows.Next() {
		var s store.EntitySummary
		var lastLinked int64
		if err := rows.Scan(&s.Kind, &s.ID, &s.MemoryCount, &lastLinked); err != nil {
			return nil, fmt.Errorf("scan entity summary: %w", err)
		}
		s.LastLinkedAt = time.Unix(lastLinked, 0).UTC()
		out = append(out, s)
	}
	return out, rows.Err()
}

// normalizeEntityRef enforces non-empty Kind + ID, lower-cases the ID
// so recall is case-insensitive, and defaults Role to "subject" when
// requireRole is true (write path); on the unlink path (requireRole=false)
// empty role flows through and the caller deletes any role.
func normalizeEntityRef(e store.EntityRef, requireRole bool) (store.EntityRef, error) {
	kind := strings.ToLower(strings.TrimSpace(e.Kind))
	id := strings.ToLower(strings.TrimSpace(e.ID))
	role := strings.ToLower(strings.TrimSpace(e.Role))
	if kind == "" {
		return store.EntityRef{}, errors.New("entity kind required")
	}
	if id == "" {
		return store.EntityRef{}, errors.New("entity id required")
	}
	if requireRole && role == "" {
		role = "subject"
	}
	return store.EntityRef{Kind: kind, ID: id, Role: role}, nil
}
