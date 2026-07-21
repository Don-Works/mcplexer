package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/oklog/ulid/v2"
)

func collaborationAuditDetails(values map[string]any) json.RawMessage {
	details, err := json.Marshal(values)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return details
}

func localOwnerPrincipalIDQ(ctx context.Context, q queryable) (string, error) {
	var principalID string
	err := q.QueryRowContext(ctx, `
		SELECT id FROM p2p_principals
		WHERE is_local_owner = 1 AND status = 'active' AND revoked_at IS NULL
		LIMIT 1`).Scan(&principalID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return principalID, err
}

func (d *DB) AppendCollaborationAudit(ctx context.Context, event *store.CollaborationAuditEvent) error {
	return appendCollaborationAuditQ(ctx, d.q, event)
}

func appendCollaborationAuditQ(ctx context.Context, q queryable, event *store.CollaborationAuditEvent) error {
	if event == nil {
		return fmt.Errorf("collaboration audit event is required")
	}
	var err error
	if event.Event, err = requiredLabel(event.Event, "audit event"); err != nil {
		return err
	}
	if event.SubjectKind, err = requiredLabel(event.SubjectKind, "audit subject kind"); err != nil {
		return err
	}
	if event.SubjectID, err = requiredLabel(event.SubjectID, "audit subject ID"); err != nil {
		return err
	}
	details, err := collaborationJSON(event.DetailsJSON)
	if err != nil {
		return err
	}
	if event.ID == "" {
		event.ID = ulid.Make().String()
	}
	event.CreatedAt = collaborationTime(event.CreatedAt)
	event.DetailsJSON = details
	_, err = q.ExecContext(ctx, `
		INSERT INTO p2p_collaboration_audit (
			id, share_id, event, actor_principal_id, actor_peer_id,
			subject_kind, subject_id, details_json, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		event.ID, nullString(event.ShareID), event.Event,
		nullString(event.ActorPrincipalID), event.ActorPeerID,
		event.SubjectKind, event.SubjectID, string(details), event.CreatedAt.Unix())
	return mapConstraintError(err)
}

func (d *DB) ListCollaborationAudit(ctx context.Context, shareID, subjectKind, subjectID string, limit int) ([]store.CollaborationAuditEvent, error) {
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	query := `
		SELECT id, COALESCE(share_id, ''), event,
		       COALESCE(actor_principal_id, ''), actor_peer_id,
		       subject_kind, subject_id, details_json, created_at
		FROM p2p_collaboration_audit WHERE 1 = 1`
	args := make([]any, 0, 4)
	if shareID != "" {
		query += ` AND share_id = ?`
		args = append(args, shareID)
	}
	if subjectKind != "" {
		query += ` AND subject_kind = ?`
		args = append(args, subjectKind)
	}
	if subjectID != "" {
		query += ` AND subject_id = ?`
		args = append(args, subjectID)
	}
	query += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := d.q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck
	var events []store.CollaborationAuditEvent
	for rows.Next() {
		var event store.CollaborationAuditEvent
		var details string
		var createdAt int64
		if err := rows.Scan(&event.ID, &event.ShareID, &event.Event,
			&event.ActorPrincipalID, &event.ActorPeerID, &event.SubjectKind,
			&event.SubjectID, &details, &createdAt); err != nil {
			return nil, err
		}
		event.DetailsJSON = []byte(details)
		event.CreatedAt = unixTime(createdAt)
		events = append(events, event)
	}
	return events, rows.Err()
}
