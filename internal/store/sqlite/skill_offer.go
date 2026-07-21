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

// InsertSkillOffer persists an in-flight peer→peer skill push.
// Direction = "inbound" (we received it) or "outbound" (we pushed it).
// Only metadata is stored — the body + bundle are pulled on accept.
// Returns store.ErrAlreadyExists if offer_id is taken.
func (d *DB) InsertSkillOffer(ctx context.Context, o *store.SkillOffer) error {
	if o == nil {
		return errors.New("InsertSkillOffer: offer required")
	}
	if o.OfferID == "" || o.Direction == "" || o.PeerID == "" || o.Name == "" {
		return errors.New("InsertSkillOffer: offer_id, direction, peer_id, name required")
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
		INSERT INTO skill_offers
		    (offer_id, direction, peer_id, name, version, content_hash,
		     bundle_sha256, description, metadata_json, status, created_at,
		     decided_at, expires_at, published_version)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		o.OfferID, o.Direction, o.PeerID, o.Name, o.Version, o.ContentHash,
		o.BundleSHA256, o.Description, meta, o.Status, formatTime(o.CreatedAt),
		formatTimePtr(o.DecidedAt), formatTime(o.ExpiresAt), o.PublishedVersion,
	)
	return mapConstraintError(err)
}

// GetSkillOffer fetches one offer by ID. Returns store.ErrNotFound if absent.
func (d *DB) GetSkillOffer(ctx context.Context, offerID string) (*store.SkillOffer, error) {
	row := d.q.QueryRowContext(ctx, `
		SELECT offer_id, direction, peer_id, name, version, content_hash,
		       bundle_sha256, description, metadata_json, status, created_at,
		       decided_at, expires_at, published_version
		FROM skill_offers WHERE offer_id = ?`, offerID)
	o, err := scanSkillOffer(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get skill offer: %w", err)
	}
	return o, nil
}

// ListPendingSkillOffers returns pending offers for the given direction
// ("inbound" or "outbound"), ordered by created_at descending. Expired rows
// are not auto-filtered here — callers reap via ExpireOldSkillOffers first.
func (d *DB) ListPendingSkillOffers(ctx context.Context, direction string) ([]*store.SkillOffer, error) {
	if direction != "inbound" && direction != "outbound" {
		return nil, fmt.Errorf("ListPendingSkillOffers: bad direction %q", direction)
	}
	rows, err := d.q.QueryContext(ctx, `
		SELECT offer_id, direction, peer_id, name, version, content_hash,
		       bundle_sha256, description, metadata_json, status, created_at,
		       decided_at, expires_at, published_version
		FROM skill_offers
		WHERE direction = ? AND status = 'pending'
		ORDER BY created_at DESC`, direction)
	if err != nil {
		return nil, fmt.Errorf("list pending skill offers: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	var out []*store.SkillOffer
	for rows.Next() {
		o, err := scanSkillOffer(rows)
		if err != nil {
			return nil, fmt.Errorf("scan skill offer: %w", err)
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// DecideSkillOffer transitions an offer to a terminal status.
// Allowed: "accepted" | "rejected" | "expired". publishedVersion records
// the local registry version produced by an accept (0 otherwise).
// Returns store.ErrNotFound if the row is absent or already decided.
func (d *DB) DecideSkillOffer(ctx context.Context, offerID, status string, decidedAt time.Time, publishedVersion int) error {
	switch status {
	case "accepted", "rejected", "expired":
	default:
		return fmt.Errorf("DecideSkillOffer: bad status %q", status)
	}
	if offerID == "" {
		return errors.New("DecideSkillOffer: offer_id required")
	}
	if decidedAt.IsZero() {
		decidedAt = time.Now().UTC()
	}
	res, err := d.q.ExecContext(ctx, `
		UPDATE skill_offers
		SET status = ?, decided_at = ?,
		    published_version = CASE WHEN ? > 0 THEN ? ELSE published_version END
		WHERE offer_id = ? AND status = 'pending'`,
		status, formatTime(decidedAt), publishedVersion, publishedVersion, offerID)
	if err != nil {
		return fmt.Errorf("decide skill offer: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return store.ErrNotFound
	}
	return nil
}

// ExpireOldSkillOffers transitions every pending offer with expires_at <= now
// to status="expired". Returns the count of rows updated. Idempotent.
func (d *DB) ExpireOldSkillOffers(ctx context.Context, now time.Time) (int64, error) {
	res, err := d.q.ExecContext(ctx, `
		UPDATE skill_offers
		SET status = 'expired', decided_at = ?
		WHERE status = 'pending' AND expires_at <= ?`,
		formatTime(now), formatTime(now))
	if err != nil {
		return 0, fmt.Errorf("expire skill offers: %w", err)
	}
	return res.RowsAffected()
}

func scanSkillOffer(r scanner) (*store.SkillOffer, error) {
	var o store.SkillOffer
	var created, expires string
	var decided sql.NullString
	var meta string
	if err := r.Scan(&o.OfferID, &o.Direction, &o.PeerID, &o.Name, &o.Version,
		&o.ContentHash, &o.BundleSHA256, &o.Description, &meta, &o.Status,
		&created, &decided, &expires, &o.PublishedVersion); err != nil {
		return nil, err
	}
	o.CreatedAt = parseTime(created)
	o.ExpiresAt = parseTime(expires)
	if decided.Valid {
		o.DecidedAt = parseTimePtr(&decided.String)
	}
	if meta != "" && meta != "{}" {
		if err := json.Unmarshal([]byte(meta), &o.Metadata); err != nil {
			return nil, fmt.Errorf("unmarshal metadata: %w", err)
		}
	}
	return &o, nil
}
