// monitoring_baseline_mine.go — gathering the evidence the baseline learner
// judges. This file measures; it never decides. Judgement is the pure
// store.EvaluateBaselineCandidate, and nothing here consults a model.
//
// One pass over a source is three bounded queries plus two small per-template
// ones, run hourly. That is negligible against the collector's own SSH
// round-trips, and it deliberately never touches the pull path.
//
// The arrival streaming lives in monitoring_baseline_arrivals.go and the
// per-template evidence assembly in monitoring_baseline_evidence.go; this file
// is the orchestration and the shortlist policy.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// MineBaselineCandidates measures every plausible recurring template on one
// source over the learning horizon.
func (d *DB) MineBaselineCandidates(
	ctx context.Context, src *store.LogSource, horizonStart, now time.Time,
) ([]store.BaselineCandidate, error) {
	if src == nil {
		return nil, errors.New("MineBaselineCandidates: source required")
	}
	health, err := d.baselineSourceHealth(ctx, src.ID)
	if err != nil {
		return nil, err
	}
	templates, err := d.baselineShortlist(ctx, src.ID, horizonStart)
	if err != nil {
		return nil, err
	}
	groups := baselineGroupByCadence(src.ID, templates)
	if len(groups) == 0 {
		return nil, nil
	}
	// Deploys are read ONCE per source and subtracted from every template's
	// gap sample below. A failure here is not fatal: mining with no deploy
	// evidence yields a baseline that has merely seen a few extra restart
	// gaps, which robust statistics absorb — whereas failing the pass would
	// leave the source with no baseline at all.
	deploys, err := d.RecentDeploys(ctx, src.ID, horizonStart)
	if err != nil {
		slog.Warn("baseline: read deploy banners; mining without deploy excision",
			"source", src.Name, "error", err)
		deploys = nil
	}
	arrivals, truncated, err := d.baselineArrivals(
		ctx, src.ID, horizonStart, baselineGroupTemplateIDs(groups),
		baselineCadenceByTemplate(groups), deploys)
	if err != nil {
		return nil, err
	}
	out := make([]store.BaselineCandidate, 0, len(groups))
	for _, g := range groups {
		series, ok := arrivals[g.key]
		if !ok || series.count == 0 {
			continue
		}
		candidate, err := d.buildBaselineCandidate(ctx, src, g, series, health, now)
		if err != nil {
			return nil, err
		}
		// Either kind of clipping counts: the per-source line budget, or this
		// template's own gap cap. Both mean the span is a floor, not the truth.
		candidate.ScanTruncated = candidate.ScanTruncated || truncated
		out = append(out, candidate)
	}
	return out, nil
}

func (d *DB) baselineSourceHealth(
	ctx context.Context, sourceID string,
) (store.SourceCollectionHealth, error) {
	var health store.SourceCollectionHealth
	var enabled int
	err := d.q.QueryRowContext(ctx,
		`SELECT enabled, consecutive_failures FROM log_sources WHERE id = ?`,
		sourceID).Scan(&enabled, &health.ConsecutiveFailures)
	if errors.Is(err, sql.ErrNoRows) {
		return health, store.ErrLogSourceNotFound
	}
	if err != nil {
		return health, fmt.Errorf("baseline source health: %w", err)
	}
	health.Enabled = enabled != 0
	return health, nil
}

// baselineShortlist reads the candidate templates and their masked text.
//
// It deliberately does NOT apply the minimum-sample floor. That floor belongs
// to the cadence GROUP (baselineGroupByCadence), because a job redeployed
// mid-horizon owns two template ids whose counts must be added before either is
// judged — filtering here would throw away precisely the halves that need
// summing. Counts come from retained lines in the horizon, never from
// log_templates.count, which is a LIFETIME counter and would badly misjudge a
// heavily pruned template.
//
// Ordering is count ASCENDING for the same reason the group policy is: the
// chattiest templates on a busy source are per-request logs, not scheduled
// jobs. The scan limit is well above BaselineMaxTemplatesPerSource so that
// grouping, not truncation, decides what survives.
func (d *DB) baselineShortlist(
	ctx context.Context, sourceID string, horizonStart time.Time,
) ([]baselineTemplate, error) {
	rows, err := d.q.QueryContext(ctx, `
		SELECT l.template_id, COALESCE(t.masked, ''), COUNT(*) AS n
		FROM log_lines l
		LEFT JOIN log_templates t ON t.id = l.template_id
		WHERE l.source_id = ? AND l.ts >= ?
		GROUP BY l.template_id
		HAVING n > 1 AND n <= ?
		ORDER BY n ASC
		LIMIT ?`,
		sourceID, formatTime(horizonStart.UTC()),
		baselineMaxTemplateLines, baselineShortlistScanLimit)
	if err != nil {
		return nil, fmt.Errorf("baseline shortlist: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	out := []baselineTemplate{}
	for rows.Next() {
		var t baselineTemplate
		if err := rows.Scan(&t.id, &t.masked, &t.lines); err != nil {
			return nil, fmt.Errorf("scan baseline shortlist: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// baselineShortlistScanLimit bounds the pre-grouping read. Templates are
// grouped by cadence key afterwards and the result capped at
// BaselineMaxTemplatesPerSource, so this only needs to be loose enough that a
// job's several release-era template ids all survive to be reunited.
const baselineShortlistScanLimit = 1000

// baselineMaxTemplateLines excludes templates arriving faster than roughly once
// every ten seconds over the horizon. Those are firehoses, not schedules, and
// measuring their gaps costs more than it can ever tell us.
const baselineMaxTemplateLines = 120000
