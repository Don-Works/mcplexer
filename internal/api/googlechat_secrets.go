package api

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/don-works/mcplexer/internal/secrets"
	"github.com/don-works/mcplexer/internal/store"
)

// ensureGoogleChatScope returns (creating if missing) the AuthScope used to
// store the Google Chat service account JSON. Mirrors ensureTelegramScope.
func ensureGoogleChatScope(ctx context.Context, s store.Store) (*store.AuthScope, error) {
	existing, err := s.GetAuthScopeByName(ctx, "googlechat")
	if err == nil && existing != nil {
		return existing, nil
	}
	if !errors.Is(err, store.ErrNotFound) && err != nil {
		return nil, fmt.Errorf("look up scope: %w", err)
	}
	now := time.Now().UTC()
	scope := &store.AuthScope{
		ID:        ulid.Make().String(),
		Name:      "googlechat",
		Type:      "googlechat",
		Source:    "googlechat",
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.CreateAuthScope(ctx, scope); err != nil {
		return nil, fmt.Errorf("create scope: %w", err)
	}
	return scope, nil
}

// GoogleChatServiceAccountFromSecrets reads the current stored service account
// JSON. Returns nil if none configured. Used by serve.go to decide whether to
// boot the client.
func GoogleChatServiceAccountFromSecrets(ctx context.Context, s store.Store, sm *secrets.Manager) ([]byte, error) {
	if sm == nil {
		return nil, nil
	}
	scope, err := s.GetAuthScopeByName(ctx, "googlechat")
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	val, err := sm.Get(ctx, scope.ID, "service_account_json")
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return val, nil
}
