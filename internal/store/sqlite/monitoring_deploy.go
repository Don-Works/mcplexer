// monitoring_deploy.go — reading deploy evidence out of logs the daemon
// already holds.
//
// There is deliberately NO new table and no new write path. A deploy is
// evidenced by the startup banner the service itself logs, so the raw lines ARE
// the record: nothing has to be recorded at ingest, nothing can drift out of
// sync with reality, and an operator auditing a suppressed alert can go and
// read the exact line that opened the window. Adding a deploys table would have
// meant a second source of truth for something the first one already proves.
package sqlite

import (
	"context"
	"fmt"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// RecentDeploys returns the times a deploy banner was observed on one source
// since `since`, ascending.
//
// Two small queries. The first shortlists candidate templates by masked text —
// the version string masks away, so one shape covers every future release — and
// the second reads their arrival times. Both are bounded: banners are rare by
// definition, and a template that is NOT rare is rejected as not-a-banner.
func (d *DB) RecentDeploys(
	ctx context.Context, sourceID string, since time.Time,
) ([]time.Time, error) {
	ids, err := d.deployBannerTemplates(ctx, sourceID, since)
	if err != nil || len(ids) == 0 {
		return nil, err
	}
	args := make([]any, 0, len(ids)+2)
	args = append(args, sourceID)
	for _, id := range ids {
		args = append(args, id)
	}
	args = append(args, formatTime(since.UTC()))
	rows, err := d.q.QueryContext(ctx, `
		SELECT ts FROM log_lines
		WHERE source_id = ? AND template_id IN (`+placeholders(len(ids))+`) AND ts >= ?
		ORDER BY ts ASC`, args...)
	if err != nil {
		return nil, fmt.Errorf("recent deploys: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	out := []time.Time{}
	for rows.Next() {
		var ts string
		if err := rows.Scan(&ts); err != nil {
			return nil, fmt.Errorf("scan deploy line: %w", err)
		}
		out = append(out, parseTime(ts).UTC())
	}
	return out, rows.Err()
}

// deployBannerTemplates shortlists the source's startup-banner templates.
//
// Severity is filtered to 'info' in SQL, and that is a load-bearing guard, not
// a tidy-up. An ERROR line mentioning a version ("unsupported version <n>") is
// a failure, not a release; honouring it as a deploy would suppress anomaly
// alerting at exactly the moment something is going wrong — the precise
// failure mode this whole grace mechanism has to avoid.
func (d *DB) deployBannerTemplates(
	ctx context.Context, sourceID string, since time.Time,
) ([]string, error) {
	rows, err := d.q.QueryContext(ctx, `
		SELECT id, masked FROM log_templates
		WHERE source_id = ? AND severity = 'info' AND last_seen >= ?`,
		sourceID, formatTime(since.UTC()))
	if err != nil {
		return nil, fmt.Errorf("deploy banner templates: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	candidates := []string{}
	for rows.Next() {
		var id, masked string
		if err := rows.Scan(&id, &masked); err != nil {
			return nil, fmt.Errorf("scan deploy banner template: %w", err)
		}
		if store.IsDeployBanner(masked) {
			candidates = append(candidates, id)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return d.rejectChattyBanners(ctx, sourceID, candidates, since)
}

// rejectChattyBanners drops templates that match a banner shape but fire too
// often to be one. A real banner is emitted once per release; a line emitted
// continuously is an ordinary log line whose text happens to match, and
// honouring it would hand out permanent grace.
func (d *DB) rejectChattyBanners(
	ctx context.Context, sourceID string, candidates []string, since time.Time,
) ([]string, error) {
	out := make([]string, 0, len(candidates))
	for _, id := range candidates {
		var n int64
		err := d.q.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM log_lines
			WHERE source_id = ? AND template_id = ? AND ts >= ?`,
			sourceID, id, formatTime(since.UTC())).Scan(&n)
		if err != nil {
			return nil, fmt.Errorf("count deploy banner lines: %w", err)
		}
		if n > 0 && n <= store.DeployBannerMaxOccurrences {
			out = append(out, id)
		}
	}
	return out, nil
}
