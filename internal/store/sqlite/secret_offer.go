package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// InsertSecretOffer persists an in-flight peer→peer secret transfer.
// Direction = "inbound" (we received it) or "outbound" (we sent it).
// The ciphertext is the age blob — plaintext is NEVER stored here.
// Returns store.ErrAlreadyExists if offer_id is taken.
func (d *DB) InsertSecretOffer(ctx context.Context, o *store.SecretOffer) error {
	if o == nil {
		return errors.New("InsertSecretOffer: offer required")
	}
	if o.OfferID == "" || o.Direction == "" || o.PeerID == "" || o.Name == "" {
		return errors.New("InsertSecretOffer: offer_id, direction, peer_id, name required")
	}
	if len(o.Ciphertext) == 0 {
		return errors.New("InsertSecretOffer: ciphertext required")
	}
	if o.Status == "" {
		o.Status = "pending"
	}
	if o.CreatedAt.IsZero() {
		o.CreatedAt = time.Now().UTC()
	}
	meta := "{}"
	if len(o.Metadata) > 0 {
		b, err := json.Marshal(o.Metadata)
		if err != nil {
			return fmt.Errorf("marshal metadata: %w", err)
		}
		meta = string(b)
	}
	_, err := d.q.ExecContext(ctx, `
		INSERT INTO secret_offers
		    (offer_id, direction, peer_id, name, metadata_json, ciphertext,
		     status, created_at, decided_at, expires_at, saved_as)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		o.OfferID, o.Direction, o.PeerID, o.Name, meta, o.Ciphertext,
		o.Status, formatTime(o.CreatedAt), formatTimePtr(o.DecidedAt),
		formatTime(o.ExpiresAt), o.SavedAs,
	)
	return mapConstraintError(err)
}

// GetSecretOffer fetches one offer by ID. Returns store.ErrNotFound if absent.
func (d *DB) GetSecretOffer(ctx context.Context, offerID string) (*store.SecretOffer, error) {
	row := d.q.QueryRowContext(ctx, `
		SELECT offer_id, direction, peer_id, name, metadata_json, ciphertext,
		       status, created_at, decided_at, expires_at, saved_as
		FROM secret_offers WHERE offer_id = ?`, offerID)
	o, err := scanSecretOffer(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get secret offer: %w", err)
	}
	return o, nil
}

// ListPendingSecretOffers returns pending offers for the given direction
// ("inbound" or "outbound"), ordered by created_at descending. Expired
// rows are not auto-filtered here — callers should compare expires_at
// against now and call DecideSecretOffer with status="expired" to lock
// them in (the worker that runs the reaper does this).
func (d *DB) ListPendingSecretOffers(ctx context.Context, direction string) ([]*store.SecretOffer, error) {
	if direction != "inbound" && direction != "outbound" {
		return nil, fmt.Errorf("ListPendingSecretOffers: bad direction %q", direction)
	}
	rows, err := d.q.QueryContext(ctx, `
		SELECT offer_id, direction, peer_id, name, metadata_json, ciphertext,
		       status, created_at, decided_at, expires_at, saved_as
		FROM secret_offers
		WHERE direction = ? AND status = 'pending'
		ORDER BY created_at DESC`, direction)
	if err != nil {
		return nil, fmt.Errorf("list pending secret offers: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	var out []*store.SecretOffer
	for rows.Next() {
		o, err := scanSecretOffer(rows)
		if err != nil {
			return nil, fmt.Errorf("scan secret offer: %w", err)
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// DecideSecretOffer transitions an offer to a terminal status.
// Allowed: "accepted" | "rejected" | "expired" | "delivered".
// savedAs is the name the receiver chose for the secret in their store
// (only meaningful for status="accepted" on inbound rows; ignored otherwise).
// Returns store.ErrNotFound if the row is absent.
func (d *DB) DecideSecretOffer(ctx context.Context, offerID, status string, decidedAt time.Time, savedAs string) error {
	switch status {
	case "accepted", "rejected", "expired", "delivered":
	default:
		return fmt.Errorf("DecideSecretOffer: bad status %q", status)
	}
	if offerID == "" {
		return errors.New("DecideSecretOffer: offer_id required")
	}
	if decidedAt.IsZero() {
		decidedAt = time.Now().UTC()
	}
	res, err := d.q.ExecContext(ctx, `
		UPDATE secret_offers
		SET status = ?, decided_at = ?, saved_as = COALESCE(NULLIF(?, ''), saved_as)
		WHERE offer_id = ? AND status = 'pending'`,
		status, formatTime(decidedAt), savedAs, offerID)
	if err != nil {
		return fmt.Errorf("decide secret offer: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		// Either the row doesn't exist or it was already decided. Return
		// ErrNotFound — callers treat "already decided" the same way they
		// treat "missing" (idempotent retry should be safe at the caller).
		return store.ErrNotFound
	}
	return nil
}

// ExpireOldSecretOffers transitions every pending offer with expires_at <= now
// to status="expired". Returns the count of rows updated. Idempotent. Called
// by a periodic reaper; safe to also call lazily before reads.
func (d *DB) ExpireOldSecretOffers(ctx context.Context, now time.Time) (int64, error) {
	res, err := d.q.ExecContext(ctx, `
		UPDATE secret_offers
		SET status = 'expired', decided_at = ?
		WHERE status = 'pending' AND expires_at <= ?`,
		formatTime(now), formatTime(now))
	if err != nil {
		return 0, fmt.Errorf("expire secret offers: %w", err)
	}
	return res.RowsAffected()
}

func scanSecretOffer(r scanner) (*store.SecretOffer, error) {
	var o store.SecretOffer
	var created, expires string
	var decided sql.NullString
	var savedAs sql.NullString
	var meta string
	if err := r.Scan(&o.OfferID, &o.Direction, &o.PeerID, &o.Name, &meta,
		&o.Ciphertext, &o.Status, &created, &decided, &expires, &savedAs); err != nil {
		return nil, err
	}
	o.CreatedAt = parseTime(created)
	o.ExpiresAt = parseTime(expires)
	if decided.Valid {
		o.DecidedAt = parseTimePtr(&decided.String)
	}
	if savedAs.Valid {
		o.SavedAs = savedAs.String
	}
	if meta != "" && meta != "{}" {
		if err := json.Unmarshal([]byte(meta), &o.Metadata); err != nil {
			return nil, fmt.Errorf("unmarshal metadata: %w", err)
		}
	}
	return &o, nil
}
