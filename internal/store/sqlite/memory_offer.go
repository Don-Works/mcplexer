// memory_offer.go — incoming memory offer CRUD for store.MemoryStore.
// Offers arrive over the /mcplexer/memory/1.0.0 libp2p protocol and sit
// in memory_offers until the local user accepts (pulls content + writes
// to memories) or declines.
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

// UpsertMemoryOffer inserts a row when (peer_id, remote_id) is new and
// is a harmless no-op otherwise. The caller's o.ID is generated when
// empty; ReceivedAt defaults to now.
func (d *DB) UpsertMemoryOffer(ctx context.Context, o *store.MemoryOffer) error {
	if o == nil {
		return errors.New("UpsertMemoryOffer: nil offer")
	}
	if o.PeerID == "" || o.RemoteID == "" || o.Name == "" {
		return errors.New("UpsertMemoryOffer: peer_id, remote_id, name required")
	}
	if o.ID == "" {
		o.ID = ulid.Make().String()
	}
	if o.ReceivedAt.IsZero() {
		o.ReceivedAt = time.Now().UTC()
	}
	if o.Kind == "" {
		o.Kind = store.MemoryKindNote
	}
	tags := normalizeJSON(o.TagsJSON, "[]")
	metadata := normalizeJSON(o.MetadataJSON, "{}")
	_, err := d.q.ExecContext(ctx, `
		INSERT INTO memory_offers (
			id, peer_id, peer_name, remote_id, name, kind,
			description, preview, tags_json, metadata_json,
			embed_model, received_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(peer_id, remote_id) DO NOTHING`,
		o.ID, o.PeerID, o.PeerName, o.RemoteID, o.Name, o.Kind,
		o.Description, o.Preview, tags, metadata,
		nullString(o.EmbedModel), o.ReceivedAt.Unix(),
	)
	if err != nil {
		return fmt.Errorf("upsert memory offer: %w", err)
	}
	return nil
}

// GetMemoryOffer returns one row by id. ErrNotFound when missing.
func (d *DB) GetMemoryOffer(ctx context.Context, id string) (*store.MemoryOffer, error) {
	row := d.q.QueryRowContext(ctx, `
		SELECT `+memoryOfferCols+`
		FROM memory_offers WHERE id = ?`, id)
	o, err := scanMemoryOffer(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get memory offer: %w", err)
	}
	return o, nil
}

// ListMemoryOffers returns offers matching the filter, newest-first.
func (d *DB) ListMemoryOffers(
	ctx context.Context, f store.MemoryOfferFilter,
) ([]store.MemoryOffer, error) {
	var b strings.Builder
	var args []any
	b.WriteString("WHERE 1=1")
	if f.PeerID != "" {
		b.WriteString(" AND peer_id = ?")
		args = append(args, f.PeerID)
	}
	if f.PendingOnly {
		b.WriteString(" AND accepted_at IS NULL AND declined_at IS NULL")
	} else if !f.IncludeDone {
		// Default: hide accepted + declined unless explicitly requested.
		b.WriteString(" AND (accepted_at IS NULL AND declined_at IS NULL)")
	}
	q := `SELECT ` + memoryOfferCols + ` FROM memory_offers ` +
		b.String() + ` ORDER BY received_at DESC`
	if f.Limit > 0 {
		q += " LIMIT ?"
		args = append(args, f.Limit)
	}
	rows, err := d.q.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list memory offers: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	var out []store.MemoryOffer
	for rows.Next() {
		o, err := scanMemoryOffer(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("scan memory offer: %w", err)
		}
		out = append(out, *o)
	}
	return out, rows.Err()
}

// AcceptMemoryOffer stamps accepted_at + accepted_as_id. Idempotent on
// the same (id, localMemoryID).
func (d *DB) AcceptMemoryOffer(ctx context.Context, id, localMemoryID string) error {
	if id == "" || localMemoryID == "" {
		return errors.New("AcceptMemoryOffer: id and localMemoryID required")
	}
	now := time.Now().Unix()
	res, err := d.q.ExecContext(ctx, `
		UPDATE memory_offers
		SET accepted_at = ?, accepted_as_id = ?
		WHERE id = ? AND accepted_at IS NULL AND declined_at IS NULL`,
		now, localMemoryID, id)
	if err != nil {
		return fmt.Errorf("accept memory offer: %w", err)
	}
	return checkRowsAffected(res)
}

// DeclineMemoryOffer stamps declined_at.
func (d *DB) DeclineMemoryOffer(ctx context.Context, id string) error {
	now := time.Now().Unix()
	res, err := d.q.ExecContext(ctx, `
		UPDATE memory_offers SET declined_at = ?
		WHERE id = ? AND accepted_at IS NULL AND declined_at IS NULL`,
		now, id)
	if err != nil {
		return fmt.Errorf("decline memory offer: %w", err)
	}
	return checkRowsAffected(res)
}

const memoryOfferCols = `id, peer_id, peer_name, remote_id, name, kind,
		description, preview, tags_json, metadata_json,
		embed_model, received_at, accepted_at, declined_at, accepted_as_id`

func scanMemoryOffer(scan func(...any) error) (*store.MemoryOffer, error) {
	var (
		o                        store.MemoryOffer
		tags, metadata           string
		embedModel, acceptedAsID sql.NullString
		acceptedAt, declinedAt   sql.NullInt64
		receivedAt               int64
	)
	if err := scan(
		&o.ID, &o.PeerID, &o.PeerName, &o.RemoteID, &o.Name, &o.Kind,
		&o.Description, &o.Preview, &tags, &metadata,
		&embedModel, &receivedAt, &acceptedAt, &declinedAt, &acceptedAsID,
	); err != nil {
		return nil, err
	}
	o.TagsJSON = json.RawMessage(tags)
	o.MetadataJSON = json.RawMessage(metadata)
	if embedModel.Valid {
		o.EmbedModel = embedModel.String
	}
	o.ReceivedAt = time.Unix(receivedAt, 0).UTC()
	if acceptedAt.Valid {
		t := time.Unix(acceptedAt.Int64, 0).UTC()
		o.AcceptedAt = &t
	}
	if declinedAt.Valid {
		t := time.Unix(declinedAt.Int64, 0).UTC()
		o.DeclinedAt = &t
	}
	if acceptedAsID.Valid {
		o.AcceptedAsID = acceptedAsID.String
	}
	return &o, nil
}
