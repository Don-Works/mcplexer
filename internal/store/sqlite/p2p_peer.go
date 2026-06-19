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

const peerScopeCASMaxAttempts = 64

// AddPeer inserts a paired peer. Returns store.ErrAlreadyExists if peer_id
// is already present (revoked or not — caller should re-pair via revoke +
// re-add explicitly).
func (d *DB) AddPeer(ctx context.Context, p *store.P2PPeer) error {
	if p == nil || p.PeerID == "" {
		return errors.New("AddPeer: peer_id required")
	}
	scopes, err := encodeScopes(p.Scopes)
	if err != nil {
		return err
	}
	paired := p.PairedAt
	if paired.IsZero() {
		paired = time.Now().UTC()
	}
	_, err = d.q.ExecContext(ctx, `
		INSERT INTO p2p_peers
		    (peer_id, display_name, paired_at, last_seen, trust_level, scopes, revoked_at, secret_transfer_recipient)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		p.PeerID, p.DisplayName, formatTime(paired),
		formatTimePtr(p.LastSeen), p.TrustLevel, scopes,
		formatTimePtr(p.RevokedAt), p.SecretTransferRecipient,
	)
	return mapConstraintError(err)
}

// GetPeer fetches a single peer by ID. Returns store.ErrNotFound if absent.
func (d *DB) GetPeer(ctx context.Context, peerID string) (*store.P2PPeer, error) {
	row := d.q.QueryRowContext(ctx, `
		SELECT peer_id, display_name, paired_at, last_seen, trust_level, scopes, revoked_at, ssh_target, secret_transfer_recipient
		FROM p2p_peers WHERE peer_id = ?`, peerID)
	p, err := scanPeer(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get peer: %w", err)
	}
	return p, nil
}

// ListPeers returns every paired peer ordered by paired_at descending.
// Includes revoked peers — callers filter as appropriate.
func (d *DB) ListPeers(ctx context.Context) ([]store.P2PPeer, error) {
	rows, err := d.q.QueryContext(ctx, `
		SELECT peer_id, display_name, paired_at, last_seen, trust_level, scopes, revoked_at, ssh_target, secret_transfer_recipient
		FROM p2p_peers ORDER BY paired_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list peers: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	var out []store.P2PPeer
	for rows.Next() {
		p, err := scanPeer(rows)
		if err != nil {
			return nil, fmt.Errorf("scan peer: %w", err)
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// RevokePeer marks peerID as revoked. Returns store.ErrNotFound if no row.
func (d *DB) RevokePeer(ctx context.Context, peerID string) error {
	now := formatTime(time.Now().UTC())
	res, err := d.q.ExecContext(ctx,
		`UPDATE p2p_peers SET revoked_at = ? WHERE peer_id = ? AND revoked_at IS NULL`,
		now, peerID)
	if err != nil {
		return fmt.Errorf("revoke peer: %w", err)
	}
	return checkRowsAffected(res)
}

// UnrevokePeer clears revoked_at on peerID. No-op if already active or
// absent — the pair handler runs this after AddPeer fails with
// ErrAlreadyExists, so the row is known to exist.
func (d *DB) UnrevokePeer(ctx context.Context, peerID string) error {
	_, err := d.q.ExecContext(ctx,
		`UPDATE p2p_peers SET revoked_at = NULL WHERE peer_id = ?`, peerID)
	if err != nil {
		return fmt.Errorf("unrevoke peer: %w", err)
	}
	return nil
}

// GrantPeerScope adds a scope to peerID's scopes JSON array. Idempotent
// — granting an already-present scope is a no-op. Returns
// store.ErrNotFound when the peer doesn't exist (or is revoked, since
// granting scopes to a revoked peer would be silent corruption).
func (d *DB) GrantPeerScope(ctx context.Context, peerID, scope string) error {
	if peerID == "" || scope == "" {
		return errors.New("GrantPeerScope: peer_id + scope required")
	}
	return d.updatePeerScopes(ctx, peerID, true, func(current []string) ([]string, bool) {
		for _, s := range current {
			if s == scope {
				return nil, false // already granted
			}
		}
		next := append(append([]string(nil), current...), scope)
		return next, true
	})
}

// RevokePeerScope removes a scope from peerID's scopes. Idempotent —
// removing a non-present scope is a no-op. Returns store.ErrNotFound
// when the peer doesn't exist.
func (d *DB) RevokePeerScope(ctx context.Context, peerID, scope string) error {
	if peerID == "" || scope == "" {
		return errors.New("RevokePeerScope: peer_id + scope required")
	}
	return d.updatePeerScopes(ctx, peerID, false, func(current []string) ([]string, bool) {
		out := current[:0]
		for _, s := range current {
			if s != scope {
				out = append(out, s)
			}
		}
		if len(out) == len(current) {
			return nil, false // not present
		}
		return append([]string(nil), out...), true
	})
}

// updatePeerScopes applies a JSON-array scope mutation using a compare-and-swap
// update. Pairing UIs can grant several scopes at once; a plain read/append/write
// loses one grant when those updates overlap.
func (d *DB) updatePeerScopes(ctx context.Context, peerID string, activeOnly bool, mutate func([]string) ([]string, bool)) error {
	where := `peer_id = ?`
	args := []any{peerID}
	if activeOnly {
		where += ` AND revoked_at IS NULL`
	}

	for attempt := 0; attempt < peerScopeCASMaxAttempts; attempt++ {
		var raw string
		err := d.q.QueryRowContext(ctx, `SELECT scopes FROM p2p_peers WHERE `+where, args...).Scan(&raw)
		if errors.Is(err, sql.ErrNoRows) {
			return store.ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("read scopes: %w", err)
		}

		var current []string
		if err := json.Unmarshal([]byte(raw), &current); err != nil {
			current = nil
		}
		next, changed := mutate(current)
		if !changed {
			return nil
		}

		encoded, err := json.Marshal(next)
		if err != nil {
			return fmt.Errorf("marshal scopes: %w", err)
		}
		updateArgs := append([]any{string(encoded)}, args...)
		updateArgs = append(updateArgs, raw)
		res, err := d.q.ExecContext(ctx,
			`UPDATE p2p_peers SET scopes = ? WHERE `+where+` AND scopes = ?`,
			updateArgs...)
		if err != nil {
			return fmt.Errorf("update scopes: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("update scopes rows affected: %w", err)
		}
		if n == 1 {
			return nil
		}
	}
	return fmt.Errorf("update scopes: concurrent changes did not settle after %d attempts", peerScopeCASMaxAttempts)
}

// UpdateLastSeen sets last_seen for an active peer. Silently no-ops if the
// peer was revoked — that's a normal race and not a caller error.
func (d *DB) UpdateLastSeen(ctx context.Context, peerID string, t time.Time) error {
	_, err := d.q.ExecContext(ctx,
		`UPDATE p2p_peers SET last_seen = ? WHERE peer_id = ? AND revoked_at IS NULL`,
		formatTime(t.UTC()), peerID)
	if err != nil {
		return fmt.Errorf("update last_seen: %w", err)
	}
	return nil
}

// UpdateDisplayName renames an already-paired peer. Used by:
//   - the pairing handler (re-pair carries an updated display_name)
//   - the display_name_changed mesh event handler (peer renamed itself)
//
// Silently no-ops on revoked peers. Empty newName is a caller error.
func (d *DB) UpdateDisplayName(ctx context.Context, peerID, newName string) error {
	if peerID == "" || newName == "" {
		return errors.New("UpdateDisplayName: peer_id + new_name required")
	}
	_, err := d.q.ExecContext(ctx,
		`UPDATE p2p_peers SET display_name = ? WHERE peer_id = ? AND revoked_at IS NULL`,
		newName, peerID)
	if err != nil {
		return fmt.Errorf("update display_name: %w", err)
	}
	return nil
}

// UpdateSecretTransferRecipient records the peer's age X25519 recipient
// learned from a `peer_identity` mesh broadcast. Silently no-ops on
// revoked peers. Empty recipient clears (useful when the peer rotates
// keys but hasn't re-announced yet — caller can prefer "stale" data
// instead of "no data" depending on policy).
func (d *DB) UpdateSecretTransferRecipient(ctx context.Context, peerID, recipient string) error {
	if peerID == "" {
		return errors.New("UpdateSecretTransferRecipient: peer_id required")
	}
	_, err := d.q.ExecContext(ctx,
		`UPDATE p2p_peers SET secret_transfer_recipient = ? WHERE peer_id = ? AND revoked_at IS NULL`,
		recipient, peerID)
	if err != nil {
		return fmt.Errorf("update secret_transfer_recipient: %w", err)
	}
	return nil
}

// SetPeerSSHTarget records the SSH user@host alias the dashboard uses
// when the user clicks "Focus" on a peer-origin agent. Empty clears.
// Silently no-ops on revoked / absent peers — the caller (UI) will
// surface a 404 / no-op for those cases at the handler layer instead.
func (d *DB) SetPeerSSHTarget(ctx context.Context, peerID, target string) error {
	if peerID == "" {
		return errors.New("SetPeerSSHTarget: peer_id required")
	}
	_, err := d.q.ExecContext(ctx,
		`UPDATE p2p_peers SET ssh_target = ? WHERE peer_id = ? AND revoked_at IS NULL`,
		target, peerID)
	if err != nil {
		return fmt.Errorf("set ssh_target: %w", err)
	}
	return nil
}

// RememberPeerAddrs writes the JSON-encoded multiaddrs slice into
// last_known_addrs. Silently no-ops on revoked peers (the WHERE excludes
// them). Empty addrs slice is allowed and is persisted as "[]" (clears any
// previously cached addrs).
func (d *DB) RememberPeerAddrs(ctx context.Context, peerID string, addrs []string) error {
	if peerID == "" {
		return errors.New("RememberPeerAddrs: peer_id required")
	}
	if addrs == nil {
		addrs = []string{}
	}
	encoded, err := json.Marshal(addrs)
	if err != nil {
		return fmt.Errorf("marshal addrs: %w", err)
	}
	_, err = d.q.ExecContext(ctx,
		`UPDATE p2p_peers SET last_known_addrs = ?
		   WHERE peer_id = ? AND revoked_at IS NULL`,
		string(encoded), peerID)
	if err != nil {
		return fmt.Errorf("remember peer addrs: %w", err)
	}
	return nil
}

// LoadPeerAddrs reads last_known_addrs for peerID. Returns ([], nil) for
// unknown peers and for revoked peers (callers shouldn't be redialing
// revoked peers anyway).
func (d *DB) LoadPeerAddrs(ctx context.Context, peerID string) ([]string, error) {
	if peerID == "" {
		return nil, errors.New("LoadPeerAddrs: peer_id required")
	}
	row := d.q.QueryRowContext(ctx,
		`SELECT COALESCE(last_known_addrs, '[]') FROM p2p_peers
		   WHERE peer_id = ? AND revoked_at IS NULL`, peerID)
	var raw string
	switch err := row.Scan(&raw); {
	case errors.Is(err, sql.ErrNoRows):
		return []string{}, nil
	case err != nil:
		return nil, fmt.Errorf("load peer addrs: %w", err)
	}
	if raw == "" {
		return []string{}, nil
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, fmt.Errorf("unmarshal peer addrs: %w", err)
	}
	if out == nil {
		out = []string{}
	}
	return out, nil
}

// CreatePendingPair persists a 6-digit pairing code with its peer info.
func (d *DB) CreatePendingPair(ctx context.Context, p *store.P2PPendingPair) error {
	if p == nil || p.Code == "" || p.PeerID == "" {
		return errors.New("CreatePendingPair: code + peer_id required")
	}
	addrs, err := json.Marshal(p.Multiaddrs)
	if err != nil {
		return fmt.Errorf("marshal multiaddrs: %w", err)
	}
	created := p.CreatedAt
	if created.IsZero() {
		created = time.Now().UTC()
	}
	_, err = d.q.ExecContext(ctx, `
		INSERT INTO p2p_pending_pairs (code, peer_id, multiaddrs, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?)`,
		p.Code, p.PeerID, string(addrs),
		formatTime(created), formatTime(p.ExpiresAt.UTC()),
	)
	return mapConstraintError(err)
}

// GetPendingPair returns the pending pair record for a code. Returns
// store.ErrNotFound if absent.
func (d *DB) GetPendingPair(ctx context.Context, code string) (*store.P2PPendingPair, error) {
	row := d.q.QueryRowContext(ctx, `
		SELECT code, peer_id, multiaddrs, created_at, expires_at
		FROM p2p_pending_pairs WHERE code = ?`, code)
	var p store.P2PPendingPair
	var addrs, created, expires string
	if err := row.Scan(&p.Code, &p.PeerID, &addrs, &created, &expires); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrNotFound
		}
		return nil, fmt.Errorf("get pending pair: %w", err)
	}
	if err := json.Unmarshal([]byte(addrs), &p.Multiaddrs); err != nil {
		return nil, fmt.Errorf("unmarshal multiaddrs: %w", err)
	}
	p.CreatedAt = parseTime(created)
	p.ExpiresAt = parseTime(expires)
	return &p, nil
}

// DeletePendingPair removes a pending pair (consumed or otherwise).
// Idempotent: returns nil if the row was already gone.
func (d *DB) DeletePendingPair(ctx context.Context, code string) error {
	_, err := d.q.ExecContext(ctx,
		`DELETE FROM p2p_pending_pairs WHERE code = ?`, code)
	if err != nil {
		return fmt.Errorf("delete pending pair: %w", err)
	}
	return nil
}

// SweepExpiredPendingPairs purges pending-pair rows whose expires_at is
// before now and returns the count of rows removed.
func (d *DB) SweepExpiredPendingPairs(ctx context.Context, now time.Time) (int, error) {
	res, err := d.q.ExecContext(ctx,
		`DELETE FROM p2p_pending_pairs WHERE expires_at < ?`,
		formatTime(now.UTC()))
	if err != nil {
		return 0, fmt.Errorf("sweep pending pairs: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(n), nil
}

// scanner captures the bits of *sql.Row and *sql.Rows we need.
type scanner interface {
	Scan(dest ...any) error
}

// scanPeer reads one peer row.
func scanPeer(r scanner) (*store.P2PPeer, error) {
	var p store.P2PPeer
	var paired string
	var lastSeen, revoked sql.NullString
	var scopes string
	if err := r.Scan(&p.PeerID, &p.DisplayName, &paired, &lastSeen,
		&p.TrustLevel, &scopes, &revoked, &p.SSHTarget, &p.SecretTransferRecipient); err != nil {
		return nil, err
	}
	p.PairedAt = parseTime(paired)
	if lastSeen.Valid {
		p.LastSeen = parseTimePtr(&lastSeen.String)
	}
	if revoked.Valid {
		p.RevokedAt = parseTimePtr(&revoked.String)
	}
	if err := json.Unmarshal([]byte(scopes), &p.Scopes); err != nil {
		return nil, fmt.Errorf("unmarshal scopes: %w", err)
	}
	if p.Scopes == nil {
		p.Scopes = []string{}
	}
	return &p, nil
}

// encodeScopes marshals scopes to JSON, defaulting nil/empty to "[]".
func encodeScopes(scopes []string) (string, error) {
	if len(scopes) == 0 {
		return "[]", nil
	}
	b, err := json.Marshal(scopes)
	if err != nil {
		return "", fmt.Errorf("marshal scopes: %w", err)
	}
	return string(b), nil
}
