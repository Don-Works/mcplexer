package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// CreateUser inserts a user row. Returns ErrAlreadyExists if user_id
// collides or (when IsSelf=true) if another self-row already exists.
func (d *DB) CreateUser(ctx context.Context, u *store.User) error {
	if u == nil || u.UserID == "" {
		return errors.New("CreateUser: user_id required")
	}
	if u.DisplayName == "" {
		return errors.New("CreateUser: display_name required")
	}
	created := u.CreatedAt
	if created.IsZero() {
		created = time.Now().UTC()
	}
	self := 0
	if u.IsSelf {
		self = 1
	}
	_, err := d.q.ExecContext(ctx, `
		INSERT INTO users (user_id, display_name, created_at, is_self)
		VALUES (?, ?, ?, ?)`,
		u.UserID, u.DisplayName, formatTime(created), self,
	)
	return mapConstraintError(err)
}

// GetUser fetches a user by ID. Returns ErrNotFound if absent.
func (d *DB) GetUser(ctx context.Context, userID string) (*store.User, error) {
	row := d.q.QueryRowContext(ctx, `
		SELECT user_id, display_name, created_at, is_self
		FROM users WHERE user_id = ?`, userID)
	u, err := scanUser(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get user: %w", err)
	}
	return u, nil
}

// GetSelfUser returns the singleton is_self=1 row. ErrNotFound when not yet
// bootstrapped (caller is expected to bootstrap on startup).
func (d *DB) GetSelfUser(ctx context.Context) (*store.User, error) {
	row := d.q.QueryRowContext(ctx, `
		SELECT user_id, display_name, created_at, is_self
		FROM users WHERE is_self = 1 LIMIT 1`)
	u, err := scanUser(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get self user: %w", err)
	}
	return u, nil
}

// ListUsers returns every user row, ordered with self first then by
// display_name for stable UI rendering.
func (d *DB) ListUsers(ctx context.Context) ([]store.User, error) {
	rows, err := d.q.QueryContext(ctx, `
		SELECT user_id, display_name, created_at, is_self
		FROM users ORDER BY is_self DESC, display_name ASC`)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	var out []store.User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, fmt.Errorf("scan user: %w", err)
		}
		out = append(out, *u)
	}
	return out, rows.Err()
}

// UpdateUserDisplayName changes a user's display_name. ErrNotFound when
// the user_id is unknown.
func (d *DB) UpdateUserDisplayName(ctx context.Context, userID, displayName string) error {
	if displayName == "" {
		return errors.New("UpdateUserDisplayName: display_name required")
	}
	res, err := d.q.ExecContext(ctx,
		`UPDATE users SET display_name = ? WHERE user_id = ?`,
		displayName, userID,
	)
	if err != nil {
		return fmt.Errorf("update user display_name: %w", err)
	}
	return checkRowsAffected(res)
}

// UpsertUser inserts a user with displayName, or updates the display_name
// if the row already exists. is_self defaults to 0 — this method is for
// remote users (the bootstrap path uses CreateUser directly to set
// is_self=1).
func (d *DB) UpsertUser(ctx context.Context, userID, displayName string) error {
	if userID == "" || displayName == "" {
		return errors.New("UpsertUser: user_id + display_name required")
	}
	_, err := d.q.ExecContext(ctx, `
		INSERT INTO users (user_id, display_name, created_at, is_self)
		VALUES (?, ?, ?, 0)
		ON CONFLICT(user_id) DO UPDATE SET display_name = excluded.display_name`,
		userID, displayName, formatTime(time.Now().UTC()),
	)
	if err != nil {
		return fmt.Errorf("upsert user: %w", err)
	}
	return nil
}

// LinkPeerToUser inserts into peer_users. Idempotent: duplicate (peer_id,
// user_id) returns nil so re-pair flows no-op cleanly.
func (d *DB) LinkPeerToUser(ctx context.Context, peerID, userID string) error {
	if peerID == "" || userID == "" {
		return errors.New("LinkPeerToUser: peer_id + user_id required")
	}
	_, err := d.q.ExecContext(ctx, `
		INSERT OR IGNORE INTO peer_users (peer_id, user_id)
		VALUES (?, ?)`, peerID, userID)
	if err != nil {
		return fmt.Errorf("link peer to user: %w", err)
	}
	return nil
}

// GetUserForPeer returns the user a peer maps to. Multiple users could
// theoretically claim a peer (the PK is composite); we return the first by
// the user's created_at ordering, which is deterministic.
func (d *DB) GetUserForPeer(ctx context.Context, peerID string) (*store.User, error) {
	row := d.q.QueryRowContext(ctx, `
		SELECT u.user_id, u.display_name, u.created_at, u.is_self
		FROM users u
		JOIN peer_users pu ON pu.user_id = u.user_id
		WHERE pu.peer_id = ?
		ORDER BY u.created_at ASC LIMIT 1`, peerID)
	u, err := scanUser(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get user for peer: %w", err)
	}
	return u, nil
}

// ListPeersForUser returns every peer linked to a user, ordered paired_at
// desc to match P2PPeerStore.ListPeers.
func (d *DB) ListPeersForUser(ctx context.Context, userID string) ([]store.P2PPeer, error) {
	rows, err := d.q.QueryContext(ctx, `
		SELECT p.peer_id, p.display_name, p.paired_at, p.last_seen,
		       p.trust_level, p.scopes, p.revoked_at, p.ssh_target, p.secret_transfer_recipient
		FROM p2p_peers p
		JOIN peer_users pu ON pu.peer_id = p.peer_id
		WHERE pu.user_id = ?
		ORDER BY p.paired_at DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("list peers for user: %w", err)
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

// scanUser reads a single user row from a *sql.Row or *sql.Rows.
func scanUser(r scanner) (*store.User, error) {
	var u store.User
	var created string
	var self int
	if err := r.Scan(&u.UserID, &u.DisplayName, &created, &self); err != nil {
		return nil, err
	}
	u.CreatedAt = parseTime(created)
	u.IsSelf = self != 0
	return &u, nil
}
