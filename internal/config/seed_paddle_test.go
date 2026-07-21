package config

import (
	"context"
	"testing"
)

func TestPaddleEnvFieldsRegistered(t *testing.T) {
	for _, scopeID := range []string{paddleSandboxAuthScopeID, paddleProductionAuthScopeID} {
		fields := GetEnvFields(scopeID)
		if len(fields) != 1 {
			t.Fatalf("scope %s: expected 1 env field, got %d", scopeID, len(fields))
		}
		if fields[0].Key != "PADDLE_API_KEY" {
			t.Fatalf("scope %s: env field key = %q, want PADDLE_API_KEY", scopeID, fields[0].Key)
		}
		if !fields[0].Secret {
			t.Fatalf("scope %s: expected PADDLE_API_KEY to be marked as secret", scopeID)
		}
	}
}

func TestSeedDefaultAuthScopes_EnsuresPaddleScopesWhenScopesExist(t *testing.T) {
	ctx := context.Background()
	db := newSeedTestDB(t, ctx)

	if err := SeedDefaultAuthScopes(ctx, db); err != nil {
		t.Fatalf("seed default auth scopes: %v", err)
	}

	for _, id := range []string{paddleSandboxAuthScopeID, paddleProductionAuthScopeID} {
		scope, err := db.GetAuthScope(ctx, id)
		if err != nil {
			t.Fatalf("expected %s auth scope to exist: %v", id, err)
		}
		if scope.Type != "env" {
			t.Fatalf("%s auth scope type = %q, want env", id, scope.Type)
		}
	}
}
