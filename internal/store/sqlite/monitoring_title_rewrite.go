package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/logwatch/distill"
	"github.com/don-works/mcplexer/internal/store"
)

// RewriteGenericMonitoringTitles replaces novelty-placeholder incident and
// task titles with evidence-derived operator signatures. Safe to re-run:
// non-generic titles are left untouched. limit caps work per call (0→200).
func (d *DB) RewriteGenericMonitoringTitles(
	ctx context.Context, workspaceID string, limit int,
) (rewritten int, err error) {
	if strings.TrimSpace(workspaceID) == "" {
		return 0, fmt.Errorf("RewriteGenericMonitoringTitles: workspace_id required")
	}
	if limit <= 0 {
		limit = 200
	}
	if limit > 1000 {
		limit = 1000
	}
	rows, err := d.db.QueryContext(ctx, `
		SELECT i.id, i.task_id, i.title, i.class_key,
			COALESCE((
				SELECT o.evidence FROM monitoring_occurrences o
				WHERE o.incident_id = i.id AND o.evidence != ''
				ORDER BY o.last_seen DESC LIMIT 1
			), ''),
			COALESCE((
				SELECT t.sample_last FROM monitoring_incident_templates mit
				JOIN log_templates t ON t.id = mit.template_id
				WHERE mit.incident_id = i.id
				ORDER BY t.last_seen DESC LIMIT 1
			), ''),
			COALESCE((
				SELECT t.masked FROM monitoring_incident_templates mit
				JOIN log_templates t ON t.id = mit.template_id
				WHERE mit.incident_id = i.id
				ORDER BY t.last_seen DESC LIMIT 1
			), '')
		FROM monitoring_incidents i
		WHERE i.workspace_id = ?
		ORDER BY i.last_seen DESC
		LIMIT ?`, workspaceID, limit)
	if err != nil {
		return 0, fmt.Errorf("list incidents for title rewrite: %w", err)
	}
	defer func() { _ = rows.Close() }()

	type item struct {
		id, taskID, title, classKey, evidence, sample, masked string
	}
	var items []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.id, &it.taskID, &it.title, &it.classKey,
			&it.evidence, &it.sample, &it.masked); err != nil {
			return 0, err
		}
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}

	now := time.Now().UTC()
	for _, it := range items {
		if !distill.IsGenericMonitoringTitle(it.title) {
			continue
		}
		next := distill.ImproveMonitoringTitle(it.title, it.evidence, it.sample, it.masked)
		if next == "" || next == it.title || distill.IsGenericMonitoringTitle(next) {
			// Last resort: correlation class text without the prefix.
			if ck := strings.TrimPrefix(it.classKey, "correlation:"); ck != "" && ck != it.classKey {
				next = ck
			} else {
				continue
			}
		}
		if next == it.title {
			continue
		}
		if err := d.applyMonitoringTitleRewrite(ctx, workspaceID, it.id, it.taskID, next, now); err != nil {
			return rewritten, err
		}
		rewritten++
	}
	return rewritten, nil
}

func (d *DB) applyMonitoringTitleRewrite(
	ctx context.Context, workspaceID, incidentID, taskID, title string, at time.Time,
) error {
	return d.withTx(ctx, func(q queryable) error {
		res, err := q.ExecContext(ctx, `
			UPDATE monitoring_incidents SET title = ?, updated_at = ?
			WHERE id = ? AND workspace_id = ?`,
			truncateMonitoringText(title, 500), formatTime(at), incidentID, workspaceID)
		if err != nil {
			return fmt.Errorf("update incident title: %w", err)
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return store.ErrMonitoringIncidentNotFound
		}
		if strings.TrimSpace(taskID) == "" {
			return nil
		}
		// Best-effort task title sync so the dashboard and Chat renotify path
		// agree. Soft-deleted tasks are skipped.
		_, err = q.ExecContext(ctx, `
			UPDATE tasks SET title = ?, updated_at = ?
			WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL`,
			truncateMonitoringText(title, 500), formatTime(at), taskID, workspaceID)
		if err != nil && err != sql.ErrNoRows {
			return fmt.Errorf("update task title: %w", err)
		}
		return nil
	})
}
