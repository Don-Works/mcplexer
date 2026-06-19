// memory_associative.go — associative-recall axis: co-occurrence + the
// entity-to-entity graph builder (AR1 + AR3 from the wave-3 epic).
//
// Spreading activation (AR2) needs the embedder + vec0 path which is
// already in memory_query.go; the service-layer composition lives in
// internal/memory/registry.go, not here, so the AR2 hook is the
// existing VectorSearchMemories plus ListMemoryEntities — no new sqlite
// method is required for it.
package sqlite

import (
	"context"
	"encoding/json"
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

// BuildEntityGraph constructs the entity-to-entity graph in scope:
// nodes are distinct entities ranked by MemoryCount (capped at nodeCap),
// edges are co-link pairs weighted by the count of memories that link
// BOTH endpoints. Edges are undirected — emitted once with Source <
// Target lexically (formatted as "kind:id").
//
// Implementation: two passes. First fetch the top-N nodes via the same
// aggregation as ListEntities. Then fetch the co-link edges restricted
// to that node set. minWeight=0 keeps every edge; >0 prunes the long
// tail so the layout stays readable.
func (d *DB) BuildEntityGraph(
	ctx context.Context, scope store.SkillScope, nodeCap, minWeight int,
) (store.EntityGraph, error) {
	if nodeCap <= 0 {
		nodeCap = 200
	}
	if minWeight < 0 {
		minWeight = 0
	}
	// Fetch nodes — reuses ListEntities semantics but materialises as a
	// flat slice we can intern in a map for the edge-pass.
	nodes, err := d.ListEntities(ctx, store.EntityFilter{
		Scope: scope,
		Limit: nodeCap + 1, // peek one past the cap so we can set Truncated
	})
	if err != nil {
		return store.EntityGraph{}, fmt.Errorf("entity graph nodes: %w", err)
	}
	truncated := false
	if len(nodes) > nodeCap {
		nodes = nodes[:nodeCap]
		truncated = true
	}
	if len(nodes) == 0 {
		return store.EntityGraph{NodeCap: nodeCap, Truncated: false}, nil
	}
	nodeKeys := make([]string, len(nodes))
	for i, n := range nodes {
		nodeKeys[i] = n.Kind + ":" + n.ID
	}
	nodeJSON, err := json.Marshal(nodeKeys)
	if err != nil {
		return store.EntityGraph{}, fmt.Errorf("entity graph node set: %w", err)
	}

	// Fetch edges restricted to the node set IN SQL. The earlier
	// implementation fetched EVERY co-link pair in scope then filtered to
	// the top-N node set in Go — an O(N^2) self-join over the whole entity
	// space with no node-set predicate. Here we marshal the (<=nodeCap)
	// node keys to a JSON array and json_each-JOIN both endpoints, so the
	// self-join is bounded to the node set up front. idx_memory_entities_lookup
	// (entity_kind, entity_id, memory_id) from migration 076 backs the
	// (entity_kind||':'||entity_id) lookups on each side.
	scopeClause, scopeArgs := scopeWhereClauseAlias(scope, "m")
	args := []any{string(nodeJSON), string(nodeJSON)}
	args = append(args, scopeArgs...)
	args = append(args, minWeight)
	// Generate undirected pairs by enforcing the lexical-ordering trick
	// inline (entity_kind || ':' || entity_id < entity_kind || ':' || …).
	q := `
		SELECT
		    me1.entity_kind || ':' || me1.entity_id AS src,
		    me2.entity_kind || ':' || me2.entity_id AS tgt,
		    COUNT(DISTINCT me1.memory_id) AS weight
		FROM memory_entities me1
		JOIN json_each(?) ns1
		    ON ns1.value = me1.entity_kind || ':' || me1.entity_id
		JOIN memory_entities me2 ON me2.memory_id = me1.memory_id
		JOIN json_each(?) ns2
		    ON ns2.value = me2.entity_kind || ':' || me2.entity_id
		JOIN memories m ON m.id = me1.memory_id
		WHERE m.deleted_at IS NULL
		  AND m.t_valid_end IS NULL
		  AND (me1.entity_kind || ':' || me1.entity_id) <
		      (me2.entity_kind || ':' || me2.entity_id)` +
		scopeClause + `
		GROUP BY src, tgt
		HAVING weight >= ?
		ORDER BY weight DESC`
	rows, err := d.q.QueryContext(ctx, q, args...)
	if err != nil {
		return store.EntityGraph{}, fmt.Errorf("entity graph edges: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	var edges []store.EntityEdge
	for rows.Next() {
		var e store.EntityEdge
		if err := rows.Scan(&e.Source, &e.Target, &e.Weight); err != nil {
			return store.EntityGraph{}, fmt.Errorf("scan edge: %w", err)
		}
		edges = append(edges, e)
	}
	if err := rows.Err(); err != nil {
		return store.EntityGraph{}, err
	}
	return store.EntityGraph{
		Nodes:     nodes,
		Edges:     edges,
		NodeCap:   nodeCap,
		Truncated: truncated,
	}, nil
}
