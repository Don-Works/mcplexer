package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// listMeshOutboundCap caps a single ListPending result to a defensive ceiling
// even when the caller passes limit <= 0. The queue is bounded by TTL but a
// daemon stuck offline for a week can still accumulate thousands of rows.
const listMeshOutboundCap = 1000

// EnqueueMeshOutbound writes one row into mesh_outbound_queue. message_id is
// UNIQUE; a second call with the same id is treated as a no-op (idempotent
// from the caller's perspective — the row that already exists wins).
func (d *DB) EnqueueMeshOutbound(ctx context.Context, o *store.MeshOutbound) error {
	if o == nil {
		return errors.New("mesh outbound: nil row")
	}
	if o.MessageID == "" || o.TargetPeerID == "" {
		return errors.New("mesh outbound: message_id + target_peer_id required")
	}
	if len(o.Envelope) == 0 {
		return errors.New("mesh outbound: envelope required")
	}
	now := time.Now().UTC()
	if o.EnqueuedAt.IsZero() {
		o.EnqueuedAt = now
	}
	if o.NextAttemptAt.IsZero() {
		o.NextAttemptAt = now
	}
	if o.ExpiresAt.IsZero() {
		o.ExpiresAt = now.Add(7 * 24 * time.Hour)
	}
	_, err := d.q.ExecContext(ctx, `
		INSERT OR IGNORE INTO mesh_outbound_queue
			(message_id, target_peer_id, target_agent_session_id, envelope,
			 attempts, last_error, enqueued_at, next_attempt_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		o.MessageID, o.TargetPeerID, o.TargetAgentSessionID, o.Envelope,
		o.Attempts, o.LastError,
		formatTime(o.EnqueuedAt), formatTime(o.NextAttemptAt),
		formatTime(o.ExpiresAt),
	)
	if err != nil {
		return fmt.Errorf("enqueue mesh outbound: %w", err)
	}
	return nil
}

// ListDueMeshOutbound returns rows whose target peer matches and
// next_attempt_at <= now AND expires_at > now AND delivered_at IS NULL.
// Ordered by enqueued_at ASC so the oldest pending message ships first.
func (d *DB) ListDueMeshOutbound(
	ctx context.Context, peerID string, now time.Time, limit int,
) ([]store.MeshOutbound, error) {
	if peerID == "" {
		return nil, nil
	}
	if limit <= 0 || limit > listMeshOutboundCap {
		limit = listMeshOutboundCap
	}
	rows, err := d.q.QueryContext(ctx, `
		SELECT id, message_id, target_peer_id, target_agent_session_id, envelope,
		       attempts, last_error, enqueued_at, next_attempt_at,
		       delivered_at, expires_at
		FROM mesh_outbound_queue
		WHERE target_peer_id = ?
		  AND delivered_at IS NULL
		  AND next_attempt_at <= ?
		  AND expires_at > ?
		ORDER BY enqueued_at ASC
		LIMIT ?`,
		peerID, formatTime(now), formatTime(now), limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list due mesh outbound: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return scanMeshOutboundRows(rows)
}

// MarkMeshOutboundDelivered stamps delivered_at for the row. Idempotent.
func (d *DB) MarkMeshOutboundDelivered(
	ctx context.Context, messageID string, now time.Time,
) error {
	_, err := d.q.ExecContext(ctx,
		`UPDATE mesh_outbound_queue
		 SET delivered_at = ?
		 WHERE message_id = ? AND delivered_at IS NULL`,
		formatTime(now), messageID,
	)
	return err
}

// BumpMeshOutboundAttempt records a failed retry: attempts++,
// last_error set, next_attempt_at pushed out by the caller-supplied schedule.
func (d *DB) BumpMeshOutboundAttempt(
	ctx context.Context, messageID, lastErr string, nextAttemptAt time.Time,
) error {
	_, err := d.q.ExecContext(ctx, `
		UPDATE mesh_outbound_queue
		SET attempts = attempts + 1,
		    last_error = ?,
		    next_attempt_at = ?
		WHERE message_id = ? AND delivered_at IS NULL`,
		lastErr, formatTime(nextAttemptAt), messageID,
	)
	return err
}

// ListPendingMeshOutbound returns every undelivered, unexpired row across all
// peers. Used by the mesh__list_queue admin tool + the 30s sweeper.
func (d *DB) ListPendingMeshOutbound(
	ctx context.Context, now time.Time, limit int,
) ([]store.MeshOutbound, error) {
	if limit <= 0 || limit > listMeshOutboundCap {
		limit = listMeshOutboundCap
	}
	rows, err := d.q.QueryContext(ctx, `
		SELECT id, message_id, target_peer_id, target_agent_session_id, envelope,
		       attempts, last_error, enqueued_at, next_attempt_at,
		       delivered_at, expires_at
		FROM mesh_outbound_queue
		WHERE delivered_at IS NULL AND expires_at > ?
		ORDER BY next_attempt_at ASC, enqueued_at ASC
		LIMIT ?`,
		formatTime(now), limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list pending mesh outbound: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return scanMeshOutboundRows(rows)
}

// ListExpiredMeshOutbound returns rows whose expires_at < now AND
// delivered_at IS NULL. Used so the daily prune can log them as warns
// before deletion.
func (d *DB) ListExpiredMeshOutbound(
	ctx context.Context, now time.Time, limit int,
) ([]store.MeshOutbound, error) {
	if limit <= 0 || limit > listMeshOutboundCap {
		limit = listMeshOutboundCap
	}
	rows, err := d.q.QueryContext(ctx, `
		SELECT id, message_id, target_peer_id, target_agent_session_id, envelope,
		       attempts, last_error, enqueued_at, next_attempt_at,
		       delivered_at, expires_at
		FROM mesh_outbound_queue
		WHERE delivered_at IS NULL AND expires_at < ?
		ORDER BY enqueued_at ASC
		LIMIT ?`,
		formatTime(now), limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list expired mesh outbound: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return scanMeshOutboundRows(rows)
}

// PruneMeshOutbound deletes delivered rows older than deliveredBefore and
// expired-undelivered rows older than expiredBefore in one batched txn.
// Returns the total row count deleted.
func (d *DB) PruneMeshOutbound(
	ctx context.Context, deliveredBefore, expiredBefore time.Time,
) (int, error) {
	res, err := d.q.ExecContext(ctx, `
		DELETE FROM mesh_outbound_queue
		WHERE (delivered_at IS NOT NULL AND delivered_at < ?)
		   OR (delivered_at IS NULL AND expires_at < ?)`,
		formatTime(deliveredBefore), formatTime(expiredBefore),
	)
	if err != nil {
		return 0, fmt.Errorf("prune mesh outbound: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// scanMeshOutboundRows is the shared row-loop used by Due/Pending/Expired
// listings. Keeps the call sites under the 50-line cap and ensures every
// query parses time columns identically.
func scanMeshOutboundRows(rows *sql.Rows) ([]store.MeshOutbound, error) {
	var out []store.MeshOutbound
	for rows.Next() {
		var (
			row                                  store.MeshOutbound
			enqueuedAt, nextAttemptAt, expiresAt string
			deliveredAt                          sql.NullString
		)
		if err := rows.Scan(
			&row.ID, &row.MessageID, &row.TargetPeerID, &row.TargetAgentSessionID,
			&row.Envelope, &row.Attempts, &row.LastError,
			&enqueuedAt, &nextAttemptAt, &deliveredAt, &expiresAt,
		); err != nil {
			return nil, fmt.Errorf("scan mesh outbound: %w", err)
		}
		row.EnqueuedAt = parseTime(enqueuedAt)
		row.NextAttemptAt = parseTime(nextAttemptAt)
		row.ExpiresAt = parseTime(expiresAt)
		if deliveredAt.Valid {
			t := parseTime(deliveredAt.String)
			row.DeliveredAt = &t
		}
		out = append(out, row)
	}
	return out, rows.Err()
}
