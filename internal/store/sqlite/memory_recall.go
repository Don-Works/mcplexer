// memory_recall.go — recall-event log + co-recall aggregation
// (AR4 from the wave-3 epic). See migration 077 for the schema header
// and the anti-noise posture (opt-in via env var, async writes, top-K
// only, etc — those policies live at the service layer; this file is
// the dumb persistence + read aggregation surface).
package sqlite

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/oklog/ulid/v2"
)

// LogMemoryRecallEvents writes a batch of recall events in one
// transaction. IDs are auto-generated when blank. Idempotent on (id) —
// duplicate IDs are no-ops so the caller can replay without
// double-counting.
func (d *DB) LogMemoryRecallEvents(
	ctx context.Context, events []store.MemoryRecallEvent,
) error {
	if len(events) == 0 {
		return nil
	}
	return d.withTx(ctx, func(q queryable) error {
		for i := range events {
			e := &events[i]
			if e.ID == "" {
				e.ID = ulid.Make().String()
			}
			ts := e.CreatedAt
			if ts.IsZero() {
				ts = time.Now().UTC()
				e.CreatedAt = ts
			}
			source := e.Source
			if source == "" {
				source = "rrf"
			}
			_, err := q.ExecContext(ctx, `
				INSERT INTO memory_recall_events(
					id, memory_id, session_id, workspace_id,
					query, entity_filter, rank_position,
					result_set_id, source, created_at
				) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT(id) DO NOTHING`,
				e.ID, e.MemoryID, nullString(e.SessionID), nullString(e.WorkspaceID),
				e.Query, e.EntityFilter, e.RankPosition,
				e.ResultSetID, source, ts.Unix(),
			)
			if err != nil {
				return fmt.Errorf("insert recall event: %w", err)
			}
		}
		return nil
	})
}

// CoRecalledMemories returns memories that frequently co-surface with
// the named memory in the recall log. Score formula:
//
//	score = SUM over shared result_set_ids of (1 / (1 + rank_distance))
//
// where rank_distance is the absolute gap between the query memory's
// rank and the candidate's rank in that result set. Two memories that
// always surface in adjacent positions score higher than two that share
// the long tail of a noisy recall.
//
// Excludes self and respects the standard scope clause via a join on
// the underlying memories row (so a deleted memory's co-recall history
// also disappears).
func (d *DB) CoRecalledMemories(
	ctx context.Context, memoryID string, scope store.SkillScope, limit int,
) ([]store.CoRecalledMemory, error) {
	if strings.TrimSpace(memoryID) == "" {
		return nil, fmt.Errorf("CoRecalledMemories: memoryID required")
	}
	if limit <= 0 {
		limit = 10
	}
	scopeClause, scopeArgs := scopeWhereClauseAlias(scope, "m")
	args := []any{memoryID, memoryID}
	args = append(args, scopeArgs...)
	args = append(args, limit)
	q := `
		WITH our_events AS (
			SELECT result_set_id, rank_position
			FROM memory_recall_events
			WHERE memory_id = ?
		)
		SELECT
		    other.memory_id,
		    m.name,
		    COUNT(DISTINCT other.result_set_id) AS co_occurrences,
		    SUM(1.0 / (1.0 + ABS(other.rank_position - our.rank_position))) AS score,
		    MAX(other.created_at) AS last_seen
		FROM memory_recall_events other
		JOIN our_events our ON our.result_set_id = other.result_set_id
		JOIN memories m ON m.id = other.memory_id
		WHERE other.memory_id != ?
		  AND m.deleted_at IS NULL
		  AND m.t_valid_end IS NULL` +
		scopeClause + `
		GROUP BY other.memory_id, m.name
		ORDER BY score DESC, co_occurrences DESC, last_seen DESC
		LIMIT ?`
	rows, err := d.q.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("co-recalled memories: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	var out []store.CoRecalledMemory
	for rows.Next() {
		var r store.CoRecalledMemory
		var lastSeen int64
		if err := rows.Scan(&r.MemoryID, &r.Name, &r.CoOccurrences, &r.Score, &lastSeen); err != nil {
			return nil, fmt.Errorf("scan co-recall: %w", err)
		}
		r.LastSeenAt = time.Unix(lastSeen, 0).UTC()
		out = append(out, r)
	}
	return out, rows.Err()
}

// recallStatsWindow is the recency window GetMemoryRecallStats counts
// recall activity over. Events older than this don't contribute to the
// recall-frequency nudge — recall pressure should be RECENT to matter,
// mirroring the "fire together, wire together" intent of AR4. Seven days
// keeps the term responsive to a working set without letting a one-off
// burst from weeks ago dominate.
const recallStatsWindow = 7 * 24 * time.Hour

// GetMemoryRecallStats returns the per-memory recall aggregate for the
// given ids in ONE grouped query (never N round-trips). RecentCount is the
// number of DISTINCT recall result sets the memory surfaced in within
// recallStatsWindow; LastRecalledAt is the most-recent surfacing. ids
// absent from the log are simply absent from the result map — the caller
// reads a missing entry as zero recall, degrading the ranking nudge to a
// no-op. An empty ids slice returns an empty map without touching the DB.
func (d *DB) GetMemoryRecallStats(
	ctx context.Context, ids []string,
) (map[string]store.MemoryRecallStat, error) {
	out := make(map[string]store.MemoryRecallStat, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, 0, len(ids)+1)
	for i, id := range ids {
		placeholders[i] = "?"
		args = append(args, id)
	}
	cutoff := time.Now().Add(-recallStatsWindow).Unix()
	args = append(args, cutoff)
	q := `
		SELECT memory_id,
		       COUNT(DISTINCT result_set_id) AS recent_count,
		       MAX(created_at) AS last_recalled
		FROM memory_recall_events
		WHERE memory_id IN (` + strings.Join(placeholders, ",") + `)
		  AND created_at >= ?
		GROUP BY memory_id`
	rows, err := d.q.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("memory recall stats: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	for rows.Next() {
		var (
			id    string
			count int
			last  int64
		)
		if err := rows.Scan(&id, &count, &last); err != nil {
			return nil, fmt.Errorf("scan recall stat: %w", err)
		}
		out[id] = store.MemoryRecallStat{
			MemoryID:       id,
			RecentCount:    count,
			LastRecalledAt: time.Unix(last, 0).UTC(),
		}
	}
	return out, rows.Err()
}

// ForgetRecallEventsBySource purges every event whose session_id matches
// inside scope. Used by the forensic redaction path so a poisoned session's
// recall trail is excised together with its memories.
func (d *DB) ForgetRecallEventsBySource(
	ctx context.Context, sessionID string, scope store.SkillScope,
) (int, error) {
	if strings.TrimSpace(sessionID) == "" {
		return 0, fmt.Errorf("ForgetRecallEventsBySource: sessionID required")
	}
	scopeClause, scopeParams := scopeWhereClause(scope)
	args := append([]any{sessionID}, scopeParams...)
	res, err := d.q.ExecContext(ctx,
		`DELETE FROM memory_recall_events WHERE session_id = ?`+scopeClause,
		args...)
	if err != nil {
		return 0, fmt.Errorf("forget recall events: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}
