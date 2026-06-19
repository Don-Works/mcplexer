package config

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// defaultAuthScopes defines built-in auth scopes seeded on first run.
var defaultAuthScopes = []store.AuthScope{
	{
		ID:     aikidoAuthScopeID,
		Name:   "Aikido Client Credentials",
		Type:   "client_credentials",
		Source: "default",
	},
	{
		ID:     notionAuthScopeID,
		Name:   "Notion Integration Token",
		Type:   "env",
		Source: "default",
	},
	{
		ID:     obsidianAuthScopeID,
		Name:   "Obsidian Local REST API",
		Type:   "header",
		Source: "default",
	},
	{
		ID:              freeagentAuthScopeID,
		Name:            "FreeAgent OAuth",
		Type:            "oauth2",
		OAuthProviderID: "freeagent",
		Source:          "default",
	},
	{
		ID:     paddleSandboxAuthScopeID,
		Name:   "Paddle Sandbox API Key",
		Type:   "env",
		Source: "default",
	},
	{
		ID:     paddleProductionAuthScopeID,
		Name:   "Paddle Production API Key",
		Type:   "env",
		Source: "default",
	},
	{
		ID:     hammerspoonAuthScopeID,
		Name:   "Hammerspoon Bridge",
		Type:   "env",
		Source: "default",
	},
}

// SeedDefaultAuthScopes creates auth scope records if none exist.
// For existing databases, ensures required default auth scopes exist.
func SeedDefaultAuthScopes(ctx context.Context, s store.Store) error {
	existing, err := s.ListAuthScopes(ctx)
	if err != nil {
		return err
	}

	if len(existing) > 0 {
		return ensureRequiredDefaultAuthScopes(ctx, s, existing)
	}

	slog.Info("seeding default auth scopes", "count", len(defaultAuthScopes))

	now := time.Now().UTC()
	for _, a := range defaultAuthScopes {
		a.CreatedAt = now
		a.UpdatedAt = now
		if err := s.CreateAuthScope(ctx, &a); err != nil {
			return err
		}
		slog.Info("seeded auth scope", "id", a.ID, "name", a.Name, "type", a.Type)
	}
	return nil
}

func ensureRequiredDefaultAuthScopes(ctx context.Context, s store.Store, existing []store.AuthScope) error {
	requiredIDs := []string{
		aikidoAuthScopeID,
		notionAuthScopeID,
		obsidianAuthScopeID,
		freeagentAuthScopeID,
		paddleSandboxAuthScopeID,
		paddleProductionAuthScopeID,
		hammerspoonAuthScopeID,
	}

	existingByID := make(map[string]struct{}, len(existing))
	for _, scope := range existing {
		existingByID[scope.ID] = struct{}{}
	}

	now := time.Now().UTC()
	for _, id := range requiredIDs {
		if _, ok := existingByID[id]; ok {
			// Migrate existing aikido scope from env → client_credentials.
			if id == aikidoAuthScopeID {
				if err := migrateAikidoScopeType(ctx, s, existing); err != nil {
					return err
				}
			}
			continue
		}

		seed, ok := defaultAuthScopeByID(id)
		if !ok {
			continue
		}
		seed.CreatedAt = now
		seed.UpdatedAt = now
		if err := s.CreateAuthScope(ctx, &seed); err != nil {
			if errors.Is(err, store.ErrAlreadyExists) {
				slog.Warn("skipping default auth scope backfill; conflicting row already exists",
					"id", seed.ID, "name", seed.Name)
				continue
			}
			return err
		}
		slog.Info("migrated: seeded default auth scope", "id", seed.ID, "name", seed.Name)
	}
	return nil
}

// migrateAikidoScopeType upgrades the aikido auth scope from env to
// client_credentials so the injector performs OAuth token exchange.
func migrateAikidoScopeType(ctx context.Context, s store.Store, existing []store.AuthScope) error {
	for _, scope := range existing {
		if scope.ID == aikidoAuthScopeID && scope.Type == "env" {
			scope.Type = "client_credentials"
			if err := s.UpdateAuthScope(ctx, &scope); err != nil {
				return err
			}
			slog.Info("migrated: aikido auth scope type env → client_credentials")
			return nil
		}
	}
	return nil
}

func defaultAuthScopeByID(id string) (store.AuthScope, bool) {
	for _, scope := range defaultAuthScopes {
		if scope.ID == id {
			return scope, true
		}
	}
	return store.AuthScope{}, false
}
