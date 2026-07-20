// monitoring_baseline_evidence.go — assembling one template's full candidate
// record: the masked text, the derived matcher verified against real lines, and
// the long-horizon day coverage.
//
// Split out of monitoring_baseline_mine.go. Nothing here decides anything;
// judgement is the pure store.EvaluateBaselineCandidate.
package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// buildBaselineCandidate assembles one cadence group's full evidence, including
// the derived matcher and its verification against real retained lines.
//
// The candidate is identified by the group's cadence key, not a template id, so
// a job's history survives the redeploys that mint new template ids for it.
func (d *DB) buildBaselineCandidate(
	ctx context.Context, src *store.LogSource, g baselineGroup,
	series *arrivalSeries, health store.SourceCollectionHealth, now time.Time,
) (store.BaselineCandidate, error) {
	c := store.BaselineCandidate{
		WorkspaceID: src.WorkspaceID, SourceID: src.ID, TemplateID: g.key,
		Gaps: series.gaps, FirstSeen: series.first, LastSeen: series.last,
		LineCount: series.count, Health: health,
		HourBucketsSeen:        len(series.hours),
		SubstringTemplateLines: series.count,
		// A gap sample that hit BaselineMaxGapsPerTemplate is clipped evidence
		// for exactly the same reason a clipped line scan is: the statistics
		// describe part of the history, so the operator must be told before
		// they read the span as fact.
		ScanTruncated:     series.capped,
		DeployGapsExcised: series.excised,
	}
	c.HourBucketsTotal = baselineHourBuckets(series.first, series.last)
	c.Masked = g.masked
	// Derived from the CADENCE-NORMALIZED text so a code location's line number
	// can never end up inside the matcher. A matcher containing `svc.go:142`
	// would stop matching the moment the next release shifted that line, which
	// is the failure mode where a promoted rule fires an absence alert forever.
	c.MatchSubstring = store.DeriveMatchSubstring(store.CadenceNormalize(g.masked))
	if len([]rune(c.MatchSubstring)) >= store.BaselineMinSubstringLen {
		matches, err := d.baselineSubstringMatches(ctx, src.ID, c.MatchSubstring, series.first)
		if err != nil {
			return c, err
		}
		c.SubstringMatches = matches
	}
	days, gaps, err := d.baselineDayCoverage(ctx, g.ids, now)
	if err != nil {
		return c, err
	}
	c.DayHistoryDays, c.DayGaps = days, gaps
	return c, nil
}

// baselineSubstringMatches counts how many of the SOURCE's retained lines the
// derived matcher hits over the same range. Compared against the template's own
// line count this yields both recall (does the matcher find its own job?) and
// precision (does it sweep in siblings?), which is what stops a matcher that
// matches nothing being promoted into a rule that alerts forever.
func (d *DB) baselineSubstringMatches(
	ctx context.Context, sourceID, substring string, since time.Time,
) (int64, error) {
	var n sql.NullInt64
	err := d.q.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM log_lines
		WHERE source_id = ? AND ts >= ? AND instr(lower(line), ?) > 0`,
		sourceID, formatTime(since.UTC()), strings.ToLower(substring)).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("baseline substring verify: %w", err)
	}
	return n.Int64, nil
}

// baselineDayCoverage reads the long-horizon day table — the only evidence
// about weekly shape that survives raw-line pruning.
//
// The union is taken across every template id in the cadence group. This is the
// half of the redeploy fix that matters most for the cold start: day rows are
// never pruned, so the days observed under a job's PREVIOUS template id are
// still on disk, and reading them is what lets a freshly-redeployed job clear
// the day-history floor instead of restarting from zero at every release.
//
// Rows with basis 'first_seen_baseline' are excluded on purpose. They record a
// lifetime boundary, not an observation, so counting one would anchor the range
// at a day we cannot prove anything about and manufacture weeks of phantom gaps
// between it and the retained history.
func (d *DB) baselineDayCoverage(
	ctx context.Context, templateIDs []string, now time.Time,
) (int, int, error) {
	if len(templateIDs) == 0 {
		return 0, 0, nil
	}
	since := now.UTC().Add(-store.BaselineDayHistoryWindow).Format("2006-01-02")
	args := make([]any, 0, len(templateIDs)+1)
	for _, id := range templateIDs {
		args = append(args, id)
	}
	args = append(args, since)
	rows, err := d.q.QueryContext(ctx, `
		SELECT DISTINCT observed_day FROM log_template_days
		WHERE template_id IN (`+placeholders(len(templateIDs))+`)
		  AND observed_day >= ? AND basis = 'observed'
		ORDER BY observed_day ASC`, args...)
	if err != nil {
		return 0, 0, fmt.Errorf("baseline day coverage: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	days := []time.Time{}
	for rows.Next() {
		var day string
		if err := rows.Scan(&day); err != nil {
			return 0, 0, fmt.Errorf("scan baseline day: %w", err)
		}
		parsed, err := time.Parse("2006-01-02", day)
		if err != nil {
			continue
		}
		days = append(days, parsed)
	}
	if err := rows.Err(); err != nil {
		return 0, 0, err
	}
	span, gaps := baselineDayGaps(days)
	return span, gaps, nil
}

// baselineDayGaps returns the span in days and how many days inside it had no
// observation at all.
func baselineDayGaps(days []time.Time) (int, int) {
	if len(days) == 0 {
		return 0, 0
	}
	span := int(days[len(days)-1].Sub(days[0]).Hours()/24) + 1
	return span, span - len(days)
}
