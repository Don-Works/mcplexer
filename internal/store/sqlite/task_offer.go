// task_offer.go — store CRUD for the task_offers table (migration 061).
// Cross-peer task offers travel here; the libp2p protocol writes via
// CreateTaskOffer on receive, the local agent updates state via
// UpdateTaskOfferState on accept/decline.
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
	"github.com/oklog/ulid/v2"
)

const taskOfferSelectCols = `id, task_id, remote_task_id, share_id, sender_principal_id,
	access_epoch, visibility_epoch, base_hlc, from_peer_id, to_peer_id,
	remote_workspace_id, remote_workspace_name, workspace_id,
	title, description_preview, meta_preview, status_preview, priority_preview, tags_json,
	is_direct_assign, envelope_nonce, envelope_created_at,
	direction, state, accepted_at, declined_at, declined_reason, created_at`

func (d *DB) CreateTaskOffer(ctx context.Context, o *store.TaskOffer) error {
	if o == nil {
		return errors.New("CreateTaskOffer: nil offer")
	}
	if o.RemoteTaskID == "" || o.FromPeerID == "" || o.ToPeerID == "" || o.Direction == "" {
		return errors.New("CreateTaskOffer: remote_task_id, from_peer_id, to_peer_id, direction required")
	}
	if o.EnvelopeNonce == "" {
		return errors.New("CreateTaskOffer: envelope_nonce required")
	}
	if o.ID == "" {
		o.ID = ulid.Make().String()
	}
	if o.CreatedAt.IsZero() {
		o.CreatedAt = time.Now().UTC()
	}
	if o.EnvelopeCreatedAt.IsZero() {
		o.EnvelopeCreatedAt = o.CreatedAt
	}
	if o.State == "" {
		o.State = store.TaskOfferPending
	}
	tags := normalizeJSON(o.TagsJSON, "[]")
	directAssign := 0
	if o.IsDirectAssign {
		directAssign = 1
	}

	_, err := d.q.ExecContext(ctx, `
		INSERT INTO task_offers (
			id, task_id, remote_task_id, share_id, sender_principal_id,
			access_epoch, visibility_epoch, base_hlc, from_peer_id, to_peer_id,
			remote_workspace_id, remote_workspace_name, workspace_id,
			title, description_preview, meta_preview, status_preview, priority_preview, tags_json,
			is_direct_assign, envelope_nonce, envelope_created_at,
			direction, state, accepted_at, declined_at, declined_reason, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		o.ID, nullString(o.TaskID), o.RemoteTaskID, nullString(o.ShareID),
		nullString(o.SenderPrincipalID), o.AccessEpoch, o.VisibilityEpoch, o.BaseHLC,
		o.FromPeerID, o.ToPeerID,
		o.RemoteWorkspaceID, o.RemoteWorkspaceName, nullString(o.WorkspaceID),
		o.Title, o.DescriptionPreview, o.MetaPreview, o.StatusPreview, o.PriorityPreview, tags,
		directAssign, o.EnvelopeNonce, o.EnvelopeCreatedAt.Unix(),
		o.Direction, o.State, unixOrNil(o.AcceptedAt), unixOrNil(o.DeclinedAt), o.DeclinedReason,
		o.CreatedAt.Unix(),
	)
	if err != nil {
		// Idempotent on duplicate (replay protection via uniq index).
		mapped := mapConstraintError(err)
		if errors.Is(mapped, store.ErrAlreadyExists) {
			return nil
		}
		return fmt.Errorf("create task offer: %w", err)
	}
	return nil
}

func (d *DB) GetTaskOffer(ctx context.Context, id string) (*store.TaskOffer, error) {
	row := d.q.QueryRowContext(ctx,
		`SELECT `+taskOfferSelectCols+` FROM task_offers WHERE id = ?`, id)
	o, err := scanTaskOffer(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get task offer: %w", err)
	}
	return o, nil
}

func (d *DB) ListTaskOffers(ctx context.Context, f store.TaskOfferFilter) ([]store.TaskOffer, error) {
	conds := []string{}
	args := []any{}
	if f.Direction != "" {
		conds = append(conds, "direction = ?")
		args = append(args, f.Direction)
	}
	if f.State != "" {
		conds = append(conds, "state = ?")
		args = append(args, f.State)
	}
	if f.PeerID != "" {
		conds = append(conds, "(from_peer_id = ? OR to_peer_id = ?)")
		args = append(args, f.PeerID, f.PeerID)
	}
	if f.Since != nil {
		conds = append(conds, "created_at >= ?")
		args = append(args, f.Since.Unix())
	}
	where := "1=1"
	if len(conds) > 0 {
		where = strings.Join(conds, " AND ")
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	args = append(args, limit)

	rows, err := d.q.QueryContext(ctx,
		`SELECT `+taskOfferSelectCols+` FROM task_offers
		WHERE `+where+`
		ORDER BY created_at DESC LIMIT ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("list task offers: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []store.TaskOffer
	for rows.Next() {
		o, err := scanTaskOffer(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *o)
	}
	return out, rows.Err()
}

// FindLocalTaskForRemoteOffer returns the local task id produced by a
// previously-accepted incoming offer from (fromPeerID, remoteTaskID).
// Used by the linked-workspace receive path to converge a re-pushed task
// onto its existing local row. Joins to tasks to skip soft-deleted rows
// so a re-push after a local delete materializes fresh rather than
// resurrecting a tombstone. ErrNotFound when no live mapping exists.
func (d *DB) FindLocalTaskForRemoteOffer(ctx context.Context, fromPeerID, remoteTaskID string) (string, error) {
	var taskID string
	err := d.q.QueryRowContext(ctx, `
		SELECT o.task_id
		FROM task_offers o
		JOIN tasks t ON t.id = o.task_id
		WHERE o.direction = 'incoming'
		  AND o.from_peer_id = ?
		  AND o.remote_task_id = ?
		  AND o.task_id IS NOT NULL AND o.task_id != ''
		  AND t.deleted_at IS NULL
		ORDER BY o.created_at DESC
		LIMIT 1`,
		fromPeerID, remoteTaskID,
	).Scan(&taskID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", store.ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("find local task for remote offer: %w", err)
	}
	return taskID, nil
}

func (d *DB) UpdateTaskOfferState(
	ctx context.Context,
	id, state string,
	acceptedAt, declinedAt *time.Time,
	declinedReason string,
	taskID, workspaceID string,
) error {
	res, err := d.q.ExecContext(ctx, `
		UPDATE task_offers SET
			state = ?,
			accepted_at = ?,
			declined_at = ?,
			declined_reason = ?,
			task_id = COALESCE(NULLIF(?, ''), task_id),
			workspace_id = COALESCE(NULLIF(?, ''), workspace_id)
		WHERE id = ?`,
		state,
		unixOrNil(acceptedAt),
		unixOrNil(declinedAt),
		declinedReason,
		taskID,
		workspaceID,
		id,
	)
	if err != nil {
		return fmt.Errorf("update task offer state: %w", err)
	}
	return checkRowsAffected(res)
}

// RefreshTaskOfferForRetry rotates the replay nonce and re-bases a durable
// outgoing publication immediately before another wire attempt. It is kept
// narrow to pending outgoing rows so an accepted/conflicted offer can never be
// resurrected by a background scheduler race.
func (d *DB) RefreshTaskOfferForRetry(
	ctx context.Context, id, nonce string, createdAt time.Time,
	accessEpoch int64, baseHLC string,
) error {
	if id == "" || nonce == "" || accessEpoch < 1 {
		return errors.New("refresh task offer: id, nonce, and access epoch required")
	}
	res, err := d.q.ExecContext(ctx, `
		UPDATE task_offers SET envelope_nonce = ?, envelope_created_at = ?,
			access_epoch = ?, base_hlc = ?, declined_at = NULL,
			declined_reason = ''
		WHERE id = ? AND direction = 'outgoing' AND state = 'pending'`,
		nonce, createdAt.UTC().Unix(), accessEpoch, baseHLC, id)
	if err != nil {
		return fmt.Errorf("refresh task offer: %w", err)
	}
	return checkRowsAffected(res)
}

func scanTaskOffer(scan func(...any) error) (*store.TaskOffer, error) {
	var (
		o                                  store.TaskOffer
		taskID, shareID, senderPrincipalID sql.NullString
		workspaceID, declinedReason        sql.NullString
		acceptedAt, declinedAt             sql.NullInt64
		envelopeCreated, createdAt         int64
		directAssign                       int
		tags                               string
	)
	if err := scan(
		&o.ID, &taskID, &o.RemoteTaskID, &shareID, &senderPrincipalID,
		&o.AccessEpoch, &o.VisibilityEpoch, &o.BaseHLC, &o.FromPeerID, &o.ToPeerID,
		&o.RemoteWorkspaceID, &o.RemoteWorkspaceName, &workspaceID,
		&o.Title, &o.DescriptionPreview, &o.MetaPreview, &o.StatusPreview, &o.PriorityPreview, &tags,
		&directAssign, &o.EnvelopeNonce, &envelopeCreated,
		&o.Direction, &o.State, &acceptedAt, &declinedAt, &declinedReason, &createdAt,
	); err != nil {
		return nil, err
	}
	if taskID.Valid {
		o.TaskID = taskID.String
	}
	o.ShareID = shareID.String
	o.SenderPrincipalID = senderPrincipalID.String
	if workspaceID.Valid {
		o.WorkspaceID = workspaceID.String
	}
	if declinedReason.Valid {
		o.DeclinedReason = declinedReason.String
	}
	o.TagsJSON = json.RawMessage(tags)
	o.IsDirectAssign = directAssign != 0
	o.EnvelopeCreatedAt = time.Unix(envelopeCreated, 0).UTC()
	o.CreatedAt = time.Unix(createdAt, 0).UTC()
	if acceptedAt.Valid {
		t := time.Unix(acceptedAt.Int64, 0).UTC()
		o.AcceptedAt = &t
	}
	if declinedAt.Valid {
		t := time.Unix(declinedAt.Int64, 0).UTC()
		o.DeclinedAt = &t
	}
	return &o, nil
}
