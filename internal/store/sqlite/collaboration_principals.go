package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/oklog/ulid/v2"
)

const principalColumns = `
	id, kind, display_name, status, COALESCE(controlling_principal_id, ''),
	is_local_owner, created_at, updated_at, activated_at, revoked_at,
	revocation_reason`

func (d *DB) CreatePrincipal(ctx context.Context, principal *store.Principal) error {
	if principal == nil {
		return fmt.Errorf("principal is required")
	}
	if !validPrincipalKind(principal.Kind) || !validPrincipalStatus(principal.Status) {
		return fmt.Errorf("invalid principal kind or status")
	}
	displayName, err := requiredLabel(principal.DisplayName, "display name")
	if err != nil {
		return err
	}
	if principal.IsLocalOwner && (principal.Kind != store.PrincipalKindPerson || principal.Status != store.PrincipalStatusActive) {
		return fmt.Errorf("local owner must be an active person")
	}
	if principal.Kind == store.PrincipalKindMachine && principal.ControllingPrincipalID == "" {
		return fmt.Errorf("machine principal requires a controlling principal")
	}
	if principal.Kind == store.PrincipalKindPerson && principal.ControllingPrincipalID != "" {
		return fmt.Errorf("person principal cannot have a controlling principal")
	}
	if principal.Status == store.PrincipalStatusLegacyUnverified && principal.IsLocalOwner {
		return fmt.Errorf("local owner cannot be legacy unverified")
	}
	if principal.Status == store.PrincipalStatusRevoked && principal.RevokedAt == nil {
		return fmt.Errorf("revoked principal requires revoked_at")
	}
	if principal.Status != store.PrincipalStatusRevoked && principal.RevokedAt != nil {
		return fmt.Errorf("non-revoked principal cannot have revoked_at")
	}
	if principal.ID == "" {
		principal.ID = ulid.Make().String()
	}
	now := collaborationTime(principal.CreatedAt)
	principal.DisplayName = displayName
	principal.CreatedAt = now
	principal.UpdatedAt = collaborationTime(principal.UpdatedAt)
	if principal.UpdatedAt.Before(now) {
		principal.UpdatedAt = now
	}
	if principal.Status == store.PrincipalStatusActive && principal.ActivatedAt == nil {
		principal.ActivatedAt = &now
	}
	_, err = d.q.ExecContext(ctx, `
		INSERT INTO p2p_principals (
			id, kind, display_name, status, controlling_principal_id,
			is_local_owner, created_at, updated_at, activated_at, revoked_at,
			revocation_reason
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		principal.ID, principal.Kind, principal.DisplayName, principal.Status,
		nullString(principal.ControllingPrincipalID), principal.IsLocalOwner,
		principal.CreatedAt.Unix(), principal.UpdatedAt.Unix(),
		nullableUnix(principal.ActivatedAt), nullableUnix(principal.RevokedAt),
		principal.RevocationReason)
	return mapConstraintError(err)
}

func (d *DB) GetPrincipal(ctx context.Context, principalID string) (*store.Principal, error) {
	return getPrincipalQ(ctx, d.q, principalID)
}

func getPrincipalQ(ctx context.Context, q queryable, principalID string) (*store.Principal, error) {
	row := q.QueryRowContext(ctx, `SELECT `+principalColumns+` FROM p2p_principals WHERE id = ?`, principalID)
	principal, err := scanPrincipal(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	return principal, err
}

func (d *DB) ListPrincipals(ctx context.Context) ([]store.Principal, error) {
	rows, err := d.q.QueryContext(ctx, `SELECT `+principalColumns+` FROM p2p_principals ORDER BY display_name, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck
	var principals []store.Principal
	for rows.Next() {
		principal, err := scanPrincipal(rows)
		if err != nil {
			return nil, err
		}
		principals = append(principals, *principal)
	}
	return principals, rows.Err()
}

func scanPrincipal(scanner rowScanner) (*store.Principal, error) {
	var principal store.Principal
	var createdAt, updatedAt int64
	var activatedAt, revokedAt sql.NullInt64
	err := scanner.Scan(&principal.ID, &principal.Kind, &principal.DisplayName,
		&principal.Status, &principal.ControllingPrincipalID, &principal.IsLocalOwner,
		&createdAt, &updatedAt, &activatedAt, &revokedAt,
		&principal.RevocationReason)
	if err != nil {
		return nil, err
	}
	principal.CreatedAt, principal.UpdatedAt = unixTime(createdAt), unixTime(updatedAt)
	principal.ActivatedAt, principal.RevokedAt = unixTimePtr(activatedAt), unixTimePtr(revokedAt)
	return &principal, nil
}

func (d *DB) UpdatePrincipalDisplayName(ctx context.Context, principalID, displayName string, at time.Time) error {
	displayName, err := requiredLabel(displayName, "display name")
	if err != nil {
		return err
	}
	result, err := d.q.ExecContext(ctx, `UPDATE p2p_principals SET display_name = ?, updated_at = ? WHERE id = ?`,
		displayName, collaborationTime(at).Unix(), principalID)
	if err != nil {
		return err
	}
	return checkRowsAffected(result)
}

func (d *DB) RevokePrincipal(ctx context.Context, principalID, reason string, at time.Time) error {
	at = collaborationTime(at)
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return fmt.Errorf("revocation reason is required")
	}
	return d.withTx(ctx, func(q queryable) error {
		principal, err := getPrincipalQ(ctx, q, principalID)
		if err != nil {
			return err
		}
		if principal.IsLocalOwner {
			return fmt.Errorf("local owner cannot be revoked: %w", store.ErrConflict)
		}
		if principal.Status == store.PrincipalStatusRevoked {
			return nil
		}
		actorID, err := localOwnerPrincipalIDQ(ctx, q)
		if err != nil {
			return err
		}
		shareIDs, err := activeGrantShareIDs(ctx, q, principalID)
		if err != nil {
			return err
		}
		if _, err := q.ExecContext(ctx, `
			UPDATE p2p_workspace_grants SET revoked_at = ?
			WHERE principal_id = ? AND revoked_at IS NULL`, at.Unix(), principalID); err != nil {
			return err
		}
		for _, shareID := range shareIDs {
			if _, err := q.ExecContext(ctx, `
				UPDATE p2p_workspace_shares
				SET access_epoch = access_epoch + 1, updated_at = ?
				WHERE share_id = ?`, at.Unix(), shareID); err != nil {
				return err
			}
		}
		statements := []struct {
			query string
			args  []any
		}{
			{`UPDATE p2p_principal_devices SET status = 'revoked', revoked_at = ?, revocation_reason = ? WHERE principal_id = ? AND status != 'revoked'`, []any{at.Unix(), reason, principalID}},
			{`UPDATE p2p_principal_keys SET status = 'revoked', revoked_at = ? WHERE principal_id = ? AND status != 'revoked'`, []any{at.Unix(), principalID}},
			{`UPDATE p2p_principal_invitations SET revoked_at = ? WHERE principal_id = ? AND consumed_at IS NULL AND revoked_at IS NULL`, []any{at.Unix(), principalID}},
			{`UPDATE p2p_principals SET status = 'revoked', is_local_owner = 0, updated_at = ?, revoked_at = ?, revocation_reason = ? WHERE id = ?`, []any{at.Unix(), at.Unix(), reason, principalID}},
		}
		for _, statement := range statements {
			if _, err := q.ExecContext(ctx, statement.query, statement.args...); err != nil {
				return err
			}
		}
		return appendCollaborationAuditQ(ctx, q, &store.CollaborationAuditEvent{
			Event: "principal.revoked", ActorPrincipalID: actorID,
			SubjectKind: "principal", SubjectID: principal.ID,
			DetailsJSON: collaborationAuditDetails(map[string]any{
				"kind":      principal.Kind,
				"reason":    reason,
				"share_ids": shareIDs,
			}),
			CreatedAt: at,
		})
	})
}

func activeGrantShareIDs(ctx context.Context, q queryable, principalID string) ([]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT DISTINCT share_id FROM p2p_workspace_grants WHERE principal_id = ? AND revoked_at IS NULL`, principalID)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
