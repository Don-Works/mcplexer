package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/google/uuid"
)

// workerMeshTriggerCols enumerates every column in worker_mesh_triggers.
// Used by both scan paths so adding a column is a one-line edit.
const workerMeshTriggerCols = `id, worker_id, tag_match, kind_match,
    audience_match, content_regex, from_filter_json,
    throttle_seconds, max_chain_depth, enabled, created_at, updated_at,
    status_from_match, status_to_match`

// CreateWorkerMeshTrigger inserts a new trigger row. ID is generated when
// empty. Defaults: ThrottleSeconds=60, MaxChainDepth=3, Enabled=true.
func (d *DB) CreateWorkerMeshTrigger(ctx context.Context, t *store.WorkerMeshTrigger) error {
	if t == nil {
		return errors.New("CreateWorkerMeshTrigger: trigger required")
	}
	if t.WorkerID == "" {
		return errors.New("CreateWorkerMeshTrigger: worker_id required")
	}
	if t.ID == "" {
		t.ID = uuid.NewString()
	}
	applyTriggerDefaults(t)
	now := time.Now().UTC()
	if t.CreatedAt.IsZero() {
		t.CreatedAt = now
	}
	if t.UpdatedAt.IsZero() {
		t.UpdatedAt = now
	}
	filtersJSON, err := marshalFromFilters(t.FromFilters)
	if err != nil {
		return fmt.Errorf("marshal from filters: %w", err)
	}
	_, err = d.q.ExecContext(ctx, `
		INSERT INTO worker_mesh_triggers (`+workerMeshTriggerCols+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ID, t.WorkerID, t.TagMatch, t.KindMatch,
		t.AudienceMatch, t.ContentRegex, filtersJSON,
		t.ThrottleSeconds, t.MaxChainDepth, boolToInt(t.Enabled),
		formatTime(t.CreatedAt), formatTime(t.UpdatedAt),
		t.StatusFromMatch, t.StatusToMatch,
	)
	return mapConstraintError(err)
}

// applyTriggerDefaults fills zero-valued fields with sane defaults.
// Mutates in place — called by Create and Update.
func applyTriggerDefaults(t *store.WorkerMeshTrigger) {
	if t.ThrottleSeconds <= 0 {
		t.ThrottleSeconds = 60
	}
	if t.MaxChainDepth <= 0 {
		t.MaxChainDepth = 3
	}
	if t.FromFilters == nil {
		t.FromFilters = []store.TriggerFromFilter{}
	}
}

// marshalFromFilters renders the canonical from_filter_json column.
// Nil/empty serialises to "[]" so the NOT NULL DEFAULT holds.
func marshalFromFilters(f []store.TriggerFromFilter) (string, error) {
	if len(f) == 0 {
		return "[]", nil
	}
	b, err := json.Marshal(f)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// unmarshalFromFilters parses the column. Empty / parse failure yields
// an empty slice — the dispatcher treats it as "anyone".
func unmarshalFromFilters(s string) []store.TriggerFromFilter {
	if s == "" || s == "[]" || s == "null" {
		return []store.TriggerFromFilter{}
	}
	var out []store.TriggerFromFilter
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return []store.TriggerFromFilter{}
	}
	if out == nil {
		return []store.TriggerFromFilter{}
	}
	return out
}

// GetWorkerMeshTrigger returns one row by id or ErrWorkerMeshTriggerNotFound.
func (d *DB) GetWorkerMeshTrigger(ctx context.Context, id string) (*store.WorkerMeshTrigger, error) {
	row := d.q.QueryRowContext(ctx,
		`SELECT `+workerMeshTriggerCols+` FROM worker_mesh_triggers WHERE id = ?`, id)
	t, err := scanWorkerMeshTrigger(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrWorkerMeshTriggerNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get worker mesh trigger: %w", err)
	}
	return t, nil
}

// ListWorkerMeshTriggers returns every row for workerID ordered created_at ASC.
func (d *DB) ListWorkerMeshTriggers(ctx context.Context, workerID string) ([]*store.WorkerMeshTrigger, error) {
	rows, err := d.q.QueryContext(ctx, `
		SELECT `+workerMeshTriggerCols+`
		FROM worker_mesh_triggers
		WHERE worker_id = ?
		ORDER BY created_at ASC`,
		workerID)
	if err != nil {
		return nil, fmt.Errorf("list worker mesh triggers: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return collectTriggerRows(rows)
}

// ListAllEnabledMeshTriggers returns every enabled trigger row across
// all workers. Used to hydrate the dispatcher cache.
func (d *DB) ListAllEnabledMeshTriggers(ctx context.Context) ([]*store.WorkerMeshTrigger, error) {
	rows, err := d.q.QueryContext(ctx, `
		SELECT `+workerMeshTriggerCols+`
		FROM worker_mesh_triggers
		WHERE enabled = 1
		ORDER BY worker_id ASC, created_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("list all enabled mesh triggers: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return collectTriggerRows(rows)
}

// collectTriggerRows scans every row from the cursor. Extracted so the
// list / list-enabled paths don't duplicate the scan loop.
func collectTriggerRows(rows *sql.Rows) ([]*store.WorkerMeshTrigger, error) {
	out := []*store.WorkerMeshTrigger{}
	for rows.Next() {
		t, err := scanWorkerMeshTrigger(rows)
		if err != nil {
			return nil, fmt.Errorf("scan worker mesh trigger: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// UpdateWorkerMeshTrigger writes every mutable column and bumps updated_at.
// Returns ErrWorkerMeshTriggerNotFound when the row is missing.
func (d *DB) UpdateWorkerMeshTrigger(ctx context.Context, t *store.WorkerMeshTrigger) error {
	if t == nil || t.ID == "" {
		return errors.New("UpdateWorkerMeshTrigger: id required")
	}
	applyTriggerDefaults(t)
	t.UpdatedAt = time.Now().UTC()
	filtersJSON, err := marshalFromFilters(t.FromFilters)
	if err != nil {
		return fmt.Errorf("marshal from filters: %w", err)
	}
	res, err := d.q.ExecContext(ctx, `
		UPDATE worker_mesh_triggers
		SET tag_match = ?, kind_match = ?, audience_match = ?,
		    content_regex = ?, from_filter_json = ?,
		    throttle_seconds = ?, max_chain_depth = ?,
		    enabled = ?, updated_at = ?,
		    status_from_match = ?, status_to_match = ?
		WHERE id = ?`,
		t.TagMatch, t.KindMatch, t.AudienceMatch,
		t.ContentRegex, filtersJSON,
		t.ThrottleSeconds, t.MaxChainDepth,
		boolToInt(t.Enabled), formatTime(t.UpdatedAt),
		t.StatusFromMatch, t.StatusToMatch,
		t.ID,
	)
	if err != nil {
		return mapConstraintError(err)
	}
	return mapNotFound(res, store.ErrWorkerMeshTriggerNotFound)
}

// DeleteWorkerMeshTrigger hard-deletes the row. Returns
// ErrWorkerMeshTriggerNotFound when missing.
func (d *DB) DeleteWorkerMeshTrigger(ctx context.Context, id string) error {
	res, err := d.q.ExecContext(ctx,
		`DELETE FROM worker_mesh_triggers WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete worker mesh trigger: %w", err)
	}
	return mapNotFound(res, store.ErrWorkerMeshTriggerNotFound)
}

// HasPeerScope returns true when peerID's scopes array (in p2p_peers)
// contains scope. Unknown / revoked peers return (false, nil) so the
// caller treats both as "no permission" without leaking peer existence.
func (d *DB) HasPeerScope(ctx context.Context, peerID, scope string) (bool, error) {
	if peerID == "" || scope == "" {
		return false, nil
	}
	var raw string
	err := d.q.QueryRowContext(ctx,
		`SELECT scopes FROM p2p_peers WHERE peer_id = ? AND revoked_at IS NULL`,
		peerID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read scopes: %w", err)
	}
	var current []string
	if err := json.Unmarshal([]byte(raw), &current); err != nil {
		return false, nil
	}
	for _, s := range current {
		if s == scope {
			return true, nil
		}
	}
	return false, nil
}

// scanWorkerMeshTrigger reads one row.
func scanWorkerMeshTrigger(r scanner) (*store.WorkerMeshTrigger, error) {
	var (
		t                    store.WorkerMeshTrigger
		enabled              int
		filtersJSON          string
		createdAt, updatedAt string
	)
	err := r.Scan(
		&t.ID, &t.WorkerID, &t.TagMatch, &t.KindMatch,
		&t.AudienceMatch, &t.ContentRegex, &filtersJSON,
		&t.ThrottleSeconds, &t.MaxChainDepth, &enabled,
		&createdAt, &updatedAt,
		&t.StatusFromMatch, &t.StatusToMatch,
	)
	if err != nil {
		return nil, err
	}
	t.Enabled = enabled != 0
	t.FromFilters = unmarshalFromFilters(filtersJSON)
	t.CreatedAt = parseTime(createdAt)
	t.UpdatedAt = parseTime(updatedAt)
	return &t, nil
}
