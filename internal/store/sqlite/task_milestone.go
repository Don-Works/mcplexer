// task_milestone.go — milestone burndown aggregation. A milestone is
// convention-only: any task carrying the `milestone` tag with `due_at`
// set. Children are discovered by parsing the parent's meta.composes
// frontmatter line. No new column / migration.
package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// ListMilestonesWithBurndown returns each milestone-tagged epic in
// workspaceID with its children rollup and per-day burndown series.
// Ordered by due_at ASC (soonest first). See MilestoneBurndown.
func (d *DB) ListMilestonesWithBurndown(ctx context.Context, workspaceID string) ([]store.MilestoneBurndown, error) {
	if strings.TrimSpace(workspaceID) == "" {
		return nil, fmt.Errorf("workspace_id required")
	}
	rows, err := d.q.QueryContext(ctx, `
		SELECT `+taskSelectCols+`
		FROM tasks
		WHERE workspace_id = ?
		  AND deleted_at IS NULL
		  AND due_at IS NOT NULL
		  AND tags_json LIKE '%"milestone"%'
		ORDER BY due_at ASC`, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("query milestones: %w", err)
	}
	defer func() { _ = rows.Close() }()

	milestones, err := scanTasks(rows)
	if err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	out := make([]store.MilestoneBurndown, 0, len(milestones))
	for _, m := range milestones {
		mb, err := d.buildMilestoneBurndown(ctx, m, now)
		if err != nil {
			return nil, err
		}
		out = append(out, mb)
	}
	return out, nil
}

// buildMilestoneBurndown gathers children for one milestone and computes
// the burndown rollup + per-day series.
func (d *DB) buildMilestoneBurndown(ctx context.Context, m store.Task, now time.Time) (store.MilestoneBurndown, error) {
	childIDs := parseMetaComposes(m.Meta)
	children, err := d.fetchChildClosedTimes(ctx, childIDs)
	if err != nil {
		return store.MilestoneBurndown{}, err
	}

	closed := 0
	for _, c := range children {
		if c != nil {
			closed++
		}
	}

	mb := store.MilestoneBurndown{
		Task:           m,
		TotalChildren:  len(children),
		ClosedChildren: closed,
		DaysRemaining:  daysBetween(now, derefTime(m.DueAt)),
		BurndownPoints: buildBurndownSeries(m.CreatedAt, derefTime(m.DueAt), children),
	}
	return mb, nil
}

// parseMetaComposes mirrors tasks.ReadMetaList without pulling the
// internal/tasks import (store is supposed to stay dependency-free of
// upper layers). Parses the `composes: id, id, ...` frontmatter line.
func parseMetaComposes(meta string) []string {
	const prefix = "composes:"
	for _, line := range strings.Split(meta, "\n") {
		trim := strings.TrimSpace(line)
		if !strings.HasPrefix(trim, prefix) {
			continue
		}
		body := strings.TrimSpace(trim[len(prefix):])
		if body == "" {
			return nil
		}
		var out []string
		for _, v := range strings.Split(body, ",") {
			if v = strings.TrimSpace(v); v != "" {
				out = append(out, v)
			}
		}
		return out
	}
	return nil
}

// fetchChildClosedTimes returns one pointer per id (nil = still open or
// missing/deleted). Order matches ids.
func (d *DB) fetchChildClosedTimes(ctx context.Context, ids []string) ([]*time.Time, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	placeholders := strings.Repeat("?,", len(ids))
	placeholders = placeholders[:len(placeholders)-1]
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := d.q.QueryContext(ctx, `
		SELECT id, closed_at FROM tasks
		WHERE deleted_at IS NULL AND id IN (`+placeholders+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("query children: %w", err)
	}
	defer func() { _ = rows.Close() }()

	closedByID := map[string]*time.Time{}
	for rows.Next() {
		var id string
		var ca sql.NullInt64
		if err := rows.Scan(&id, &ca); err != nil {
			return nil, err
		}
		if ca.Valid {
			t := time.Unix(ca.Int64, 0).UTC()
			closedByID[id] = &t
		} else {
			closedByID[id] = nil
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]*time.Time, 0, len(ids))
	for _, id := range ids {
		// Only include children that still exist (not soft-deleted).
		if _, ok := closedByID[id]; ok {
			out = append(out, closedByID[id])
		}
	}
	return out, nil
}

// buildBurndownSeries emits one point per UTC day from start..end
// (inclusive). For each day, children are partitioned as closed by
// end-of-day vs. open. Returns nil when end <= start, when no children,
// or when start.IsZero (defensive).
func buildBurndownSeries(start, end time.Time, children []*time.Time) []store.BurndownPoint {
	if start.IsZero() || end.IsZero() || !end.After(start) || len(children) == 0 {
		return nil
	}
	startDay := dayUTC(start)
	endDay := dayUTC(end)
	// Pre-sort children's close times (nils sort last; only non-nils used).
	closeTimes := make([]time.Time, 0, len(children))
	for _, c := range children {
		if c != nil {
			closeTimes = append(closeTimes, *c)
		}
	}
	sort.Slice(closeTimes, func(i, j int) bool { return closeTimes[i].Before(closeTimes[j]) })

	total := len(children)
	days := int(endDay.Sub(startDay).Hours()/24) + 1
	if days > 365 {
		// Defensive cap — a milestone with a year+ horizon doesn't
		// benefit from per-day resolution and would explode the payload.
		days = 365
	}

	out := make([]store.BurndownPoint, 0, days)
	for i := 0; i < days; i++ {
		day := startDay.Add(time.Duration(i) * 24 * time.Hour)
		endOfDay := day.Add(24 * time.Hour)
		closedByDay := countClosedBefore(closeTimes, endOfDay)
		out = append(out, store.BurndownPoint{
			Date:           day.Format("2006-01-02"),
			ChildrenClosed: closedByDay,
			ChildrenOpen:   total - closedByDay,
		})
	}
	return out
}

// countClosedBefore counts entries in sorted closeTimes that are strictly
// before `cutoff`.
func countClosedBefore(sortedTimes []time.Time, cutoff time.Time) int {
	// sort.Search returns the smallest index where the predicate is true.
	idx := sort.Search(len(sortedTimes), func(i int) bool {
		return !sortedTimes[i].Before(cutoff)
	})
	return idx
}

// dayUTC truncates t to the start of its UTC day.
func dayUTC(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// daysBetween returns whole-day difference from `from` to `to`. Negative
// when `to` is in the past relative to `from`. Truncates to UTC days so a
// late-evening tick doesn't make the answer flutter across midnight.
func daysBetween(from, to time.Time) int {
	if from.IsZero() || to.IsZero() {
		return 0
	}
	d := dayUTC(to).Sub(dayUTC(from))
	return int(d.Hours() / 24)
}

func derefTime(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}
