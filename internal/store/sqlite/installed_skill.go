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

// UpsertInstalledSkill inserts or replaces a row in installed_skills.
// Re-installing a skill at the same name overwrites the previous registry
// entry; on-disk files are managed by the caller (internal/skills.Install).
func (d *DB) UpsertInstalledSkill(ctx context.Context, s *store.InstalledSkill) error {
	if s == nil {
		return errors.New("UpsertInstalledSkill: nil skill")
	}
	if s.Name == "" || s.Version == "" {
		return errors.New("UpsertInstalledSkill: name and version required")
	}
	if len(s.ManifestJSON) == 0 {
		return errors.New("UpsertInstalledSkill: manifest_json required")
	}
	if s.InstalledAt.IsZero() {
		s.InstalledAt = time.Now().UTC()
	}
	_, err := d.q.ExecContext(ctx, `
		INSERT INTO installed_skills
			(name, version, manifest_json, signer_pubkey, source, installed_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(name) DO UPDATE SET
			version = excluded.version,
			manifest_json = excluded.manifest_json,
			signer_pubkey = excluded.signer_pubkey,
			source = excluded.source,
			installed_at = excluded.installed_at`,
		s.Name, s.Version, string(s.ManifestJSON),
		s.SignerPubkey, s.Source, s.InstalledAt.Unix(),
	)
	if err != nil {
		return fmt.Errorf("upsert installed_skills: %w", err)
	}
	return nil
}

// GetInstalledSkill returns the row keyed by name. Returns store.ErrNotFound
// when no row exists.
func (d *DB) GetInstalledSkill(ctx context.Context, name string) (*store.InstalledSkill, error) {
	row := d.q.QueryRowContext(ctx, `
		SELECT name, version, manifest_json, signer_pubkey, source, installed_at
		FROM installed_skills WHERE name = ?`, name)
	s, err := scanInstalledSkill(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get installed_skill: %w", err)
	}
	return s, nil
}

// ListInstalledSkills returns every row ordered by name ascending.
func (d *DB) ListInstalledSkills(ctx context.Context) ([]store.InstalledSkill, error) {
	rows, err := d.q.QueryContext(ctx, `
		SELECT name, version, manifest_json, signer_pubkey, source, installed_at
		FROM installed_skills ORDER BY name ASC`)
	if err != nil {
		return nil, fmt.Errorf("list installed_skills: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	var out []store.InstalledSkill
	for rows.Next() {
		s, err := scanInstalledSkill(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("scan installed_skill: %w", err)
		}
		out = append(out, *s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate installed_skills: %w", err)
	}
	return out, nil
}

// DeleteInstalledSkill drops the row keyed by name. Returns store.ErrNotFound
// when no row was removed.
func (d *DB) DeleteInstalledSkill(ctx context.Context, name string) error {
	res, err := d.q.ExecContext(ctx,
		`DELETE FROM installed_skills WHERE name = ?`, name)
	if err != nil {
		return fmt.Errorf("delete installed_skill: %w", err)
	}
	return checkRowsAffected(res)
}

// scanInstalledSkill is the shared columnset reader for QueryRow / Query rows.
func scanInstalledSkill(scan func(...any) error) (*store.InstalledSkill, error) {
	var (
		s         store.InstalledSkill
		manifest  string
		installed int64
	)
	if err := scan(&s.Name, &s.Version, &manifest,
		&s.SignerPubkey, &s.Source, &installed); err != nil {
		return nil, err
	}
	s.ManifestJSON = json.RawMessage(manifest)
	s.InstalledAt = time.Unix(installed, 0).UTC()
	return &s, nil
}
