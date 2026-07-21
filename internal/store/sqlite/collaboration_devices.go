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

const principalKeyColumns = `
	id, principal_id, canonical_public_key, fingerprint, algorithm, status,
	COALESCE(replaces_key_id, ''), comment,
	COALESCE(added_by_principal_id, ''), created_at, verified_at, revoked_at`

func (d *DB) AddPrincipalIdentityKey(ctx context.Context, key *store.PrincipalIdentityKey) error {
	if key == nil {
		return fmt.Errorf("identity key is required")
	}
	if key.Algorithm != "ssh-ed25519" || !validPrincipalKeyStatus(key.Status) {
		return fmt.Errorf("identity key must use ssh-ed25519 and a valid status")
	}
	key.CanonicalPublicKey = strings.TrimSpace(key.CanonicalPublicKey)
	key.Fingerprint = strings.TrimSpace(key.Fingerprint)
	if !strings.HasPrefix(key.CanonicalPublicKey, "ssh-ed25519 ") || key.Fingerprint == "" || key.PrincipalID == "" {
		return fmt.Errorf("principal, canonical public key, and fingerprint are required")
	}
	if key.Status == store.PrincipalKeyStatusActive && key.VerifiedAt == nil {
		return fmt.Errorf("active identity key requires verified_at")
	}
	if key.Status == store.PrincipalKeyStatusRevoked && key.RevokedAt == nil {
		return fmt.Errorf("revoked identity key requires revoked_at")
	}
	if key.Status != store.PrincipalKeyStatusRevoked && key.RevokedAt != nil {
		return fmt.Errorf("non-revoked identity key cannot have revoked_at")
	}
	if key.ID == "" {
		key.ID = ulid.Make().String()
	}
	key.CreatedAt = collaborationTime(key.CreatedAt)
	_, err := d.q.ExecContext(ctx, `
		INSERT INTO p2p_principal_keys (
			id, principal_id, canonical_public_key, fingerprint, algorithm,
			status, replaces_key_id, comment, added_by_principal_id,
			created_at, verified_at, revoked_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		key.ID, key.PrincipalID, key.CanonicalPublicKey, key.Fingerprint,
		key.Algorithm, key.Status, nullString(key.ReplacesKeyID), key.Comment,
		nullString(key.AddedByPrincipalID), key.CreatedAt.Unix(),
		nullableUnix(key.VerifiedAt), nullableUnix(key.RevokedAt))
	return mapConstraintError(err)
}

func (d *DB) GetPrincipalIdentityKey(ctx context.Context, keyID string) (*store.PrincipalIdentityKey, error) {
	return getPrincipalIdentityKeyQ(ctx, d.q, "id", keyID)
}

func (d *DB) GetPrincipalIdentityKeyByFingerprint(ctx context.Context, fingerprint string) (*store.PrincipalIdentityKey, error) {
	return getPrincipalIdentityKeyQ(ctx, d.q, "fingerprint", fingerprint)
}

func getPrincipalIdentityKeyQ(ctx context.Context, q queryable, field, value string) (*store.PrincipalIdentityKey, error) {
	if field != "id" && field != "fingerprint" {
		return nil, fmt.Errorf("unsupported key lookup")
	}
	key, err := scanPrincipalIdentityKey(q.QueryRowContext(ctx,
		`SELECT `+principalKeyColumns+` FROM p2p_principal_keys WHERE `+field+` = ?`, value))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	return key, err
}

func (d *DB) ListPrincipalIdentityKeys(ctx context.Context, principalID string) ([]store.PrincipalIdentityKey, error) {
	rows, err := d.q.QueryContext(ctx, `SELECT `+principalKeyColumns+`
		FROM p2p_principal_keys WHERE principal_id = ? ORDER BY created_at DESC, id`, principalID)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck
	var keys []store.PrincipalIdentityKey
	for rows.Next() {
		key, err := scanPrincipalIdentityKey(rows)
		if err != nil {
			return nil, err
		}
		keys = append(keys, *key)
	}
	return keys, rows.Err()
}

func scanPrincipalIdentityKey(scanner rowScanner) (*store.PrincipalIdentityKey, error) {
	var key store.PrincipalIdentityKey
	var createdAt int64
	var verifiedAt, revokedAt sql.NullInt64
	err := scanner.Scan(&key.ID, &key.PrincipalID, &key.CanonicalPublicKey,
		&key.Fingerprint, &key.Algorithm, &key.Status, &key.ReplacesKeyID,
		&key.Comment, &key.AddedByPrincipalID, &createdAt, &verifiedAt, &revokedAt)
	if err != nil {
		return nil, err
	}
	key.CreatedAt = unixTime(createdAt)
	key.VerifiedAt, key.RevokedAt = unixTimePtr(verifiedAt), unixTimePtr(revokedAt)
	return &key, nil
}

func (d *DB) RevokePrincipalIdentityKey(ctx context.Context, keyID string, at time.Time) error {
	at = collaborationTime(at)
	return d.withTx(ctx, func(q queryable) error {
		key, err := getPrincipalIdentityKeyQ(ctx, q, "id", keyID)
		if err != nil {
			return err
		}
		if key.Status == store.PrincipalKeyStatusRevoked {
			return nil
		}
		actorID, err := localOwnerPrincipalIDQ(ctx, q)
		if err != nil {
			return err
		}
		if _, err := q.ExecContext(ctx, `
			UPDATE p2p_principal_devices
			SET status = 'revoked', revoked_at = ?, revocation_reason = 'identity key revoked'
			WHERE identity_key_id = ? AND status = 'active'`, at.Unix(), keyID); err != nil {
			return err
		}
		if _, err = q.ExecContext(ctx, `UPDATE p2p_principal_keys SET status = 'revoked', revoked_at = ? WHERE id = ?`, at.Unix(), keyID); err != nil {
			return err
		}
		return appendCollaborationAuditQ(ctx, q, &store.CollaborationAuditEvent{
			Event: "identity_key.revoked", ActorPrincipalID: actorID,
			SubjectKind: "identity_key", SubjectID: key.ID,
			DetailsJSON: collaborationAuditDetails(map[string]any{
				"principal_id": key.PrincipalID,
				"fingerprint":  key.Fingerprint,
			}),
			CreatedAt: at,
		})
	})
}

const principalDeviceColumns = `
	id, peer_id, principal_id, COALESCE(identity_key_id, ''), display_name,
	kind, status, binding_version, binding_transcript_hash, binding_signature,
	created_at, verified_at, revoked_at, revocation_reason`

func (d *DB) ResolveActivePrincipalForPeer(ctx context.Context, peerID string) (*store.PrincipalDevice, *store.Principal, error) {
	row := d.q.QueryRowContext(ctx, `
		SELECT `+qualifiedDeviceColumns("d")+`, `+qualifiedPrincipalColumns("p")+`
		FROM p2p_principal_devices d
		JOIN p2p_principals p ON p.id = d.principal_id
		JOIN p2p_principal_keys k ON k.id = d.identity_key_id
		WHERE d.peer_id = ? AND d.status = 'active' AND d.revoked_at IS NULL
		  AND p.status = 'active' AND p.revoked_at IS NULL
		  AND k.status = 'active' AND k.revoked_at IS NULL`, peerID)
	device, principal, err := scanDeviceAndPrincipal(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, store.ErrNotFound
	}
	return device, principal, err
}

func qualifiedDeviceColumns(alias string) string {
	return alias + `.id, ` + alias + `.peer_id, ` + alias + `.principal_id, COALESCE(` + alias + `.identity_key_id, ''), ` +
		alias + `.display_name, ` + alias + `.kind, ` + alias + `.status, ` + alias + `.binding_version, ` +
		alias + `.binding_transcript_hash, ` + alias + `.binding_signature, ` + alias + `.created_at, ` +
		alias + `.verified_at, ` + alias + `.revoked_at, ` + alias + `.revocation_reason`
}

func qualifiedPrincipalColumns(alias string) string {
	return alias + `.id, ` + alias + `.kind, ` + alias + `.display_name, ` + alias + `.status, COALESCE(` +
		alias + `.controlling_principal_id, ''), ` + alias + `.is_local_owner, ` + alias + `.created_at, ` +
		alias + `.updated_at, ` + alias + `.activated_at, ` + alias + `.revoked_at, ` + alias + `.revocation_reason`
}

func scanDeviceAndPrincipal(scanner rowScanner) (*store.PrincipalDevice, *store.Principal, error) {
	var device store.PrincipalDevice
	var principal store.Principal
	var deviceCreated, principalCreated, principalUpdated int64
	var deviceVerified, deviceRevoked, principalActivated, principalRevoked sql.NullInt64
	err := scanner.Scan(
		&device.ID, &device.PeerID, &device.PrincipalID, &device.IdentityKeyID,
		&device.DisplayName, &device.Kind, &device.Status, &device.BindingVersion,
		&device.BindingTranscriptHash, &device.BindingSignature, &deviceCreated,
		&deviceVerified, &deviceRevoked, &device.RevocationReason,
		&principal.ID, &principal.Kind, &principal.DisplayName, &principal.Status,
		&principal.ControllingPrincipalID, &principal.IsLocalOwner, &principalCreated,
		&principalUpdated, &principalActivated, &principalRevoked, &principal.RevocationReason)
	if err != nil {
		return nil, nil, err
	}
	device.CreatedAt = unixTime(deviceCreated)
	device.VerifiedAt, device.RevokedAt = unixTimePtr(deviceVerified), unixTimePtr(deviceRevoked)
	principal.CreatedAt, principal.UpdatedAt = unixTime(principalCreated), unixTime(principalUpdated)
	principal.ActivatedAt, principal.RevokedAt = unixTimePtr(principalActivated), unixTimePtr(principalRevoked)
	return &device, &principal, nil
}

func (d *DB) ListPrincipalDevices(ctx context.Context, principalID string) ([]store.PrincipalDevice, error) {
	rows, err := d.q.QueryContext(ctx, `SELECT `+principalDeviceColumns+`
		FROM p2p_principal_devices WHERE principal_id = ? ORDER BY created_at DESC, id`, principalID)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck
	var devices []store.PrincipalDevice
	for rows.Next() {
		device, err := scanPrincipalDevice(rows)
		if err != nil {
			return nil, err
		}
		devices = append(devices, *device)
	}
	return devices, rows.Err()
}

func scanPrincipalDevice(scanner rowScanner) (*store.PrincipalDevice, error) {
	var device store.PrincipalDevice
	var createdAt int64
	var verifiedAt, revokedAt sql.NullInt64
	err := scanner.Scan(&device.ID, &device.PeerID, &device.PrincipalID,
		&device.IdentityKeyID, &device.DisplayName, &device.Kind, &device.Status,
		&device.BindingVersion, &device.BindingTranscriptHash, &device.BindingSignature,
		&createdAt, &verifiedAt, &revokedAt, &device.RevocationReason)
	if err != nil {
		return nil, err
	}
	device.CreatedAt = unixTime(createdAt)
	device.VerifiedAt, device.RevokedAt = unixTimePtr(verifiedAt), unixTimePtr(revokedAt)
	return &device, nil
}

func (d *DB) RevokePrincipalDevice(ctx context.Context, peerID, reason string, at time.Time) error {
	at = collaborationTime(at)
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return fmt.Errorf("revocation reason is required")
	}
	return d.withTx(ctx, func(q queryable) error {
		device, err := scanPrincipalDevice(q.QueryRowContext(ctx,
			`SELECT `+principalDeviceColumns+` FROM p2p_principal_devices WHERE peer_id = ?`, peerID))
		if errors.Is(err, sql.ErrNoRows) {
			return store.ErrNotFound
		}
		if err != nil {
			return err
		}
		if device.Status == store.PrincipalDeviceStatusRevoked {
			return nil
		}
		actorID, err := localOwnerPrincipalIDQ(ctx, q)
		if err != nil {
			return err
		}
		if _, err = q.ExecContext(ctx, `
			UPDATE p2p_principal_devices
			SET status = 'revoked', revoked_at = ?, revocation_reason = ?
			WHERE peer_id = ? AND status != 'revoked'`, at.Unix(), reason, peerID); err != nil {
			return err
		}
		return appendCollaborationAuditQ(ctx, q, &store.CollaborationAuditEvent{
			Event: "device.revoked", ActorPrincipalID: actorID,
			SubjectKind: "device", SubjectID: device.ID,
			DetailsJSON: collaborationAuditDetails(map[string]any{
				"principal_id": device.PrincipalID,
				"peer_id":      device.PeerID,
				"reason":       reason,
			}),
			CreatedAt: at,
		})
	})
}
