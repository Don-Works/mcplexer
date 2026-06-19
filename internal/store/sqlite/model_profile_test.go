package sqlite_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
)

// newModelProfile is a minimal valid profile (anthropic by default).
func newModelProfile(name string, knownModels []string) *store.ModelProfile {
	return &store.ModelProfile{
		Name:        name,
		Provider:    "anthropic",
		EndpointURL: "https://api.anthropic.com",
		KnownModels: knownModels,
	}
}

func TestModelProfileCRUD(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	// Seed an AuthScope so SecretScopeID FK resolves.
	scope := &store.AuthScope{Name: "anthropic-profile-key", Type: "env"}
	if err := db.CreateAuthScope(ctx, scope); err != nil {
		t.Fatalf("create auth scope: %v", err)
	}

	t.Run("create with empty known_models", func(t *testing.T) {
		p := newModelProfile("p-empty", nil)
		p.SecretScopeID = scope.ID
		if err := db.CreateModelProfile(ctx, p); err != nil {
			t.Fatalf("create: %v", err)
		}
		if p.ID == "" {
			t.Fatal("expected ID to be set")
		}
		got, err := db.GetModelProfile(ctx, p.ID)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if got.KnownModels != nil {
			t.Fatalf("expected nil KnownModels, got %v", got.KnownModels)
		}
		if got.SecretScopeID != scope.ID {
			t.Fatalf("secret_scope_id = %q, want %q", got.SecretScopeID, scope.ID)
		}
	})

	t.Run("create with populated known_models", func(t *testing.T) {
		want := []string{"claude-opus-4-7", "claude-sonnet-4-7", "claude-haiku-4-5"}
		p := newModelProfile("p-populated", want)
		p.SecretScopeID = scope.ID
		if err := db.CreateModelProfile(ctx, p); err != nil {
			t.Fatalf("create: %v", err)
		}
		got, err := db.GetModelProfile(ctx, p.ID)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if !reflect.DeepEqual(got.KnownModels, want) {
			t.Fatalf("KnownModels = %v, want %v", got.KnownModels, want)
		}
	})

	t.Run("create with explicit empty slice", func(t *testing.T) {
		p := newModelProfile("p-explicit-empty", []string{})
		p.SecretScopeID = scope.ID
		if err := db.CreateModelProfile(ctx, p); err != nil {
			t.Fatalf("create: %v", err)
		}
		got, err := db.GetModelProfile(ctx, p.ID)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		// Empty slice round-trips to nil (canonical form).
		if got.KnownModels != nil {
			t.Fatalf("expected nil KnownModels, got %v", got.KnownModels)
		}
	})

	t.Run("get_by_name returns ErrNotFound when missing", func(t *testing.T) {
		_, err := db.GetModelProfileByName(ctx, "does-not-exist")
		if err != store.ErrNotFound {
			t.Fatalf("expected ErrNotFound, got %v", err)
		}
	})

	t.Run("get returns ErrNotFound for unknown id", func(t *testing.T) {
		_, err := db.GetModelProfile(ctx, "nope")
		if err != store.ErrNotFound {
			t.Fatalf("expected ErrNotFound, got %v", err)
		}
	})

	t.Run("list returns ordered rows", func(t *testing.T) {
		list, err := db.ListModelProfiles(ctx)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if len(list) < 3 {
			t.Fatalf("expected >=3 profiles, got %d", len(list))
		}
		// Names should be sorted ascending.
		for i := 1; i < len(list); i++ {
			if list[i-1].Name > list[i].Name {
				t.Fatalf("not ordered by name: %s > %s",
					list[i-1].Name, list[i].Name)
			}
		}
	})

	t.Run("update bumps UpdatedAt and overwrites fields", func(t *testing.T) {
		p := newModelProfile("p-update", []string{"v1"})
		p.SecretScopeID = scope.ID
		if err := db.CreateModelProfile(ctx, p); err != nil {
			t.Fatalf("create: %v", err)
		}
		original := p.UpdatedAt

		p.Name = "p-update-renamed"
		p.KnownModels = []string{"v2", "v3"}
		p.EndpointURL = "https://api.anthropic.com/v2"
		if err := db.UpdateModelProfile(ctx, p); err != nil {
			t.Fatalf("update: %v", err)
		}
		if !p.UpdatedAt.After(original) {
			t.Fatalf("UpdatedAt not advanced: %v vs %v", p.UpdatedAt, original)
		}
		got, err := db.GetModelProfile(ctx, p.ID)
		if err != nil {
			t.Fatalf("get after update: %v", err)
		}
		if got.Name != "p-update-renamed" {
			t.Fatalf("name = %q, want renamed", got.Name)
		}
		if !reflect.DeepEqual(got.KnownModels, []string{"v2", "v3"}) {
			t.Fatalf("KnownModels = %v", got.KnownModels)
		}
		if got.EndpointURL != "https://api.anthropic.com/v2" {
			t.Fatalf("endpoint_url = %q", got.EndpointURL)
		}
	})

	t.Run("update returns ErrNotFound for missing id", func(t *testing.T) {
		err := db.UpdateModelProfile(ctx, &store.ModelProfile{
			ID:       "nope-update",
			Name:     "nope",
			Provider: "anthropic",
		})
		if err != store.ErrNotFound {
			t.Fatalf("expected ErrNotFound, got %v", err)
		}
	})

	t.Run("unique name rejects duplicate", func(t *testing.T) {
		p1 := newModelProfile("dup-name", nil)
		p1.SecretScopeID = scope.ID
		if err := db.CreateModelProfile(ctx, p1); err != nil {
			t.Fatalf("create first: %v", err)
		}
		p2 := newModelProfile("dup-name", nil)
		p2.SecretScopeID = scope.ID
		err := db.CreateModelProfile(ctx, p2)
		if err != store.ErrAlreadyExists {
			t.Fatalf("expected ErrAlreadyExists, got %v", err)
		}
	})

	t.Run("delete then get returns ErrNotFound", func(t *testing.T) {
		p := newModelProfile("p-delete", nil)
		p.SecretScopeID = scope.ID
		if err := db.CreateModelProfile(ctx, p); err != nil {
			t.Fatalf("create: %v", err)
		}
		if err := db.DeleteModelProfile(ctx, p.ID); err != nil {
			t.Fatalf("delete: %v", err)
		}
		_, err := db.GetModelProfile(ctx, p.ID)
		if err != store.ErrNotFound {
			t.Fatalf("expected ErrNotFound, got %v", err)
		}
	})

	t.Run("delete returns ErrNotFound for missing id", func(t *testing.T) {
		err := db.DeleteModelProfile(ctx, "nope-delete")
		if err != store.ErrNotFound {
			t.Fatalf("expected ErrNotFound, got %v", err)
		}
	})

	t.Run("get_by_name finds existing", func(t *testing.T) {
		p := newModelProfile("p-byname", nil)
		p.SecretScopeID = scope.ID
		if err := db.CreateModelProfile(ctx, p); err != nil {
			t.Fatalf("create: %v", err)
		}
		got, err := db.GetModelProfileByName(ctx, "p-byname")
		if err != nil {
			t.Fatalf("get by name: %v", err)
		}
		if got.ID != p.ID {
			t.Fatalf("id mismatch: %q vs %q", got.ID, p.ID)
		}
	})

	t.Run("builtin flag round-trips", func(t *testing.T) {
		p := newModelProfile("p-builtin", nil)
		p.SecretScopeID = scope.ID
		p.Builtin = true
		if err := db.CreateModelProfile(ctx, p); err != nil {
			t.Fatalf("create: %v", err)
		}
		got, err := db.GetModelProfile(ctx, p.ID)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if !got.Builtin {
			t.Fatal("Builtin not round-tripped")
		}
	})

	t.Run("claude_cli profile with empty secret scope", func(t *testing.T) {
		p := &store.ModelProfile{
			Name:        "p-claude-cli",
			Provider:    "claude_cli",
			EndpointURL: "/usr/local/bin/claude",
			// SecretScopeID intentionally empty — claude_cli uses OAuth login.
		}
		if err := db.CreateModelProfile(ctx, p); err != nil {
			t.Fatalf("create: %v", err)
		}
		got, err := db.GetModelProfile(ctx, p.ID)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if got.SecretScopeID != "" {
			t.Fatalf("expected empty SecretScopeID, got %q", got.SecretScopeID)
		}
		if got.Provider != "claude_cli" {
			t.Fatalf("provider = %q", got.Provider)
		}
	})
}
