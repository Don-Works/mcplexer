package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// Task-resolution feedback into monitoring triage (migration 147).
//
// Everything here is deterministic bookkeeping inside one transaction over
// columns the daemon already writes. No model is consulted; the entire point
// is to stop waking one.
//
// The suppression contract, in one place so it can be audited:
//
//   - Only outcome "benign" suppresses. It writes disposition=benign (which
//     monitoringNotificationDue already mutes — this file calls that policy by
//     writing the disposition it reads, it does not reimplement it) and acks
//     the incident's currently linked templates so they leave the pending
//     queue and stop counting toward novelty wake-ups.
//   - Outcome "fixed" writes a resolution row for visibility and changes
//     NOTHING about notification or acks. A later recurrence of a fixed class
//     notifies exactly as it would have if the task had never been closed.
//   - Every suppression is reversible from its own row: disposition_before and
//     acked_template_ids_json are the undo log, and clearing also nulls
//     last_notified_at so the next observation is guaranteed to notify.

// The tasks service discovers this surface by type-asserting its store rather
// than through store.Store (which a test double also implements). That makes a
// silent detachment possible — the service would simply go quiet and every
// resolution would stop feeding back — so assert the contract at compile time.
var _ store.MonitoringResolutionStore = (*DB)(nil)

const monitoringResolutionCols = `incident_id, workspace_id, task_id, outcome,
    status_text, disposition_before, severity_at_resolution,
    acked_template_ids_json, resolved_at, resolved_by_session, resolved_by_actor,
    cleared_at, cleared_reason, cleared_by_session, created_at, updated_at`

// ApplyMonitoringTaskResolution feeds a terminal task back to its incident.
//
// Returns (nil, nil) when the task is not linked to a monitoring incident.
// That is the common case — most tasks in a workspace are hand-written — and
// it must never be an error, or every ordinary task closure starts failing.
func (d *DB) ApplyMonitoringTaskResolution(
	ctx context.Context, in store.MonitoringResolutionInput,
) (*store.MonitoringResolution, error) {
	if strings.TrimSpace(in.WorkspaceID) == "" || strings.TrimSpace(in.TaskID) == "" {
		return nil, errors.New("ApplyMonitoringTaskResolution: workspace_id and task_id required")
	}
	if !store.ValidMonitoringOutcome(in.Outcome) {
		return nil, fmt.Errorf("ApplyMonitoringTaskResolution: invalid outcome %q", in.Outcome)
	}
	resolvedAt := in.ResolvedAt.UTC()
	if resolvedAt.IsZero() {
		resolvedAt = time.Now().UTC()
	}
	var out *store.MonitoringResolution
	err := d.withTx(ctx, func(q queryable) error {
		incident, err := getMonitoringIncidentByTaskQ(q, ctx, in.WorkspaceID, in.TaskID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil // unlinked / legacy task — nothing to feed back
		}
		if err != nil {
			return fmt.Errorf("resolve incident for task: %w", err)
		}
		acked := []string{}
		dispositionBefore := incident.Disposition
		if in.Outcome == store.MonitoringOutcomeBenign {
			if acked, err = suppressMonitoringIncidentQ(q, ctx, incident, resolvedAt); err != nil {
				return err
			}
		}
		row := &store.MonitoringResolution{
			IncidentID: incident.ID, WorkspaceID: incident.WorkspaceID, TaskID: in.TaskID,
			Outcome: in.Outcome, StatusText: truncateMonitoringText(in.StatusText, 100),
			DispositionBefore:    dispositionBefore,
			SeverityAtResolution: incident.Severity,
			AckedTemplateIDs:     acked,
			ResolvedAt:           resolvedAt,
			ResolvedBySession:    truncateMonitoringText(in.BySession, 200),
			ResolvedByActor:      truncateMonitoringText(in.ByActor, 50),
			CreatedAt:            resolvedAt, UpdatedAt: resolvedAt,
		}
		if err := upsertMonitoringResolutionQ(q, ctx, row); err != nil {
			return err
		}
		out = row
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// suppressMonitoringIncidentQ applies the benign suppression and returns the
// template ids it actually acked. Templates already acked are excluded from
// the return value so a later clear cannot un-ack somebody else's ack.
func suppressMonitoringIncidentQ(
	q queryable, ctx context.Context, incident *store.MonitoringIncident, at time.Time,
) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT t.id FROM log_templates t
		JOIN monitoring_incident_templates it ON it.template_id = t.id
		WHERE it.incident_id = ? AND t.acked = 0 ORDER BY t.id`, incident.ID)
	if err != nil {
		return nil, fmt.Errorf("list unacked incident templates: %w", err)
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("scan incident template: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("list unacked incident templates: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close incident templates: %w", err)
	}
	if len(ids) > 0 {
		note := "resolved as not-a-problem on task " + incident.TaskID
		args := []any{note}
		for _, id := range ids {
			args = append(args, id)
		}
		if _, err := q.ExecContext(ctx, `UPDATE log_templates
			SET acked = 1, ack_note = ?
			WHERE id IN (`+placeholders(len(ids))+`)`, args...); err != nil {
			return nil, fmt.Errorf("ack incident templates: %w", err)
		}
	}
	// disposition=benign is the EXISTING mute hook; monitoringNotificationDue
	// short-circuits on it. Writing the disposition is deliberately the only
	// notification change made here — the policy itself is not duplicated.
	if _, err := q.ExecContext(ctx, `UPDATE monitoring_incidents
		SET disposition = ?, updated_at = ? WHERE id = ?`,
		store.MonitoringDispositionBenign, formatTime(at), incident.ID); err != nil {
		return nil, fmt.Errorf("mark incident benign: %w", err)
	}
	return ids, nil
}
