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

func (d *DB) CreateOAuthProvider(ctx context.Context, p *store.OAuthProvider) error {
	if p.ID == "" {
		p.ID = uuid.NewString()
	}
	now := time.Now().UTC()
	p.CreatedAt = now
	p.UpdatedAt = now

	scopes := normalizeJSON(p.Scopes, "[]")
	if p.Source == "" {
		p.Source = "api"
	}

	var usePKCE int
	if p.UsePKCE {
		usePKCE = 1
	}

	_, err := d.q.ExecContext(ctx, `
		INSERT INTO oauth_providers
		(id, name, template_id, authorize_url, token_url, client_id, encrypted_client_secret,
		 scopes, use_pkce, redirect_uri, source, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.Name, p.TemplateID, p.AuthorizeURL, p.TokenURL, p.ClientID,
		p.EncryptedClientSecret, scopes, usePKCE, p.RedirectURI, p.Source,
		formatTime(p.CreatedAt), formatTime(p.UpdatedAt),
	)
	if err != nil {
		return mapConstraintError(err)
	}
	return nil
}

func (d *DB) GetOAuthProvider(ctx context.Context, id string) (*store.OAuthProvider, error) {
	return d.scanOAuthProvider(d.q.QueryRowContext(ctx, `
		SELECT id, name, template_id, authorize_url, token_url, client_id,
		       encrypted_client_secret, scopes, use_pkce, redirect_uri,
		       source, created_at, updated_at
		FROM oauth_providers WHERE id = ?`, id))
}

func (d *DB) GetOAuthProviderByName(ctx context.Context, name string) (*store.OAuthProvider, error) {
	return d.scanOAuthProvider(d.q.QueryRowContext(ctx, `
		SELECT id, name, template_id, authorize_url, token_url, client_id,
		       encrypted_client_secret, scopes, use_pkce, redirect_uri,
		       source, created_at, updated_at
		FROM oauth_providers WHERE name = ?`, name))
}

func (d *DB) scanOAuthProvider(row *sql.Row) (*store.OAuthProvider, error) {
	var p store.OAuthProvider
	var createdAt, updatedAt, scopes string
	var usePKCE int

	err := row.Scan(&p.ID, &p.Name, &p.TemplateID, &p.AuthorizeURL, &p.TokenURL,
		&p.ClientID, &p.EncryptedClientSecret, &scopes, &usePKCE, &p.RedirectURI,
		&p.Source, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	p.Scopes = json.RawMessage(scopes)
	p.UsePKCE = usePKCE != 0
	p.CreatedAt = parseTime(createdAt)
	p.UpdatedAt = parseTime(updatedAt)
	return &p, nil
}

func (d *DB) ListOAuthProviders(ctx context.Context) ([]store.OAuthProvider, error) {
	rows, err := d.q.QueryContext(ctx, `
		SELECT id, name, template_id, authorize_url, token_url, client_id,
		       encrypted_client_secret, scopes, use_pkce, redirect_uri,
		       source, created_at, updated_at
		FROM oauth_providers ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []store.OAuthProvider
	for rows.Next() {
		var p store.OAuthProvider
		var createdAt, updatedAt, scopes string
		var usePKCE int
		if err := rows.Scan(&p.ID, &p.Name, &p.TemplateID, &p.AuthorizeURL, &p.TokenURL,
			&p.ClientID, &p.EncryptedClientSecret, &scopes, &usePKCE, &p.RedirectURI,
			&p.Source, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		p.Scopes = json.RawMessage(scopes)
		p.UsePKCE = usePKCE != 0
		p.CreatedAt = parseTime(createdAt)
		p.UpdatedAt = parseTime(updatedAt)
		out = append(out, p)
	}
	return out, rows.Err()
}

func (d *DB) UpdateOAuthProvider(ctx context.Context, p *store.OAuthProvider) error {
	p.UpdatedAt = time.Now().UTC()
	scopes := normalizeJSON(p.Scopes, "[]")
	if p.Source == "" {
		p.Source = "api"
	}

	var usePKCE int
	if p.UsePKCE {
		usePKCE = 1
	}

	res, err := d.q.ExecContext(ctx, `
		UPDATE oauth_providers
		SET name = ?, template_id = ?, authorize_url = ?, token_url = ?,
		    client_id = ?, encrypted_client_secret = ?, scopes = ?,
		    use_pkce = ?, redirect_uri = ?, source = ?, updated_at = ?
		WHERE id = ?`,
		p.Name, p.TemplateID, p.AuthorizeURL, p.TokenURL, p.ClientID,
		p.EncryptedClientSecret, scopes, usePKCE, p.RedirectURI,
		p.Source, formatTime(p.UpdatedAt), p.ID,
	)
	if err != nil {
		return mapConstraintError(err)
	}
	return checkRowsAffected(res)
}

func (d *DB) DeleteOAuthProvider(ctx context.Context, id string) error {
	return d.withTx(ctx, func(q queryable) error {
		if _, err := q.ExecContext(ctx,
			`UPDATE auth_scopes SET oauth_provider_id = '' WHERE oauth_provider_id = ?`,
			id); err != nil {
			return fmt.Errorf("cascade nullify auth_scopes: %w", err)
		}
		res, err := q.ExecContext(ctx, `DELETE FROM oauth_providers WHERE id = ?`, id)
		if err != nil {
			return err
		}
		return checkRowsAffected(res)
	})
}
