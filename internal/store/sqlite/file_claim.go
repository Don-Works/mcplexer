package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

const fileClaimCols = `claim_id, claimer_user_id, claimer_peer_id, claimer_display_name,
    repo, branch, paths_json, intent, claimed_at, expires_at, released_at`

// InsertFileClaim persists one advisory file claim (table from migration
// 031). ClaimID is required — callers use deterministic IDs (e.g.
// "fc-<delegation_id>") so release-by-ID needs no lookup.
func (d *DB) InsertFileClaim(ctx context.Context, c *store.FileClaim) error {
	if c == nil {
		return errors.New("InsertFileClaim: claim required")
	}
	if c.ClaimID == "" {
		return errors.New("InsertFileClaim: claim_id required")
	}
	if len(c.Paths) == 0 {
		return errors.New("InsertFileClaim: at least one path required")
	}
	if c.ClaimedAt.IsZero() {
		c.ClaimedAt = time.Now().UTC()
	}
	paths, err := json.Marshal(c.Paths)
	if err != nil {
		return fmt.Errorf("InsertFileClaim: marshal paths: %w", err)
	}
	_, err = d.q.ExecContext(ctx, `
        INSERT INTO file_claims (`+fileClaimCols+`)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL)`,
		c.ClaimID, c.ClaimerUserID, c.ClaimerPeerID, c.ClaimerDisplayName,
		c.Repo, c.Branch, string(paths), c.Intent,
		formatTime(c.ClaimedAt), formatTime(c.ExpiresAt))
	if err != nil {
		return fmt.Errorf("InsertFileClaim: %w", err)
	}
	return nil
}

// ListFileClaims returns claims matching the filter, newest first. Path
// filtering glob-matches each claim's stored path patterns against the
// filter's literal path (per the FileClaimFilter contract); Claimer is a
// substring match over user id, peer id, and display name.
func (d *DB) ListFileClaims(ctx context.Context, f store.FileClaimFilter) ([]store.FileClaim, error) {
	q := `SELECT ` + fileClaimCols + ` FROM file_claims WHERE 1=1`
	var args []any
	if f.Repo != "" {
		q += ` AND repo = ?`
		args = append(args, f.Repo)
	}
	if f.Branch != "" {
		q += ` AND branch = ?`
		args = append(args, f.Branch)
	}
	if f.ActiveOnly {
		now := f.Now
		if now.IsZero() {
			now = time.Now().UTC()
		}
		q += ` AND released_at IS NULL AND expires_at > ?`
		args = append(args, formatTime(now))
	}
	q += ` ORDER BY claimed_at DESC`

	rows, err := d.q.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("ListFileClaims: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	var out []store.FileClaim
	for rows.Next() {
		c, err := scanFileClaim(rows)
		if err != nil {
			return nil, err
		}
		if f.Claimer != "" && !fileClaimMatchesClaimer(c, f.Claimer) {
			continue
		}
		if f.Path != "" && !fileClaimMatchesPath(c, f.Path) {
			continue
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ReleaseFileClaim marks a claim released. Releasing an unknown or
// already-released claim returns store.ErrNotFound.
func (d *DB) ReleaseFileClaim(ctx context.Context, claimID string, releasedAt time.Time) error {
	if claimID == "" {
		return errors.New("ReleaseFileClaim: claim_id required")
	}
	if releasedAt.IsZero() {
		releasedAt = time.Now().UTC()
	}
	res, err := d.q.ExecContext(ctx, `
        UPDATE file_claims SET released_at = ? WHERE claim_id = ? AND released_at IS NULL`,
		formatTime(releasedAt), claimID)
	if err != nil {
		return fmt.Errorf("ReleaseFileClaim: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("ReleaseFileClaim: %w", err)
	}
	if n == 0 {
		return store.ErrNotFound
	}
	return nil
}

func scanFileClaim(rows *sql.Rows) (store.FileClaim, error) {
	var c store.FileClaim
	var pathsJSON, claimedAt, expiresAt string
	var releasedAt *string
	if err := rows.Scan(&c.ClaimID, &c.ClaimerUserID, &c.ClaimerPeerID, &c.ClaimerDisplayName,
		&c.Repo, &c.Branch, &pathsJSON, &c.Intent, &claimedAt, &expiresAt, &releasedAt); err != nil {
		return c, fmt.Errorf("scan file claim: %w", err)
	}
	if err := json.Unmarshal([]byte(pathsJSON), &c.Paths); err != nil {
		return c, fmt.Errorf("scan file claim paths: %w", err)
	}
	c.ClaimedAt = parseTime(claimedAt)
	c.ExpiresAt = parseTime(expiresAt)
	c.ReleasedAt = parseTimePtr(releasedAt)
	return c, nil
}

func fileClaimMatchesClaimer(c store.FileClaim, claimer string) bool {
	return strings.Contains(c.ClaimerUserID, claimer) ||
		strings.Contains(c.ClaimerPeerID, claimer) ||
		strings.Contains(c.ClaimerDisplayName, claimer)
}

func fileClaimMatchesPath(c store.FileClaim, literal string) bool {
	for _, pattern := range c.Paths {
		if pattern == literal {
			return true
		}
		if ok, err := path.Match(pattern, literal); err == nil && ok {
			return true
		}
	}
	return false
}
