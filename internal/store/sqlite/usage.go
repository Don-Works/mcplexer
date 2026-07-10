// Package sqlite — usage.go implements UsageStore for persisting cached
// usage snapshots (task 01KX685FTG7CJ7X591KNSYGPSD).
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/don-works/mcplexer/internal/store"
)

// UpsertCachedProviderSnapshot inserts or replaces a provider snapshot.
func (d *DB) UpsertCachedProviderSnapshot(
	ctx context.Context, s *store.CachedProviderSnapshot,
) error {
	if s == nil || s.Provider == "" {
		return errors.New("UpsertCachedProviderSnapshot: provider required")
	}
	_, err := d.q.ExecContext(ctx, `
		INSERT INTO usage_provider_cache (provider, snapshot, updated_at)
		VALUES (?, ?, ?)
		ON CONFLICT(provider) DO UPDATE SET
			snapshot = excluded.snapshot,
			updated_at = excluded.updated_at`,
		s.Provider, s.Snapshot, formatTime(s.UpdatedAt),
	)
	if err != nil {
		return fmt.Errorf("upsert provider snapshot %s: %w", s.Provider, err)
	}
	return nil
}

// GetCachedProviderSnapshot returns one provider's cached snapshot.
func (d *DB) GetCachedProviderSnapshot(
	ctx context.Context, provider string,
) (*store.CachedProviderSnapshot, error) {
	var s store.CachedProviderSnapshot
	var updatedAt string
	err := d.q.QueryRowContext(ctx, `
		SELECT provider, snapshot, updated_at
		FROM usage_provider_cache WHERE provider = ?`,
		provider,
	).Scan(&s.Provider, &s.Snapshot, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get provider snapshot %s: %w", provider, err)
	}
	s.UpdatedAt = parseTime(updatedAt)
	return &s, nil
}

// ListCachedProviderSnapshots returns every cached provider snapshot.
func (d *DB) ListCachedProviderSnapshots(
	ctx context.Context,
) ([]store.CachedProviderSnapshot, error) {
	rows, err := d.q.QueryContext(ctx, `
		SELECT provider, snapshot, updated_at
		FROM usage_provider_cache ORDER BY provider`)
	if err != nil {
		return nil, fmt.Errorf("list provider snapshots: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []store.CachedProviderSnapshot
	for rows.Next() {
		var s store.CachedProviderSnapshot
		var updatedAt string
		if err := rows.Scan(&s.Provider, &s.Snapshot, &updatedAt); err != nil {
			return nil, fmt.Errorf("scan provider snapshot: %w", err)
		}
		s.UpdatedAt = parseTime(updatedAt)
		out = append(out, s)
	}
	return out, rows.Err()
}

// UpsertCachedOpenRouter inserts or replaces the OpenRouter snapshot.
// The table holds at most one row (id=1).
func (d *DB) UpsertCachedOpenRouter(
	ctx context.Context, s *store.CachedOpenRouter,
) error {
	if s == nil {
		return errors.New("UpsertCachedOpenRouter: snapshot required")
	}
	_, err := d.q.ExecContext(ctx, `
		INSERT INTO usage_openrouter_cache (id, snapshot, updated_at)
		VALUES (1, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			snapshot = excluded.snapshot,
			updated_at = excluded.updated_at`,
		s.Snapshot, formatTime(s.UpdatedAt),
	)
	if err != nil {
		return fmt.Errorf("upsert openrouter snapshot: %w", err)
	}
	return nil
}

// GetCachedOpenRouter returns the cached OpenRouter snapshot.
func (d *DB) GetCachedOpenRouter(
	ctx context.Context,
) (*store.CachedOpenRouter, error) {
	var s store.CachedOpenRouter
	var updatedAt string
	err := d.q.QueryRowContext(ctx, `
		SELECT id, snapshot, updated_at
		FROM usage_openrouter_cache WHERE id = 1`,
	).Scan(&s.ID, &s.Snapshot, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get openrouter snapshot: %w", err)
	}
	s.UpdatedAt = parseTime(updatedAt)
	return &s, nil
}
