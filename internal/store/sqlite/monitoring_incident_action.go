package sqlite

// monitoring_incident_action.go — SQLite side of store.MonitoringIncidentActionStore
// (migration 150). Acknowledge and silence write the pause columns the
// notification policy reads; dismiss delegates to the EXISTING benign
// suppression + resolution machinery so it is visible and reversible on the same
// paths a benign task resolution is. Everything here is a bounded transaction
// over columns the daemon already writes — no model is consulted.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// The router type-asserts this narrow interface off the concrete store. Assert
// it at compile time so a signature drift is a build failure, not a silent 501.
var _ store.MonitoringIncidentActionStore = (*DB)(nil)

// AckMonitoringIncident pauses re-notification while leaving the incident open.
func (d *DB) AckMonitoringIncident(
	ctx context.Context, in store.MonitoringIncidentActionRef,
) (*store.MonitoringIncidentView, error) {
	if strings.TrimSpace(in.Actor) == "" {
		return nil, store.ErrMonitoringActionActorRequired
	}
	at := monitoringActionTime(in.At)
	return d.mutateIncidentAction(ctx, in.WorkspaceID, in.IncidentID, at,
		func(q queryable, inc *store.MonitoringIncident) error {
			// The floor is the effective severity right now: a later escalation
			// past it pierces the ack. Computed before the write, off columns the
			// pause does not touch, so it is the classifier severity raised by age.
			floor := monitoringEffectiveSeverity(inc, at)
			_, err := q.ExecContext(ctx, `UPDATE monitoring_incidents
				SET acked_at = ?, acked_by = ?, acked_severity = ?, updated_at = ?
				WHERE id = ?`, formatTime(at), truncateMonitoringText(in.Actor, 50),
				floor, formatTime(at), inc.ID)
			return err
		})
}

// UnackMonitoringIncident lifts an acknowledgement so the nag resumes. It is
// idempotent: clearing an already-unacked incident is a no-op.
func (d *DB) UnackMonitoringIncident(
	ctx context.Context, in store.MonitoringIncidentActionRef,
) (*store.MonitoringIncidentView, error) {
	at := monitoringActionTime(in.At)
	return d.mutateIncidentAction(ctx, in.WorkspaceID, in.IncidentID, at,
		func(q queryable, inc *store.MonitoringIncident) error {
			_, err := q.ExecContext(ctx, `UPDATE monitoring_incidents
				SET acked_at = NULL, acked_by = '', acked_severity = '', updated_at = ?
				WHERE id = ?`, formatTime(at), inc.ID)
			return err
		})
}

// SilenceMonitoringIncident pauses re-notification until a bounded expiry.
func (d *DB) SilenceMonitoringIncident(
	ctx context.Context, in store.MonitoringIncidentSilenceInput,
) (*store.MonitoringIncidentView, error) {
	if strings.TrimSpace(in.Actor) == "" {
		return nil, store.ErrMonitoringActionActorRequired
	}
	if in.Duration < store.MonitoringIncidentMinSilence || in.Duration > store.MonitoringIncidentMaxSilence {
		return nil, store.ErrMonitoringSilenceUnbounded
	}
	at := monitoringActionTime(in.At)
	until := at.Add(in.Duration)
	return d.mutateIncidentAction(ctx, in.WorkspaceID, in.IncidentID, at,
		func(q queryable, inc *store.MonitoringIncident) error {
			floor := monitoringEffectiveSeverity(inc, at)
			_, err := q.ExecContext(ctx, `UPDATE monitoring_incidents
				SET silenced_at = ?, silenced_until = ?, silenced_by = ?,
				    silenced_severity = ?, updated_at = ?
				WHERE id = ?`, formatTime(at), formatTime(until),
				truncateMonitoringText(in.Actor, 50), floor, formatTime(at), inc.ID)
			return err
		})
}

// UnsilenceMonitoringIncident clears an active silence. Idempotent.
func (d *DB) UnsilenceMonitoringIncident(
	ctx context.Context, in store.MonitoringIncidentActionRef,
) (*store.MonitoringIncidentView, error) {
	at := monitoringActionTime(in.At)
	return d.mutateIncidentAction(ctx, in.WorkspaceID, in.IncidentID, at,
		func(q queryable, inc *store.MonitoringIncident) error {
			_, err := q.ExecContext(ctx, `UPDATE monitoring_incidents
				SET silenced_at = NULL, silenced_until = NULL, silenced_by = '',
				    silenced_severity = '', updated_at = ?
				WHERE id = ?`, formatTime(at), inc.ID)
			return err
		})
}

// DismissMonitoringIncident resolves the incident as over. It reuses the benign
// suppression path (disposition=benign + acked templates) and writes a reversible
// resolution receipt, so a dismiss is indistinguishable from a benign task
// resolution to the read, clear and recurrence-break paths — including firing
// again if the class recurs. Any live ack/silence pause is cleared: dismiss is
// terminal, so a pending timed pause on the same incident is moot.
func (d *DB) DismissMonitoringIncident(
	ctx context.Context, in store.MonitoringIncidentDismissInput,
) (*store.MonitoringResolution, error) {
	if strings.TrimSpace(in.Actor) == "" {
		return nil, store.ErrMonitoringActionActorRequired
	}
	at := monitoringActionTime(in.At)
	statusText := strings.TrimSpace(in.StatusText)
	if statusText == "" {
		statusText = "dismissed"
	}
	var out *store.MonitoringResolution
	err := d.withTx(ctx, func(q queryable) error {
		inc, err := loadIncidentForActionQ(q, ctx, in.WorkspaceID, in.IncidentID)
		if err != nil {
			return err
		}
		dispositionBefore := inc.Disposition
		acked, err := suppressMonitoringIncidentQ(q, ctx, inc, at)
		if err != nil {
			return err
		}
		row := &store.MonitoringResolution{
			IncidentID: inc.ID, WorkspaceID: inc.WorkspaceID, TaskID: inc.TaskID,
			Outcome: store.MonitoringOutcomeBenign, StatusText: truncateMonitoringText(statusText, 100),
			DispositionBefore: dispositionBefore, SeverityAtResolution: inc.Severity,
			AckedTemplateIDs:  acked,
			ResolvedAt:        at,
			ResolvedBySession: truncateMonitoringText(in.Session, 200),
			ResolvedByActor:   truncateMonitoringText(in.Actor, 50),
			CreatedAt:         at, UpdatedAt: at,
		}
		if err := upsertMonitoringResolutionQ(q, ctx, row); err != nil {
			return err
		}
		if _, err := q.ExecContext(ctx, `UPDATE monitoring_incidents
			SET acked_at = NULL, acked_by = '', acked_severity = '',
			    silenced_at = NULL, silenced_until = NULL, silenced_by = '',
			    silenced_severity = '', updated_at = ?
			WHERE id = ?`, formatTime(at), inc.ID); err != nil {
			return fmt.Errorf("clear pause on dismiss: %w", err)
		}
		out = row
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ListSuppressedMonitoringIncidents returns incidents whose ack or silence is in
// force at now, most recently seen first. A pause that has been pierced by an
// escalation, or a silence that has expired, is not "in force" and is excluded —
// so the list answers "what is actually muted right now", not "what was ever
// paused". Dismissed incidents live on ListMonitoringResolutions.
func (d *DB) ListSuppressedMonitoringIncidents(
	ctx context.Context, workspaceID string, now time.Time, limit int,
) ([]*store.MonitoringIncidentView, error) {
	if strings.TrimSpace(workspaceID) == "" {
		return []*store.MonitoringIncidentView{}, nil
	}
	now = monitoringActionTime(now)
	rows, err := d.q.QueryContext(ctx, `SELECT `+monitoringIncidentReadCols+`
		FROM monitoring_incidents
		WHERE workspace_id = ? AND (acked_at IS NOT NULL OR silenced_until IS NOT NULL)
		ORDER BY last_seen DESC, id DESC LIMIT ?`,
		workspaceID, monitoringIncidentLimit(limit))
	if err != nil {
		return nil, fmt.Errorf("list suppressed monitoring incidents: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	out := []*store.MonitoringIncidentView{}
	for rows.Next() {
		inc, err := scanMonitoringIncident(rows)
		if err != nil {
			return nil, fmt.Errorf("scan suppressed monitoring incident: %w", err)
		}
		// monitoringIncidentView folds escalation-piercing and silence expiry into
		// Suppressed, so a pierced ack or an expired silence — matched by the SQL
		// predicate but no longer in force — is dropped here.
		if view := monitoringIncidentView(inc, now); view.Suppressed {
			out = append(out, view)
		}
	}
	return out, rows.Err()
}

// mutateIncidentAction loads the workspace-scoped incident, applies mut in the
// same transaction, and returns the reloaded view so the caller sees exactly the
// state it wrote. The shared shell keeps every ack/silence verb one small mut.
func (d *DB) mutateIncidentAction(
	ctx context.Context, workspaceID, incidentID string, at time.Time,
	mut func(q queryable, inc *store.MonitoringIncident) error,
) (*store.MonitoringIncidentView, error) {
	var view *store.MonitoringIncidentView
	err := d.withTx(ctx, func(q queryable) error {
		inc, err := loadIncidentForActionQ(q, ctx, workspaceID, incidentID)
		if err != nil {
			return err
		}
		if err := mut(q, inc); err != nil {
			return fmt.Errorf("apply incident action: %w", err)
		}
		reloaded, err := getMonitoringIncidentByIDQ(q, ctx, inc.ID)
		if err != nil {
			return fmt.Errorf("reload incident after action: %w", err)
		}
		view = monitoringIncidentView(reloaded, at)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return view, nil
}

// loadIncidentForActionQ reads one incident scoped to a workspace. A foreign or
// absent id is reported as ErrMonitoringIncidentNotFound, so the error shape can
// never be used to probe for another workspace's incident ids.
func loadIncidentForActionQ(
	q queryable, ctx context.Context, workspaceID, incidentID string,
) (*store.MonitoringIncident, error) {
	if strings.TrimSpace(workspaceID) == "" || strings.TrimSpace(incidentID) == "" {
		return nil, store.ErrMonitoringIncidentNotFound
	}
	inc, err := scanMonitoringIncident(q.QueryRowContext(ctx, `SELECT `+monitoringIncidentReadCols+`
		FROM monitoring_incidents WHERE id = ? AND workspace_id = ?`, incidentID, workspaceID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrMonitoringIncidentNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load incident for action: %w", err)
	}
	return inc, nil
}

func monitoringActionTime(t time.Time) time.Time {
	if t.IsZero() {
		return time.Now().UTC()
	}
	return t.UTC()
}
