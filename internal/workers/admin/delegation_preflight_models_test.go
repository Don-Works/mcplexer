package admin_test

import (
	"context"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/workers/admin"
)

// TestPreflightRejectsUnknownCLIModelID is the regression test for the
// grok-composer-2.5-fast class of failure: a hallucinated model id on a
// CLI provider whose catalog IS locally known (a registered model
// profile declares KnownModels) must die at preflight — before any
// WorkerRun exists and any wall-clock budget is burned — with an error
// naming the known ids.
func TestPreflightRejectsUnknownCLIModelID(t *testing.T) {
	svc, db, wsID, scopeID := newTestService(t)
	ctx := context.Background()

	if err := db.CreateModelProfile(ctx, &store.ModelProfile{
		Name: "grok", Provider: "grok_cli", SecretScopeID: scopeID,
		KnownModels: []string{"grok-build", "grok-code"},
	}); err != nil {
		t.Fatal(err)
	}

	_, err := svc.Delegate(ctx, admin.DelegationInput{
		WorkspaceID:     wsID,
		Objective:       "must reject the unknown model id before any run exists",
		ModelProvider:   "grok_cli",
		ModelID:         "grok-composer-2.5-fast",
		SecretScopeID:   scopeID,
		WorkerIsolation: "none",
	})
	if err == nil || !strings.Contains(err.Error(), "not a known model") {
		t.Fatalf("error = %v, want unknown-model preflight rejection", err)
	}
	if !strings.Contains(err.Error(), "grok-build") {
		t.Fatalf("error should name the known ids, got: %v", err)
	}
	workers, listErr := db.ListWorkers(ctx, wsID, false)
	if listErr != nil {
		t.Fatal(listErr)
	}
	if len(workers) != 0 {
		t.Fatalf("preflight rejection must not create workers, got %d", len(workers))
	}
}

// TestPreflightKnownModelIDPasses — a declared id sails through the
// known-model gate (later stages may still fail for unrelated runtime
// reasons; this test only pins the preflight decision).
func TestPreflightKnownModelIDPasses(t *testing.T) {
	svc, db, wsID, scopeID := newTestService(t)
	ctx := context.Background()

	if err := db.CreateModelProfile(ctx, &store.ModelProfile{
		Name: "grok", Provider: "grok_cli", SecretScopeID: scopeID,
		KnownModels: []string{"grok-build"},
	}); err != nil {
		t.Fatal(err)
	}

	_, err := svc.Delegate(ctx, admin.DelegationInput{
		WorkspaceID:         wsID,
		Objective:           "known id must pass the model-id gate",
		ModelProvider:       "grok_cli",
		ModelID:             "grok-build",
		SecretScopeID:       scopeID,
		WorkerIsolation:     "none",
		MaxWallClockSeconds: 30,
	})
	if err != nil && strings.Contains(err.Error(), "not a known model") {
		t.Fatalf("known id must not trip the model-id gate: %v", err)
	}
}

// TestPreflightSkipsProvidersWithoutCatalog — no profile declares
// KnownModels for the provider, so there is nothing to validate against
// and any id must pass the gate. Also pins that non-CLI providers are
// never checked even when a same-provider catalog exists.
func TestPreflightSkipsProvidersWithoutCatalog(t *testing.T) {
	svc, db, wsID, scopeID := newTestService(t)
	ctx := context.Background()

	_, err := svc.Delegate(ctx, admin.DelegationInput{
		WorkspaceID:     wsID,
		Objective:       "no catalog registered, id must pass the gate",
		ModelProvider:   "mimo_cli",
		ModelID:         "xiaomi/anything-goes",
		SecretScopeID:   scopeID,
		WorkerIsolation: "none",
	})
	if err != nil && strings.Contains(err.Error(), "not a known model") {
		t.Fatalf("catalog-less provider must not trip the model-id gate: %v", err)
	}

	if err := db.CreateModelProfile(ctx, &store.ModelProfile{
		Name: "anthropic", Provider: "anthropic", SecretScopeID: scopeID,
		KnownModels: []string{"claude-sonnet-4-5"},
	}); err != nil {
		t.Fatal(err)
	}
	_, err = svc.Delegate(ctx, admin.DelegationInput{
		WorkspaceID:   wsID,
		Objective:     "API providers are exempt from the local catalog gate",
		ModelProvider: "anthropic",
		ModelID:       "claude-brand-new-model",
		SecretScopeID: scopeID,
	})
	if err != nil && strings.Contains(err.Error(), "not a known model") {
		t.Fatalf("non-CLI provider must not trip the model-id gate: %v", err)
	}
}
