package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// Reversal half of the task-resolution feedback loop (migration 147).
//
// Every suppression this daemon applies must be undoable, and there is exactly
// one implementation of "undo a suppression" — restoreMonitoringIncidentQ —
// shared by the operator-facing clear, the task-reopen path, and the
// recurrence break-out inside RecordMonitoringTriage. A second, divergent
// reversal is how a suppression ends up half-lifted and an alert stays silent.

// ClearMonitoringResolutionForTask reverses the resolution attached to a task.
func (d *DB) ClearMonitoringResolutionForTask(
	ctx context.Context, workspaceID, taskID, reason, bySession string,
) (*store.MonitoringResolution, error) {
	return d.clearMonitoringResolution(ctx, workspaceID, "task_id = ?", taskID, reason, bySession)
}

// ClearMonitoringResolution reverses the resolution on one incident — the
// operator-facing "unsuppress this" path.
func (d *DB) ClearMonitoringResolution(
	ctx context.Context, workspaceID, incidentID, reason, bySession string,
) (*store.MonitoringResolution, error) {
	return d.clearMonitoringResolution(ctx, workspaceID, "incident_id = ?", incidentID, reason, bySession)
}

// clearMonitoringResolution is the single reversal implementation. It is
// idempotent: with no live resolution it returns (nil, nil) rather than an
// error, so the recurrence path can call it unconditionally.
func (d *DB) clearMonitoringResolution(
	ctx context.Context, workspaceID, predicate, key, reason, bySession string,
) (*store.MonitoringResolution, error) {
	if strings.TrimSpace(workspaceID) == "" || strings.TrimSpace(key) == "" {
		return nil, errors.New("clear monitoring resolution: workspace_id and key required")
	}
	if strings.TrimSpace(reason) == "" {
		reason = store.MonitoringClearReasonManual
	}
	now := time.Now().UTC()
	var out *store.MonitoringResolution
	err := d.withTx(ctx, func(q queryable) error {
		row, err := scanMonitoringResolution(q.QueryRowContext(ctx,
			`SELECT `+monitoringResolutionCols+` FROM monitoring_resolutions
			 WHERE workspace_id = ? AND `+predicate+` AND cleared_at IS NULL`,
			workspaceID, key))
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read monitoring resolution: %w", err)
		}
		if err := restoreMonitoringIncidentQ(q, ctx, row, now); err != nil {
			return err
		}
		if _, err := q.ExecContext(ctx, `UPDATE monitoring_resolutions
			SET cleared_at = ?, cleared_reason = ?, cleared_by_session = ?, updated_at = ?
			WHERE incident_id = ?`, formatTime(now), truncateMonitoringText(reason, 100),
			truncateMonitoringText(bySession, 200), formatTime(now), row.IncidentID); err != nil {
			return fmt.Errorf("clear monitoring resolution: %w", err)
		}
		row.ClearedAt, row.ClearedReason, row.ClearedBySession = &now, reason, bySession
		row.UpdatedAt = now
		out = row
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// restoreMonitoringIncidentQ undoes exactly what the resolution did and
// nothing else. A "fixed" resolution suppressed nothing, so it restores
// nothing — but it still clears last_notified_at, because clearing is only
// ever triggered by a recurrence or an explicit operator reversal, and in both
// cases the next observation must be allowed to speak.
func restoreMonitoringIncidentQ(
	q queryable, ctx context.Context, row *store.MonitoringResolution, now time.Time,
) error {
	if row.Outcome == store.MonitoringOutcomeBenign {
		disposition := row.DispositionBefore
		if !store.ValidMonitoringDisposition(disposition) || disposition == store.MonitoringDispositionBenign {
			// Fail safe: an unknown or benign prior value must not leave the
			// incident muted after an explicit unsuppress.
			disposition = store.MonitoringDispositionActionable
		}
		if _, err := q.ExecContext(ctx, `UPDATE monitoring_incidents
			SET disposition = ?, updated_at = ? WHERE id = ?`,
			disposition, formatTime(now), row.IncidentID); err != nil {
			return fmt.Errorf("restore incident disposition: %w", err)
		}
		if len(row.AckedTemplateIDs) > 0 {
			args := make([]any, 0, len(row.AckedTemplateIDs))
			for _, id := range row.AckedTemplateIDs {
				args = append(args, id)
			}
			// triaged_at is nulled alongside acked so the class re-enters the
			// durable pending queue. Un-acking without this leaves the shape
			// permanently invisible to the worker, which is the silent-alert
			// failure mode this whole path exists to prevent.
			if _, err := q.ExecContext(ctx, `UPDATE log_templates
				SET acked = 0, ack_note = '', triaged_at = NULL
				WHERE id IN (`+placeholders(len(row.AckedTemplateIDs))+`)`, args...); err != nil {
				return fmt.Errorf("un-ack incident templates: %w", err)
			}
		}
	}
	if _, err := q.ExecContext(ctx, `UPDATE monitoring_incidents
		SET last_notified_at = NULL, last_notified_severity = '', updated_at = ?
		WHERE id = ?`, formatTime(now), row.IncidentID); err != nil {
		return fmt.Errorf("reset incident notification state: %w", err)
	}
	return nil
}

func upsertMonitoringResolutionQ(q queryable, ctx context.Context, r *store.MonitoringResolution) error {
	idsJSON, err := json.Marshal(r.AckedTemplateIDs)
	if err != nil {
		return fmt.Errorf("encode acked template ids: %w", err)
	}
	// REPLACE keeps exactly one row per incident: re-resolving after a
	// recurrence overwrites the stale receipt rather than accumulating one row
	// per cycle (the duplicate-by-accretion pattern this epic is fixing).
	if _, err := q.ExecContext(ctx, `INSERT OR REPLACE INTO monitoring_resolutions
		(`+monitoringResolutionCols+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, '', '', ?, ?)`,
		r.IncidentID, r.WorkspaceID, r.TaskID, r.Outcome, r.StatusText,
		r.DispositionBefore, r.SeverityAtResolution, string(idsJSON),
		formatTime(r.ResolvedAt), r.ResolvedBySession, r.ResolvedByActor,
		formatTime(r.CreatedAt), formatTime(r.UpdatedAt)); err != nil {
		return fmt.Errorf("upsert monitoring resolution: %w", err)
	}
	return nil
}

// breakMonitoringSuppressionQ is the in-transaction reversal used by
// RecordMonitoringTriage when fresh triage lands on a still-suppressed
// incident. It shares restoreMonitoringIncidentQ with the operator-facing
// clear so there is exactly one definition of "undo a suppression".
//
// A missing resolution row is not an error: an incident can be benign without
// a receipt (a pre-147 row, or a hand-edited disposition), and the caller's
// own UPDATE will lift the disposition regardless.
func breakMonitoringSuppressionQ(q queryable, ctx context.Context, incidentID string, at time.Time) error {
	row, err := scanMonitoringResolution(q.QueryRowContext(ctx,
		`SELECT `+monitoringResolutionCols+` FROM monitoring_resolutions
		 WHERE incident_id = ? AND cleared_at IS NULL`, incidentID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read monitoring resolution for recurrence: %w", err)
	}
	if err := restoreMonitoringIncidentQ(q, ctx, row, at); err != nil {
		return err
	}
	if _, err := q.ExecContext(ctx, `UPDATE monitoring_resolutions
		SET cleared_at = ?, cleared_reason = ?, updated_at = ?
		WHERE incident_id = ?`, formatTime(at), store.MonitoringClearReasonRecurrence,
		formatTime(at), incidentID); err != nil {
		return fmt.Errorf("clear monitoring resolution on recurrence: %w", err)
	}
	return nil
}

func getMonitoringIncidentByTaskQ(
	q queryable, ctx context.Context, workspaceID, taskID string,
) (*store.MonitoringIncident, error) {
	return scanMonitoringIncident(q.QueryRowContext(ctx, `SELECT `+monitoringIncidentReadCols+`
		FROM monitoring_incidents WHERE workspace_id = ? AND task_id = ?`, workspaceID, taskID))
}
