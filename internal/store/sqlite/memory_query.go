// memory_query.go — read + search + vector ops for store.MemoryStore.
package sqlite

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// ListMemories returns rows matching the filter, ordered by updated_at
// DESC. Honors scope, kind, tags (AND), worker/run/user/source filters,
// and the IncludeInvalid / IncludeDeleted flags.
func (d *DB) ListMemories(
	ctx context.Context, f store.MemoryFilter,
) ([]store.MemoryEntry, error) {
	where, args := memoryWhere(f, "")
	q := `SELECT ` + memorySelectCols + `
		FROM memories
		WHERE 1=1 ` + where + `
		ORDER BY updated_at DESC, id DESC`
	if f.Limit > 0 {
		q += ` LIMIT ?`
		args = append(args, f.Limit)
		if f.Offset > 0 {
			q += ` OFFSET ?`
			args = append(args, f.Offset)
		}
	}
	rows, err := d.q.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list memories: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	var out []store.MemoryEntry
	for rows.Next() {
		e, err := scanMemory(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("scan memory: %w", err)
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}

// SearchMemories runs an FTS5 MATCH query within the filter scope.
// Returns hits ranked by BM25 (lower=better). The query string is
// sanitised internally — punctuation (':', '-', '(', '*', etc.) that
// would otherwise be interpreted as FTS5 metacharacters is escaped via
// per-term quoting so any natural-language or agent-supplied input is
// safe to pass without a wrapper. Pass an empty string to fall back to
// ListMemories.
func (d *DB) SearchMemories(
	ctx context.Context, f store.MemoryFilter, query string,
) ([]store.MemoryHit, error) {
	expr := sanitizeFTS5Query(query)
	if expr == "" {
		entries, err := d.ListMemories(ctx, f)
		if err != nil {
			return nil, err
		}
		hits := make([]store.MemoryHit, 0, len(entries))
		for _, e := range entries {
			hits = append(hits, store.MemoryHit{Entry: e, Score: 0, Source: "list"})
		}
		return hits, nil
	}
	where, args := memoryWhere(f, "m")
	args = append([]any{expr}, args...)
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	q := `SELECT ` + memoryColsPrefixed("m") + `, fts.rank
		FROM memories_fts fts
		JOIN memories m ON m.rowid = fts.rowid
		WHERE memories_fts MATCH ? ` + where + `
		ORDER BY fts.rank
		LIMIT ?`
	args = append(args, limit)
	rows, err := d.q.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("search memories: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	var hits []store.MemoryHit
	for rows.Next() {
		var rank float64
		e, err := scanMemoryWithExtra(rows.Scan, &rank)
		if err != nil {
			return nil, fmt.Errorf("scan memory hit: %w", err)
		}
		hits = append(hits, store.MemoryHit{Entry: *e, Score: rank, Source: "fts"})
	}
	return hits, rows.Err()
}

// VectorSearchMemories runs a vec0 KNN query against memories_vec.
// Vectors are serialized as JSON arrays (sqlite-vec accepts that format
// for both insert and MATCH). Embedding model is enforced — rows with a
// different embed_model are excluded.
func (d *DB) VectorSearchMemories(
	ctx context.Context, f store.MemoryFilter,
	embedModel string, vector []float32, k int,
) ([]store.MemoryHit, error) {
	if embedModel == "" {
		return nil, fmt.Errorf("VectorSearchMemories: embed_model required")
	}
	if len(vector) == 0 {
		return nil, fmt.Errorf("VectorSearchMemories: empty vector")
	}
	if k <= 0 {
		k = 20
	}
	vecJSON := vectorToJSON(vector)
	where, args := memoryWhere(f, "m")
	args = append([]any{vecJSON, k, embedModel}, args...)
	q := `SELECT ` + memoryColsPrefixed("m") + `, v.distance
		FROM memories_vec v
		JOIN memories m ON m.id = v.memory_id
		WHERE v.embedding MATCH ? AND v.k = ?
		  AND m.embed_model = ?
		  ` + where + `
		ORDER BY v.distance`
	rows, err := d.q.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("vector search memories: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	var hits []store.MemoryHit
	for rows.Next() {
		var dist float64
		e, err := scanMemoryWithExtra(rows.Scan, &dist)
		if err != nil {
			return nil, fmt.Errorf("scan vector hit: %w", err)
		}
		hits = append(hits, store.MemoryHit{Entry: *e, Score: dist, Source: "vec"})
	}
	return hits, rows.Err()
}

// UpsertMemoryEmbedding replaces the vector for one memory ID and stamps
// embed_model + embed_version on the memories row so callers can detect
// stale vectors.
func (d *DB) UpsertMemoryEmbedding(
	ctx context.Context, id, embedModel string, embedVersion int,
	vector []float32,
) error {
	if id == "" || embedModel == "" || len(vector) == 0 {
		return fmt.Errorf("UpsertMemoryEmbedding: id, embed_model and vector required")
	}
	vecJSON := vectorToJSON(vector)
	now := time.Now().Unix()
	return d.withTx(ctx, func(q queryable) error {
		_, err := q.ExecContext(ctx,
			`DELETE FROM memories_vec WHERE memory_id = ?`, id)
		if err != nil {
			return fmt.Errorf("delete prior vector: %w", err)
		}
		_, err = q.ExecContext(ctx,
			`INSERT INTO memories_vec(memory_id, embedding) VALUES (?, ?)`,
			id, vecJSON)
		if err != nil {
			return fmt.Errorf("insert vector: %w", err)
		}
		res, err := q.ExecContext(ctx, `
			UPDATE memories SET embed_model = ?, embed_version = ?, updated_at = ?
			WHERE id = ? AND deleted_at IS NULL`,
			embedModel, embedVersion, now, id)
		if err != nil {
			return fmt.Errorf("stamp embedding meta: %w", err)
		}
		return checkRowsAffected(res)
	})
}

// ListMemoriesNeedingEmbedding returns up to limit active memories with no
// stored vector yet (embed_model empty/null) and non-empty content, oldest
// first so a resumable backfill makes monotonic progress.
func (d *DB) ListMemoriesNeedingEmbedding(
	ctx context.Context, limit int,
) ([]store.MemoryEmbedTarget, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := d.q.QueryContext(ctx, `
		SELECT id, content FROM memories
		WHERE (embed_model IS NULL OR embed_model = '')
		  AND deleted_at IS NULL
		  AND content != ''
		ORDER BY created_at ASC
		LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list memories needing embedding: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	var out []store.MemoryEmbedTarget
	for rows.Next() {
		var t store.MemoryEmbedTarget
		if err := rows.Scan(&t.ID, &t.Content); err != nil {
			return nil, fmt.Errorf("scan embed target: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// CountMemoriesNeedingEmbedding returns (pending, total) over active rows:
// pending = active memories with non-empty content but no vector yet;
// total = all active memories. Backfill progress = (total-pending)/total.
func (d *DB) CountMemoriesNeedingEmbedding(
	ctx context.Context,
) (pending, total int, err error) {
	row := d.q.QueryRowContext(ctx, `
		SELECT
		  COALESCE(SUM(CASE WHEN (embed_model IS NULL OR embed_model = '')
		                     AND content != '' THEN 1 ELSE 0 END), 0),
		  COUNT(*)
		FROM memories
		WHERE deleted_at IS NULL`)
	if err := row.Scan(&pending, &total); err != nil {
		return 0, 0, fmt.Errorf("count memories needing embedding: %w", err)
	}
	return pending, total, nil
}

// GetMemoryEmbedding returns the stored vector for one memory ID plus
// the embed_model it was written under. The vector lives in the vec0
// virtual table memories_vec; the model is read from the memories row.
// Returns store.ErrNotFound when no vector row exists for the id.
//
// sqlite-vec's vec0 surfaces the embedding column as a little-endian
// FLOAT32 blob on a plain SELECT; vectorFromBlob decodes it. As a
// defensive fallback (older snapshots, or a future build that returns a
// JSON-text representation) vectorFromBlob also parses a JSON array.
func (d *DB) GetMemoryEmbedding(
	ctx context.Context, id string,
) (string, []float32, error) {
	if strings.TrimSpace(id) == "" {
		return "", nil, fmt.Errorf("GetMemoryEmbedding: id required")
	}
	var (
		raw        []byte
		embedModel sql.NullString
	)
	row := d.q.QueryRowContext(ctx, `
		SELECT v.embedding, m.embed_model
		FROM memories_vec v
		LEFT JOIN memories m ON m.id = v.memory_id
		WHERE v.memory_id = ?`, id)
	if err := row.Scan(&raw, &embedModel); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil, store.ErrNotFound
		}
		return "", nil, fmt.Errorf("get memory embedding: %w", err)
	}
	vec, err := vectorFromBlob(raw)
	if err != nil {
		return "", nil, fmt.Errorf("decode memory embedding: %w", err)
	}
	model := ""
	if embedModel.Valid {
		model = embedModel.String
	}
	return model, vec, nil
}

// memoryWhere builds the (clause, args) pair representing every filter
// dimension that maps to a simple WHERE fragment. Tags filter in SQL via
// EXISTS over json_each(tags_json) — filtering in Go after the SQL LIMIT
// (the old behaviour) returned short pages and silently dropped matches
// past the limit. When alias is non-empty, every column reference is
// prefixed with "<alias>." so the clause is safe to use in a JOIN where
// memories is aliased.
func memoryWhere(f store.MemoryFilter, alias string) (string, []any) {
	var b strings.Builder
	var args []any
	col := func(name string) string {
		if alias == "" {
			return name
		}
		return alias + "." + name
	}
	if !f.IncludeDeleted {
		b.WriteString(" AND " + col("deleted_at") + " IS NULL")
	}
	switch {
	case f.ValidAt != nil:
		// Point-in-time recall: a row was valid at the instant T when its
		// window covered T. This deliberately supersedes the IncludeInvalid
		// active-only clause — a row invalidated after T was still believed
		// at T, so it must surface for an "as of T" query.
		at := f.ValidAt.Unix()
		b.WriteString(" AND " + col("t_valid_start") + " <= ?" +
			" AND (" + col("t_valid_end") + " IS NULL OR " + col("t_valid_end") + " > ?)")
		args = append(args, at, at)
	case !f.IncludeInvalid:
		b.WriteString(" AND " + col("t_valid_end") + " IS NULL")
	}
	// Entity-driven recall against globally-identifiable kinds bypasses
	// workspace scope — semantic facts about a person/task/skill follow
	// the entity, not the context. The handler decides eligibility (see
	// store.EntityRecallCanEscapeScope) and sets the flag; the SQL layer
	// just respects it. Defence in depth: we require at least one entity
	// filter to actually be present, so a stray flag can't expose the
	// global pool.
	hasEntityFilter := len(f.Entities) > 0 || len(f.EntitiesAny) > 0
	if !f.EntityDrivenIgnoresScope || !hasEntityFilter {
		clause, params := scopeFilteredWhereClauseAlias(f.Scope, alias, f.ScopeFilter)
		b.WriteString(clause)
		args = append(args, params...)
	}
	if f.Kind != "" {
		b.WriteString(" AND " + col("kind") + " = ?")
		args = append(args, f.Kind)
	}
	if f.Name != "" {
		b.WriteString(" AND " + col("name") + " = ?")
		args = append(args, f.Name)
	}
	if f.WorkerID != "" {
		b.WriteString(" AND " + col("worker_id") + " = ?")
		args = append(args, f.WorkerID)
	}
	if f.RunID != "" {
		b.WriteString(" AND " + col("run_id") + " = ?")
		args = append(args, f.RunID)
	}
	if f.UserID != "" {
		b.WriteString(" AND " + col("user_id") + " = ?")
		args = append(args, f.UserID)
	}
	if f.SourceKind != "" {
		b.WriteString(" AND " + col("source_kind") + " = ?")
		args = append(args, f.SourceKind)
	}
	if f.SourceSessionID != "" {
		b.WriteString(" AND " + col("source_session_id") + " = ?")
		args = append(args, f.SourceSessionID)
	}
	if f.OriginPeerID != "" {
		b.WriteString(" AND " + col("origin_peer_id") + " = ?")
		args = append(args, f.OriginPeerID)
	}
	if f.SinceUpdated != nil {
		b.WriteString(" AND " + col("updated_at") + " >= ?")
		args = append(args, f.SinceUpdated.Unix())
	}
	if clause, params := memoryTagsClause(f.Tags, col("tags_json")); clause != "" {
		b.WriteString(clause)
		args = append(args, params...)
	}
	memIDExpr := col("id")
	if alias == "" {
		// EXISTS subquery references memory_entities.id (its own column),
		// so the bare "id" would resolve to the wrong table inside the
		// subquery. Always qualify with the outer memories table.
		memIDExpr = "memories.id"
	}
	if clause, params := memoryEntityClause(f, memIDExpr); clause != "" {
		b.WriteString(clause)
		args = append(args, params...)
	}
	return b.String(), args
}

// memoryEntityClause composes f.Entities (AND) + f.EntitiesAny (OR) into
// EXISTS subqueries against memory_entities. memoryIDExpr is the SQL
// expression that references the memories row's id column (already
// alias-prefixed by the caller). Returns ("", nil) when neither filter
// is set.
//
// Implementation notes:
//   - Each Entities entry produces its own EXISTS — that's what gives us
//     AND semantics across links without forcing a self-join per entry.
//   - EntitiesAny collapses into ONE EXISTS whose subquery uses an OR-of-
//     triples WHERE — at-least-one match satisfies the row.
//   - Role is honoured when non-empty; empty role matches any role.
//   - Kind + ID are lower-cased on the way in to match the write-path
//     normalisation (see normalizeEntityRef).
func memoryEntityClause(f store.MemoryFilter, memoryIDExpr string) (string, []any) {
	var b strings.Builder
	var args []any
	for _, e := range f.Entities {
		kind := strings.ToLower(strings.TrimSpace(e.Kind))
		id := strings.ToLower(strings.TrimSpace(e.ID))
		role := strings.ToLower(strings.TrimSpace(e.Role))
		if kind == "" || id == "" {
			continue
		}
		b.WriteString(" AND EXISTS (SELECT 1 FROM memory_entities me_a WHERE me_a.memory_id = ")
		b.WriteString(memoryIDExpr)
		b.WriteString(" AND me_a.entity_kind = ? AND me_a.entity_id = ?")
		args = append(args, kind, id)
		if role != "" {
			b.WriteString(" AND me_a.role = ?")
			args = append(args, role)
		}
		b.WriteString(")")
	}
	anyTriples := make([]store.EntityRef, 0, len(f.EntitiesAny))
	for _, e := range f.EntitiesAny {
		kind := strings.ToLower(strings.TrimSpace(e.Kind))
		id := strings.ToLower(strings.TrimSpace(e.ID))
		role := strings.ToLower(strings.TrimSpace(e.Role))
		if kind == "" || id == "" {
			continue
		}
		anyTriples = append(anyTriples, store.EntityRef{Kind: kind, ID: id, Role: role})
	}
	if len(anyTriples) > 0 {
		b.WriteString(" AND EXISTS (SELECT 1 FROM memory_entities me_o WHERE me_o.memory_id = ")
		b.WriteString(memoryIDExpr)
		b.WriteString(" AND (")
		for i, e := range anyTriples {
			if i > 0 {
				b.WriteString(" OR ")
			}
			b.WriteString("(me_o.entity_kind = ? AND me_o.entity_id = ?")
			args = append(args, e.Kind, e.ID)
			if e.Role != "" {
				b.WriteString(" AND me_o.role = ?")
				args = append(args, e.Role)
			}
			b.WriteString(")")
		}
		b.WriteString("))")
	}
	return b.String(), args
}

// scopeFilteredWhereClauseAlias is the consolidator-aware variant of
// scopeWhereClauseAlias. When filter is "" the behaviour is identical
// (workspaces ∪ global). "global_only" excludes workspace-scoped rows
// even when the scope carries workspace ids. "workspace_only" excludes
// global rows; if no workspace ids are in scope it short-circuits to
// "0 = 1" (nothing matches) rather than silently widening.
func scopeFilteredWhereClauseAlias(scope store.SkillScope, alias, filter string) (string, []any) {
	if scope.IncludeAll {
		return "", nil
	}
	prefix := ""
	if alias != "" {
		prefix = alias + "."
	}
	switch filter {
	case "global_only":
		return " AND " + prefix + "workspace_id IS NULL", nil
	case "workspace_only":
		if len(scope.WorkspaceIDs) == 0 {
			return " AND 0 = 1", nil
		}
		placeholders := make([]string, len(scope.WorkspaceIDs))
		params := make([]any, 0, len(scope.WorkspaceIDs))
		for i, id := range scope.WorkspaceIDs {
			placeholders[i] = "?"
			params = append(params, id)
		}
		return " AND " + prefix + "workspace_id IN (" + strings.Join(placeholders, ",") + ")", params
	default:
		return scopeWhereClauseAlias(scope, alias)
	}
}

// scopeWhereClauseAlias mirrors scopeWhereClause but lets the caller
// prefix workspace_id with a table alias for JOIN-aware queries.
func scopeWhereClauseAlias(scope store.SkillScope, alias string) (string, []any) {
	prefix := ""
	if alias != "" {
		prefix = alias + "."
	}
	if scope.IncludeAll {
		return "", nil
	}
	if len(scope.WorkspaceIDs) == 0 {
		return " AND " + prefix + "workspace_id IS NULL", nil
	}
	placeholders := make([]string, len(scope.WorkspaceIDs))
	params := make([]any, 0, len(scope.WorkspaceIDs))
	for i, id := range scope.WorkspaceIDs {
		placeholders[i] = "?"
		params = append(params, id)
	}
	clause := " AND (" + prefix + "workspace_id IS NULL OR " +
		prefix + "workspace_id IN (" + strings.Join(placeholders, ",") + "))"
	return clause, params
}

// memoryTagsClause renders the tag filter as SQL: one EXISTS subquery
// over json_each(tags_json) per wanted tag (AND semantics), matched
// case-insensitively to mirror the historical Go-side comparison.
// Filtering in SQL — not in Go after the LIMIT — keeps pages full and
// matches findable past the limit. tagsCol is the (alias-prefixed)
// tags_json column expression. Empty want = no clause.
func memoryTagsClause(want []string, tagsCol string) (string, []any) {
	var b strings.Builder
	var args []any
	for _, t := range want {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" {
			continue
		}
		b.WriteString(" AND EXISTS (SELECT 1 FROM json_each(" + tagsCol +
			") WHERE lower(json_each.value) = ?)")
		args = append(args, t)
	}
	return b.String(), args
}

func memoryColsPrefixed(prefix string) string {
	cols := strings.Split(memorySelectCols, ",")
	for i, c := range cols {
		cols[i] = prefix + "." + strings.TrimSpace(c)
	}
	return strings.Join(cols, ", ")
}

func scanMemoryWithExtra(
	scan func(...any) error, extra ...any,
) (*store.MemoryEntry, error) {
	// Build a destination slice that matches scanMemory's pattern then
	// append the extra trailing column(s).
	var (
		e                                                     store.MemoryEntry
		tags, metadata                                        string
		wsID, userID, workerID, runID                         sql.NullString
		sourceSession, sourcePeer, sourceToolCall, originPeer sql.NullString
		embedModel, invalidatedBy                             sql.NullString
		tValidEnd, deletedAt                                  sql.NullInt64
		tValidStart, createdAt, updatedAt                     int64
		pinned                                                int
	)
	dest := []any{
		&e.ID, &e.Name, &e.Kind, &e.Content, &tags, &metadata,
		&wsID, &userID, &workerID, &runID,
		&e.SourceKind, &sourceSession, &sourcePeer,
		&sourceToolCall, &originPeer,
		&embedModel, &e.EmbedVersion,
		&tValidStart, &tValidEnd, &invalidatedBy,
		&pinned, &createdAt, &updatedAt, &deletedAt,
	}
	dest = append(dest, extra...)
	if err := scan(dest...); err != nil {
		return nil, err
	}
	e.TagsJSON = json.RawMessage(tags)
	e.MetadataJSON = json.RawMessage(metadata)
	if wsID.Valid {
		s := wsID.String
		e.WorkspaceID = &s
	}
	if userID.Valid {
		e.UserID = userID.String
	}
	if workerID.Valid {
		e.WorkerID = workerID.String
	}
	if runID.Valid {
		e.RunID = runID.String
	}
	if sourceSession.Valid {
		e.SourceSessionID = sourceSession.String
	}
	if sourcePeer.Valid {
		e.SourcePeerID = sourcePeer.String
	}
	if sourceToolCall.Valid {
		e.SourceToolCallID = sourceToolCall.String
	}
	if originPeer.Valid {
		e.OriginPeerID = originPeer.String
	}
	if embedModel.Valid {
		e.EmbedModel = embedModel.String
	}
	if invalidatedBy.Valid {
		e.InvalidatedBy = invalidatedBy.String
	}
	e.TValidStart = time.Unix(tValidStart, 0).UTC()
	if tValidEnd.Valid {
		t := time.Unix(tValidEnd.Int64, 0).UTC()
		e.TValidEnd = &t
	}
	e.Pinned = pinned != 0
	e.CreatedAt = time.Unix(createdAt, 0).UTC()
	e.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	if deletedAt.Valid {
		t := time.Unix(deletedAt.Int64, 0).UTC()
		e.DeletedAt = &t
	}
	return &e, nil
}

// sanitizeFTS5Query turns an arbitrary user / agent input into an FTS5
// expression that's safe to MATCH. Strategy: split on whitespace and
// FTS5 metacharacters, lower-case, drop empties, then quote every
// remaining term and OR them together. The unicode61 tokenizer + porter
// stemmer on the index side handle the matching, so we don't need to
// preserve phrase semantics — bag-of-words quoting is what BM25 ranks
// on anyway. Returns "" when the input has no usable terms.
//
// The split set mirrors FTS5's reserved punctuation:
//   - whitespace             — token separator (obvious)
//   - ':'                    — column-restrict operator ("col:term")
//   - '-' / '+'              — NOT / required (also kebab-case in tags)
//   - '(' / ')'              — grouping
//   - '"'                    — phrase quote
//   - '*'                    — prefix wildcard
//   - ',' / '[' / ']'        — bracket/list noise from raw tag JSON
//   - ';'                    — historical FTS3 ND operator
func sanitizeFTS5Query(q string) string {
	q = strings.ToLower(strings.TrimSpace(q))
	if q == "" {
		return ""
	}
	isSep := func(r rune) bool {
		switch r {
		case ' ', '\t', '\n', '\r',
			':', '-', '+', '(', ')', '"', '*',
			',', '[', ']', ';':
			return true
		}
		return false
	}
	parts := strings.FieldsFunc(q, isSep)
	if len(parts) == 0 {
		return ""
	}
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		out = append(out, `"`+p+`"`)
	}
	if len(out) == 0 {
		return ""
	}
	return strings.Join(out, " OR ")
}

// vectorToJSON encodes a float32 slice as a JSON array string suitable
// for sqlite-vec INSERT + MATCH. Single-precision is preserved with 7
// significant digits — sqlite-vec re-parses to float32 internally.
func vectorToJSON(v []float32) string {
	if len(v) == 0 {
		return "[]"
	}
	var b strings.Builder
	b.Grow(len(v) * 9)
	b.WriteByte('[')
	for i, f := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(f), 'g', 7, 32))
	}
	b.WriteByte(']')
	return b.String()
}

// memoryVecDim is the fixed dimension of the memories_vec vec0 column
// (FLOAT[1536], migration 058). A decoded binary embedding MUST have exactly
// this many float32 elements; anything else is a corrupt/foreign blob.
const memoryVecDim = 1536

// vectorFromBlob decodes the raw bytes sqlite-vec returns for a vec0
// FLOAT[N] column back into a []float32. A plain SELECT on a vec0 column
// ALWAYS yields a packed little-endian array of float32 (4 bytes each), so a
// well-formed blob length is a multiple of 4 AND length/4 equals the column
// dimension. We therefore decode binary UNCONDITIONALLY when len(raw)%4==0 —
// no content-byte heuristic, because the first byte of a packed float32 is the
// low byte of a mantissa and can legitimately be any value (including 0x5B,
// the ASCII '[' that a naive "looks like JSON" check would misclassify).
//
// The only resilience fallback is for the impossible-for-binary case where the
// length is NOT a multiple of 4: that can only be a JSON-text representation
// (e.g. a value produced via vec_to_json, or a foreign/legacy snapshot), so we
// attempt a JSON-array parse there.
func vectorFromBlob(raw []byte) ([]float32, error) {
	if len(raw) == 0 {
		return nil, errors.New("vectorFromBlob: empty embedding")
	}
	if len(raw)%4 == 0 {
		n := len(raw) / 4
		if n != memoryVecDim {
			return nil, fmt.Errorf(
				"vectorFromBlob: decoded %d float32 elements, want %d (memories_vec FLOAT[%d])",
				n, memoryVecDim, memoryVecDim)
		}
		out := make([]float32, n)
		for i := 0; i < n; i++ {
			bits := binary.LittleEndian.Uint32(raw[i*4 : i*4+4])
			out[i] = math.Float32frombits(bits)
		}
		return out, nil
	}
	// len(raw)%4 != 0 → not a packed float32 blob; try the JSON-text fallback.
	var arr []float32
	if err := json.Unmarshal(raw, &arr); err != nil {
		return nil, fmt.Errorf("vectorFromBlob: not a float blob or JSON array: %w", err)
	}
	return arr, nil
}
