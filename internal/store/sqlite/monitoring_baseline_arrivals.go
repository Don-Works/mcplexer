// monitoring_baseline_arrivals.go — streaming a template's arrival times into
// the bounded evidence the learner judges.
//
// Split out of monitoring_baseline_mine.go: that file orchestrates a pass and
// picks what to measure, this one turns raw timestamps into gaps and hour
// buckets. Timestamps are never retained past the streaming loop, so memory is
// bounded by the gap cap no matter how busy a source is.
package sqlite

import (
	"context"
	"fmt"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// arrivalSeries is one template's streamed arrival evidence.
type arrivalSeries struct {
	first  time.Time
	last   time.Time
	prev   time.Time
	count  int64
	gaps   []time.Duration
	hours  map[int64]struct{}
	capped bool
	// excised counts gaps dropped because a deploy happened inside them.
	excised int
}

// observe folds one arrival into the series. Callers MUST supply arrivals in
// ascending time order: gaps are only meaningful forwards.
//
// A gap containing a DEPLOY is dropped rather than recorded. A restart is a
// known cause, and the rule is that a known cause is subtracted from evidence:
// the pause while a service comes back is not something the job's schedule did,
// so letting it into the sample would teach the learner that the job's normal
// behaviour includes a several-minute hole. Dropping the gap keeps the arrivals
// on both sides — the series is not split, only the artefact is removed.
//
// deploys must be sorted ascending.
func (s *arrivalSeries) observe(ts time.Time, deploys []time.Time) {
	if s.hours == nil {
		s.hours = map[int64]struct{}{}
	}
	switch {
	case s.count == 0:
		s.first = ts
	case store.DeploySpansGap(deploys, s.prev, ts):
		s.excised++
	default:
		if gap := ts.Sub(s.prev); gap > 0 {
			if len(s.gaps) < store.BaselineMaxGapsPerTemplate {
				s.gaps = append(s.gaps, gap)
			} else {
				s.capped = true
			}
		}
	}
	s.hours[ts.Unix()/3600] = struct{}{}
	s.prev, s.last = ts, ts
	s.count++
}

// baselineArrivals streams the shortlisted templates' arrivals in time order,
// keyed by CADENCE key rather than template id.
//
// Merging happens here, in the single ascending pass, which is the only place
// it can be done correctly: gaps are meaningful only forwards, so two template
// ids belonging to the same job across a redeploy have to be interleaved by
// timestamp before any gap is taken. Summarising them separately and adding the
// results afterwards would invent a false gap at the release boundary and lose
// the true one that spans it.
//
// The scan takes the most RECENT BaselineMaxScanLines rows rather than the
// oldest, so a source that overflows the budget yields a truthful recent
// picture instead of a stale one — and reports truncation so the resulting span
// is read as a floor rather than a fact.
func (d *DB) baselineArrivals(
	ctx context.Context, sourceID string, horizonStart time.Time, shortlist []string,
	cadenceByTemplate map[string]string, deploys []time.Time,
) (map[string]*arrivalSeries, bool, error) {
	rows, err := d.q.QueryContext(ctx,
		`SELECT template_id, ts FROM log_lines
			WHERE source_id = ? AND ts >= ? AND template_id IN (`+
			placeholders(len(shortlist))+`)
			ORDER BY ts DESC LIMIT ?`,
		baselineArrivalArgs(sourceID, horizonStart, shortlist)...)
	if err != nil {
		return nil, false, fmt.Errorf("baseline arrivals: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	type arrival struct {
		id string
		ts time.Time
	}
	descending := make([]arrival, 0, 1024)
	for rows.Next() {
		var id, ts string
		if err := rows.Scan(&id, &ts); err != nil {
			return nil, false, fmt.Errorf("scan baseline arrival: %w", err)
		}
		descending = append(descending, arrival{id: id, ts: parseTime(ts)})
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	series := map[string]*arrivalSeries{}
	// Reverse into ascending order: gaps are only meaningful forwards, and the
	// query had to run backwards to make the budget take the recent tail.
	for i := len(descending) - 1; i >= 0; i-- {
		key, ok := cadenceByTemplate[descending[i].id]
		if !ok {
			continue
		}
		s, ok := series[key]
		if !ok {
			s = &arrivalSeries{}
			series[key] = s
		}
		s.observe(descending[i].ts.UTC(), deploys)
	}
	return series, len(descending) >= store.BaselineMaxScanLines, nil
}

// baselineArrivalArgs binds the arrival scan's positional parameters.
func baselineArrivalArgs(sourceID string, horizonStart time.Time, shortlist []string) []any {
	args := make([]any, 0, len(shortlist)+3)
	args = append(args, sourceID, formatTime(horizonStart.UTC()))
	for _, id := range shortlist {
		args = append(args, id)
	}
	return append(args, store.BaselineMaxScanLines)
}

// baselineHourBuckets counts whole hour buckets spanned, inclusive of both ends.
func baselineHourBuckets(first, last time.Time) int {
	if first.IsZero() || last.IsZero() || last.Before(first) {
		return 0
	}
	return int(last.Unix()/3600-first.Unix()/3600) + 1
}
