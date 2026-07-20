// monitoring_expected_signal_record.go — observation gathering and durable
// outcome recording for expected-signal (absence) rules.
//
// Raising goes through the EXISTING incident machinery (monitoring_incidents,
// monitoring_occurrences, monitoringNotificationDue) rather than a parallel
// ledger, so absence incidents inherit occurrence history, canonical-task
// binding and notification policy for free. Nothing here consults a model.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/oklog/ulid/v2"
)

// severityRankSQL ranks log_templates.severity inline so a min_severity floor
// is one comparison. Kept as SQL (not a Go filter) so the window never has to
// be materialised in memory.
const severityRankSQL = `(CASE t.severity WHEN 'critical' THEN 3 WHEN 'error' THEN 2 ` +
	`WHEN 'warn' THEN 1 WHEN 'info' THEN 0 ELSE -1 END)`

// ObserveExpectedSignal measures the rule's window in one scan over the
// (source_id, ts) index, plus one source read for pull health.
//
// TotalLines counts every retained line from the source regardless of pattern.
// It is the collection-liveness fact that separates "the integration ingested
// no orders" from "we lost the stream" — log_sources.cursor_ts is a log
// WATERMARK and is deliberately never used as a liveness proxy.
func (d *DB) ObserveExpectedSignal(
	ctx context.Context, r *store.MonitoringExpectedSignal, now time.Time,
) (store.ExpectedSignalObservation, store.SourceCollectionHealth, error) {
	var obs store.ExpectedSignalObservation
	var health store.SourceCollectionHealth
	if r == nil {
		return obs, health, errors.New("ObserveExpectedSignal: rule required")
	}
	var enabled int
	err := d.q.QueryRowContext(ctx,
		`SELECT enabled, consecutive_failures FROM log_sources WHERE id = ?`,
		r.SourceID).Scan(&enabled, &health.ConsecutiveFailures)
	if errors.Is(err, sql.ErrNoRows) {
		return obs, health, store.ErrLogSourceNotFound
	}
	if err != nil {
		return obs, health, fmt.Errorf("read expected signal source health: %w", err)
	}
	health.Enabled = enabled != 0

	query, args := expectedSignalObservationQuery(r, now)
	var total, matched sql.NullInt64
	var lastMatch sql.NullString
	if err := d.q.QueryRowContext(ctx, query, args...).Scan(&total, &matched, &lastMatch); err != nil {
		return obs, health, fmt.Errorf("observe expected signal: %w", err)
	}
	obs.TotalLines, obs.MatchCount = total.Int64, matched.Int64
	obs.LastMatchAt = nullTimePtr(lastMatch)
	return obs, health, nil
}

// expectedSignalObservationQuery builds the single-scan aggregate. The
// log_templates join is added only when a severity floor is configured, so the
// common case stays a pure log_lines index scan.
func expectedSignalObservationQuery(r *store.MonitoringExpectedSignal, now time.Time) (string, []any) {
	windowStart := formatTime(r.WindowStart(now))
	match, matchArgs := "1 = 1", []any{}
	if sub := strings.TrimSpace(r.MatchSubstring); sub != "" {
		match = "instr(lower(l.line), ?) > 0"
		matchArgs = append(matchArgs, strings.ToLower(sub))
	}
	from := "FROM log_lines l"
	if store.ValidSeverity(r.MinSeverity) {
		from = "FROM log_lines l JOIN log_templates t ON t.id = l.template_id AND t.source_id = l.source_id"
		match += " AND " + severityRankSQL + " >= ?"
		matchArgs = append(matchArgs, store.SeverityRank(r.MinSeverity))
	}
	args := []any{windowStart, windowStart}
	args = append(args, matchArgs...)
	args = append(args, matchArgs...)
	args = append(args, r.SourceID)
	query := `SELECT
			COALESCE(SUM(CASE WHEN l.ts >= ? THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN l.ts >= ? AND ` + match + ` THEN 1 ELSE 0 END), 0),
			MAX(CASE WHEN ` + match + ` THEN l.ts END)
		` + from + `
		WHERE l.source_id = ?`
	return query, args
}

// RecordExpectedSignalOutcome persists one evaluation. A non-raising decision
// records state and clears the recovery latch; a raising decision converges on
// the incident class named by the decision, so repeat evaluations produce ONE
// incident with a growing occurrence ledger rather than N incidents.
func (d *DB) RecordExpectedSignalOutcome(
	ctx context.Context, in store.ExpectedSignalRecord,
) (*store.ExpectedSignalResult, error) {
	if strings.TrimSpace(in.RuleID) == "" {
		return nil, errors.New("RecordExpectedSignalOutcome: rule_id required")
	}
	observedAt := in.ObservedAt.UTC()
	if observedAt.IsZero() {
		observedAt = time.Now().UTC()
	}
	if in.Decision.Raise {
		if !store.ValidSeverity(in.Decision.Severity) {
			return nil, fmt.Errorf("RecordExpectedSignalOutcome: invalid severity %q", in.Decision.Severity)
		}
		if strings.TrimSpace(in.Decision.ClassKey) == "" || strings.TrimSpace(in.TaskID) == "" {
			return nil, errors.New("RecordExpectedSignalOutcome: class_key and task_id required to raise")
		}
	}
	var result *store.ExpectedSignalResult
	err := d.withTx(ctx, func(q queryable) error {
		rule, err := getExpectedSignalQ(q, ctx, in.RuleID)
		if errors.Is(err, sql.ErrNoRows) {
			return store.ErrMonitoringExpectedSignalNotFound
		}
		if err != nil {
			return fmt.Errorf("read expected signal for record: %w", err)
		}
		result = &store.ExpectedSignalResult{Rule: rule}
		if in.Decision.Raise {
			if err := raiseExpectedSignalIncident(q, ctx, rule, in, observedAt, result); err != nil {
				return err
			}
		} else {
			result.Recovered = in.Decision.SignalPresent && rule.ActiveIncidentID != ""
		}
		return persistExpectedSignalState(q, ctx, rule, in, observedAt, result)
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// raiseExpectedSignalIncident reuses the incident/occurrence helpers owned by
// monitoring_incident.go — including monitoringNotificationDue, so absence
// incidents follow exactly the same notification policy as triaged ones.
func raiseExpectedSignalIncident(
	q queryable, ctx context.Context, rule *store.MonitoringExpectedSignal,
	in store.ExpectedSignalRecord, observedAt time.Time, result *store.ExpectedSignalResult,
) error {
	if err := requireMonitoringTask(q, ctx, rule.WorkspaceID, in.TaskID); err != nil {
		return err
	}
	incident, newIncident, err := upsertExpectedSignalIncident(q, ctx, rule, in, observedAt)
	if err != nil {
		return err
	}
	occurrence, newOccurrence, err := upsertExpectedSignalOccurrence(q, ctx, rule, in, incident.ID, observedAt)
	if err != nil {
		return err
	}
	occurrenceDelta := 0
	if newOccurrence {
		occurrenceDelta = 1
	}
	if _, err := q.ExecContext(ctx, `UPDATE monitoring_incidents
		SET severity = ?, title = ?, occurrence_count = occurrence_count + ?,
		    event_count = event_count + ?,
		    last_seen = CASE WHEN last_seen < ? THEN ? ELSE last_seen END,
		    updated_at = ?
		WHERE id = ?`, in.Decision.Severity, incident.Title, occurrenceDelta, occurrenceDelta,
		formatTime(observedAt), formatTime(observedAt), formatTime(observedAt), incident.ID); err != nil {
		return fmt.Errorf("update expected signal incident: %w", err)
	}
	if incident, err = getMonitoringIncidentByIDQ(q, ctx, incident.ID); err != nil {
		return fmt.Errorf("read expected signal incident: %w", err)
	}
	result.Incident, result.Occurrence = incident, occurrence
	result.NewIncident, result.NewOccurrence = newIncident, newOccurrence
	// Shared with the triaged-incident path on purpose: an absence held at a
	// steady severity must keep saying "still broken" on the same widening
	// backoff, and must age-escalate the same way, or Gap B would reintroduce
	// Gap A for absence incidents specifically.
	decision := monitoringNotificationDue(incident, newIncident, observedAt)
	result.ShouldNotify = decision.Notify
	result.NotificationReason = decision.Reason
	result.EffectiveSeverity = decision.EffectiveSeverity
	return nil
}

func upsertExpectedSignalIncident(
	q queryable, ctx context.Context, rule *store.MonitoringExpectedSignal,
	in store.ExpectedSignalRecord, observedAt time.Time,
) (*store.MonitoringIncident, bool, error) {
	title := truncateMonitoringText(strings.TrimSpace(in.Decision.Title), 500)
	incident, err := getMonitoringIncidentByClassQ(q, ctx, rule.WorkspaceID, in.Decision.ClassKey)
	if err == nil {
		if title == "" {
			title = incident.Title
		}
		incident.Title = title
		return incident, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, false, fmt.Errorf("get expected signal incident: %w", err)
	}
	incident = &store.MonitoringIncident{
		ID: ulid.Make().String(), WorkspaceID: rule.WorkspaceID,
		ClassKey: in.Decision.ClassKey, TaskID: in.TaskID,
		Disposition: store.MonitoringDispositionActionable,
		Severity:    in.Decision.Severity, Title: title,
		FirstSeen: observedAt, LastSeen: observedAt,
		CreatedAt: observedAt, UpdatedAt: observedAt,
	}
	if _, err := q.ExecContext(ctx, `INSERT INTO monitoring_incidents (`+monitoringIncidentCols+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, 0, 0, ?, ?, NULL, '', ?, ?)`,
		incident.ID, incident.WorkspaceID, incident.ClassKey, incident.TaskID,
		incident.Disposition, incident.Severity, incident.Title,
		formatTime(observedAt), formatTime(observedAt),
		formatTime(observedAt), formatTime(observedAt)); err != nil {
		return nil, false, fmt.Errorf("insert expected signal incident: %w", mapConstraintError(err))
	}
	return incident, true, nil
}

// upsertExpectedSignalOccurrence writes the 15-minute bucket. No templates are
// linked: an absence has no log template by construction, which is precisely
// why the template-driven pipeline could never see it.
func upsertExpectedSignalOccurrence(
	q queryable, ctx context.Context, rule *store.MonitoringExpectedSignal,
	in store.ExpectedSignalRecord, incidentID string, observedAt time.Time,
) (*store.MonitoringOccurrence, bool, error) {
	key := monitoringBucketKey(observedAt)
	evidence := truncateMonitoringText(expectedSignalEvidence(rule, in.Decision), 6000)
	occurrence, err := getMonitoringOccurrenceQ(q, ctx, incidentID, key)
	if errors.Is(err, sql.ErrNoRows) {
		occurrence = &store.MonitoringOccurrence{
			ID: ulid.Make().String(), IncidentID: incidentID, OccurrenceKey: key,
			SourceID: rule.SourceID, TemplateIDsJSON: "[]",
			Severity: in.Decision.Severity, EventCount: 1,
			FirstSeen: observedAt, LastSeen: observedAt,
			Evidence: evidence, CreatedAt: observedAt,
		}
		if err := insertMonitoringOccurrence(q, ctx, occurrence); err != nil {
			return nil, false, err
		}
		return occurrence, true, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("get expected signal occurrence: %w", err)
	}
	severity := occurrence.Severity
	if severityHigher(in.Decision.Severity, severity) {
		severity = in.Decision.Severity
	}
	if _, err := q.ExecContext(ctx, `UPDATE monitoring_occurrences
		SET severity = ?, last_seen = ?, evidence = ? WHERE id = ?`,
		severity, formatTime(observedAt), evidence, occurrence.ID); err != nil {
		return nil, false, fmt.Errorf("merge expected signal occurrence: %w", err)
	}
	occurrence.Severity, occurrence.LastSeen, occurrence.Evidence = severity, observedAt, evidence
	return occurrence, false, nil
}

func expectedSignalEvidence(rule *store.MonitoringExpectedSignal, d store.ExpectedSignalDecision) string {
	return fmt.Sprintf("outcome=%s reason=%s\nrule=%s source=%s\nwindow=%s .. %s\n\n%s",
		d.Outcome, d.Reason, rule.Name, rule.SourceID,
		d.WindowStart.UTC().Format(time.RFC3339), d.WindowEnd.UTC().Format(time.RFC3339),
		d.Detail)
}

// persistExpectedSignalState writes the evaluation latches. LastSignalAt is
// the bootstrap guard and active_incident_id is the recovery latch; clearing
// the latter is what makes the signal's return stop the incident firing.
func persistExpectedSignalState(
	q queryable, ctx context.Context, rule *store.MonitoringExpectedSignal,
	in store.ExpectedSignalRecord, observedAt time.Time, result *store.ExpectedSignalResult,
) error {
	lastSignal := rule.LastSignalAt
	if in.Decision.SignalPresent {
		signal := observedAt
		lastSignal = &signal
	}
	incidentID, raisedAt, recoveredAt := "", rule.LastRaisedAt, rule.LastRecoveredAt
	if in.Decision.Raise && result.Incident != nil {
		incidentID = result.Incident.ID
		raised := observedAt
		raisedAt = &raised
	} else if result.Recovered {
		recovered := observedAt
		recoveredAt = &recovered
	}
	if _, err := q.ExecContext(ctx, `UPDATE monitoring_expected_signals
		SET last_evaluated_at = ?, last_signal_at = ?, last_outcome = ?,
		    last_raised_at = ?, last_recovered_at = ?, active_incident_id = ?,
		    updated_at = ?
		WHERE id = ?`,
		formatTime(observedAt), nullableTime(lastSignal), string(in.Decision.Outcome),
		nullableTime(raisedAt), nullableTime(recoveredAt), nullString(incidentID),
		formatTime(observedAt), rule.ID); err != nil {
		return fmt.Errorf("persist expected signal state: %w", err)
	}
	updated, err := getExpectedSignalQ(q, ctx, rule.ID)
	if err != nil {
		return fmt.Errorf("read expected signal after record: %w", err)
	}
	result.Rule = updated
	return nil
}

func getExpectedSignalQ(
	q queryable, ctx context.Context, id string,
) (*store.MonitoringExpectedSignal, error) {
	return scanExpectedSignal(q.QueryRowContext(ctx,
		`SELECT `+expectedSignalCols+` FROM monitoring_expected_signals WHERE id = ?`, id))
}
