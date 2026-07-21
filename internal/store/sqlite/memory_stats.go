// memory_stats.go — aggregate "shape of the brain" queries powering the
// /api/v1/memory/stats endpoint. Everything is computed in SQL — we
// deliberately never load the full memory set into Go just to count.
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// GetMemoryStats assembles the MemoryStats snapshot in one logical pass.
// Each sub-query honors `scope`; failures bubble up so the handler can
// surface them. The implementation favors clarity over raw cycles — this
// endpoint is called once on page load, not in a hot loop.
func (d *DB) GetMemoryStats(
	ctx context.Context, scope store.SkillScope,
) (store.MemoryStats, error) {
	clause, params := scopeWhereClause(scope)
	now := time.Now().UTC()
	stats := store.MemoryStats{
		TypeMix:         map[string]int{},
		WritesPerDay30d: []store.MemoryDailyCount{},
		TopTags:         []store.MemoryTagCount{},
	}
	if err := d.fetchMemoryTotals(ctx, clause, params, &stats); err != nil {
		return stats, err
	}
	if err := d.fetchMemoryTypeMix(ctx, clause, params, &stats); err != nil {
		return stats, err
	}
	if err := d.fetchMemoryRecency(ctx, clause, params, now, &stats); err != nil {
		return stats, err
	}
	if err := d.fetchMemoryDailyWrites(ctx, clause, params, now, &stats); err != nil {
		return stats, err
	}
	if err := d.fetchMemoryNetworkReach(ctx, clause, params, &stats); err != nil {
		return stats, err
	}
	if err := d.fetchMemoryTopTags(ctx, clause, params, &stats); err != nil {
		return stats, err
	}
	// Decay pressure + recall rate: driven by the recall-event log
	// (migration 077) when populated; both fall back gracefully (decay to
	// the updated_at heuristic, recall rate to 0) when tracking is off.
	// Probe the recall log ONCE here and pass the result into both fetchers
	// (each used to probe it independently — a redundant query per call).
	logEmpty, err := d.recallLogIsEmpty(ctx)
	if err != nil {
		return stats, err
	}
	if err := d.fetchMemoryDecayPressure(ctx, clause, params, now, logEmpty, &stats); err != nil {
		return stats, err
	}
	if err := d.fetchMemoryRecallRate7d(ctx, clause, params, now, logEmpty, &stats); err != nil {
		return stats, err
	}
	stats.PagesEquivalent = float64(stats.TotalBytes) / 500.0
	return stats, nil
}

func (d *DB) fetchMemoryTotals(
	ctx context.Context, clause string, params []any, out *store.MemoryStats,
) error {
	q := `
		SELECT COUNT(*), COALESCE(SUM(LENGTH(content)), 0), MIN(created_at)
		FROM memories
		WHERE deleted_at IS NULL ` + clause
	row := d.q.QueryRowContext(ctx, q, params...)
	var (
		total, bytes int64
		minCreated   sql.NullInt64
	)
	if err := row.Scan(&total, &bytes, &minCreated); err != nil {
		return fmt.Errorf("memory totals: %w", err)
	}
	out.TotalMemories = int(total)
	out.TotalBytes = bytes
	if minCreated.Valid {
		t := time.Unix(minCreated.Int64, 0).UTC()
		out.BrainAgeBornAt = &t
		days := int(time.Since(t).Hours() / 24)
		if days < 0 {
			days = 0
		}
		out.BrainAgeDays = days
	}
	return nil
}

func (d *DB) fetchMemoryTypeMix(
	ctx context.Context, clause string, params []any, out *store.MemoryStats,
) error {
	q := `
		SELECT kind, COUNT(*)
		FROM memories
		WHERE deleted_at IS NULL ` + clause + `
		GROUP BY kind`
	rows, err := d.q.QueryContext(ctx, q, params...)
	if err != nil {
		return fmt.Errorf("memory type mix: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	for rows.Next() {
		var (
			kind string
			n    int
		)
		if err := rows.Scan(&kind, &n); err != nil {
			return fmt.Errorf("scan type mix: %w", err)
		}
		out.TypeMix[kind] = n
	}
	return rows.Err()
}

func (d *DB) fetchMemoryRecency(
	ctx context.Context, clause string, params []any,
	now time.Time, out *store.MemoryStats,
) error {
	day := int64(86400)
	t7 := now.Unix() - 7*day
	t30 := now.Unix() - 30*day
	t180 := now.Unix() - 180*day
	q := `
		SELECT
			SUM(CASE WHEN updated_at >= ?              THEN 1 ELSE 0 END) AS fresh,
			SUM(CASE WHEN updated_at <  ? AND updated_at >= ? THEN 1 ELSE 0 END) AS warm,
			SUM(CASE WHEN updated_at <  ? AND updated_at >= ? THEN 1 ELSE 0 END) AS cold,
			SUM(CASE WHEN updated_at <  ?              THEN 1 ELSE 0 END) AS dormant
		FROM memories
		WHERE deleted_at IS NULL ` + clause
	args := []any{t7, t7, t30, t30, t180, t180}
	args = append(args, params...)
	row := d.q.QueryRowContext(ctx, q, args...)
	var fresh, warm, cold, dormant sql.NullInt64
	if err := row.Scan(&fresh, &warm, &cold, &dormant); err != nil {
		return fmt.Errorf("memory recency buckets: %w", err)
	}
	out.RecencyBuckets = store.MemoryRecencyBuckets{
		Fresh:   int(fresh.Int64),
		Warm:    int(warm.Int64),
		Cold:    int(cold.Int64),
		Dormant: int(dormant.Int64),
	}
	return nil
}

func (d *DB) fetchMemoryDailyWrites(
	ctx context.Context, clause string, params []any,
	now time.Time, out *store.MemoryStats,
) error {
	// 30-day window anchored on UTC midnight.
	end := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	start := end.AddDate(0, 0, -29)
	q := `
		SELECT
			strftime('%Y-%m-%d', datetime(created_at, 'unixepoch')) AS day,
			COUNT(*)
		FROM memories
		WHERE deleted_at IS NULL
		  AND created_at >= ? ` + clause + `
		GROUP BY day
		ORDER BY day ASC`
	args := []any{start.Unix()}
	args = append(args, params...)
	rows, err := d.q.QueryContext(ctx, q, args...)
	if err != nil {
		return fmt.Errorf("memory daily writes: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	byDay := map[string]int{}
	for rows.Next() {
		var (
			day string
			n   int
		)
		if err := rows.Scan(&day, &n); err != nil {
			return fmt.Errorf("scan daily writes: %w", err)
		}
		byDay[day] = n
	}
	if err := rows.Err(); err != nil {
		return err
	}
	// Backfill zero buckets so the sparkline has a contiguous 30-day
	// shape regardless of write density.
	series := make([]store.MemoryDailyCount, 0, 30)
	for i := 0; i < 30; i++ {
		d := start.AddDate(0, 0, i)
		key := d.Format("2006-01-02")
		series = append(series, store.MemoryDailyCount{Date: key, Count: byDay[key]})
	}
	out.WritesPerDay30d = series
	return nil
}

func (d *DB) fetchMemoryNetworkReach(
	ctx context.Context, clause string, params []any, out *store.MemoryStats,
) error {
	q := `
		SELECT COUNT(*), COUNT(DISTINCT origin_peer_id)
		FROM memories
		WHERE deleted_at IS NULL
		  AND origin_peer_id IS NOT NULL
		  AND origin_peer_id <> '' ` + clause
	row := d.q.QueryRowContext(ctx, q, params...)
	var shared, peers int
	if err := row.Scan(&shared, &peers); err != nil {
		return fmt.Errorf("memory network reach: %w", err)
	}
	out.NetworkReach = store.MemoryNetworkReach{
		SharedMemoryCount: shared,
		PeerCount:         peers,
	}
	return nil
}

func (d *DB) fetchMemoryTopTags(
	ctx context.Context, clause string, params []any, out *store.MemoryStats,
) error {
	// We only pull tags_json — never the heavy content column. SQLite's
	// JSON1 has no "explode-array-and-group" aggregator without a virtual
	// table, so we sum in Go after the read.
	q := `
		SELECT tags_json
		FROM memories
		WHERE deleted_at IS NULL
		  AND tags_json IS NOT NULL
		  AND tags_json <> '[]'
		  AND tags_json <> '' ` + clause
	rows, err := d.q.QueryContext(ctx, q, params...)
	if err != nil {
		return fmt.Errorf("memory top tags: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	counts := map[string]int{}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return fmt.Errorf("scan tags row: %w", err)
		}
		var tags []string
		if err := json.Unmarshal([]byte(raw), &tags); err != nil {
			continue // tolerate malformed; skip the row
		}
		for _, t := range tags {
			t = strings.TrimSpace(t)
			if t == "" {
				continue
			}
			counts[strings.ToLower(t)]++
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	pairs := make([]store.MemoryTagCount, 0, len(counts))
	for t, n := range counts {
		pairs = append(pairs, store.MemoryTagCount{Tag: t, Count: n})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].Count != pairs[j].Count {
			return pairs[i].Count > pairs[j].Count
		}
		return pairs[i].Tag < pairs[j].Tag
	})
	if len(pairs) > 5 {
		pairs = pairs[:5]
	}
	out.TopTags = pairs
	return nil
}

// recallLogIsEmpty reports whether the recall-event log has any rows at
// all. Recall tracking is opt-in (migration 077), so on most installs the
// table is empty and the decay/recall stats must fall back to the
// updated_at heuristic rather than reporting that EVERY memory is decaying
// (which would be the naive result of a recall LEFT JOIN against an empty
// log). GetMemoryStats runs this one cheap EXISTS probe ONCE and hands the
// result to both decay-pressure and recall-rate fetchers, so the choice of
// path is made with a single query per stats call.
func (d *DB) recallLogIsEmpty(ctx context.Context) (bool, error) {
	var present int
	err := d.q.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM memory_recall_events)`).Scan(&present)
	if err != nil {
		return false, fmt.Errorf("recall log probe: %w", err)
	}
	return present == 0, nil
}

// fetchMemoryDecayPressure counts memories under decay pressure. When the
// recall log carries data, decay = old (updated_at < 180d) AND not pinned
// AND still valid AND NOT recalled within the last 30 days — an honest
// "stale and nobody's looked at it" signal. When the log is empty the
// recall predicate can't say anything, so we fall back to the historical
// updated_at-only heuristic. The last-recall lookup is a correlated
// subquery (MAX(created_at) over memory_recall_events for the row) so no
// table alias is needed and the existing scope clause drops in unchanged.
func (d *DB) fetchMemoryDecayPressure(
	ctx context.Context, clause string, params []any,
	now time.Time, logEmpty bool, out *store.MemoryStats,
) error {
	oldThreshold := now.Unix() - int64(180*86400)
	if logEmpty {
		q := `
			SELECT COUNT(*)
			FROM memories
			WHERE deleted_at IS NULL
			  AND t_valid_end IS NULL
			  AND pinned = 0
			  AND updated_at < ? ` + clause
		args := append([]any{oldThreshold}, params...)
		var n int
		if err := d.q.QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
			return fmt.Errorf("memory decay pressure (fallback): %w", err)
		}
		out.DecayPressure = n
		return nil
	}
	recallThreshold := now.Unix() - int64(30*86400)
	q := `
		SELECT COUNT(*)
		FROM memories
		WHERE deleted_at IS NULL
		  AND t_valid_end IS NULL
		  AND pinned = 0
		  AND updated_at < ?
		  AND COALESCE((
		      SELECT MAX(re.created_at)
		      FROM memory_recall_events re
		      WHERE re.memory_id = memories.id
		  ), 0) < ? ` + clause
	args := append([]any{oldThreshold, recallThreshold}, params...)
	var n int
	if err := d.q.QueryRowContext(ctx, q, args...).Scan(&n); err != nil {
		return fmt.Errorf("memory decay pressure: %w", err)
	}
	out.DecayPressure = n
	return nil
}

// fetchMemoryRecallRate7d computes the share of in-scope, valid, non-
// deleted memories that surfaced in at least one recall event in the last
// 7 days. Returns 0 when there are no qualifying memories or the recall
// log is empty (tracking disabled) — never errors the whole stats call on
// a missing log. denominator = valid non-deleted memories in scope;
// numerator = those with a recall event newer than the 7-day cutoff.
func (d *DB) fetchMemoryRecallRate7d(
	ctx context.Context, clause string, params []any,
	now time.Time, logEmpty bool, out *store.MemoryStats,
) error {
	if logEmpty {
		out.RecallRate7d = 0
		return nil
	}
	cutoff := now.Unix() - int64(7*86400)
	q := `
		SELECT
			COUNT(*) AS total,
			SUM(CASE WHEN EXISTS(
				SELECT 1 FROM memory_recall_events re
				WHERE re.memory_id = memories.id
				  AND re.created_at >= ?
			) THEN 1 ELSE 0 END) AS recalled
		FROM memories
		WHERE deleted_at IS NULL
		  AND t_valid_end IS NULL ` + clause
	args := append([]any{cutoff}, params...)
	var total int
	var recalled sql.NullInt64
	if err := d.q.QueryRowContext(ctx, q, args...).Scan(&total, &recalled); err != nil {
		return fmt.Errorf("memory recall rate: %w", err)
	}
	if total == 0 {
		out.RecallRate7d = 0
		return nil
	}
	out.RecallRate7d = float64(recalled.Int64) / float64(total)
	return nil
}
