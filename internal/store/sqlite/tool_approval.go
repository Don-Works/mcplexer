package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/google/uuid"
)

func (d *DB) CreateToolApproval(ctx context.Context, a *store.ToolApproval) error {
	if a.ID == "" {
		a.ID = uuid.NewString()
	}
	if a.CreatedAt.IsZero() {
		a.CreatedAt = time.Now().UTC()
	}
	if a.Status == "" {
		a.Status = "pending"
	}

	_, err := d.q.ExecContext(ctx, `
		INSERT INTO tool_approvals
			(id, status, request_session_id, request_client_type, request_model,
			 workspace_id, workspace_name, tool_name, arguments, justification,
			 route_rule_id, downstream_server_id, auth_scope_id,
			 approver_session_id, approver_type, resolution,
			 timeout_sec, surface,
			 originating_workspace, kind, summary,
			 created_at, resolved_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ID, a.Status, a.RequestSessionID, a.RequestClientType, a.RequestModel,
		a.WorkspaceID, a.WorkspaceName, a.ToolName, a.Arguments, a.Justification,
		a.RouteRuleID, a.DownstreamServerID, a.AuthScopeID,
		a.ApproverSessionID, a.ApproverType, a.Resolution,
		a.TimeoutSec, a.Surface,
		a.OriginatingWorkspace, a.Kind, a.Summary,
		formatTime(a.CreatedAt), formatTimePtr(a.ResolvedAt),
	)
	return err
}

func (d *DB) GetToolApproval(ctx context.Context, id string) (*store.ToolApproval, error) {
	row := d.q.QueryRowContext(ctx, `
		SELECT id, status, request_session_id, request_client_type, request_model,
		       workspace_id, workspace_name, tool_name, arguments, justification,
		       route_rule_id, downstream_server_id, auth_scope_id,
		       approver_session_id, approver_type, resolution,
		       timeout_sec, surface,
		       originating_workspace, kind, summary,
		       created_at, resolved_at
		FROM tool_approvals WHERE id = ?`, id)

	return scanToolApproval(row)
}

func (d *DB) ListPendingApprovals(ctx context.Context) ([]store.ToolApproval, error) {
	rows, err := d.q.QueryContext(ctx, `
		SELECT id, status, request_session_id, request_client_type, request_model,
		       workspace_id, workspace_name, tool_name, arguments, justification,
		       route_rule_id, downstream_server_id, auth_scope_id,
		       approver_session_id, approver_type, resolution,
		       timeout_sec, surface,
		       originating_workspace, kind, summary,
		       created_at, resolved_at
		FROM tool_approvals
		WHERE status = 'pending'
		ORDER BY created_at ASC`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []store.ToolApproval
	for rows.Next() {
		a, err := scanToolApprovalRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

// defaultResolvedApprovalLimit caps the history query when the caller
// passes limit <= 0, so an unbounded SELECT can't hand the dashboard the
// entire approval table.
const defaultResolvedApprovalLimit = 200

func (d *DB) ListResolvedApprovals(ctx context.Context, limit int) ([]store.ToolApproval, error) {
	if limit <= 0 {
		limit = defaultResolvedApprovalLimit
	}
	rows, err := d.q.QueryContext(ctx, `
		SELECT id, status, request_session_id, request_client_type, request_model,
		       workspace_id, workspace_name, tool_name, arguments, justification,
		       route_rule_id, downstream_server_id, auth_scope_id,
		       approver_session_id, approver_type, resolution,
		       timeout_sec, surface,
		       originating_workspace, kind, summary,
		       created_at, resolved_at
		FROM tool_approvals
		WHERE status != 'pending'
		ORDER BY COALESCE(resolved_at, created_at) DESC, created_at DESC
		LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []store.ToolApproval
	for rows.Next() {
		a, err := scanToolApprovalRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

func (d *DB) ResolveToolApproval(
	ctx context.Context,
	id, status, approverSessionID, approverType, resolution string,
) error {
	now := formatTime(time.Now().UTC())
	res, err := d.q.ExecContext(ctx, `
		UPDATE tool_approvals
		SET status = ?, approver_session_id = ?, approver_type = ?,
		    resolution = ?, resolved_at = ?
		WHERE id = ? AND status = 'pending'`,
		status, approverSessionID, approverType, resolution, now, id,
	)
	if err != nil {
		return err
	}
	return checkRowsAffected(res)
}

func (d *DB) ExpirePendingApprovals(ctx context.Context, before time.Time) (int, error) {
	res, err := d.q.ExecContext(ctx, `
		UPDATE tool_approvals
		SET status = 'timeout', resolved_at = ?
		WHERE status = 'pending' AND created_at < ?`,
		formatTime(time.Now().UTC()), formatTime(before),
	)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

func (d *DB) GetApprovalMetrics(
	ctx context.Context, after, before time.Time,
) (*store.ApprovalMetrics, error) {
	var m store.ApprovalMetrics
	err := d.q.QueryRowContext(ctx, `
		SELECT
			COUNT(*) FILTER (WHERE status = 'pending') AS pending_count,
			COUNT(*) FILTER (WHERE status = 'approved') AS approved_count,
			COUNT(*) FILTER (WHERE status = 'denied') AS denied_count,
			COUNT(*) FILTER (WHERE status = 'timeout') AS timed_out_count,
			COALESCE(AVG(
				CASE WHEN resolved_at IS NOT NULL
				THEN (julianday(resolved_at) - julianday(created_at)) * 86400000
				END
			), 0) AS avg_wait_ms
		FROM tool_approvals
		WHERE created_at >= ? AND created_at <= ?`,
		formatTime(after), formatTime(before),
	).Scan(&m.PendingCount, &m.ApprovedCount, &m.DeniedCount, &m.TimedOutCount, &m.AvgWaitMs)
	if err != nil {
		return nil, err
	}
	return &m, nil
}

func scanToolApproval(row *sql.Row) (*store.ToolApproval, error) {
	var a store.ToolApproval
	var createdAt string
	var resolvedAt *string
	err := row.Scan(
		&a.ID, &a.Status, &a.RequestSessionID, &a.RequestClientType, &a.RequestModel,
		&a.WorkspaceID, &a.WorkspaceName, &a.ToolName, &a.Arguments, &a.Justification,
		&a.RouteRuleID, &a.DownstreamServerID, &a.AuthScopeID,
		&a.ApproverSessionID, &a.ApproverType, &a.Resolution,
		&a.TimeoutSec, &a.Surface,
		&a.OriginatingWorkspace, &a.Kind, &a.Summary,
		&createdAt, &resolvedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	a.CreatedAt = parseTime(createdAt)
	a.ResolvedAt = parseTimePtr(resolvedAt)
	return &a, nil
}

func scanToolApprovalRow(row rowScanner) (*store.ToolApproval, error) {
	var a store.ToolApproval
	var createdAt string
	var resolvedAt *string
	err := row.Scan(
		&a.ID, &a.Status, &a.RequestSessionID, &a.RequestClientType, &a.RequestModel,
		&a.WorkspaceID, &a.WorkspaceName, &a.ToolName, &a.Arguments, &a.Justification,
		&a.RouteRuleID, &a.DownstreamServerID, &a.AuthScopeID,
		&a.ApproverSessionID, &a.ApproverType, &a.Resolution,
		&a.TimeoutSec, &a.Surface,
		&a.OriginatingWorkspace, &a.Kind, &a.Summary,
		&createdAt, &resolvedAt,
	)
	if err != nil {
		return nil, err
	}
	a.CreatedAt = parseTime(createdAt)
	a.ResolvedAt = parseTimePtr(resolvedAt)
	return &a, nil
}
