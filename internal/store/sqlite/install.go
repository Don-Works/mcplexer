package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

const installedClientCols = `id, name, config_path, installed, hooks_installed,
    hooks_drifted, shim_installed, sandbox_enabled, installed_at, updated_at`

const installReceiptCols = `id, client_id, action, target_path, backup_path,
    reverse_data, applied_at, reversed_at, reverse_error`

// UpsertInstalledClient inserts a new client row or updates every field
// (apart from id) on conflict. updated_at is always bumped to now.
func (d *DB) UpsertInstalledClient(ctx context.Context, c *store.InstalledClient) error {
	if c == nil || c.ID == "" {
		return errors.New("UpsertInstalledClient: id required")
	}
	c.UpdatedAt = time.Now().UTC()
	_, err := d.q.ExecContext(ctx, `
		INSERT INTO installed_clients (`+installedClientCols+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name             = excluded.name,
			config_path      = excluded.config_path,
			installed        = excluded.installed,
			hooks_installed  = excluded.hooks_installed,
			hooks_drifted    = excluded.hooks_drifted,
			shim_installed   = excluded.shim_installed,
			sandbox_enabled  = excluded.sandbox_enabled,
			installed_at     = excluded.installed_at,
			updated_at       = excluded.updated_at`,
		c.ID, c.Name, c.ConfigPath,
		boolToInt(c.Installed), boolToInt(c.HooksInstalled),
		boolToInt(c.HooksDrifted),
		boolToInt(c.ShimInstalled), boolToInt(c.SandboxEnabled),
		formatTimePtr(c.InstalledAt), formatTime(c.UpdatedAt),
	)
	if err != nil {
		return fmt.Errorf("upsert installed_client: %w", err)
	}
	return nil
}

// GetInstalledClient returns one client by ID or ErrNotFound.
func (d *DB) GetInstalledClient(
	ctx context.Context, id string,
) (*store.InstalledClient, error) {
	row := d.q.QueryRowContext(ctx,
		`SELECT `+installedClientCols+` FROM installed_clients WHERE id = ?`, id)
	c, err := scanInstalledClient(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get installed_client: %w", err)
	}
	return c, nil
}

// ListInstalledClients returns every installed-client row ordered by id.
func (d *DB) ListInstalledClients(ctx context.Context) ([]store.InstalledClient, error) {
	rows, err := d.q.QueryContext(ctx,
		`SELECT `+installedClientCols+` FROM installed_clients ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list installed_clients: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []store.InstalledClient
	for rows.Next() {
		c, err := scanInstalledClient(rows)
		if err != nil {
			return nil, fmt.Errorf("scan installed_client: %w", err)
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

// CreateInstallReceipt inserts one OS-mutation receipt row.
func (d *DB) CreateInstallReceipt(ctx context.Context, r *store.InstallReceipt) error {
	if r == nil || r.ID == "" || r.Action == "" {
		return errors.New("CreateInstallReceipt: id + action required")
	}
	if r.AppliedAt.IsZero() {
		r.AppliedAt = time.Now().UTC()
	}
	_, err := d.q.ExecContext(ctx, `
		INSERT INTO install_receipts (`+installReceiptCols+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.ClientID, r.Action, r.TargetPath, r.BackupPath,
		r.ReverseData, formatTime(r.AppliedAt),
		formatTimePtr(r.ReversedAt), r.ReverseError,
	)
	if err != nil {
		return fmt.Errorf("create install_receipt: %w", err)
	}
	return nil
}

// ListInstallReceipts returns receipts ordered by applied_at descending.
// Empty clientID returns rows for every client. includeReversed=false
// hides rows that have already been reversed.
func (d *DB) ListInstallReceipts(
	ctx context.Context, clientID string, includeReversed bool,
) ([]store.InstallReceipt, error) {
	query := `SELECT ` + installReceiptCols + ` FROM install_receipts WHERE 1=1`
	var args []any
	if clientID != "" {
		query += ` AND client_id = ?`
		args = append(args, clientID)
	}
	if !includeReversed {
		query += ` AND reversed_at IS NULL`
	}
	query += ` ORDER BY applied_at DESC`
	rows, err := d.q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list install_receipts: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []store.InstallReceipt
	for rows.Next() {
		r, err := scanInstallReceipt(rows)
		if err != nil {
			return nil, fmt.Errorf("scan install_receipt: %w", err)
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// MarkReceiptReversed flips reversed_at = now and records reverseError
// (often empty for the success case). Idempotent — a second call against
// the same id just re-stamps reversed_at.
func (d *DB) MarkReceiptReversed(
	ctx context.Context, id, reverseError string,
) error {
	now := formatTime(time.Now().UTC())
	res, err := d.q.ExecContext(ctx,
		`UPDATE install_receipts SET reversed_at = ?, reverse_error = ?
		 WHERE id = ?`, now, reverseError, id)
	if err != nil {
		return fmt.Errorf("mark receipt reversed: %w", err)
	}
	return checkRowsAffected(res)
}

func scanInstalledClient(r scanner) (*store.InstalledClient, error) {
	var (
		c                                        store.InstalledClient
		installed, hooks, drifted, shim, sandbox int
		installedAt                              sql.NullString
		updatedAt                                string
	)
	err := r.Scan(
		&c.ID, &c.Name, &c.ConfigPath,
		&installed, &hooks, &drifted, &shim, &sandbox,
		&installedAt, &updatedAt,
	)
	if err != nil {
		return nil, err
	}
	c.Installed = installed != 0
	c.HooksInstalled = hooks != 0
	c.HooksDrifted = drifted != 0
	c.ShimInstalled = shim != 0
	c.SandboxEnabled = sandbox != 0
	if installedAt.Valid {
		t := parseTime(installedAt.String)
		c.InstalledAt = &t
	}
	c.UpdatedAt = parseTime(updatedAt)
	return &c, nil
}

func scanInstallReceipt(r scanner) (*store.InstallReceipt, error) {
	var (
		rec        store.InstallReceipt
		appliedAt  string
		reversedAt sql.NullString
	)
	err := r.Scan(
		&rec.ID, &rec.ClientID, &rec.Action, &rec.TargetPath, &rec.BackupPath,
		&rec.ReverseData, &appliedAt, &reversedAt, &rec.ReverseError,
	)
	if err != nil {
		return nil, err
	}
	rec.AppliedAt = parseTime(appliedAt)
	if reversedAt.Valid {
		t := parseTime(reversedAt.String)
		rec.ReversedAt = &t
	}
	return &rec, nil
}
