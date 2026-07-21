package admin_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/models"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/workers/admin"
)

// fakeCatalogReader serves a fixed catalog snapshot to preflight.
type fakeCatalogReader struct{ cat models.Catalog }

func (f fakeCatalogReader) Catalog() models.Catalog { return f.cat }

func liveGrokCatalog(ids ...string) models.Catalog {
	now := time.Date(2026, 7, 21, 9, 0, 0, 0, time.UTC)
	return models.Catalog{
		RefreshedAt: now,
		Providers: []models.ProviderCatalog{{
			Provider:      "grok_cli",
			Models:        ids,
			Source:        models.ModelSourceLive,
			AuthState:     models.ModelAuthOK,
			LastRefreshed: now,
		}},
	}
}

// TestPreflightCatalogFastRejectsUnavailableID is the headline behaviour:
// grok's live catalog offers only grok-4.5, so a request for
// grok-composer-2.5 dies at preflight — before any run/budget — with a
// message that names what IS available and where the list came from.
func TestPreflightCatalogFastRejectsUnavailableID(t *testing.T) {
	svc, db, wsID, scopeID := newTestService(t)
	svc.SetModelCatalog(fakeCatalogReader{cat: liveGrokCatalog("grok-4.5")})
	ctx := context.Background()

	_, err := svc.Delegate(ctx, admin.DelegationInput{
		WorkspaceID:     wsID,
		Objective:       "must fast-reject the unavailable model id",
		ModelProvider:   "grok_cli",
		ModelID:         "grok-composer-2.5",
		SecretScopeID:   scopeID,
		WorkerIsolation: "none",
	})
	if err == nil {
		t.Fatal("expected preflight rejection, got nil")
	}
	for _, want := range []string{"not currently available", "grok-4.5", "live-probed"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q missing %q", err.Error(), want)
		}
	}
	workers, listErr := db.ListWorkers(ctx, wsID, false)
	if listErr != nil {
		t.Fatal(listErr)
	}
	if len(workers) != 0 {
		t.Fatalf("rejection must not create workers, got %d", len(workers))
	}
}

// TestPreflightCatalogAcceptsAvailableID — an id present in the live catalog
// passes the model-id gate.
func TestPreflightCatalogAcceptsAvailableID(t *testing.T) {
	svc, _, wsID, scopeID := newTestService(t)
	svc.SetModelCatalog(fakeCatalogReader{cat: liveGrokCatalog("grok-4.5")})
	ctx := context.Background()

	_, err := svc.Delegate(ctx, admin.DelegationInput{
		WorkspaceID:         wsID,
		Objective:           "available id must clear the catalog gate",
		ModelProvider:       "grok_cli",
		ModelID:             "grok-4.5",
		SecretScopeID:       scopeID,
		WorkerIsolation:     "none",
		MaxWallClockSeconds: 30,
	})
	if err != nil && strings.Contains(err.Error(), "not currently available") {
		t.Fatalf("available id must not trip the catalog gate: %v", err)
	}
}

// TestPreflightCatalogFallsBackToStaticWhenNoEntry — when the catalog has no
// row for a provider (cold/partial), preflight falls back to the declared
// KnownModels and its legacy "not a known model" wording still applies.
func TestPreflightCatalogFallsBackToStaticWhenNoEntry(t *testing.T) {
	svc, db, wsID, scopeID := newTestService(t)
	// Catalog knows grok, but NOT mimo.
	svc.SetModelCatalog(fakeCatalogReader{cat: liveGrokCatalog("grok-4.5")})
	ctx := context.Background()

	if err := db.CreateModelProfile(ctx, &store.ModelProfile{
		Name: "mimo", Provider: "mimo_cli", SecretScopeID: scopeID,
		KnownModels: []string{"xiaomi/mimo-v2.5"},
	}); err != nil {
		t.Fatal(err)
	}

	_, err := svc.Delegate(ctx, admin.DelegationInput{
		WorkspaceID:     wsID,
		Objective:       "no catalog row -> legacy static gate applies",
		ModelProvider:   "mimo_cli",
		ModelID:         "xiaomi/not-a-real-one",
		SecretScopeID:   scopeID,
		WorkerIsolation: "none",
	})
	if err == nil || !strings.Contains(err.Error(), "not a known model") {
		t.Fatalf("expected legacy static rejection, got: %v", err)
	}
	if !strings.Contains(err.Error(), "xiaomi/mimo-v2.5") {
		t.Fatalf("legacy error should name the declared id, got: %v", err)
	}
}
