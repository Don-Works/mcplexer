// monitoring_baseline.go — sqlite persistence for learned signal baselines,
// migration 146. Mining lives in monitoring_baseline_mine.go; judgement lives
// in the pure store.EvaluateBaselineCandidate. This file only moves rows.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/google/uuid"
)

const signalBaselineCols = `id, workspace_id, source_id, template_id, rule_id,
    masked, match_substring, decision, reason,
    period_seconds, p95_seconds, mad_seconds, relative_mad, p95_ratio,
    sample_count, cycles_observed, hour_occupancy, span_seconds, confidence,
    window_seconds, active_start_minute, active_end_minute, scan_truncated,
    first_seen, last_seen, observed_at, learned_runs, created_at, updated_at`

// UpsertSignalBaseline records one candidate's evidence and verdict.
//
// Keyed on template_id so repeated passes converge on one row per signal rather
// than accumulating a history of near-identical judgements. learned_runs counts
// how many passes have looked at this template — an operator reading a baseline
// wants to know whether a verdict is the considered result of eighty passes or
// the first thing the learner ever thought about it.
func (d *DB) UpsertSignalBaseline(ctx context.Context, b *store.SignalBaseline) error {
	if b == nil {
		return errors.New("UpsertSignalBaseline: baseline required")
	}
	if b.WorkspaceID == "" || b.SourceID == "" || b.TemplateID == "" {
		return errors.New("UpsertSignalBaseline: workspace_id, source_id and template_id required")
	}
	now := time.Now().UTC()
	if b.ID == "" {
		b.ID = uuid.NewString()
	}
	if b.CreatedAt.IsZero() {
		b.CreatedAt = now
	}
	if b.ObservedAt.IsZero() {
		b.ObservedAt = now
	}
	b.UpdatedAt = now
	_, err := d.q.ExecContext(ctx, `
		INSERT INTO monitoring_signal_baselines (`+signalBaselineCols+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(template_id) DO UPDATE SET
			rule_id = excluded.rule_id,
			masked = excluded.masked,
			match_substring = excluded.match_substring,
			decision = excluded.decision,
			reason = excluded.reason,
			period_seconds = excluded.period_seconds,
			p95_seconds = excluded.p95_seconds,
			mad_seconds = excluded.mad_seconds,
			relative_mad = excluded.relative_mad,
			p95_ratio = excluded.p95_ratio,
			sample_count = excluded.sample_count,
			cycles_observed = excluded.cycles_observed,
			hour_occupancy = excluded.hour_occupancy,
			span_seconds = excluded.span_seconds,
			confidence = excluded.confidence,
			window_seconds = excluded.window_seconds,
			active_start_minute = excluded.active_start_minute,
			active_end_minute = excluded.active_end_minute,
			scan_truncated = excluded.scan_truncated,
			first_seen = excluded.first_seen,
			last_seen = excluded.last_seen,
			observed_at = excluded.observed_at,
			learned_runs = monitoring_signal_baselines.learned_runs + 1,
			updated_at = excluded.updated_at`,
		b.ID, b.WorkspaceID, b.SourceID, b.TemplateID, nullString(b.RuleID),
		b.Masked, b.MatchSubstring, string(b.Decision), b.Reason,
		b.PeriodSeconds, b.P95Seconds, b.MADSeconds, b.RelativeMAD, b.P95Ratio,
		b.SampleCount, b.CyclesObserved, b.HourOccupancy, b.SpanSeconds, b.Confidence,
		b.WindowSeconds, b.ActiveStartMinute, b.ActiveEndMinute, boolToInt(b.ScanTruncated),
		nullableTimeValue(b.FirstSeen), nullableTimeValue(b.LastSeen),
		formatTime(b.ObservedAt), b.LearnedRuns,
		formatTime(b.CreatedAt), formatTime(b.UpdatedAt),
	)
	if err != nil {
		return fmt.Errorf("upsert signal baseline: %w", mapConstraintError(err))
	}
	return nil
}

// GetSignalBaselineByTemplate returns one template's baseline or ErrNotFound.
func (d *DB) GetSignalBaselineByTemplate(
	ctx context.Context, templateID string,
) (*store.SignalBaseline, error) {
	row := d.q.QueryRowContext(ctx,
		`SELECT `+signalBaselineCols+` FROM monitoring_signal_baselines WHERE template_id = ?`,
		templateID)
	b, err := scanSignalBaseline(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get signal baseline: %w", err)
	}
	return b, nil
}

// ListSignalBaselines is the operator's "what does the system think normal
// looks like" view: promoted rules first, then the strongest near-misses, so
// the answer to "why is there no alert for this job" is on the same page as
// the alerts that do exist.
func (d *DB) ListSignalBaselines(
	ctx context.Context, workspaceID string, limit int,
) ([]*store.SignalBaseline, error) {
	return d.querySignalBaselines(ctx, `SELECT `+signalBaselineCols+`
		FROM monitoring_signal_baselines WHERE workspace_id = ?
		ORDER BY (rule_id IS NOT NULL) DESC, confidence DESC, template_id ASC
		LIMIT ?`, workspaceID, baselineListLimit(limit))
}

// ListSignalBaselinesForSource is the same view scoped to one source.
func (d *DB) ListSignalBaselinesForSource(
	ctx context.Context, sourceID string, limit int,
) ([]*store.SignalBaseline, error) {
	return d.querySignalBaselines(ctx, `SELECT `+signalBaselineCols+`
		FROM monitoring_signal_baselines WHERE source_id = ?
		ORDER BY (rule_id IS NOT NULL) DESC, confidence DESC, template_id ASC
		LIMIT ?`, sourceID, baselineListLimit(limit))
}

// baselineListLimit bounds an operator-supplied page size.
func baselineListLimit(limit int) int {
	const fallback, max = 100, 500
	switch {
	case limit <= 0:
		return fallback
	case limit > max:
		return max
	default:
		return limit
	}
}

func (d *DB) querySignalBaselines(
	ctx context.Context, query string, args ...any,
) ([]*store.SignalBaseline, error) {
	rows, err := d.q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list signal baselines: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	out := []*store.SignalBaseline{}
	for rows.Next() {
		b, err := scanSignalBaseline(rows)
		if err != nil {
			return nil, fmt.Errorf("scan signal baseline: %w", err)
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func scanSignalBaseline(row interface{ Scan(...any) error }) (*store.SignalBaseline, error) {
	var b store.SignalBaseline
	var decision string
	var ruleID, firstSeen, lastSeen sql.NullString
	var truncated int
	var observedAt, createdAt, updatedAt string
	err := row.Scan(&b.ID, &b.WorkspaceID, &b.SourceID, &b.TemplateID, &ruleID,
		&b.Masked, &b.MatchSubstring, &decision, &b.Reason,
		&b.PeriodSeconds, &b.P95Seconds, &b.MADSeconds, &b.RelativeMAD, &b.P95Ratio,
		&b.SampleCount, &b.CyclesObserved, &b.HourOccupancy, &b.SpanSeconds, &b.Confidence,
		&b.WindowSeconds, &b.ActiveStartMinute, &b.ActiveEndMinute, &truncated,
		&firstSeen, &lastSeen, &observedAt, &b.LearnedRuns, &createdAt, &updatedAt)
	if err != nil {
		return nil, err
	}
	b.Decision = store.BaselineDecision(decision)
	if ruleID.Valid {
		b.RuleID = ruleID.String
	}
	b.ScanTruncated = truncated != 0
	if t := nullTimePtr(firstSeen); t != nil {
		b.FirstSeen = *t
	}
	if t := nullTimePtr(lastSeen); t != nil {
		b.LastSeen = *t
	}
	b.ObservedAt, b.CreatedAt, b.UpdatedAt =
		parseTime(observedAt), parseTime(createdAt), parseTime(updatedAt)
	return &b, nil
}

// nullableTimeValue stores a zero time as SQL NULL rather than as year 1,
// so "we never saw this" reads as absent instead of as an ancient observation.
func nullableTimeValue(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return formatTime(t.UTC())
}
