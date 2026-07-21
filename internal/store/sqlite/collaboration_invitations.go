package sqlite

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/oklog/ulid/v2"
)

const invitationColumns = `
	id, token_hash, purpose, principal_id, identity_key_id,
	created_by_principal_id, created_at, expires_at, consumed_at,
	consumed_by_peer_id, revoked_at`

func (d *DB) CreatePrincipalInvitation(ctx context.Context, invitation *store.PrincipalInvitation, grants []store.InvitationWorkspaceGrant) error {
	if invitation == nil {
		return fmt.Errorf("principal invitation is required")
	}
	if !validInvitationPurpose(invitation.Purpose) || len(invitation.TokenHash) != 32 {
		return fmt.Errorf("invitation purpose and 256-bit token hash are required")
	}
	if invitation.PrincipalID == "" || invitation.IdentityKeyID == "" || invitation.CreatedByPrincipalID == "" {
		return fmt.Errorf("invited principal, identity key, and inviter are required")
	}
	if invitation.ConsumedAt != nil || invitation.RevokedAt != nil || invitation.ConsumedByPeerID != "" {
		return fmt.Errorf("new invitation cannot already be consumed or revoked")
	}
	if invitation.ID == "" {
		invitation.ID = ulid.Make().String()
	}
	invitation.CreatedAt = collaborationTime(invitation.CreatedAt)
	invitation.ExpiresAt = collaborationTime(invitation.ExpiresAt)
	if !invitation.ExpiresAt.After(invitation.CreatedAt) {
		return fmt.Errorf("invitation expiry must be after creation")
	}
	if invitation.Purpose != store.InvitationPurposeNewPrincipal && len(grants) != 0 {
		return fmt.Errorf("device and key invitations cannot modify workspace grants")
	}
	prepared, err := prepareInvitationGrants(invitation, grants)
	if err != nil {
		return err
	}
	return d.withTx(ctx, func(q queryable) error {
		principal, err := getPrincipalQ(ctx, q, invitation.PrincipalID)
		if err != nil {
			return err
		}
		key, err := getPrincipalIdentityKeyQ(ctx, q, "id", invitation.IdentityKeyID)
		if err != nil {
			return err
		}
		actor, err := getPrincipalQ(ctx, q, invitation.CreatedByPrincipalID)
		if err != nil {
			return err
		}
		if key.PrincipalID != principal.ID || actor.Status != store.PrincipalStatusActive {
			return fmt.Errorf("invalid invitation principal, key, or inviter state: %w", store.ErrConflict)
		}
		if err := validateInvitationLifecycle(invitation.Purpose, principal.Status, key.Status); err != nil {
			return err
		}
		_, err = q.ExecContext(ctx, `
			INSERT INTO p2p_principal_invitations (
				id, token_hash, purpose, principal_id, identity_key_id,
				created_by_principal_id, created_at, expires_at,
				consumed_at, consumed_by_peer_id, revoked_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, NULL, '', NULL)`,
			invitation.ID, invitation.TokenHash, invitation.Purpose,
			invitation.PrincipalID, invitation.IdentityKeyID,
			invitation.CreatedByPrincipalID, invitation.CreatedAt.Unix(),
			invitation.ExpiresAt.Unix())
		if err != nil {
			return mapConstraintError(err)
		}
		for _, grant := range prepared {
			if _, err := getWorkspaceShareQ(ctx, q, grant.ShareID); err != nil {
				return err
			}
			_, err := q.ExecContext(ctx, `
				INSERT INTO p2p_invitation_grants (
					invitation_id, share_id, capability, constraints_json, expires_at
				) VALUES (?, ?, ?, ?, ?)`, invitation.ID, grant.ShareID,
				grant.Capability, string(grant.ConstraintsJSON), nullableUnix(grant.ExpiresAt))
			if err != nil {
				return mapConstraintError(err)
			}
		}
		return nil
	})
}

func validateInvitationLifecycle(purpose, principalStatus, keyStatus string) error {
	valid := false
	switch purpose {
	case store.InvitationPurposeNewPrincipal:
		valid = (principalStatus == store.PrincipalStatusPending ||
			principalStatus == store.PrincipalStatusLegacyUnverified) &&
			keyStatus == store.PrincipalKeyStatusPending
	case store.InvitationPurposeAddDevice:
		valid = principalStatus == store.PrincipalStatusActive && keyStatus == store.PrincipalKeyStatusActive
	case store.InvitationPurposeRotateKey:
		valid = principalStatus == store.PrincipalStatusActive && keyStatus == store.PrincipalKeyStatusPending
	}
	if !valid {
		return fmt.Errorf("invitation does not match principal and key lifecycle: %w", store.ErrConflict)
	}
	return nil
}

func prepareInvitationGrants(invitation *store.PrincipalInvitation, grants []store.InvitationWorkspaceGrant) ([]store.InvitationWorkspaceGrant, error) {
	seen := make(map[string]struct{}, len(grants))
	prepared := make([]store.InvitationWorkspaceGrant, 0, len(grants))
	for _, grant := range grants {
		if grant.ShareID == "" || !store.ValidWorkspaceCapability(grant.Capability) {
			return nil, fmt.Errorf("valid invitation share and capability are required")
		}
		key := grant.ShareID + "\x00" + grant.Capability
		if _, exists := seen[key]; exists {
			return nil, fmt.Errorf("duplicate invitation workspace capability")
		}
		seen[key] = struct{}{}
		constraints, err := collaborationJSON(grant.ConstraintsJSON)
		if err != nil {
			return nil, err
		}
		if grant.ExpiresAt != nil && !collaborationTime(*grant.ExpiresAt).After(invitation.ExpiresAt) {
			return nil, fmt.Errorf("grant expiry must outlive invitation")
		}
		grant.InvitationID = invitation.ID
		grant.ConstraintsJSON = constraints
		prepared = append(prepared, grant)
	}
	return prepared, nil
}

func (d *DB) GetPrincipalInvitation(ctx context.Context, invitationID string) (*store.PrincipalInvitation, []store.InvitationWorkspaceGrant, error) {
	invitation, err := getPrincipalInvitationQ(ctx, d.q, invitationID)
	if err != nil {
		return nil, nil, err
	}
	grants, err := listInvitationGrantsQ(ctx, d.q, invitationID)
	return invitation, grants, err
}

func getPrincipalInvitationQ(ctx context.Context, q queryable, invitationID string) (*store.PrincipalInvitation, error) {
	invitation, err := scanPrincipalInvitation(q.QueryRowContext(ctx,
		`SELECT `+invitationColumns+` FROM p2p_principal_invitations WHERE id = ?`, invitationID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	return invitation, err
}

func scanPrincipalInvitation(scanner rowScanner) (*store.PrincipalInvitation, error) {
	var invitation store.PrincipalInvitation
	var createdAt, expiresAt int64
	var consumedAt, revokedAt sql.NullInt64
	err := scanner.Scan(&invitation.ID, &invitation.TokenHash, &invitation.Purpose,
		&invitation.PrincipalID, &invitation.IdentityKeyID,
		&invitation.CreatedByPrincipalID, &createdAt, &expiresAt,
		&consumedAt, &invitation.ConsumedByPeerID, &revokedAt)
	if err != nil {
		return nil, err
	}
	invitation.CreatedAt, invitation.ExpiresAt = unixTime(createdAt), unixTime(expiresAt)
	invitation.ConsumedAt, invitation.RevokedAt = unixTimePtr(consumedAt), unixTimePtr(revokedAt)
	return &invitation, nil
}

func listInvitationGrantsQ(ctx context.Context, q queryable, invitationID string) ([]store.InvitationWorkspaceGrant, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT invitation_id, share_id, capability, constraints_json, expires_at
		FROM p2p_invitation_grants WHERE invitation_id = ? ORDER BY share_id, capability`, invitationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck
	var grants []store.InvitationWorkspaceGrant
	for rows.Next() {
		var grant store.InvitationWorkspaceGrant
		var constraints string
		var expiresAt sql.NullInt64
		if err := rows.Scan(&grant.InvitationID, &grant.ShareID, &grant.Capability,
			&constraints, &expiresAt); err != nil {
			return nil, err
		}
		grant.ConstraintsJSON = []byte(constraints)
		grant.ExpiresAt = unixTimePtr(expiresAt)
		grants = append(grants, grant)
	}
	return grants, rows.Err()
}

func (d *DB) ListPrincipalInvitations(ctx context.Context, principalID string) ([]store.PrincipalInvitation, error) {
	rows, err := d.q.QueryContext(ctx, `SELECT `+invitationColumns+`
		FROM p2p_principal_invitations WHERE principal_id = ? ORDER BY created_at DESC, id`, principalID)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck
	var invitations []store.PrincipalInvitation
	for rows.Next() {
		invitation, err := scanPrincipalInvitation(rows)
		if err != nil {
			return nil, err
		}
		invitations = append(invitations, *invitation)
	}
	return invitations, rows.Err()
}

func (d *DB) RevokePrincipalInvitation(ctx context.Context, invitationID string, at time.Time) error {
	at = collaborationTime(at)
	return d.withTx(ctx, func(q queryable) error {
		invitation, err := getPrincipalInvitationQ(ctx, q, invitationID)
		if err != nil {
			return err
		}
		if invitation.ConsumedAt != nil {
			return fmt.Errorf("consumed invitation cannot be revoked: %w", store.ErrConflict)
		}
		if invitation.RevokedAt != nil {
			return nil
		}
		actorID, err := localOwnerPrincipalIDQ(ctx, q)
		if err != nil {
			return err
		}
		if _, err = q.ExecContext(ctx, `UPDATE p2p_principal_invitations
			SET revoked_at = ? WHERE id = ? AND consumed_at IS NULL AND revoked_at IS NULL`, at.Unix(), invitationID); err != nil {
			return err
		}
		return appendCollaborationAuditQ(ctx, q, &store.CollaborationAuditEvent{
			Event: "invitation.revoked", ActorPrincipalID: actorID,
			SubjectKind: "invitation", SubjectID: invitation.ID,
			DetailsJSON: collaborationAuditDetails(map[string]any{
				"principal_id": invitation.PrincipalID,
				"purpose":      invitation.Purpose,
			}),
			CreatedAt: at,
		})
	})
}

const challengeColumns = `
	id, invitation_id, initiator_peer_id, responder_peer_id, nonce_hash,
	transcript_hash, issued_at, expires_at, consumed_at`

func (d *DB) CreatePrincipalIdentityChallenge(ctx context.Context, challenge *store.PrincipalIdentityChallenge) error {
	if challenge == nil || challenge.ID == "" || challenge.InvitationID == "" {
		return fmt.Errorf("identity challenge and IDs are required")
	}
	if challenge.InitiatorPeerID == "" || challenge.ResponderPeerID == "" || len(challenge.NonceHash) != 32 {
		return fmt.Errorf("challenge peers and 256-bit nonce hash are required")
	}
	if _, err := hex.DecodeString(challenge.TranscriptHash); err != nil || len(challenge.TranscriptHash) != 64 || strings.ToLower(challenge.TranscriptHash) != challenge.TranscriptHash {
		return fmt.Errorf("challenge transcript hash must be lowercase SHA-256 hex")
	}
	challenge.IssuedAt = collaborationTime(challenge.IssuedAt)
	challenge.ExpiresAt = collaborationTime(challenge.ExpiresAt)
	if !challenge.ExpiresAt.After(challenge.IssuedAt) || challenge.ExpiresAt.Sub(challenge.IssuedAt) > 5*time.Minute {
		return fmt.Errorf("challenge must expire within five minutes")
	}
	if challenge.ConsumedAt != nil {
		return fmt.Errorf("new challenge cannot be consumed")
	}
	invitation, err := getPrincipalInvitationQ(ctx, d.q, challenge.InvitationID)
	if err != nil {
		return err
	}
	if invitation.ConsumedAt != nil || invitation.RevokedAt != nil || !invitation.ExpiresAt.After(challenge.IssuedAt) {
		return fmt.Errorf("invitation is not pending: %w", store.ErrConflict)
	}
	_, err = d.q.ExecContext(ctx, `
		INSERT INTO p2p_identity_challenges (
			id, invitation_id, initiator_peer_id, responder_peer_id,
			nonce_hash, transcript_hash, issued_at, expires_at, consumed_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, NULL)`, challenge.ID,
		challenge.InvitationID, challenge.InitiatorPeerID,
		challenge.ResponderPeerID, challenge.NonceHash,
		challenge.TranscriptHash, challenge.IssuedAt.Unix(), challenge.ExpiresAt.Unix())
	return mapConstraintError(err)
}

func (d *DB) GetPrincipalIdentityChallenge(ctx context.Context, challengeID string) (*store.PrincipalIdentityChallenge, error) {
	return getPrincipalIdentityChallengeQ(ctx, d.q, challengeID)
}

func getPrincipalIdentityChallengeQ(ctx context.Context, q queryable, challengeID string) (*store.PrincipalIdentityChallenge, error) {
	challenge, err := scanPrincipalIdentityChallenge(q.QueryRowContext(ctx,
		`SELECT `+challengeColumns+` FROM p2p_identity_challenges WHERE id = ?`, challengeID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	return challenge, err
}

func scanPrincipalIdentityChallenge(scanner rowScanner) (*store.PrincipalIdentityChallenge, error) {
	var challenge store.PrincipalIdentityChallenge
	var issuedAt, expiresAt int64
	var consumedAt sql.NullInt64
	err := scanner.Scan(&challenge.ID, &challenge.InvitationID,
		&challenge.InitiatorPeerID, &challenge.ResponderPeerID,
		&challenge.NonceHash, &challenge.TranscriptHash,
		&issuedAt, &expiresAt, &consumedAt)
	if err != nil {
		return nil, err
	}
	challenge.IssuedAt, challenge.ExpiresAt = unixTime(issuedAt), unixTime(expiresAt)
	challenge.ConsumedAt = unixTimePtr(consumedAt)
	return &challenge, nil
}

func (d *DB) ActivateInvitedDevice(ctx context.Context, activation store.InvitedDeviceActivation) (*store.PrincipalDevice, []store.WorkspaceGrant, error) {
	at := collaborationTime(activation.At)
	if err := validateActivation(activation); err != nil {
		return nil, nil, err
	}
	var device *store.PrincipalDevice
	var applied []store.WorkspaceGrant
	err := d.withTx(ctx, func(q queryable) error {
		invitation, err := getPrincipalInvitationQ(ctx, q, activation.InvitationID)
		if err != nil {
			return err
		}
		if subtle.ConstantTimeCompare(invitationToken(invitation), activation.InvitationTokenHash) != 1 {
			return store.ErrNotFound
		}
		if invitation.ConsumedAt != nil || invitation.RevokedAt != nil || !invitation.ExpiresAt.After(at) {
			return fmt.Errorf("invitation is not pending: %w", store.ErrConflict)
		}
		principal, err := getPrincipalQ(ctx, q, invitation.PrincipalID)
		if err != nil {
			return err
		}
		key, err := getPrincipalIdentityKeyQ(ctx, q, "id", invitation.IdentityKeyID)
		if err != nil {
			return err
		}
		if key.PrincipalID != principal.ID || validateInvitationLifecycle(invitation.Purpose, principal.Status, key.Status) != nil {
			return fmt.Errorf("invitation identity state changed: %w", store.ErrConflict)
		}
		challenge, err := getPrincipalIdentityChallengeQ(ctx, q, activation.ChallengeID)
		if err != nil {
			return err
		}
		if challenge.InvitationID != invitation.ID || challenge.InitiatorPeerID != activation.PeerID ||
			challenge.ResponderPeerID != activation.ResponderPeerID ||
			challenge.TranscriptHash != activation.BindingTranscriptHash ||
			challenge.ConsumedAt != nil || at.After(challenge.ExpiresAt.Add(30*time.Second)) || at.Before(challenge.IssuedAt.Add(-30*time.Second)) {
			return fmt.Errorf("identity challenge does not match live proof: %w", store.ErrConflict)
		}
		if err := consumeInvitationAndChallenge(ctx, q, invitation.ID, challenge.ID, activation.PeerID, at); err != nil {
			return err
		}
		device, err = bindInvitedDevice(ctx, q, invitation, activation, at)
		if err != nil {
			return err
		}
		if invitation.Purpose == store.InvitationPurposeNewPrincipal {
			if _, err := q.ExecContext(ctx, `UPDATE p2p_principals
				SET status = 'active', activated_at = ?, updated_at = ?
				WHERE id = ? AND status IN ('pending', 'legacy_unverified')`,
				at.Unix(), at.Unix(), principal.ID); err != nil {
				return err
			}
		}
		if key.Status == store.PrincipalKeyStatusPending {
			if _, err := q.ExecContext(ctx, `UPDATE p2p_principal_keys
				SET status = 'active', verified_at = ? WHERE id = ? AND status = 'pending'`,
				at.Unix(), key.ID); err != nil {
				return err
			}
		}
		applied, err = applyInvitationGrants(ctx, q, invitation, at)
		if err != nil {
			return err
		}
		details, _ := json.Marshal(map[string]any{"purpose": invitation.Purpose, "peer_id": activation.PeerID})
		return appendCollaborationAuditQ(ctx, q, &store.CollaborationAuditEvent{
			Event: "device.activated", ActorPrincipalID: principal.ID,
			ActorPeerID: activation.PeerID, SubjectKind: "device",
			SubjectID: device.ID, DetailsJSON: details, CreatedAt: at,
		})
	})
	return device, applied, err
}

func invitationToken(invitation *store.PrincipalInvitation) []byte {
	if invitation == nil {
		return nil
	}
	return invitation.TokenHash
}

func validateActivation(activation store.InvitedDeviceActivation) error {
	if activation.InvitationID == "" || len(activation.InvitationTokenHash) != 32 || activation.ChallengeID == "" {
		return fmt.Errorf("invitation token and challenge are required")
	}
	if activation.PeerID == "" || activation.ResponderPeerID == "" || !validDeviceKind(activation.DeviceKind) {
		return fmt.Errorf("valid live peers and device kind are required")
	}
	if activation.BindingVersion != store.DeviceBindingVersionV1 || len(activation.BindingSignature) == 0 {
		return fmt.Errorf("verified device-binding v1 signature is required")
	}
	if _, err := hex.DecodeString(activation.BindingTranscriptHash); err != nil || len(activation.BindingTranscriptHash) != 64 ||
		strings.ToLower(activation.BindingTranscriptHash) != activation.BindingTranscriptHash {
		return fmt.Errorf("valid binding transcript hash is required")
	}
	return nil
}

func consumeInvitationAndChallenge(ctx context.Context, q queryable, invitationID, challengeID, peerID string, at time.Time) error {
	result, err := q.ExecContext(ctx, `UPDATE p2p_identity_challenges SET consumed_at = ?
		WHERE id = ? AND invitation_id = ? AND consumed_at IS NULL`, at.Unix(), challengeID, invitationID)
	if err != nil {
		return err
	}
	if err := checkRowsAffected(result); err != nil {
		return fmt.Errorf("challenge replay: %w", store.ErrConflict)
	}
	result, err = q.ExecContext(ctx, `UPDATE p2p_principal_invitations
		SET consumed_at = ?, consumed_by_peer_id = ?
		WHERE id = ? AND consumed_at IS NULL AND revoked_at IS NULL AND expires_at > ?`,
		at.Unix(), peerID, invitationID, at.Unix())
	if err != nil {
		return err
	}
	if err := checkRowsAffected(result); err != nil {
		return fmt.Errorf("invitation replay: %w", store.ErrConflict)
	}
	return nil
}
