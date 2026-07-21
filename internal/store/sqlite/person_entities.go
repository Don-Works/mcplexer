// person_entities.go — entity-link operations for store.PersonStore
// (migration 094). Links answer "what is this person linked to" — org, deal,
// task, peer, agent, skill, artifact. Mirrors memory_entities.go: freeform
// kind vocabulary, lower-cased ids, role defaults to "subject".
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

// LinkPersonEntity inserts (or no-ops on duplicate) a person_entities row.
// Idempotency comes from the unique index on
// (person_id, entity_kind, entity_id, role).
func (d *DB) LinkPersonEntity(
	ctx context.Context, personID string, e store.EntityRef, createdBy string,
) error {
	norm, err := normalizeEntityRef(e, true)
	if err != nil {
		return err
	}
	if strings.TrimSpace(personID) == "" {
		return errors.New("LinkPersonEntity: personID required")
	}
	id := ulid.Make().String()
	now := time.Now().Unix()
	_, err = d.q.ExecContext(ctx, `
		INSERT INTO person_entities(
			id, person_id, entity_kind, entity_id, role, created_at, created_by
		) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(person_id, entity_kind, entity_id, role) DO NOTHING`,
		id, personID, norm.Kind, norm.ID, norm.Role, now, nullString(createdBy),
	)
	if err != nil {
		return fmt.Errorf("link person entity: %w", err)
	}
	return nil
}

// UnlinkPersonEntity deletes the matching person_entities row(s). Empty role
// means "any role". Idempotent.
func (d *DB) UnlinkPersonEntity(
	ctx context.Context, personID string, e store.EntityRef,
) error {
	norm, err := normalizeEntityRef(e, false)
	if err != nil {
		return err
	}
	if strings.TrimSpace(personID) == "" {
		return errors.New("UnlinkPersonEntity: personID required")
	}
	if norm.Role == "" {
		_, err = d.q.ExecContext(ctx, `
			DELETE FROM person_entities
			WHERE person_id = ? AND entity_kind = ? AND entity_id = ?`,
			personID, norm.Kind, norm.ID,
		)
	} else {
		_, err = d.q.ExecContext(ctx, `
			DELETE FROM person_entities
			WHERE person_id = ? AND entity_kind = ? AND entity_id = ?
			  AND role = ?`,
			personID, norm.Kind, norm.ID, norm.Role,
		)
	}
	if err != nil {
		return fmt.Errorf("unlink person entity: %w", err)
	}
	return nil
}

// ListPersonEntities returns every link for one person, ordered by created_at
// ASC so the UI renders them in add-order.
func (d *DB) ListPersonEntities(
	ctx context.Context, personID string,
) ([]store.PersonEntityRow, error) {
	rows, err := d.q.QueryContext(ctx, `
		SELECT id, person_id, entity_kind, entity_id, role,
		       created_at, COALESCE(created_by, '')
		FROM person_entities
		WHERE person_id = ?
		ORDER BY created_at ASC, id ASC`, personID,
	)
	if err != nil {
		return nil, fmt.Errorf("list person entities: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	var out []store.PersonEntityRow
	for rows.Next() {
		var r store.PersonEntityRow
		var createdAt int64
		if err := rows.Scan(
			&r.ID, &r.PersonID, &r.EntityKind, &r.EntityID, &r.Role,
			&createdAt, &r.CreatedBy,
		); err != nil {
			return nil, fmt.Errorf("scan person entity: %w", err)
		}
		r.CreatedAt = time.Unix(createdAt, 0).UTC()
		out = append(out, r)
	}
	return out, rows.Err()
}
