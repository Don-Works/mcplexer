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

// listWorkerApprovalCap bounds ListWorkerApprovals when the caller
// passes a non-positive limit so a runaway ledger can't fill a UI page.
const listWorkerApprovalCap = 500

const workerApprovalCols = `id, worker_id, run_id, tool_name, tool_input,
    reason, status, decision, decided_by, created_at, decided_at,
    resumed_run_id`

// CreateWorkerApproval inserts a new approval row. ID is generated when
// empty; CreatedAt defaults to now. Status defaults to "pending".
func (d *DB) CreateWorkerApproval(ctx context.Context, a *store.WorkerApproval) error {
	if a == nil {
		return errors.New("CreateWorkerApproval: row required")
	}
	if a.WorkerID == "" || a.RunID == "" || a.ToolName == "" {
		return errors.New("CreateWorkerApproval: worker_id/run_id/tool_name required")
	}
	if a.ID == "" {
		a.ID = "wapp-" + uuid.NewString()
	}
	if a.CreatedAt.IsZero() {
		a.CreatedAt = time.Now().UTC()
	}
	if a.Status == "" {
		a.Status = "pending"
	}
	if a.ToolInput == "" {
		a.ToolInput = "{}"
	}
	_, err := d.q.ExecContext(ctx, `
		INSERT INTO worker_approvals (`+workerApprovalCols+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ID, a.WorkerID, a.RunID, a.ToolName, a.ToolInput,
		a.Reason, a.Status, a.Decision, a.DecidedBy,
		formatTime(a.CreatedAt), formatTimePtr(a.DecidedAt),
		a.ResumedRunID,
	)
	if err != nil {
		return fmt.Errorf("insert worker_approval: %w", err)
	}
	return nil
}

// GetWorkerApproval returns one row or ErrWorkerApprovalNotFound.
func (d *DB) GetWorkerApproval(
	ctx context.Context, id string,
) (*store.WorkerApproval, error) {
	row := d.q.QueryRowContext(ctx,
		`SELECT `+workerApprovalCols+` FROM worker_approvals WHERE id = ?`, id)
	a, err := scanWorkerApproval(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrWorkerApprovalNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get worker_approval: %w", err)
	}
	return a, nil
}

// ListWorkerApprovals returns approvals filtered by status (empty
// matches every status). Ordered created_at DESC. limit <= 0 falls
// back to listWorkerApprovalCap.
func (d *DB) ListWorkerApprovals(
	ctx context.Context, status string, limit int,
) ([]*store.WorkerApproval, error) {
	if limit <= 0 || limit > listWorkerApprovalCap {
		limit = listWorkerApprovalCap
	}
	query := `SELECT ` + workerApprovalCols + ` FROM worker_approvals`
	args := []any{}
	if status != "" {
		query += ` WHERE status = ?`
		args = append(args, status)
	}
	query += ` ORDER BY created_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := d.q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list worker_approvals: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]*store.WorkerApproval, 0, limit)
	for rows.Next() {
		a, err := scanWorkerApproval(rows)
		if err != nil {
			return nil, fmt.Errorf("scan worker_approval: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// DecideWorkerApproval transitions a row from pending to terminal.
// Returns ErrWorkerApprovalNotFound when the row is missing OR when
// status != "pending" (idempotent — same row can't be decided twice).
func (d *DB) DecideWorkerApproval(
	ctx context.Context, id, decision, decidedBy, resumedRunID string,
	decidedAt time.Time,
) error {
	if decision != "approved" && decision != "rejected" {
		return fmt.Errorf("decision %q invalid (want approved|rejected)", decision)
	}
	if decidedAt.IsZero() {
		decidedAt = time.Now().UTC()
	}
	status := decision // 1:1 mapping for now (status mirrors decision)
	res, err := d.q.ExecContext(ctx, `
		UPDATE worker_approvals
		SET status = ?, decision = ?, decided_by = ?,
		    decided_at = ?, resumed_run_id = ?
		WHERE id = ? AND status = 'pending'`,
		status, decision, decidedBy, formatTime(decidedAt), resumedRunID, id,
	)
	if err != nil {
		return fmt.Errorf("update worker_approval: %w", err)
	}
	return mapNotFound(res, store.ErrWorkerApprovalNotFound)
}

// CountPendingWorkerApprovals is the cheap dashboard-badge query.
func (d *DB) CountPendingWorkerApprovals(ctx context.Context) (int, error) {
	var n int
	err := d.q.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM worker_approvals WHERE status = 'pending'`,
	).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count pending worker_approvals: %w", err)
	}
	return n, nil
}

func scanWorkerApproval(r scanner) (*store.WorkerApproval, error) {
	var (
		a         store.WorkerApproval
		createdAt string
		decidedAt sql.NullString
	)
	err := r.Scan(
		&a.ID, &a.WorkerID, &a.RunID, &a.ToolName, &a.ToolInput,
		&a.Reason, &a.Status, &a.Decision, &a.DecidedBy,
		&createdAt, &decidedAt, &a.ResumedRunID,
	)
	if err != nil {
		return nil, err
	}
	a.CreatedAt = parseTime(createdAt)
	if decidedAt.Valid {
		t := parseTime(decidedAt.String)
		a.DecidedAt = &t
	}
	return &a, nil
}
