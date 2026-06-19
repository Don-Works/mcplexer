// memory.go — SQLite implementation of store.MemoryStore writes
// (migration 058). Splits the surface across three files to honor the
// 300-line cap: memory.go (writes + deletes + counts), memory_query.go
// (reads + search + vector ops), memory_offer.go (peer offer CRUD).
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
	"github.com/oklog/ulid/v2"
)

// WriteMemory inserts a row. For Kind=fact, this is atomic invalidate-
// then-insert: any existing active row in the same (workspace, worker,
// name) bucket has its t_valid_end + invalidated_by stamped before the
// new row goes in. For Kind=note, a plain insert.
func (d *DB) WriteMemory(ctx context.Context, e *store.MemoryEntry) error {
	if e == nil {
		return errors.New("WriteMemory: nil entry")
	}
	if strings.TrimSpace(e.Name) == "" {
		return errors.New("WriteMemory: name required")
	}
	if strings.TrimSpace(e.Content) == "" {
		return errors.New("WriteMemory: content required")
	}
	if e.Kind == "" {
		e.Kind = store.MemoryKindNote
	}
	if e.Kind != store.MemoryKindFact && e.Kind != store.MemoryKindNote {
		return fmt.Errorf("WriteMemory: invalid kind %q", e.Kind)
	}
	if e.SourceKind == "" {
		e.SourceKind = store.MemorySourceAgent
	}
	if e.ID == "" {
		e.ID = ulid.Make().String()
	}
	now := time.Now().UTC()
	if e.CreatedAt.IsZero() {
		e.CreatedAt = now
	}
	if e.UpdatedAt.IsZero() {
		e.UpdatedAt = now
	}
	if e.TValidStart.IsZero() {
		e.TValidStart = now
	}

	return d.withTx(ctx, func(q queryable) error {
		if e.Kind == store.MemoryKindFact {
			if err := invalidateActiveFact(ctx, q, e, now); err != nil {
				return err
			}
		}
		return insertMemoryRow(ctx, q, e)
	})
}

// invalidateActiveFact finds and stamps t_valid_end on the existing
// active fact row that shares (workspace, worker, name) with the
// incoming entry. The new row becomes the active one; this row becomes
// historical (still queryable via IncludeInvalid=true).
func invalidateActiveFact(
	ctx context.Context, q queryable, e *store.MemoryEntry, now time.Time,
) error {
	_, wsClause, wsParams := workspaceClause(e.WorkspaceID)
	workerClause, workerArgs := workerArgClause(e.WorkerID)
	args := []any{now.Unix(), e.ID, now.Unix(), e.Name}
	args = append(args, wsParams...)
	args = append(args, workerArgs...)
	res, err := q.ExecContext(ctx, `
		UPDATE memories
		SET t_valid_end = ?, invalidated_by = ?, updated_at = ?
		WHERE kind = 'fact' AND deleted_at IS NULL AND t_valid_end IS NULL
		  AND name = ?`+wsClause+workerClause, args...)
	if err != nil {
		return fmt.Errorf("invalidate active fact: %w", err)
	}
	if rows, _ := res.RowsAffected(); rows > 1 {
		// Should never happen given the unique partial index, but
		// surface defensively.
		return fmt.Errorf("invalidate active fact: %d rows affected", rows)
	}
	return nil
}

// workerArgClause produces the equivalent of workspaceClause for the
// worker_id column. Empty string = NULL match.
func workerArgClause(workerID string) (string, []any) {
	if workerID == "" {
		return " AND worker_id IS NULL", nil
	}
	return " AND worker_id = ?", []any{workerID}
}

func insertMemoryRow(ctx context.Context, q queryable, e *store.MemoryEntry) error {
	tags := normalizeJSON(e.TagsJSON, "[]")
	metadata := normalizeJSON(e.MetadataJSON, "{}")
	var (
		wsID, userID, workerID, runID                     any
		sourceSession, sourcePeer, sourceToolCall, origin any
		embedModel                                        any
		tValidEnd                                         any
		invalidatedBy                                     any
	)
	if e.WorkspaceID != nil {
		wsID = *e.WorkspaceID
	}
	if e.UserID != "" {
		userID = e.UserID
	}
	if e.WorkerID != "" {
		workerID = e.WorkerID
	}
	if e.RunID != "" {
		runID = e.RunID
	}
	if e.SourceSessionID != "" {
		sourceSession = e.SourceSessionID
	}
	if e.SourcePeerID != "" {
		sourcePeer = e.SourcePeerID
	}
	if e.SourceToolCallID != "" {
		sourceToolCall = e.SourceToolCallID
	}
	if e.OriginPeerID != "" {
		origin = e.OriginPeerID
	}
	if e.EmbedModel != "" {
		embedModel = e.EmbedModel
	}
	if e.TValidEnd != nil {
		tValidEnd = e.TValidEnd.Unix()
	}
	if e.InvalidatedBy != "" {
		invalidatedBy = e.InvalidatedBy
	}
	pinned := 0
	if e.Pinned {
		pinned = 1
	}
	_, err := q.ExecContext(ctx, `
		INSERT INTO memories (
			id, name, kind, content, tags_json, metadata_json,
			workspace_id, user_id, worker_id, run_id,
			source_kind, source_session_id, source_peer_id,
			source_tool_call_id, origin_peer_id,
			embed_model, embed_version,
			t_valid_start, t_valid_end, invalidated_by,
			pinned, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.ID, e.Name, e.Kind, e.Content, tags, metadata,
		wsID, userID, workerID, runID,
		e.SourceKind, sourceSession, sourcePeer, sourceToolCall, origin,
		embedModel, e.EmbedVersion,
		e.TValidStart.Unix(), tValidEnd, invalidatedBy,
		pinned, e.CreatedAt.Unix(), e.UpdatedAt.Unix(),
	)
	if err != nil {
		return fmt.Errorf("insert memory: %w", mapConstraintError(err))
	}
	return nil
}

// UpdateMemory rewrites the human-editable fields of an existing row in
// place (name, content, tags, kind, pinned, source, bi-temporal validity,
// workspace, updated_at). The memories_au FTS trigger fires. ErrNotFound
// when the row is absent or soft-deleted.
//
// Stale-vector correctness: when the content actually CHANGES, the stored
// embedding in memories_vec no longer describes the text, so KNN would
// surface this row against the OLD meaning. We therefore drop the
// memories_vec row and clear embed_model/embed_version in the same
// transaction — KNN simply skips the row (no vector) until a re-embed
// runs, and recall degrades to FTS5 for it (which always tracks the live
// text via the memories_au trigger). The whole update + vector clear is
// one transaction so a row never sits with fresh text + a stale vector.
func (d *DB) UpdateMemory(ctx context.Context, e *store.MemoryEntry) error {
	if e == nil {
		return errors.New("UpdateMemory: nil entry")
	}
	if strings.TrimSpace(e.ID) == "" {
		return errors.New("UpdateMemory: id required")
	}
	if strings.TrimSpace(e.Content) == "" {
		return errors.New("UpdateMemory: content required")
	}
	tags := normalizeJSON(e.TagsJSON, "[]")
	metadata := normalizeJSON(e.MetadataJSON, "{}")
	var wsID, tValidEnd, invalidatedBy any
	if e.WorkspaceID != nil {
		wsID = *e.WorkspaceID
	}
	if e.TValidEnd != nil {
		tValidEnd = e.TValidEnd.Unix()
	}
	if e.InvalidatedBy != "" {
		invalidatedBy = e.InvalidatedBy
	}
	pinned := 0
	if e.Pinned {
		pinned = 1
	}
	now := time.Now().UTC()
	if e.UpdatedAt.IsZero() {
		e.UpdatedAt = now
	}
	tValidStart := e.TValidStart
	if tValidStart.IsZero() {
		tValidStart = now
	}
	return d.withTx(ctx, func(q queryable) error {
		// Detect a content change before the UPDATE so we know whether the
		// stored embedding has gone stale. A missing row maps to ErrNotFound
		// (same as the UPDATE affecting zero rows below).
		var prior string
		switch err := q.QueryRowContext(ctx,
			`SELECT content FROM memories WHERE id = ? AND deleted_at IS NULL`,
			e.ID).Scan(&prior); {
		case errors.Is(err, sql.ErrNoRows):
			return store.ErrNotFound
		case err != nil:
			return fmt.Errorf("update memory: load prior content: %w", err)
		}
		res, err := q.ExecContext(ctx, `
			UPDATE memories SET
				name = ?, kind = ?, content = ?, tags_json = ?, metadata_json = ?,
				workspace_id = ?, pinned = ?,
				source_kind = ?, t_valid_start = ?, t_valid_end = ?,
				invalidated_by = ?, updated_at = ?
			WHERE id = ? AND deleted_at IS NULL`,
			e.Name, e.Kind, e.Content, tags, metadata,
			wsID, pinned,
			e.SourceKind, tValidStart.Unix(), tValidEnd,
			invalidatedBy, e.UpdatedAt.Unix(),
			e.ID,
		)
		if err != nil {
			return fmt.Errorf("update memory: %w", mapConstraintError(err))
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return store.ErrNotFound
		}
		if prior == e.Content {
			return nil
		}
		return retireStaleEmbedding(ctx, q, e)
	})
}

// retireStaleEmbedding drops the memories_vec row and clears the
// embed_model/embed_version pointer for a memory whose content just changed,
// so KNN can't surface the row against its now-stale vector. After this runs,
// the row is dropped from vector/KNN search and is recallable via FTS5 ONLY.
// NOTE: there is no automatic re-embed — no code path re-embeds the row after
// an UpdateMemory content edit, so it stays FTS5-only until a re-embed is
// triggered by a higher layer (a separate follow-up; not yet wired). Do not
// read this as a promise that the vector self-heals. The in-memory entry is
// kept consistent so a caller re-reading e doesn't believe a vector still
// exists. Runs inside UpdateMemory's transaction.
func retireStaleEmbedding(ctx context.Context, q queryable, e *store.MemoryEntry) error {
	if _, err := q.ExecContext(ctx,
		`DELETE FROM memories_vec WHERE memory_id = ?`, e.ID); err != nil {
		return fmt.Errorf("update memory: drop stale vector: %w", err)
	}
	if _, err := q.ExecContext(ctx,
		`UPDATE memories SET embed_model = NULL, embed_version = 0 WHERE id = ?`,
		e.ID); err != nil {
		return fmt.Errorf("update memory: clear embed pointer: %w", err)
	}
	e.EmbedModel = ""
	e.EmbedVersion = 0
	return nil
}

// GetMemory returns one row by id. Excludes soft-deleted rows.
func (d *DB) GetMemory(ctx context.Context, id string) (*store.MemoryEntry, error) {
	row := d.q.QueryRowContext(ctx, `
		SELECT `+memorySelectCols+`
		FROM memories
		WHERE id = ? AND deleted_at IS NULL`, id)
	e, err := scanMemory(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get memory: %w", err)
	}
	return e, nil
}

// GetMemoryForPeer is the cross-peer scope-aware variant of GetMemory.
// Returns the row ONLY when its workspace_id falls within
// allowedWorkspaceIDs — global rows (workspace_id IS NULL) are returned
// when allowGlobal=true, workspace-scoped rows are returned only when
// the explicit id is in the allowlist.
//
// Why it lives in the store: the bug description (JTAC65) requires the
// scope filter to run in SQL, NOT in Go after fetching. The earlier
// implementation (`GetMemory` then check in Go) had two side-channels:
// scanMemory ran to completion (timing leak based on row size), and the
// fetched MemoryEntry briefly sat in process memory where a debug log
// could disclose it. Pushing the check into the WHERE clause means the
// un-granted row never crosses the SQL/Go boundary.
//
// On no-match this returns store.ErrNotFound (the SAME sentinel as a
// genuinely missing id) so the cross-peer handler can map both to the
// constant-shape deny envelope without distinguishing causes on the
// wire. The local audit row preserves the original cause.
//
// allowedWorkspaceIDs may be empty; allowGlobal=false + empty allowlist
// = effectively "no rows match" (returns ErrNotFound).
func (d *DB) GetMemoryForPeer(
	ctx context.Context, id string,
	allowedWorkspaceIDs []string, allowGlobal bool,
) (*store.MemoryEntry, error) {
	// Build the workspace clause inline so the un-granted rows are
	// excluded by the planner — not loaded then filtered. A scope vector
	// of (allowGlobal=false, len(allowed)=0) short-circuits to ErrNotFound
	// without a query, defending against a degenerate caller.
	if !allowGlobal && len(allowedWorkspaceIDs) == 0 {
		return nil, store.ErrNotFound
	}
	clauses := make([]string, 0, 2)
	args := make([]any, 0, 1+len(allowedWorkspaceIDs))
	args = append(args, id)
	if allowGlobal {
		clauses = append(clauses, "workspace_id IS NULL")
	}
	if len(allowedWorkspaceIDs) > 0 {
		placeholders := make([]string, len(allowedWorkspaceIDs))
		for i, ws := range allowedWorkspaceIDs {
			placeholders[i] = "?"
			args = append(args, ws)
		}
		clauses = append(clauses,
			"workspace_id IN ("+strings.Join(placeholders, ",")+")")
	}
	scopeClause := strings.Join(clauses, " OR ")
	row := d.q.QueryRowContext(ctx, `
		SELECT `+memorySelectCols+`
		FROM memories
		WHERE id = ?
		  AND deleted_at IS NULL
		  AND ( `+scopeClause+` )`, args...)
	e, err := scanMemory(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get memory for peer: %w", err)
	}
	return e, nil
}

// InvalidateMemory stamps t_valid_end + invalidated_by on the row.
// Does NOT soft-delete — the row stays visible to history queries.
func (d *DB) InvalidateMemory(ctx context.Context, id, supersededByID string) error {
	now := time.Now().Unix()
	res, err := d.q.ExecContext(ctx, `
		UPDATE memories
		SET t_valid_end = ?, invalidated_by = ?, updated_at = ?
		WHERE id = ? AND deleted_at IS NULL AND t_valid_end IS NULL`,
		now, supersededByID, now, id)
	if err != nil {
		return fmt.Errorf("invalidate memory: %w", err)
	}
	return checkRowsAffected(res)
}

// SoftDeleteMemory stamps deleted_at on the row + drops the matching
// memories_vec row so KNN doesn't surface it.
func (d *DB) SoftDeleteMemory(ctx context.Context, id string) error {
	now := time.Now().Unix()
	return d.withTx(ctx, func(q queryable) error {
		res, err := q.ExecContext(ctx, `
			UPDATE memories SET deleted_at = ?, updated_at = ?
			WHERE id = ? AND deleted_at IS NULL`, now, now, id)
		if err != nil {
			return fmt.Errorf("soft delete memory: %w", err)
		}
		if rows, _ := res.RowsAffected(); rows == 0 {
			return store.ErrNotFound
		}
		// Drop the vector row too. Best-effort — absent rows are fine.
		_, _ = q.ExecContext(ctx,
			`DELETE FROM memories_vec WHERE memory_id = ?`, id)
		return nil
	})
}

// SetMemoryPinned flips the pinned flag. Idempotent.
func (d *DB) SetMemoryPinned(ctx context.Context, id string, pinned bool) error {
	p := 0
	if pinned {
		p = 1
	}
	res, err := d.q.ExecContext(ctx, `
		UPDATE memories SET pinned = ?, updated_at = ?
		WHERE id = ? AND deleted_at IS NULL`,
		p, time.Now().Unix(), id)
	if err != nil {
		return fmt.Errorf("set pinned: %w", err)
	}
	return checkRowsAffected(res)
}

// ForgetMemoryBySource hard-deletes every row whose source_session_id
// matches inside scope. Drops the matching memories_vec rows too. Returns the
// count.
func (d *DB) ForgetMemoryBySource(
	ctx context.Context, sourceSessionID string, scope store.SkillScope,
) (int, error) {
	if sourceSessionID == "" {
		return 0, errors.New("ForgetMemoryBySource: empty session id")
	}
	scopeClause, scopeParams := scopeWhereClause(scope)
	var n int
	err := d.withTx(ctx, func(q queryable) error {
		vecArgs := append([]any{sourceSessionID}, scopeParams...)
		_, err := q.ExecContext(ctx, `
			DELETE FROM memories_vec
			WHERE memory_id IN (
				SELECT id FROM memories WHERE source_session_id = ?`+scopeClause+`
			)`, vecArgs...)
		if err != nil {
			return fmt.Errorf("forget vec rows: %w", err)
		}
		memArgs := append([]any{sourceSessionID}, scopeParams...)
		res, err := q.ExecContext(ctx,
			`DELETE FROM memories WHERE source_session_id = ?`+scopeClause,
			memArgs...)
		if err != nil {
			return fmt.Errorf("forget memory rows: %w", err)
		}
		rows, _ := res.RowsAffected()
		n = int(rows)
		return nil
	})
	return n, err
}

// CountMemories returns counts by kind for the given scope. Used by the
// dashboard vitals card.
func (d *DB) CountMemories(
	ctx context.Context, scope store.SkillScope,
) (int, int, error) {
	clause, params := scopeWhereClause(scope)
	q := `
		SELECT
			COALESCE(SUM(CASE WHEN kind='fact' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN kind='note' THEN 1 ELSE 0 END), 0)
		FROM memories
		WHERE deleted_at IS NULL ` + clause
	row := d.q.QueryRowContext(ctx, q, params...)
	var facts, notes int
	if err := row.Scan(&facts, &notes); err != nil {
		return 0, 0, fmt.Errorf("count memories: %w", err)
	}
	return facts, notes, nil
}

const memorySelectCols = `id, name, kind, content, tags_json, metadata_json,
		workspace_id, user_id, worker_id, run_id,
		source_kind, source_session_id, source_peer_id,
		source_tool_call_id, origin_peer_id,
		embed_model, embed_version,
		t_valid_start, t_valid_end, invalidated_by,
		pinned, created_at, updated_at, deleted_at`

func scanMemory(scan func(...any) error) (*store.MemoryEntry, error) {
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
	if err := scan(
		&e.ID, &e.Name, &e.Kind, &e.Content, &tags, &metadata,
		&wsID, &userID, &workerID, &runID,
		&e.SourceKind, &sourceSession, &sourcePeer,
		&sourceToolCall, &originPeer,
		&embedModel, &e.EmbedVersion,
		&tValidStart, &tValidEnd, &invalidatedBy,
		&pinned, &createdAt, &updatedAt, &deletedAt,
	); err != nil {
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
