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

func (d *DB) CreateAuthScope(ctx context.Context, a *store.AuthScope) error {
	if a.ID == "" {
		a.ID = uuid.NewString()
	}
	now := time.Now().UTC()
	a.CreatedAt = now
	a.UpdatedAt = now

	hints := normalizeJSON(a.RedactionHints, "[]")
	if a.Source == "" {
		a.Source = "api"
	}

	_, err := d.q.ExecContext(ctx, `
		INSERT INTO auth_scopes
		(id, name, display_name, type, encrypted_data, redaction_hints,
		 oauth_provider_id, oauth_token_data, source, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ID, a.Name, a.DisplayName, a.Type, a.EncryptedData, hints,
		a.OAuthProviderID, a.OAuthTokenData, a.Source,
		formatTime(a.CreatedAt), formatTime(a.UpdatedAt),
	)
	if err != nil {
		return mapConstraintError(err)
	}
	return nil
}

func (d *DB) GetAuthScope(ctx context.Context, id string) (*store.AuthScope, error) {
	var a store.AuthScope
	var createdAt, updatedAt, hints string
	err := d.q.QueryRowContext(ctx, `
		SELECT id, name, display_name, type, encrypted_data, redaction_hints,
		       oauth_provider_id, oauth_token_data, source, created_at, updated_at
		FROM auth_scopes WHERE id = ?`, id,
	).Scan(&a.ID, &a.Name, &a.DisplayName, &a.Type, &a.EncryptedData, &hints,
		&a.OAuthProviderID, &a.OAuthTokenData, &a.Source, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	a.RedactionHints = json.RawMessage(hints)
	a.CreatedAt = parseTime(createdAt)
	a.UpdatedAt = parseTime(updatedAt)
	return &a, nil
}

func (d *DB) GetAuthScopeByName(ctx context.Context, name string) (*store.AuthScope, error) {
	var a store.AuthScope
	var createdAt, updatedAt, hints string
	err := d.q.QueryRowContext(ctx, `
		SELECT id, name, display_name, type, encrypted_data, redaction_hints,
		       oauth_provider_id, oauth_token_data, source, created_at, updated_at
		FROM auth_scopes WHERE name = ?`, name,
	).Scan(&a.ID, &a.Name, &a.DisplayName, &a.Type, &a.EncryptedData, &hints,
		&a.OAuthProviderID, &a.OAuthTokenData, &a.Source, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	a.RedactionHints = json.RawMessage(hints)
	a.CreatedAt = parseTime(createdAt)
	a.UpdatedAt = parseTime(updatedAt)
	return &a, nil
}

func (d *DB) ListAuthScopes(ctx context.Context) ([]store.AuthScope, error) {
	rows, err := d.q.QueryContext(ctx, `
		SELECT id, name, display_name, type, encrypted_data, redaction_hints,
		       oauth_provider_id, oauth_token_data, source, created_at, updated_at
		FROM auth_scopes ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []store.AuthScope
	for rows.Next() {
		var a store.AuthScope
		var createdAt, updatedAt, hints string
		if err := rows.Scan(&a.ID, &a.Name, &a.DisplayName, &a.Type, &a.EncryptedData,
			&hints, &a.OAuthProviderID, &a.OAuthTokenData,
			&a.Source, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		a.RedactionHints = json.RawMessage(hints)
		a.CreatedAt = parseTime(createdAt)
		a.UpdatedAt = parseTime(updatedAt)
		out = append(out, a)
	}
	return out, rows.Err()
}

func (d *DB) UpdateAuthScope(ctx context.Context, a *store.AuthScope) error {
	a.UpdatedAt = time.Now().UTC()
	hints := normalizeJSON(a.RedactionHints, "[]")
	if a.Source == "" {
		a.Source = "api"
	}

	res, err := d.q.ExecContext(ctx, `
		UPDATE auth_scopes
		SET name = ?, display_name = ?, type = ?, redaction_hints = ?,
		    oauth_provider_id = ?, source = ?, updated_at = ?
		WHERE id = ?`,
		a.Name, a.DisplayName, a.Type, hints,
		a.OAuthProviderID, a.Source,
		formatTime(a.UpdatedAt), a.ID,
	)
	if err != nil {
		return mapConstraintError(err)
	}
	return checkRowsAffected(res)
}

func (d *DB) UpdateAuthScopeTokenData(ctx context.Context, id string, data []byte) error {
	res, err := d.q.ExecContext(ctx, `
		UPDATE auth_scopes SET oauth_token_data = ?, updated_at = ? WHERE id = ?`,
		data, formatTime(time.Now().UTC()), id,
	)
	if err != nil {
		return err
	}
	return checkRowsAffected(res)
}

func (d *DB) UpdateAuthScopeEncryptedData(ctx context.Context, id string, data []byte) error {
	res, err := d.q.ExecContext(ctx, `
		UPDATE auth_scopes SET encrypted_data = ?, updated_at = ? WHERE id = ?`,
		data, formatTime(time.Now().UTC()), id,
	)
	if err != nil {
		return err
	}
	return checkRowsAffected(res)
}

func (d *DB) DeleteAuthScope(ctx context.Context, id string) error {
	return d.withTx(ctx, func(q queryable) error {
		if _, err := q.ExecContext(ctx,
			`UPDATE route_rules SET auth_scope_id = '' WHERE auth_scope_id = ?`, id); err != nil {
			return fmt.Errorf("cascade nullify route_rules: %w", err)
		}
		if _, err := q.ExecContext(ctx,
			`UPDATE tool_approvals SET status = 'cancelled', resolved_at = ?
			 WHERE auth_scope_id = ? AND status = 'pending'`,
			formatTime(time.Now().UTC()), id); err != nil {
			return fmt.Errorf("cascade cancel tool_approvals: %w", err)
		}
		res, err := q.ExecContext(ctx, `DELETE FROM auth_scopes WHERE id = ?`, id)
		if err != nil {
			return err
		}
		return checkRowsAffected(res)
	})
}
