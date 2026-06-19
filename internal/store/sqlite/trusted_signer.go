package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// AddTrustedSigner inserts a new trusted signer. Returns store.ErrAlreadyExists
// if either pubkey_id or pubkey_string is already present.
func (d *DB) AddTrustedSigner(ctx context.Context, s *store.TrustedSigner) error {
	if s == nil {
		return errors.New("AddTrustedSigner: nil signer")
	}
	if s.PubkeyID == "" || s.PubkeyString == "" {
		return errors.New("AddTrustedSigner: pubkey_id and pubkey_string required")
	}
	added := s.AddedAt
	if added.IsZero() {
		added = time.Now().UTC()
	}
	_, err := d.q.ExecContext(ctx, `
		INSERT INTO trusted_signers (pubkey_id, pubkey_string, name, added_at, revoked_at)
		VALUES (?, ?, ?, ?, ?)`,
		s.PubkeyID, s.PubkeyString, s.Name, formatTime(added), formatTimePtr(s.RevokedAt),
	)
	return mapConstraintError(err)
}

// RemoveTrustedSigner deletes the row keyed by pubkeyID. Returns
// store.ErrNotFound if no row was removed.
func (d *DB) RemoveTrustedSigner(ctx context.Context, pubkeyID string) error {
	res, err := d.q.ExecContext(ctx,
		`DELETE FROM trusted_signers WHERE pubkey_id = ?`, pubkeyID)
	if err != nil {
		return fmt.Errorf("delete trusted_signer: %w", err)
	}
	return checkRowsAffected(res)
}

// IsTrusted reports whether pubkeyID is present and not revoked.
func (d *DB) IsTrusted(ctx context.Context, pubkeyID string) (bool, error) {
	var revoked sql.NullString
	err := d.q.QueryRowContext(ctx,
		`SELECT revoked_at FROM trusted_signers WHERE pubkey_id = ?`,
		pubkeyID,
	).Scan(&revoked)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("query trusted_signer: %w", err)
	}
	return !revoked.Valid, nil
}

// ListTrustedSigners returns every row ordered by added_at ascending.
func (d *DB) ListTrustedSigners(ctx context.Context) ([]store.TrustedSigner, error) {
	rows, err := d.q.QueryContext(ctx, `
		SELECT pubkey_id, pubkey_string, name, added_at, revoked_at
		FROM trusted_signers
		ORDER BY added_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("list trusted_signers: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	var out []store.TrustedSigner
	for rows.Next() {
		var s store.TrustedSigner
		var added string
		var revoked sql.NullString
		if err := rows.Scan(&s.PubkeyID, &s.PubkeyString, &s.Name, &added, &revoked); err != nil {
			return nil, fmt.Errorf("scan trusted_signer: %w", err)
		}
		s.AddedAt = parseTime(added)
		if revoked.Valid {
			s.RevokedAt = parseTimePtr(&revoked.String)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate trusted_signers: %w", err)
	}
	return out, nil
}
