package p2p

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// SQLPeerLookup adapts a *sql.DB to the PeerLookup interface used by the
// discovery service. It reads/writes the `p2p_peers` table created by M1.2
// (and extended with `connection_mode` by M1.3's migration).
//
// Defensive contract: when the table does not yet exist (e.g. M1.2 hasn't
// merged on this branch) every call returns "peer not paired" / no-op.
type SQLPeerLookup struct {
	db     *sql.DB
	logger *slog.Logger
}

// NewSQLPeerLookup constructs a PeerLookup over a sqlite database.
func NewSQLPeerLookup(db *sql.DB, logger *slog.Logger) *SQLPeerLookup {
	if logger == nil {
		logger = slog.Default()
	}
	return &SQLPeerLookup{db: db, logger: logger}
}

// IsPaired implements PeerLookup. A revoked peer is NOT paired: revocation
// sets revoked_at, and this predicate is the sole authorization gate on the
// always-on transport surfaces (inbound mesh, agent-directory gossip, mDNS
// auto-dial), so it MUST exclude revoked rows exactly like the sibling
// address/enumeration queries below do.
func (s *SQLPeerLookup) IsPaired(ctx context.Context, peerID string) (bool, error) {
	if s == nil || s.db == nil || peerID == "" {
		return false, nil
	}
	row := s.db.QueryRowContext(ctx,
		`SELECT 1 FROM p2p_peers WHERE peer_id = ? AND revoked_at IS NULL LIMIT 1`, peerID)
	var n int
	switch err := row.Scan(&n); {
	case err == nil:
		return true, nil
	case err == sql.ErrNoRows:
		return false, nil
	case IsTableMissing(err):
		return false, nil
	default:
		return false, fmt.Errorf("p2p_peers lookup: %w", err)
	}
}

// MarkConnectionMode implements PeerLookup. Best-effort; logs on failure.
func (s *SQLPeerLookup) MarkConnectionMode(
	ctx context.Context, peerID string, mode ConnectionMode,
) {
	if s == nil || s.db == nil || peerID == "" {
		return
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.ExecContext(ctx,
		`UPDATE p2p_peers
		   SET connection_mode = ?, last_seen = ?
		 WHERE peer_id = ?`,
		string(mode), now, peerID)
	if err == nil {
		return
	}
	if IsTableMissing(err) {
		return
	}
	s.logger.Debug("p2p: failed to update connection_mode",
		"peer", peerID, "mode", mode, "err", err)
}

// RememberPeerAddrs implements PeerLookup. Persists the JSON-encoded addr
// list into p2p_peers.last_known_addrs so the next daemon boot can hot-
// start the libp2p peerstore. Best-effort; logs on failure.
func (s *SQLPeerLookup) RememberPeerAddrs(
	ctx context.Context, peerID string, addrs []string,
) {
	if s == nil || s.db == nil || peerID == "" {
		return
	}
	if addrs == nil {
		addrs = []string{}
	}
	encoded, err := json.Marshal(addrs)
	if err != nil {
		s.logger.Debug("p2p: marshal last_known_addrs",
			"peer", peerID, "err", err)
		return
	}
	_, err = s.db.ExecContext(ctx,
		`UPDATE p2p_peers SET last_known_addrs = ?
		   WHERE peer_id = ? AND revoked_at IS NULL`,
		string(encoded), peerID)
	if err == nil || IsTableMissing(err) || isMissingColumn(err) {
		return
	}
	s.logger.Debug("p2p: failed to update last_known_addrs",
		"peer", peerID, "err", err)
}

// LoadPeerAddrs implements PeerLookup. Returns the most recently persisted
// addr list for peerID, or nil for unknown peers / missing table / missing
// column. Errors are logged.
func (s *SQLPeerLookup) LoadPeerAddrs(
	ctx context.Context, peerID string,
) []string {
	if s == nil || s.db == nil || peerID == "" {
		return nil
	}
	row := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(last_known_addrs, '[]') FROM p2p_peers
		   WHERE peer_id = ? AND revoked_at IS NULL`, peerID)
	var raw string
	switch err := row.Scan(&raw); {
	case err == nil:
	case err == sql.ErrNoRows:
		return nil
	case IsTableMissing(err) || isMissingColumn(err):
		return nil
	default:
		s.logger.Debug("p2p: load last_known_addrs",
			"peer", peerID, "err", err)
		return nil
	}
	if raw == "" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		s.logger.Debug("p2p: unmarshal last_known_addrs",
			"peer", peerID, "err", err)
		return nil
	}
	return out
}

// isMissingColumn detects sqlite "no such column" errors so this branch
// keeps running on databases stuck at an older migration revision.
func isMissingColumn(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "no such column")
}

// ListPeerIDs returns the libp2p peer IDs of every active (non-revoked) row
// in the p2p_peers table. Empty (not error) when the table does not exist.
// Used by the libp2p mesh transport to enumerate broadcast targets.
func (s *SQLPeerLookup) ListPeerIDs(ctx context.Context) ([]string, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT peer_id FROM p2p_peers WHERE revoked_at IS NULL`)
	if err != nil {
		if IsTableMissing(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("p2p_peers list: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ListLocalWorkspaceIDsForPeer returns the distinct local workspace IDs the
// given peer is bound to via workspace_peer_bindings — the authoritative
// authorization set for workspace-scoped pairing. The libp2p mesh transport
// uses it to scope OUTBOUND broadcasts: a workspace-scoped envelope is only
// delivered to peers whose binding set contains that workspace.
//
// Returns an empty slice (not an error) for an unbound peer or a missing
// workspace_peer_bindings table — both mean "no authorization", i.e.
// default-deny at the call site.
func (s *SQLPeerLookup) ListLocalWorkspaceIDsForPeer(ctx context.Context, peerID string) ([]string, error) {
	if s == nil || s.db == nil || peerID == "" {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT DISTINCT local_workspace_id
		   FROM workspace_peer_bindings
		  WHERE peer_id = ? AND local_workspace_id != ''`, peerID)
	if err != nil {
		if IsTableMissing(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("p2p workspace bindings list: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// PeerStatus mirrors the JSON shape returned by GET /api/p2p/peers/{id}/status.
//
// Reconnect fields are filled in by the API handler from the in-memory
// Reconnector — they are NOT persisted in p2p_peers. They appear in the
// JSON only when set (omitempty), so a stub-build response is unchanged.
type PeerStatus struct {
	PeerID            string   `json:"peer_id"`
	ConnectionMode    string   `json:"connection_mode,omitempty"`
	LastSeen          string   `json:"last_seen,omitempty"`
	Addrs             []string `json:"addrs"`
	LastDialAttemptAt string   `json:"last_dial_attempt_at,omitempty"`
	LastDialError     string   `json:"last_dial_error,omitempty"`
	ReconnectState    string   `json:"reconnect_state,omitempty"`
}

// GetPeerStatus reads (connection_mode, last_seen) for a paired peer. The
// caller is responsible for filling in Addrs (which lives in the libp2p
// peerstore, not the SQL row). Returns a zero-value PeerStatus and no error
// when the row is missing — that includes the "table missing" case.
func (s *SQLPeerLookup) GetPeerStatus(
	ctx context.Context, peerID string,
) (PeerStatus, error) {
	out := PeerStatus{PeerID: peerID, Addrs: []string{}}
	if s == nil || s.db == nil || peerID == "" {
		return out, nil
	}
	row := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(connection_mode, ''), COALESCE(last_seen, '')
		   FROM p2p_peers WHERE peer_id = ? LIMIT 1`, peerID)
	var mode, last string
	switch err := row.Scan(&mode, &last); {
	case err == nil:
		out.ConnectionMode = mode
		out.LastSeen = last
		return out, nil
	case err == sql.ErrNoRows:
		return out, nil
	case IsTableMissing(err):
		return out, nil
	default:
		return out, fmt.Errorf("p2p_peers status: %w", err)
	}
}
