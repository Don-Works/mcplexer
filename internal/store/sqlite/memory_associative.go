// memory_associative.go — associative-recall axis: entity co-occurrence
// (AR1 from the wave-3 epic).
//
// Spreading activation (AR2) needs the embedder + vec0 path which is
// already in memory_query.go; the service-layer composition lives in
// internal/memory/registry.go, not here, so the AR2 hook is the
// existing VectorSearchMemories plus ListMemoryEntities — no new sqlite
// method is required for it.
package sqlite

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// RelatedEntities returns entities that co-link with the given entity
// in at least one memory. SharedCount is the number of memories that
// link BOTH the query entity and this entity. Self is excluded.
//
// SQL pattern: self-join memory_entities on memory_id, then group by
// the *other* (kind, id) pair, count distinct memories. The standard
// scope clause is applied via the joined memories row so visibility
// stays consistent with ListMemories / Recall.
func (d *DB) RelatedEntities(
	ctx context.Context, x store.EntityRef, scope store.SkillScope, limit int,
) ([]store.EntityCoLink, error) {
	kind := strings.ToLower(strings.TrimSpace(x.Kind))
	id := strings.ToLower(strings.TrimSpace(x.ID))
	if kind == "" || id == "" {
		return nil, fmt.Errorf("RelatedEntities: kind and id required")
	}
	if limit <= 0 {
		limit = 20
	}
	scopeClause, scopeArgs := scopeWhereClauseAlias(scope, "m")
	args := []any{kind, id, kind, id}
	args = append(args, scopeArgs...)
	args = append(args, limit)
	q := `
		SELECT me2.entity_kind, me2.entity_id,
		       COUNT(DISTINCT me2.memory_id) AS shared_count,
		       MAX(me2.created_at) AS last_seen
		FROM memory_entities me1
		JOIN memory_entities me2 ON me2.memory_id = me1.memory_id
		JOIN memories m ON m.id = me1.memory_id
		WHERE me1.entity_kind = ? AND me1.entity_id = ?
		  AND NOT (me2.entity_kind = ? AND me2.entity_id = ?)
		  AND m.deleted_at IS NULL
		  AND m.t_valid_end IS NULL` +
		scopeClause + `
		GROUP BY me2.entity_kind, me2.entity_id
		ORDER BY shared_count DESC, last_seen DESC, me2.entity_id ASC
		LIMIT ?`
	rows, err := d.q.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("related entities: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	var out []store.EntityCoLink
	for rows.Next() {
		var r store.EntityCoLink
		var lastSeen int64
		if err := rows.Scan(&r.Kind, &r.ID, &r.SharedCount, &lastSeen); err != nil {
			return nil, fmt.Errorf("scan related entity: %w", err)
		}
		r.LastSeenAt = time.Unix(lastSeen, 0).UTC()
		out = append(out, r)
	}
	return out, rows.Err()
}
