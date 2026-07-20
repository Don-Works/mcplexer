package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/don-works/mcplexer/internal/store"
)

// The operator visibility path for suppression: "what is currently muted, why,
// by whom, since when — and is the reason still true?".
//
// The projection deliberately joins the incident AND the canonical task, so a
// stale suppression (resolution still live but the task has been reopened by
// hand) shows up as an inconsistency the operator can see, rather than as
// silence. A suppression nobody can enumerate is indistinguishable from a bug.

// ListMonitoringResolutions returns resolution receipts newest first.
// suppressingOnly restricts to live benign rows — the ones actually muting
// notifications and novelty wake-ups right now.
func (d *DB) ListMonitoringResolutions(
	ctx context.Context, workspaceID string, suppressingOnly bool, limit int,
) ([]*store.MonitoringResolution, error) {
	if workspaceID == "" {
		return []*store.MonitoringResolution{}, nil
	}
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	query := `SELECT ` + prefixedCols("r", monitoringResolutionCols) + `,
			i.class_key, i.title, i.severity, i.disposition, i.last_seen,
			COALESCE(t.status, ''), CASE WHEN t.closed_at IS NULL THEN 0 ELSE 1 END
		FROM monitoring_resolutions r
		JOIN monitoring_incidents i ON i.id = r.incident_id
		LEFT JOIN tasks t ON t.id = r.task_id AND t.deleted_at IS NULL
		WHERE r.workspace_id = ?`
	if suppressingOnly {
		query += ` AND r.cleared_at IS NULL AND r.outcome = '` + store.MonitoringOutcomeBenign + `'`
	}
	query += ` ORDER BY r.resolved_at DESC LIMIT ?`

	rows, err := d.q.QueryContext(ctx, query, workspaceID, limit)
	if err != nil {
		return nil, fmt.Errorf("list monitoring resolutions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []*store.MonitoringResolution{}
	for rows.Next() {
		r, err := scanMonitoringResolutionWithIncident(rows)
		if err != nil {
			return nil, fmt.Errorf("scan monitoring resolution: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func scanMonitoringResolution(row interface{ Scan(...any) error }) (*store.MonitoringResolution, error) {
	var r store.MonitoringResolution
	var resolvedAt, createdAt, updatedAt, idsJSON string
	var clearedAt sql.NullString
	if err := row.Scan(&r.IncidentID, &r.WorkspaceID, &r.TaskID, &r.Outcome,
		&r.StatusText, &r.DispositionBefore, &r.SeverityAtResolution, &idsJSON,
		&resolvedAt, &r.ResolvedBySession, &r.ResolvedByActor,
		&clearedAt, &r.ClearedReason, &r.ClearedBySession,
		&createdAt, &updatedAt); err != nil {
		return nil, err
	}
	finishMonitoringResolution(&r, idsJSON, resolvedAt, createdAt, updatedAt, clearedAt)
	return &r, nil
}

func scanMonitoringResolutionWithIncident(row interface{ Scan(...any) error }) (*store.MonitoringResolution, error) {
	var r store.MonitoringResolution
	var resolvedAt, createdAt, updatedAt, idsJSON, lastSeen string
	var clearedAt sql.NullString
	var taskClosed int
	if err := row.Scan(&r.IncidentID, &r.WorkspaceID, &r.TaskID, &r.Outcome,
		&r.StatusText, &r.DispositionBefore, &r.SeverityAtResolution, &idsJSON,
		&resolvedAt, &r.ResolvedBySession, &r.ResolvedByActor,
		&clearedAt, &r.ClearedReason, &r.ClearedBySession,
		&createdAt, &updatedAt,
		&r.ClassKey, &r.IncidentTitle, &r.Severity, &r.Disposition, &lastSeen,
		&r.TaskStatus, &taskClosed); err != nil {
		return nil, err
	}
	finishMonitoringResolution(&r, idsJSON, resolvedAt, createdAt, updatedAt, clearedAt)
	r.IncidentLastSeen = parseTime(lastSeen)
	r.TaskClosed = taskClosed != 0
	return &r, nil
}

func finishMonitoringResolution(
	r *store.MonitoringResolution, idsJSON, resolvedAt, createdAt, updatedAt string,
	clearedAt sql.NullString,
) {
	r.AckedTemplateIDs = []string{}
	if idsJSON != "" {
		_ = json.Unmarshal([]byte(idsJSON), &r.AckedTemplateIDs)
	}
	if r.AckedTemplateIDs == nil {
		r.AckedTemplateIDs = []string{}
	}
	r.ResolvedAt = parseTime(resolvedAt)
	r.CreatedAt, r.UpdatedAt = parseTime(createdAt), parseTime(updatedAt)
	if clearedAt.Valid && clearedAt.String != "" {
		t := parseTime(clearedAt.String)
		r.ClearedAt = &t
	}
}
