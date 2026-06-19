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

const modelProfileCols = `id, name, provider, endpoint_url, secret_scope_id,
    known_models_json, builtin, created_at, updated_at`

// CreateModelProfile inserts one row. ID is generated when empty;
// CreatedAt / UpdatedAt are stamped when zero. KnownModels=nil or empty
// serialises to "[]" so the NOT NULL default holds. Validation (provider
// enum, name required, etc.) lives at the admin layer — the store layer
// trusts whatever the caller writes.
func (d *DB) CreateModelProfile(ctx context.Context, p *store.ModelProfile) error {
	if p == nil {
		return errors.New("CreateModelProfile: profile required")
	}
	if p.ID == "" {
		p.ID = uuid.NewString()
	}
	now := time.Now().UTC()
	if p.CreatedAt.IsZero() {
		p.CreatedAt = now
	}
	if p.UpdatedAt.IsZero() {
		p.UpdatedAt = now
	}
	knownJSON, err := marshalKnownModels(p.KnownModels)
	if err != nil {
		return fmt.Errorf("marshal known_models: %w", err)
	}
	_, err = d.q.ExecContext(ctx, `
		INSERT INTO worker_model_profiles (`+modelProfileCols+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.Name, p.Provider, p.EndpointURL,
		nullString(p.SecretScopeID), knownJSON, boolToInt(p.Builtin),
		formatTime(p.CreatedAt), formatTime(p.UpdatedAt),
	)
	return mapConstraintError(err)
}

// GetModelProfile returns the row for id or store.ErrNotFound.
func (d *DB) GetModelProfile(
	ctx context.Context, id string,
) (store.ModelProfile, error) {
	row := d.q.QueryRowContext(ctx,
		`SELECT `+modelProfileCols+` FROM worker_model_profiles WHERE id = ?`,
		id)
	p, err := scanModelProfile(row)
	if errors.Is(err, sql.ErrNoRows) {
		return store.ModelProfile{}, store.ErrNotFound
	}
	if err != nil {
		return store.ModelProfile{}, fmt.Errorf("get model profile: %w", err)
	}
	return p, nil
}

// GetModelProfileByName returns the row for name or store.ErrNotFound.
// The unique index on (name) makes this an O(1) lookup.
func (d *DB) GetModelProfileByName(
	ctx context.Context, name string,
) (store.ModelProfile, error) {
	row := d.q.QueryRowContext(ctx,
		`SELECT `+modelProfileCols+` FROM worker_model_profiles WHERE name = ?`,
		name)
	p, err := scanModelProfile(row)
	if errors.Is(err, sql.ErrNoRows) {
		return store.ModelProfile{}, store.ErrNotFound
	}
	if err != nil {
		return store.ModelProfile{}, fmt.Errorf("get model profile by name: %w", err)
	}
	return p, nil
}

// ListModelProfiles returns every profile ordered by name ASC.
func (d *DB) ListModelProfiles(ctx context.Context) ([]store.ModelProfile, error) {
	rows, err := d.q.QueryContext(ctx,
		`SELECT `+modelProfileCols+` FROM worker_model_profiles ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list model profiles: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]store.ModelProfile, 0)
	for rows.Next() {
		p, err := scanModelProfile(rows)
		if err != nil {
			return nil, fmt.Errorf("scan model profile: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// UpdateModelProfile writes every mutable column and bumps updated_at.
// Returns store.ErrNotFound when the row is missing.
func (d *DB) UpdateModelProfile(ctx context.Context, p *store.ModelProfile) error {
	if p == nil || p.ID == "" {
		return errors.New("UpdateModelProfile: id required")
	}
	p.UpdatedAt = time.Now().UTC()
	knownJSON, err := marshalKnownModels(p.KnownModels)
	if err != nil {
		return fmt.Errorf("marshal known_models: %w", err)
	}
	res, err := d.q.ExecContext(ctx, `
		UPDATE worker_model_profiles
		SET name = ?, provider = ?, endpoint_url = ?, secret_scope_id = ?,
		    known_models_json = ?, builtin = ?, updated_at = ?
		WHERE id = ?`,
		p.Name, p.Provider, p.EndpointURL, nullString(p.SecretScopeID),
		knownJSON, boolToInt(p.Builtin),
		formatTime(p.UpdatedAt), p.ID,
	)
	if err != nil {
		return mapConstraintError(err)
	}
	return checkRowsAffected(res)
}

// DeleteModelProfile hard-deletes the row. Returns store.ErrNotFound
// when absent. Workers' model_profile_id is set to NULL by the FK's
// ON DELETE SET NULL clause declared in migration 056.
func (d *DB) DeleteModelProfile(ctx context.Context, id string) error {
	res, err := d.q.ExecContext(ctx,
		`DELETE FROM worker_model_profiles WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete model profile: %w", err)
	}
	return checkRowsAffected(res)
}

// marshalKnownModels renders the canonical JSON for the known_models
// column. Nil / empty slice serialises to "[]" so the NOT NULL default
// holds across round-trips.
func marshalKnownModels(models []string) (string, error) {
	if len(models) == 0 {
		return "[]", nil
	}
	b, err := json.Marshal(models)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// unmarshalKnownModels parses the known_models_json column. Empty /
// "null" / parse failure all collapse to nil so a corrupted row never
// silently surfaces a stray model id.
func unmarshalKnownModels(s string) []string {
	if s == "" || s == "null" || s == "[]" {
		return nil
	}
	var models []string
	if err := json.Unmarshal([]byte(s), &models); err != nil {
		return nil
	}
	if len(models) == 0 {
		return nil
	}
	return models
}

func scanModelProfile(r scanner) (store.ModelProfile, error) {
	var (
		p                    store.ModelProfile
		secretScopeID        sql.NullString
		knownJSON            string
		builtin              int
		createdAt, updatedAt string
	)
	err := r.Scan(
		&p.ID, &p.Name, &p.Provider, &p.EndpointURL, &secretScopeID,
		&knownJSON, &builtin, &createdAt, &updatedAt,
	)
	if err != nil {
		return store.ModelProfile{}, err
	}
	if secretScopeID.Valid {
		p.SecretScopeID = secretScopeID.String
	}
	p.KnownModels = unmarshalKnownModels(knownJSON)
	p.Builtin = builtin != 0
	p.CreatedAt = parseTime(createdAt)
	p.UpdatedAt = parseTime(updatedAt)
	return p, nil
}
