package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/oklog/ulid/v2"
)

const monitoringIncidentCols = `id, workspace_id, class_key, task_id,
    disposition, severity, title, occurrence_count, event_count,
    first_seen, last_seen, last_notified_at, last_notified_severity,
    created_at, updated_at`

const monitoringOccurrenceCols = `id, incident_id, occurrence_key, source_id,
    template_ids_json, severity, event_count, first_seen, last_seen,
    evidence, created_at`

const monitoringOccurrenceBucket = 15 * time.Minute

func (d *DB) GetMonitoringIncidentByClass(
	ctx context.Context, workspaceID, classKey string,
) (*store.MonitoringIncident, error) {
	row := d.q.QueryRowContext(ctx, `SELECT `+monitoringIncidentCols+`
		FROM monitoring_incidents WHERE workspace_id = ? AND class_key = ?`,
		workspaceID, classKey)
	incident, err := scanMonitoringIncident(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrMonitoringIncidentNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get monitoring incident: %w", err)
	}
	return incident, nil
}

func (d *DB) ListMonitoringIncidentsByTemplateIDs(
	ctx context.Context, workspaceID string, templateIDs []string,
) ([]*store.MonitoringIncident, error) {
	ids := uniqueStrings(templateIDs)
	if len(ids) == 0 {
		return []*store.MonitoringIncident{}, nil
	}
	args := make([]any, 0, len(ids)+1)
	args = append(args, workspaceID)
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := d.q.QueryContext(ctx, `SELECT DISTINCT `+prefixedCols("i", monitoringIncidentCols)+`
		FROM monitoring_incidents i
		JOIN monitoring_incident_templates it ON it.incident_id = i.id
		WHERE i.workspace_id = ? AND it.template_id IN (`+placeholders(len(ids))+`)
		ORDER BY i.created_at`, args...)
	if err != nil {
		return nil, fmt.Errorf("list monitoring incidents by templates: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	out := []*store.MonitoringIncident{}
	for rows.Next() {
		incident, err := scanMonitoringIncident(rows)
		if err != nil {
			return nil, fmt.Errorf("scan monitoring incident: %w", err)
		}
		out = append(out, incident)
	}
	return out, rows.Err()
}

// RecordMonitoringTriage creates or reuses an incident class, links its exact
// templates, and inserts one idempotent time-bucketed occurrence. Completion
// (template triaged state + worker effect receipt) is deliberately separate:
// the gateway performs notification first, then commits completion, so a
// failed delivery leaves the work retryable.
func (d *DB) RecordMonitoringTriage(
	ctx context.Context, in store.MonitoringTriageRecord,
) (*store.MonitoringTriageResult, error) {
	if strings.TrimSpace(in.WorkspaceID) == "" || strings.TrimSpace(in.ClassKey) == "" || strings.TrimSpace(in.TaskID) == "" {
		return nil, errors.New("RecordMonitoringTriage: workspace_id, class_key and task_id required")
	}
	if !store.ValidSeverity(in.Severity) {
		return nil, fmt.Errorf("RecordMonitoringTriage: invalid severity %q", in.Severity)
	}
	if !store.ValidMonitoringDisposition(in.Disposition) || in.Disposition == store.MonitoringDispositionBenign {
		return nil, fmt.Errorf("RecordMonitoringTriage: invalid incident disposition %q", in.Disposition)
	}
	ids := uniqueStrings(in.TemplateIDs)
	if len(ids) == 0 {
		return nil, errors.New("RecordMonitoringTriage: template_ids required")
	}
	observedAt := in.ObservedAt.UTC()
	if observedAt.IsZero() {
		observedAt = time.Now().UTC()
	}
	in.Evidence = truncateMonitoringText(in.Evidence, 6000)
	in.Title = truncateMonitoringText(strings.TrimSpace(in.Title), 500)

	var result *store.MonitoringTriageResult
	err := d.withTx(ctx, func(q queryable) error {
		if err := requireMonitoringTask(q, ctx, in.WorkspaceID, in.TaskID); err != nil {
			return err
		}
		sourceID, err := requireMonitoringTemplates(q, ctx, in.WorkspaceID, ids)
		if err != nil {
			return err
		}
		if in.SourceID == "" {
			in.SourceID = sourceID
		}

		incident, err := getMonitoringIncidentByClassQ(q, ctx, in.WorkspaceID, in.ClassKey)
		newIncident := false
		if errors.Is(err, sql.ErrNoRows) {
			newIncident = true
			incident = &store.MonitoringIncident{
				ID: ulid.Make().String(), WorkspaceID: in.WorkspaceID,
				ClassKey: in.ClassKey, TaskID: in.TaskID,
				Disposition: in.Disposition, Severity: in.Severity, Title: in.Title,
				FirstSeen: observedAt, LastSeen: observedAt,
				CreatedAt: observedAt, UpdatedAt: observedAt,
			}
			if _, err := q.ExecContext(ctx, `INSERT INTO monitoring_incidents (`+monitoringIncidentCols+`)
				VALUES (?, ?, ?, ?, ?, ?, ?, 0, 0, ?, ?, NULL, '', ?, ?)`,
				incident.ID, incident.WorkspaceID, incident.ClassKey, incident.TaskID,
				incident.Disposition, incident.Severity, incident.Title,
				formatTime(incident.FirstSeen), formatTime(incident.LastSeen),
				formatTime(incident.CreatedAt), formatTime(incident.UpdatedAt)); err != nil {
				return fmt.Errorf("insert monitoring incident: %w", mapConstraintError(err))
			}
		} else if err != nil {
			return fmt.Errorf("get monitoring incident for triage: %w", err)
		} else if incident.TaskID != in.TaskID {
			// A soft-deleted canonical task frees the partial unique class
			// index. Permit the replacement elected by the task service, but
			// never switch away from a still-live canonical row.
			var oldTaskLive int
			if err := q.QueryRowContext(ctx, `SELECT EXISTS(
				SELECT 1 FROM tasks WHERE id = ? AND deleted_at IS NULL
			)`, incident.TaskID).Scan(&oldTaskLive); err != nil {
				return fmt.Errorf("validate prior canonical monitoring task: %w", err)
			}
			if oldTaskLive != 0 {
				return fmt.Errorf("monitoring class %q already points to canonical task %s", in.ClassKey, incident.TaskID)
			}
			if _, err := q.ExecContext(ctx, `UPDATE monitoring_incidents
				SET task_id = ?, updated_at = ? WHERE id = ?`,
				in.TaskID, formatTime(observedAt), incident.ID); err != nil {
				return fmt.Errorf("replace deleted canonical monitoring task: %w", err)
			}
			incident.TaskID = in.TaskID
		}

		// Suppression break-out. A class that is being triaged again is by
		// definition not settled, so a live benign suppression is reversed
		// here rather than being allowed to swallow the recurrence. Without
		// this the UPDATE below would silently overwrite disposition=benign
		// with the incoming actionable value, leaving the resolution receipt
		// claiming a suppression that no longer exists and the acked
		// templates invisible to the worker forever.
		//
		// RecordMonitoringTriage rejects a benign in.Disposition outright, so
		// this can only ever run when real triage is landing on the class.
		if !newIncident && incident.Disposition == store.MonitoringDispositionBenign {
			if err := breakMonitoringSuppressionQ(q, ctx, incident.ID, observedAt); err != nil {
				return err
			}
		}

		for _, templateID := range ids {
			var existingIncident string
			err := q.QueryRowContext(ctx,
				`SELECT incident_id FROM monitoring_incident_templates WHERE template_id = ?`,
				templateID).Scan(&existingIncident)
			switch {
			case errors.Is(err, sql.ErrNoRows):
				if _, err := q.ExecContext(ctx, `INSERT INTO monitoring_incident_templates
					(template_id, incident_id, linked_at) VALUES (?, ?, ?)`,
					templateID, incident.ID, formatTime(observedAt)); err != nil {
					return fmt.Errorf("link monitoring template: %w", mapConstraintError(err))
				}
			case err != nil:
				return fmt.Errorf("get monitoring template class: %w", err)
			case existingIncident != incident.ID:
				return fmt.Errorf("%w: template %s", store.ErrMonitoringTemplateClassConflict, templateID)
			}
		}

		occurrenceKey := monitoringBucketKey(observedAt)
		occurrence, err := getMonitoringOccurrenceQ(q, ctx, incident.ID, occurrenceKey)
		newOccurrence := false
		if errors.Is(err, sql.ErrNoRows) {
			newOccurrence = true
			idsJSON, _ := json.Marshal(ids)
			occurrence = &store.MonitoringOccurrence{
				ID: ulid.Make().String(), IncidentID: incident.ID,
				OccurrenceKey: occurrenceKey, SourceID: in.SourceID,
				TemplateIDsJSON: string(idsJSON), Severity: in.Severity,
				EventCount: 1, FirstSeen: observedAt, LastSeen: observedAt,
				Evidence: in.Evidence, CreatedAt: observedAt,
			}
			if err := insertMonitoringOccurrence(q, ctx, occurrence); err != nil {
				return err
			}
		} else if err != nil {
			return fmt.Errorf("get monitoring occurrence: %w", err)
		} else {
			merged := mergeTemplateIDsJSON(occurrence.TemplateIDsJSON, ids)
			evidence := occurrence.Evidence
			if evidence == "" {
				evidence = in.Evidence
			}
			severity := occurrence.Severity
			if severityHigher(in.Severity, severity) {
				severity = in.Severity
			}
			if _, err := q.ExecContext(ctx, `UPDATE monitoring_occurrences
				SET template_ids_json = ?, severity = ?, last_seen = ?, evidence = ?
				WHERE id = ?`, merged, severity, formatTime(observedAt), evidence, occurrence.ID); err != nil {
				return fmt.Errorf("merge monitoring occurrence: %w", err)
			}
		}

		severity := incident.Severity
		severityEscalated := severityHigher(in.Severity, severity)
		if severityHigher(in.Severity, severity) {
			severity = in.Severity
		}
		// This call is the classifier's durable judgement. Automatic mapped
		// occurrences may have raised the raw severity before the worker ran;
		// always accept the subsequent disposition even when the occurrence
		// bucket already exists.
		disposition := in.Disposition
		title := incident.Title
		if title == "" || severityEscalated {
			title = in.Title
		}
		occurrenceDelta := 0
		eventDelta := 0
		if newOccurrence {
			occurrenceDelta, eventDelta = 1, 1
		}
		if _, err := q.ExecContext(ctx, `UPDATE monitoring_incidents
			SET severity = ?, disposition = ?, title = ?,
			    occurrence_count = occurrence_count + ?, event_count = event_count + ?,
			    last_seen = CASE WHEN last_seen < ? THEN ? ELSE last_seen END,
			    updated_at = ?
			WHERE id = ?`, severity, disposition, title, occurrenceDelta, eventDelta,
			formatTime(observedAt), formatTime(observedAt), formatTime(observedAt), incident.ID); err != nil {
			return fmt.Errorf("update monitoring incident: %w", err)
		}

		incident, err = getMonitoringIncidentByIDQ(q, ctx, incident.ID)
		if err != nil {
			return fmt.Errorf("read monitoring incident after triage: %w", err)
		}
		occurrence, err = getMonitoringOccurrenceQ(q, ctx, incident.ID, occurrenceKey)
		if err != nil {
			return fmt.Errorf("read monitoring occurrence after triage: %w", err)
		}
		// observedAt, not the wall clock, is "now": it is the timeline the
		// incident's own first_seen/last_seen live on, so the verdict stays
		// deterministic and correct when collection lags.
		decision := monitoringNotificationDue(incident, newIncident, observedAt)
		result = &store.MonitoringTriageResult{
			Incident: incident, Occurrence: occurrence,
			NewIncident: newIncident, NewOccurrence: newOccurrence,
			ShouldNotify: decision.Notify, NotificationReason: decision.Reason,
			EffectiveSeverity: decision.EffectiveSeverity,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (d *DB) CompleteMonitoringTriage(ctx context.Context, in store.MonitoringTriageCompletion) error {
	if strings.TrimSpace(in.WorkspaceID) == "" || !store.ValidMonitoringDisposition(in.Disposition) {
		return errors.New("CompleteMonitoringTriage: workspace_id and valid disposition required")
	}
	ids := uniqueStrings(in.TemplateIDs)
	if len(ids) == 0 {
		return errors.New("CompleteMonitoringTriage: template_ids required")
	}
	completedAt := in.CompletedAt.UTC()
	if completedAt.IsZero() {
		completedAt = time.Now().UTC()
	}
	return d.withTx(ctx, func(q queryable) error {
		if _, err := requireMonitoringTemplates(q, ctx, in.WorkspaceID, ids); err != nil {
			return err
		}
		if in.IncidentID != "" {
			incident, err := getMonitoringIncidentByIDQ(q, ctx, in.IncidentID)
			if errors.Is(err, sql.ErrNoRows) || (err == nil && incident.WorkspaceID != in.WorkspaceID) {
				return store.ErrMonitoringIncidentNotFound
			}
			if err != nil {
				return err
			}
		}
		args := []any{formatTime(completedAt)}
		query := `UPDATE log_templates SET triaged_at = ?, triaged_severity = severity`
		if in.Disposition == store.MonitoringDispositionBenign {
			query += `, acked = 1, ack_note = ?`
			args = append(args, truncateMonitoringText(in.Note, 1000))
		}
		query += ` WHERE id IN (` + placeholders(len(ids)) + `)`
		for _, id := range ids {
			args = append(args, id)
		}
		if _, err := q.ExecContext(ctx, query, args...); err != nil {
			return fmt.Errorf("complete monitoring templates: %w", err)
		}
		if strings.TrimSpace(in.RunID) == "" {
			return nil
		}
		claimArgs := []any{formatTime(completedAt), in.RunID, in.WorkspaceID}
		claimQuery := `UPDATE monitoring_triage_claims
			SET completed = 1, completed_at = ?
			WHERE run_id = ? AND workspace_id = ? AND template_id IN (` + placeholders(len(ids)) + `)`
		for _, id := range ids {
			claimArgs = append(claimArgs, id)
		}
		if _, err := q.ExecContext(ctx, claimQuery, claimArgs...); err != nil {
			return fmt.Errorf("complete monitoring triage claims: %w", err)
		}
		var existingWorkspace string
		err := q.QueryRowContext(ctx, `SELECT workspace_id FROM monitoring_triage_receipts WHERE run_id = ?`, in.RunID).Scan(&existingWorkspace)
		if err == nil {
			if existingWorkspace != in.WorkspaceID {
				return fmt.Errorf("triage receipt %s belongs to another workspace", in.RunID)
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("check monitoring triage receipt: %w", err)
		}
		if _, err := q.ExecContext(ctx, `INSERT INTO monitoring_triage_receipts
			(run_id, workspace_id, incident_id, disposition, completed_at)
			VALUES (?, ?, ?, ?, ?)`, in.RunID, in.WorkspaceID,
			nullString(in.IncidentID), in.Disposition, formatTime(completedAt)); err != nil {
			return fmt.Errorf("insert monitoring triage receipt: %w", mapConstraintError(err))
		}
		return nil
	})
}

func (d *DB) ClaimMonitoringTriageTemplates(
	ctx context.Context, workspaceID, runID string, templateIDs []string, at time.Time,
) error {
	ids := uniqueStrings(templateIDs)
	if workspaceID == "" || runID == "" || len(ids) == 0 {
		return errors.New("ClaimMonitoringTriageTemplates: workspace_id, run_id and template_ids required")
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	return d.withTx(ctx, func(q queryable) error {
		var runExists int
		if err := q.QueryRowContext(ctx, `SELECT EXISTS(
			SELECT 1 FROM worker_runs WHERE id = ? AND workspace_id = ?
		)`, runID, workspaceID).Scan(&runExists); err != nil {
			return fmt.Errorf("validate monitoring triage run: %w", err)
		}
		if runExists == 0 {
			return store.ErrWorkerRunNotFound
		}
		if _, err := requireMonitoringTemplates(q, ctx, workspaceID, ids); err != nil {
			return err
		}
		for _, templateID := range ids {
			if _, err := q.ExecContext(ctx, `INSERT INTO monitoring_triage_claims
				(run_id, workspace_id, template_id, completed, claimed_at)
				VALUES (?, ?, ?, 0, ?) ON CONFLICT(run_id, template_id) DO NOTHING`,
				runID, workspaceID, templateID, formatTime(at.UTC())); err != nil {
				return fmt.Errorf("claim monitoring triage template: %w", err)
			}
		}
		return nil
	})
}

func (d *DB) MarkMonitoringIncidentNotified(
	ctx context.Context, incidentID, severity string, at time.Time,
) error {
	if !store.ValidSeverity(severity) {
		return fmt.Errorf("invalid notified severity %q", severity)
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	res, err := d.q.ExecContext(ctx, `UPDATE monitoring_incidents
		SET last_notified_at = ?, last_notified_severity = ?, updated_at = ?
		WHERE id = ?`, formatTime(at.UTC()), severity, formatTime(at.UTC()), incidentID)
	if err != nil {
		return fmt.Errorf("mark monitoring incident notified: %w", err)
	}
	return requireRowAffected(res, store.ErrMonitoringIncidentNotFound)
}

func (d *DB) HasMonitoringTriageReceipt(
	ctx context.Context, workspaceID, runID string,
) (bool, error) {
	if workspaceID == "" || runID == "" {
		return false, nil
	}
	var claimCount, incomplete int
	err := d.q.QueryRowContext(ctx, `SELECT COUNT(*),
		COALESCE(SUM(CASE WHEN completed = 0 THEN 1 ELSE 0 END), 0)
		FROM monitoring_triage_claims WHERE workspace_id = ? AND run_id = ?`,
		workspaceID, runID).Scan(&claimCount, &incomplete)
	if err != nil {
		return false, fmt.Errorf("check monitoring triage claims: %w", err)
	}
	if claimCount > 0 {
		return incomplete == 0, nil
	}
	var receipt int
	err = d.q.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM monitoring_triage_receipts WHERE workspace_id = ? AND run_id = ?
	)`, workspaceID, runID).Scan(&receipt)
	if err != nil {
		return false, fmt.Errorf("check monitoring triage receipt: %w", err)
	}
	return receipt != 0, nil
}

// recordMappedMonitoringOccurrence is called in the same transaction as a
// template upsert. Once a template has been classified, repeat log batches
// increment the current occurrence bucket and incident counters with no AI.
func recordMappedMonitoringOccurrence(
	ctx context.Context, q queryable, t *store.LogTemplate, n int64,
) error {
	incident, err := getMonitoringIncidentByTemplateQ(q, ctx, t.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get mapped monitoring incident: %w", err)
	}
	seen := t.LastSeen.UTC()
	if seen.IsZero() {
		seen = time.Now().UTC()
	}
	key := monitoringBucketKey(seen)
	occurrence, err := getMonitoringOccurrenceQ(q, ctx, incident.ID, key)
	newOccurrence := false
	if errors.Is(err, sql.ErrNoRows) {
		newOccurrence = true
		idsJSON, _ := json.Marshal([]string{t.ID})
		occurrence = &store.MonitoringOccurrence{
			ID: ulid.Make().String(), IncidentID: incident.ID,
			OccurrenceKey: key, SourceID: t.SourceID,
			TemplateIDsJSON: string(idsJSON), Severity: t.Severity,
			EventCount: n, FirstSeen: seen, LastSeen: seen, CreatedAt: seen,
		}
		if err := insertMonitoringOccurrence(q, ctx, occurrence); err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else {
		severity := occurrence.Severity
		if severityHigher(t.Severity, severity) {
			severity = t.Severity
		}
		if _, err := q.ExecContext(ctx, `UPDATE monitoring_occurrences
			SET template_ids_json = ?, severity = ?, event_count = event_count + ?,
			    last_seen = CASE WHEN last_seen < ? THEN ? ELSE last_seen END
			WHERE id = ?`, mergeTemplateIDsJSON(occurrence.TemplateIDsJSON, []string{t.ID}),
			severity, n, formatTime(seen), formatTime(seen), occurrence.ID); err != nil {
			return fmt.Errorf("update mapped monitoring occurrence: %w", err)
		}
	}
	severity := incident.Severity
	if severityHigher(t.Severity, severity) {
		severity = t.Severity
	}
	occurrenceDelta := 0
	if newOccurrence {
		occurrenceDelta = 1
	}
	if _, err := q.ExecContext(ctx, `UPDATE monitoring_incidents
		SET severity = ?, occurrence_count = occurrence_count + ?,
		    event_count = event_count + ?,
		    last_seen = CASE WHEN last_seen < ? THEN ? ELSE last_seen END,
		    updated_at = ? WHERE id = ?`, severity, occurrenceDelta, n,
		formatTime(seen), formatTime(seen), formatTime(seen), incident.ID); err != nil {
		return fmt.Errorf("update mapped monitoring incident: %w", err)
	}
	return nil
}

func requireMonitoringTask(q queryable, ctx context.Context, workspaceID, taskID string) error {
	var exists int
	err := q.QueryRowContext(ctx, `SELECT EXISTS(
		SELECT 1 FROM tasks WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL
	)`, taskID, workspaceID).Scan(&exists)
	if err != nil {
		return fmt.Errorf("validate monitoring task: %w", err)
	}
	if exists == 0 {
		return store.ErrNotFound
	}
	return nil
}

func requireMonitoringTemplates(
	q queryable, ctx context.Context, workspaceID string, ids []string,
) (string, error) {
	args := make([]any, 0, len(ids)+1)
	args = append(args, workspaceID)
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := q.QueryContext(ctx, `SELECT lt.id, lt.source_id
		FROM log_templates lt JOIN log_sources ls ON ls.id = lt.source_id
		WHERE ls.workspace_id = ? AND lt.id IN (`+placeholders(len(ids))+`)`, args...)
	if err != nil {
		return "", fmt.Errorf("validate monitoring templates: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	seen := map[string]bool{}
	firstSource := ""
	for rows.Next() {
		var id, sourceID string
		if err := rows.Scan(&id, &sourceID); err != nil {
			return "", err
		}
		seen[id] = true
		if firstSource == "" {
			firstSource = sourceID
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if len(seen) != len(ids) {
		return "", store.ErrLogTemplateNotFound
	}
	return firstSource, nil
}

func getMonitoringIncidentByClassQ(
	q queryable, ctx context.Context, workspaceID, classKey string,
) (*store.MonitoringIncident, error) {
	return scanMonitoringIncident(q.QueryRowContext(ctx, `SELECT `+monitoringIncidentCols+`
		FROM monitoring_incidents WHERE workspace_id = ? AND class_key = ?`, workspaceID, classKey))
}

func getMonitoringIncidentByIDQ(
	q queryable, ctx context.Context, id string,
) (*store.MonitoringIncident, error) {
	return scanMonitoringIncident(q.QueryRowContext(ctx, `SELECT `+monitoringIncidentCols+`
		FROM monitoring_incidents WHERE id = ?`, id))
}

func getMonitoringIncidentByTemplateQ(
	q queryable, ctx context.Context, templateID string,
) (*store.MonitoringIncident, error) {
	return scanMonitoringIncident(q.QueryRowContext(ctx, `SELECT `+prefixedCols("i", monitoringIncidentCols)+`
		FROM monitoring_incidents i
		JOIN monitoring_incident_templates it ON it.incident_id = i.id
		WHERE it.template_id = ?`, templateID))
}

func getMonitoringOccurrenceQ(
	q queryable, ctx context.Context, incidentID, key string,
) (*store.MonitoringOccurrence, error) {
	return scanMonitoringOccurrence(q.QueryRowContext(ctx, `SELECT `+monitoringOccurrenceCols+`
		FROM monitoring_occurrences WHERE incident_id = ? AND occurrence_key = ?`, incidentID, key))
}

func insertMonitoringOccurrence(q queryable, ctx context.Context, o *store.MonitoringOccurrence) error {
	_, err := q.ExecContext(ctx, `INSERT INTO monitoring_occurrences (`+monitoringOccurrenceCols+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		o.ID, o.IncidentID, o.OccurrenceKey, o.SourceID, o.TemplateIDsJSON,
		o.Severity, o.EventCount, formatTime(o.FirstSeen), formatTime(o.LastSeen),
		o.Evidence, formatTime(o.CreatedAt))
	if err != nil {
		return fmt.Errorf("insert monitoring occurrence: %w", mapConstraintError(err))
	}
	return nil
}

func scanMonitoringIncident(row interface{ Scan(...any) error }) (*store.MonitoringIncident, error) {
	var i store.MonitoringIncident
	var firstSeen, lastSeen, createdAt, updatedAt string
	var lastNotified sql.NullString
	err := row.Scan(&i.ID, &i.WorkspaceID, &i.ClassKey, &i.TaskID,
		&i.Disposition, &i.Severity, &i.Title, &i.OccurrenceCount, &i.EventCount,
		&firstSeen, &lastSeen, &lastNotified, &i.LastNotifiedSeverity,
		&createdAt, &updatedAt)
	if err != nil {
		return nil, err
	}
	i.FirstSeen, i.LastSeen = parseTime(firstSeen), parseTime(lastSeen)
	i.CreatedAt, i.UpdatedAt = parseTime(createdAt), parseTime(updatedAt)
	if lastNotified.Valid && lastNotified.String != "" {
		t := parseTime(lastNotified.String)
		i.LastNotifiedAt = &t
	}
	return &i, nil
}

func scanMonitoringOccurrence(row interface{ Scan(...any) error }) (*store.MonitoringOccurrence, error) {
	var o store.MonitoringOccurrence
	var firstSeen, lastSeen, createdAt string
	err := row.Scan(&o.ID, &o.IncidentID, &o.OccurrenceKey, &o.SourceID,
		&o.TemplateIDsJSON, &o.Severity, &o.EventCount, &firstSeen, &lastSeen,
		&o.Evidence, &createdAt)
	if err != nil {
		return nil, err
	}
	o.FirstSeen, o.LastSeen, o.CreatedAt = parseTime(firstSeen), parseTime(lastSeen), parseTime(createdAt)
	return &o, nil
}

func monitoringBucketKey(at time.Time) string {
	return fmt.Sprintf("bucket:%d", at.UTC().Truncate(monitoringOccurrenceBucket).Unix())
}

func severityHigher(a, b string) bool { return store.SeverityRank(a) > store.SeverityRank(b) }

func uniqueStrings(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, value := range in {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func mergeTemplateIDsJSON(raw string, add []string) string {
	ids := []string{}
	_ = json.Unmarshal([]byte(raw), &ids)
	ids = uniqueStrings(append(ids, add...))
	b, _ := json.Marshal(ids)
	return string(b)
}

func truncateMonitoringText(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return s[:limit]
}

// prefixedCols qualifies a trusted compile-time comma-separated column list.
func prefixedCols(alias, cols string) string {
	parts := strings.Split(cols, ",")
	for idx := range parts {
		parts[idx] = alias + "." + strings.TrimSpace(parts[idx])
	}
	return strings.Join(parts, ", ")
}
