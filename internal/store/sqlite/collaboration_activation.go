package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/oklog/ulid/v2"
)

func bindInvitedDevice(
	ctx context.Context,
	q queryable,
	invitation *store.PrincipalInvitation,
	activation store.InvitedDeviceActivation,
	at time.Time,
) (*store.PrincipalDevice, error) {
	displayName, err := requiredLabel(activation.DisplayName, "device display name")
	if err != nil {
		return nil, err
	}
	device := &store.PrincipalDevice{
		ID: ulid.Make().String(), PeerID: activation.PeerID,
		PrincipalID: invitation.PrincipalID, IdentityKeyID: invitation.IdentityKeyID,
		DisplayName: displayName, Kind: activation.DeviceKind,
		Status:                store.PrincipalDeviceStatusActive,
		BindingVersion:        activation.BindingVersion,
		BindingTranscriptHash: activation.BindingTranscriptHash,
		BindingSignature:      append([]byte(nil), activation.BindingSignature...),
		CreatedAt:             at, VerifiedAt: &at,
	}
	existing, lookupErr := scanPrincipalDevice(q.QueryRowContext(ctx,
		`SELECT `+principalDeviceColumns+` FROM p2p_principal_devices WHERE peer_id = ?`, activation.PeerID))
	switch {
	case errors.Is(lookupErr, sql.ErrNoRows):
		_, err = q.ExecContext(ctx, `
			INSERT INTO p2p_principal_devices (
				id, peer_id, principal_id, identity_key_id, display_name, kind,
				status, binding_version, binding_transcript_hash, binding_signature,
				created_at, verified_at, revoked_at, revocation_reason
			) VALUES (?, ?, ?, ?, ?, ?, 'active', ?, ?, ?, ?, ?, NULL, '')`,
			device.ID, device.PeerID, device.PrincipalID, device.IdentityKeyID,
			device.DisplayName, device.Kind, device.BindingVersion,
			device.BindingTranscriptHash, device.BindingSignature,
			device.CreatedAt.Unix(), at.Unix())
		if err != nil {
			return nil, mapConstraintError(err)
		}
		return device, nil
	case lookupErr != nil:
		return nil, lookupErr
	case existing.Status == store.PrincipalDeviceStatusActive &&
		existing.PrincipalID == invitation.PrincipalID &&
		(invitation.Purpose == store.InvitationPurposeRotateKey || invitation.Purpose == store.InvitationPurposeAddDevice):
		device.ID = existing.ID
		device.CreatedAt = existing.CreatedAt
		result, err := q.ExecContext(ctx, `
			UPDATE p2p_principal_devices SET
				identity_key_id = ?, display_name = ?, kind = ?,
				binding_version = ?, binding_transcript_hash = ?, binding_signature = ?,
				verified_at = ?, revoked_at = NULL, revocation_reason = ''
			WHERE id = ? AND status = 'active' AND principal_id = ?`,
			device.IdentityKeyID, device.DisplayName, device.Kind,
			device.BindingVersion, device.BindingTranscriptHash,
			device.BindingSignature, at.Unix(), device.ID, device.PrincipalID)
		if err != nil {
			return nil, err
		}
		if err := checkRowsAffected(result); err != nil {
			return nil, fmt.Errorf("active device changed during key binding: %w", store.ErrConflict)
		}
		return device, nil
	case existing.Status != store.PrincipalDeviceStatusLegacyUnverified || existing.PrincipalID != invitation.PrincipalID:
		return nil, fmt.Errorf("peer is already bound or was revoked: %w", store.ErrConflict)
	default:
		device.ID = existing.ID
		device.CreatedAt = existing.CreatedAt
		result, err := q.ExecContext(ctx, `
			UPDATE p2p_principal_devices SET
				identity_key_id = ?, display_name = ?, kind = ?, status = 'active',
				binding_version = ?, binding_transcript_hash = ?, binding_signature = ?,
				verified_at = ?, revoked_at = NULL, revocation_reason = ''
			WHERE id = ? AND status = 'legacy_unverified' AND principal_id = ?`,
			device.IdentityKeyID, device.DisplayName, device.Kind,
			device.BindingVersion, device.BindingTranscriptHash,
			device.BindingSignature, at.Unix(), device.ID, device.PrincipalID)
		if err != nil {
			return nil, err
		}
		if err := checkRowsAffected(result); err != nil {
			return nil, fmt.Errorf("legacy device changed during activation: %w", store.ErrConflict)
		}
		return device, nil
	}
}

func applyInvitationGrants(
	ctx context.Context,
	q queryable,
	invitation *store.PrincipalInvitation,
	at time.Time,
) ([]store.WorkspaceGrant, error) {
	staged, err := listInvitationGrantsQ(ctx, q, invitation.ID)
	if err != nil || len(staged) == 0 {
		return nil, err
	}
	byShare := make(map[string][]store.InvitationWorkspaceGrant)
	shareOrder := make([]string, 0)
	for _, grant := range staged {
		if _, seen := byShare[grant.ShareID]; !seen {
			shareOrder = append(shareOrder, grant.ShareID)
		}
		byShare[grant.ShareID] = append(byShare[grant.ShareID], grant)
	}
	applied := make([]store.WorkspaceGrant, 0, len(staged))
	for _, shareID := range shareOrder {
		share, err := getWorkspaceShareQ(ctx, q, shareID)
		if err != nil {
			return nil, err
		}
		if share.Status != store.WorkspaceShareStatusActive {
			return nil, fmt.Errorf("invited workspace is revoked: %w", store.ErrConflict)
		}
		result, err := q.ExecContext(ctx, `
			UPDATE p2p_workspace_shares
			SET access_epoch = access_epoch + 1, updated_at = ?
			WHERE share_id = ? AND status = 'active'`, at.Unix(), shareID)
		if err != nil {
			return nil, err
		}
		if err := checkRowsAffected(result); err != nil {
			return nil, err
		}
		if _, err := q.ExecContext(ctx, `UPDATE p2p_workspace_grants SET revoked_at = ?
			WHERE share_id = ? AND principal_id = ? AND revoked_at IS NULL`,
			at.Unix(), shareID, invitation.PrincipalID); err != nil {
			return nil, err
		}
		var epoch int64
		if err := q.QueryRowContext(ctx, `SELECT access_epoch FROM p2p_workspace_shares WHERE share_id = ?`, shareID).Scan(&epoch); err != nil {
			return nil, err
		}
		capabilities := make([]string, 0, len(byShare[shareID]))
		for _, stagedGrant := range byShare[shareID] {
			if stagedGrant.ExpiresAt != nil && !stagedGrant.ExpiresAt.After(at) {
				return nil, fmt.Errorf("invited workspace grant expired: %w", store.ErrConflict)
			}
			grant := store.WorkspaceGrant{
				ID: ulid.Make().String(), ShareID: shareID,
				PrincipalID:          invitation.PrincipalID,
				Capability:           stagedGrant.Capability,
				ConstraintsJSON:      stagedGrant.ConstraintsJSON,
				CreatedByPrincipalID: invitation.CreatedByPrincipalID,
				GrantedEpoch:         epoch, CreatedAt: at, ExpiresAt: stagedGrant.ExpiresAt,
			}
			if err := insertWorkspaceGrantQ(ctx, q, &grant); err != nil {
				return nil, err
			}
			capabilities = append(capabilities, grant.Capability)
			applied = append(applied, grant)
		}
		details, _ := json.Marshal(map[string]any{"capabilities": capabilities, "access_epoch": epoch})
		if err := appendCollaborationAuditQ(ctx, q, &store.CollaborationAuditEvent{
			ShareID: shareID, Event: "workspace.grants.activated",
			ActorPrincipalID: invitation.CreatedByPrincipalID,
			SubjectKind:      "principal", SubjectID: invitation.PrincipalID,
			DetailsJSON: details, CreatedAt: at,
		}); err != nil {
			return nil, err
		}
	}
	return applied, nil
}
