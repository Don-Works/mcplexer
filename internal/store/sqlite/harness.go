package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

const harnessInitCols = `key, last_initialize_at, client_info, bootstrap_installed,
    bootstrap_version, bootstrap_hash, registry_version, drifted, updated_at`

// RecordHarnessInitialize upserts the last_initialize_at = now and
// client_info for the harness key. Leaves bootstrap fields alone.
func (d *DB) RecordHarnessInitialize(ctx context.Context, key, clientInfo string) error {
	if key == "" {
		return errors.New("RecordHarnessInitialize: key required")
	}
	now := time.Now().UTC()
	_, err := d.q.ExecContext(ctx, `
		INSERT INTO harness_initializations (key, last_initialize_at, client_info, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET
			last_initialize_at = excluded.last_initialize_at,
			client_info = excluded.client_info,
			updated_at = excluded.updated_at
	`, key, formatTime(now), clientInfo, formatTime(now))
	if err != nil {
		return fmt.Errorf("record harness init: %w", err)
	}
	return nil
}

// GetHarnessInitialization returns the row or ErrNotFound.
func (d *DB) GetHarnessInitialization(ctx context.Context, key string) (*store.HarnessInitialization, error) {
	row := d.q.QueryRowContext(ctx,
		`SELECT `+harnessInitCols+` FROM harness_initializations WHERE key = ?`, key)
	h, err := scanHarnessInit(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get harness init: %w", err)
	}
	return h, nil
}

// ListHarnessInitializations returns all rows (for status aggregation).
func (d *DB) ListHarnessInitializations(ctx context.Context) ([]store.HarnessInitialization, error) {
	rows, err := d.q.QueryContext(ctx,
		`SELECT `+harnessInitCols+` FROM harness_initializations ORDER BY key`)
	if err != nil {
		return nil, fmt.Errorf("list harness inits: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []store.HarnessInitialization
	for rows.Next() {
		h, err := scanHarnessInit(rows)
		if err != nil {
			return nil, fmt.Errorf("scan harness init: %w", err)
		}
		out = append(out, *h)
	}
	return out, rows.Err()
}

// UpsertHarnessBootstrap writes the bootstrap receipt fields (installed,
// version, hash, registry ver, drifted). updated_at bumped.
func (d *DB) UpsertHarnessBootstrap(ctx context.Context, h *store.HarnessInitialization) error {
	if h == nil || h.Key == "" {
		return errors.New("UpsertHarnessBootstrap: key required")
	}
	h.UpdatedAt = time.Now().UTC()
	_, err := d.q.ExecContext(ctx, `
		INSERT INTO harness_initializations (key, bootstrap_installed, bootstrap_version,
			bootstrap_hash, registry_version, drifted, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET
			bootstrap_installed = excluded.bootstrap_installed,
			bootstrap_version   = excluded.bootstrap_version,
			bootstrap_hash      = excluded.bootstrap_hash,
			registry_version    = excluded.registry_version,
			drifted             = excluded.drifted,
			updated_at          = excluded.updated_at
	`, h.Key, boolToInt(h.BootstrapInstalled), intPtrOrNull(h.BootstrapVersion),
		h.BootstrapHash, h.RegistryVersion, boolToInt(h.Drifted),
		formatTime(h.UpdatedAt))
	if err != nil {
		return fmt.Errorf("upsert harness bootstrap: %w", err)
	}
	return nil
}

func scanHarnessInit(r scanner) (*store.HarnessInitialization, error) {
	var (
		h                    store.HarnessInitialization
		bsInstalled, drifted int
		bsVer                sql.NullInt64
		lastInit, clientInfo sql.NullString
		updated              string
	)
	err := r.Scan(
		&h.Key, &lastInit, &clientInfo,
		&bsInstalled, &bsVer, &h.BootstrapHash, &h.RegistryVersion, &drifted, &updated,
	)
	if err != nil {
		return nil, err
	}
	h.BootstrapInstalled = bsInstalled != 0
	h.Drifted = drifted != 0
	if bsVer.Valid {
		v := int(bsVer.Int64)
		h.BootstrapVersion = &v
	}
	if lastInit.Valid {
		t := parseTime(lastInit.String)
		h.LastInitializeAt = &t
	}
	if clientInfo.Valid {
		h.ClientInfo = clientInfo.String
	}
	h.UpdatedAt = parseTime(updated)
	return &h, nil
}

func intPtrOrNull(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}
