// model_profile_service_test.go — unit tests for ModelProfileCore, the
// shared validation + mutation core both the REST handlers and the MCP
// tools dispatch through. These pin the partial-patch merge semantics and
// the Builtin guard at the core level so neither transport can drift.
package admin

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

func newCoreTestStore(t *testing.T) (*ModelProfileCore, *sqlite.DB, string) {
	t.Helper()
	ctx := context.Background()
	db, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "mp_core.db"))
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	scope := &store.AuthScope{Name: "key", Type: "env"}
	if err := db.CreateAuthScope(ctx, scope); err != nil {
		t.Fatalf("create scope: %v", err)
	}
	return NewModelProfileCore(db), db, scope.ID
}

func ptr[T any](v T) *T { return &v }

func TestModelProfileCorePartialUpdateLeavesOmittedFields(t *testing.T) {
	core, _, scopeID := newCoreTestStore(t)
	ctx := context.Background()

	created, err := core.Create(ctx, &store.ModelProfile{
		Name:          "p",
		Provider:      "anthropic",
		EndpointURL:   "https://api.anthropic.com",
		SecretScopeID: scopeID,
		KnownModels:   []string{"a", "b"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Patch only the name. Everything else must survive.
	got, err := core.Update(ctx, created.ID, ModelProfilePatch{Name: ptr("renamed")})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if got.Name != "renamed" {
		t.Fatalf("name = %q", got.Name)
	}
	if got.Provider != "anthropic" || got.EndpointURL != "https://api.anthropic.com" {
		t.Fatalf("provider/endpoint clobbered: %+v", got)
	}
	if got.SecretScopeID != scopeID {
		t.Fatalf("secret clobbered: %q", got.SecretScopeID)
	}
	if len(got.KnownModels) != 2 {
		t.Fatalf("known_models clobbered: %v", got.KnownModels)
	}
}

func TestModelProfileCoreSetKnownModelsReplacesWholeList(t *testing.T) {
	core, _, scopeID := newCoreTestStore(t)
	ctx := context.Background()
	created, err := core.Create(ctx, &store.ModelProfile{
		Name: "p", Provider: "openai", SecretScopeID: scopeID,
		KnownModels: []string{"old"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := core.SetKnownModels(ctx, created.ID, []string{"x", "y"})
	if err != nil {
		t.Fatalf("set: %v", err)
	}
	if len(got.KnownModels) != 2 || got.KnownModels[0] != "x" {
		t.Fatalf("known_models = %v", got.KnownModels)
	}
	// nil normalises to empty (clears) without panicking.
	got, err = core.SetKnownModels(ctx, created.ID, nil)
	if err != nil {
		t.Fatalf("set nil: %v", err)
	}
	if len(got.KnownModels) != 0 {
		t.Fatalf("set nil: known_models = %v", got.KnownModels)
	}
}

func TestModelProfileCoreBuiltinGuard(t *testing.T) {
	core, db, scopeID := newCoreTestStore(t)
	ctx := context.Background()
	builtin := &store.ModelProfile{
		Name: "b", Provider: "anthropic", SecretScopeID: scopeID, Builtin: true,
	}
	if err := db.CreateModelProfile(ctx, builtin); err != nil {
		t.Fatalf("seed builtin: %v", err)
	}
	if _, err := core.Update(ctx, builtin.ID, ModelProfilePatch{Name: ptr("x")}); !errors.Is(err, ErrModelProfileBuiltin) {
		t.Fatalf("update builtin err = %v, want ErrModelProfileBuiltin", err)
	}
	if _, err := core.SetKnownModels(ctx, builtin.ID, []string{"x"}); !errors.Is(err, ErrModelProfileBuiltin) {
		t.Fatalf("set builtin err = %v, want ErrModelProfileBuiltin", err)
	}
	if err := core.Delete(ctx, builtin.ID); !errors.Is(err, ErrModelProfileBuiltin) {
		t.Fatalf("delete builtin err = %v, want ErrModelProfileBuiltin", err)
	}
}

func TestModelProfileCoreCreateForcesNonBuiltin(t *testing.T) {
	core, _, scopeID := newCoreTestStore(t)
	created, err := core.Create(context.Background(), &store.ModelProfile{
		Name: "p", Provider: "anthropic", SecretScopeID: scopeID, Builtin: true,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if created.Builtin {
		t.Fatal("Create must force Builtin=false")
	}
}

func TestModelProfileCoreValidationErrorIsTyped(t *testing.T) {
	core, _, _ := newCoreTestStore(t)
	_, err := core.Create(context.Background(), &store.ModelProfile{
		Name: "p", Provider: "openai", // missing secret scope
	})
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !isModelProfileValidationErr(err) {
		t.Fatalf("err = %v, want ModelProfileValidationError", err)
	}
}
