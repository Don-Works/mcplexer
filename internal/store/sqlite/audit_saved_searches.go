// audit_saved_searches.go — CRUD + evaluator for persisted audit alert
// definitions (audit_saved_searches, migration 116).
//
// A saved search is a free-text query (q) plus an AuditFilter subset
// (filter_json) plus a (window_sec, threshold_count) pair. The evaluator
// counts matches over [now-window, now] and fires when count >= threshold,
// debounced by last_fired_at so it won't re-fire within its own window.
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

const savedSearchCols = `id, name, q, filter_json, threshold_count,
	window_sec, workspace_id, enabled, last_fired_at, created_at`

func (d *DB) ListSavedSearches(ctx context.Context) ([]store.SavedSearch, error) {
	rows, err := d.q.QueryContext(ctx,
		`SELECT `+savedSearchCols+` FROM audit_saved_searches ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list saved searches: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []store.SavedSearch
	for rows.Next() {
		s, err := scanSavedSearch(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *s)
	}
	return out, rows.Err()
}

func (d *DB) GetSavedSearch(ctx context.Context, id string) (*store.SavedSearch, error) {
	row := d.q.QueryRowContext(ctx,
		`SELECT `+savedSearchCols+` FROM audit_saved_searches WHERE id = ?`, id)
	s, err := scanSavedSearch(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return s, nil
}

func (d *DB) CreateSavedSearch(ctx context.Context, s *store.SavedSearch) error {
	if s.ID == "" {
		s.ID = uuid.NewString()
	}
	if s.CreatedAt.IsZero() {
		s.CreatedAt = time.Now().UTC()
	}
	if s.WindowSec <= 0 {
		s.WindowSec = 3600
	}
	if s.ThresholdCount <= 0 {
		s.ThresholdCount = 1
	}
	filterJSON, err := marshalFilter(s.Filter)
	if err != nil {
		return err
	}
	_, err = d.q.ExecContext(ctx, `
		INSERT INTO audit_saved_searches
			(id, name, q, filter_json, threshold_count, window_sec,
			 workspace_id, enabled, last_fired_at, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		s.ID, s.Name, s.Q, filterJSON, s.ThresholdCount, s.WindowSec,
		s.WorkspaceID, boolToInt(s.Enabled), formatTimePtr(s.LastFiredAt),
		formatTime(s.CreatedAt),
	)
	if err != nil {
		return fmt.Errorf("create saved search: %w", err)
	}
	return nil
}

func (d *DB) UpdateSavedSearch(ctx context.Context, s *store.SavedSearch) error {
	filterJSON, err := marshalFilter(s.Filter)
	if err != nil {
		return err
	}
	res, err := d.q.ExecContext(ctx, `
		UPDATE audit_saved_searches SET
			name = ?, q = ?, filter_json = ?, threshold_count = ?,
			window_sec = ?, workspace_id = ?, enabled = ?, last_fired_at = ?
		WHERE id = ?`,
		s.Name, s.Q, filterJSON, s.ThresholdCount, s.WindowSec,
		s.WorkspaceID, boolToInt(s.Enabled), formatTimePtr(s.LastFiredAt), s.ID,
	)
	if err != nil {
		return fmt.Errorf("update saved search: %w", err)
	}
	return checkRowsAffected(res)
}

func (d *DB) DeleteSavedSearch(ctx context.Context, id string) error {
	res, err := d.q.ExecContext(ctx,
		`DELETE FROM audit_saved_searches WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete saved search: %w", err)
	}
	return checkRowsAffected(res)
}

// EvaluateSavedSearches runs every enabled saved search: counts matches
// over its rolling window and fires (stamps last_fired_at) when count >=
// threshold and the search hasn't fired within its own window.
func (d *DB) EvaluateSavedSearches(
	ctx context.Context, now time.Time,
) ([]store.FiredSavedSearch, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	searches, err := d.ListSavedSearches(ctx)
	if err != nil {
		return nil, err
	}
	var fired []store.FiredSavedSearch
	for _, s := range searches {
		if !s.Enabled {
			continue
		}
		window := time.Duration(s.WindowSec) * time.Second
		if s.LastFiredAt != nil && now.Sub(*s.LastFiredAt) < window {
			continue // debounce — already fired within its own window
		}
		f := savedSearchToFilter(s, now, window)
		n, err := d.CountAuditMatching(ctx, f)
		if err != nil {
			return nil, err
		}
		if n < s.ThresholdCount {
			continue
		}
		stamped := now
		s.LastFiredAt = &stamped
		if err := d.UpdateSavedSearch(ctx, &s); err != nil {
			return nil, err
		}
		fired = append(fired, store.FiredSavedSearch{Search: s, Count: n})
	}
	return fired, nil
}

// savedSearchToFilter projects a SavedSearch into an AuditFilter scoped to
// its rolling window. The filter map is applied via filterMapToAuditFilter
// so saved searches honour the same exact-match dimensions as live queries.
func savedSearchToFilter(s store.SavedSearch, now time.Time, window time.Duration) store.AuditFilter {
	f := filterMapToAuditFilter(s.Filter)
	f.Q = s.Q
	from := now.Add(-window)
	f.After = &from
	f.Before = &now
	if s.WorkspaceID != "" {
		ws := s.WorkspaceID
		f.WorkspaceID = &ws
	}
	return f
}

func scanSavedSearch(scan func(...any) error) (*store.SavedSearch, error) {
	var (
		s          store.SavedSearch
		filterJSON string
		enabled    int
		lastFired  sql.NullString
		createdAt  string
	)
	if err := scan(&s.ID, &s.Name, &s.Q, &filterJSON, &s.ThresholdCount,
		&s.WindowSec, &s.WorkspaceID, &enabled, &lastFired, &createdAt); err != nil {
		return nil, err
	}
	s.Enabled = enabled != 0
	s.CreatedAt = parseTime(createdAt)
	if lastFired.Valid && lastFired.String != "" {
		t := parseTime(lastFired.String)
		s.LastFiredAt = &t
	}
	if filterJSON != "" && filterJSON != "{}" {
		_ = json.Unmarshal([]byte(filterJSON), &s.Filter)
	}
	if s.Filter == nil {
		s.Filter = map[string]any{}
	}
	return &s, nil
}

func marshalFilter(m map[string]any) (string, error) {
	if len(m) == 0 {
		return "{}", nil
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "", fmt.Errorf("marshal saved-search filter: %w", err)
	}
	return string(b), nil
}
