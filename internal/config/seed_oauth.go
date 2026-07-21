package config

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/don-works/mcplexer/internal/oauth"
	"github.com/don-works/mcplexer/internal/store"
)

// SeedDefaultOAuthProviders creates or updates OAuth provider records from
// built-in templates. On first startup it creates all providers; on subsequent
// runs it updates template-sourced fields (URLs, scopes, PKCE) on existing
// seeded providers while preserving user-configured fields (client ID/secret).
func SeedDefaultOAuthProviders(ctx context.Context, s store.Store) error {
	existing, err := s.ListOAuthProviders(ctx)
	if err != nil {
		return err
	}

	// Index existing providers by template_id for fast lookup.
	byTemplate := make(map[string]*store.OAuthProvider, len(existing))
	for i := range existing {
		if existing[i].TemplateID != "" {
			byTemplate[existing[i].TemplateID] = &existing[i]
		}
	}

	templates := oauth.ListTemplates()
	for _, t := range templates {
		scopes, _ := json.Marshal(t.Scopes)
		now := time.Now().UTC()

		if ep, ok := byTemplate[t.ID]; ok {
			scopesChanged := string(ep.Scopes) != string(scopes)

			// Update template-sourced fields; preserve client credentials.
			ep.AuthorizeURL = t.AuthorizeURL
			ep.TokenURL = t.TokenURL
			ep.Scopes = scopes
			ep.UsePKCE = t.UsePKCE
			ep.UpdatedAt = now
			if err := s.UpdateOAuthProvider(ctx, ep); err != nil {
				return err
			}

			// If scopes changed, invalidate tokens on linked auth scopes
			// so users re-auth with the correct permissions.
			if scopesChanged {
				invalidateProviderTokens(ctx, s, ep.ID)
				slog.Info("updated seeded OAuth provider (scopes changed, tokens invalidated)",
					"id", ep.ID, "name", ep.Name)
			} else {
				slog.Info("updated seeded OAuth provider",
					"id", ep.ID, "name", ep.Name)
			}
			continue
		}

		p := &store.OAuthProvider{
			ID:           t.ID,
			Name:         t.Name,
			TemplateID:   t.ID,
			AuthorizeURL: t.AuthorizeURL,
			TokenURL:     t.TokenURL,
			Scopes:       scopes,
			UsePKCE:      t.UsePKCE,
			Source:       "default",
			CreatedAt:    now,
			UpdatedAt:    now,
		}
		if err := s.CreateOAuthProvider(ctx, p); err != nil {
			if errors.Is(err, store.ErrAlreadyExists) {
				if existingByName, lookupErr := s.GetOAuthProviderByName(ctx, t.Name); lookupErr == nil {
					existingByName.TemplateID = t.ID
					existingByName.AuthorizeURL = t.AuthorizeURL
					existingByName.TokenURL = t.TokenURL
					existingByName.Scopes = scopes
					existingByName.UsePKCE = t.UsePKCE
					existingByName.Source = "default"
					existingByName.UpdatedAt = now
					if updateErr := s.UpdateOAuthProvider(ctx, existingByName); updateErr != nil {
						return updateErr
					}
					slog.Info("migrated seeded OAuth provider by name",
						"id", existingByName.ID, "template_id", t.ID, "name", t.Name)
					continue
				}
				slog.Warn("skipping seeded OAuth provider; conflicting row already exists",
					"id", t.ID, "name", t.Name)
				continue
			}
			return err
		}
		slog.Info("seeded OAuth provider", "id", t.ID, "name", t.Name)
	}
	return nil
}

// invalidateProviderTokens clears OAuth token data on all auth scopes
// linked to the given provider, forcing users to re-authenticate.
func invalidateProviderTokens(ctx context.Context, s store.Store, providerID string) {
	scopes, err := s.ListAuthScopes(ctx)
	if err != nil {
		slog.Warn("failed to list auth scopes for token invalidation", "error", err)
		return
	}
	for _, scope := range scopes {
		if scope.OAuthProviderID != providerID {
			continue
		}
		if err := s.UpdateAuthScopeTokenData(ctx, scope.ID, nil); err != nil {
			slog.Warn("failed to invalidate token",
				"scope_id", scope.ID, "error", err)
			continue
		}
		slog.Info("invalidated token for scope (provider scopes changed)",
			"scope_id", scope.ID, "scope_name", scope.Name)
	}
}
