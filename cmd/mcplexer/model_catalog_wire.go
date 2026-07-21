// model_catalog_wire.go — daemon wiring for the live model catalog refresher.
//
// This file exists for the same reason the monitoring boot wiring does: this
// codebase has repeatedly shipped correct code that NOTHING CALLED. A catalog
// that is never refreshed is indistinguishable from the static Go list it
// replaces, so the boot start is asserted by TestModelCatalogBootStartsRefresher
// (runtime) AND TestServeBootWiresModelCatalog (source), mirroring the
// monitoring-baseline guard.
//
// The refresher probes each enabled provider's OWN model listing (pi's
// models.json, `grok models`, `mimo models`) on an hourly cadence and folds
// in declared KnownModels as a labelled static fallback. The delegation hot
// path never triggers a probe — preflight and the API read only the cached
// snapshot — so a slow provider can never block a delegation.
package main

import (
	"context"
	"log/slog"
	"sync"

	"github.com/don-works/mcplexer/internal/mesh"
	"github.com/don-works/mcplexer/internal/models"
	"github.com/don-works/mcplexer/internal/store"
)

var (
	// modelCatalogOnce guards the refresh loop: exactly one per daemon, so
	// two loops can't race on the cached snapshot.
	modelCatalogOnce        sync.Once
	modelCatalogMu          sync.Mutex
	modelCatalogStartedFlag bool
)

// newModelCatalogRefresher builds the live catalog refresher for the enabled
// providers, backed by the model-profile store for the static fallback. The
// auth-alert evaluator is wired as the post-refresh hook so an ENABLED provider
// that is UNAUTHENTICATED raises a bounded, human-facing mesh notification on
// the same cadence. meshMgr may be nil (no transport yet); the hook is still
// wired but the notifier no-ops until a manager exists.
func newModelCatalogRefresher(db store.Store, meshMgr *mesh.Manager) *models.Refresher {
	var notifier models.AuthAlertNotifier
	if meshMgr != nil {
		notifier = meshAuthAlertNotifier{mesh: meshMgr}
	}
	alerter := models.NewAuthAlerter(models.AuthAlerterOptions{Notifier: notifier})
	return models.NewRefresher(models.RefresherOptions{
		Probers:   models.EnabledProbers(),
		Providers: models.EnabledCLIProviders(),
		Static: func(ctx context.Context) (map[string][]string, error) {
			return staticKnownModels(ctx, db)
		},
		// Evaluate enabled-but-unauthenticated providers right after each
		// refresh publishes its snapshot. Asserted wired by
		// TestModelCatalogAuthAlertWiredIntoRefresh + TestServeBootWiresModelCatalog.
		OnRefresh: alerter.Evaluate,
	})
}

// meshAuthAlertPoster is the slice of *mesh.Manager the auth-alert notifier
// needs. Narrowed to an interface so the boot-wiring test injects a fake.
type meshAuthAlertPoster interface {
	Send(ctx context.Context, meta mesh.SessionMeta, req mesh.SendRequest) (*store.MeshMessage, error)
}

// meshAuthAlertNotifier delivers a model-catalog auth alert onto the mesh as a
// human-facing, operator-wide alert. It carries no secret — only the provider
// name and observation time, both already in the rendered message.
type meshAuthAlertNotifier struct {
	mesh meshAuthAlertPoster
}

// Notify maps an AuthAlert onto a global-namespace mesh alert that buzzes the
// operator (NotifyUser) and reaches paired peers (audience "*"). Unauthenticated
// is high priority; recovery is normal. ActorKind "system" marks the daemon as
// the origin, matching the monitoring dispatcher's mesh sends.
func (n meshAuthAlertNotifier) Notify(ctx context.Context, a models.AuthAlert) error {
	if n.mesh == nil {
		return nil
	}
	priority := "high"
	tags := "model-catalog,auth-unauthenticated," + a.Provider
	if a.Recovered {
		priority = "normal"
		tags = "model-catalog,auth-recovered," + a.Provider
	}
	_, err := n.mesh.Send(ctx, mesh.SessionMeta{
		SessionID:  "model-catalog-auth",
		ClientType: "system",
	}, mesh.SendRequest{
		Kind:        "alert",
		Content:     a.Message,
		Priority:    priority,
		Audience:    "*",
		Tags:        tags,
		ActorKind:   "system",
		NotifyUser:  true,
		ToWorkspace: "*", // operator-wide: every session + paired peers
	})
	return err
}

// staticKnownModels reads declared KnownModels per provider from the model
// profile store — the "static" fallback the catalog labels when a provider
// has no live source.
func staticKnownModels(ctx context.Context, db store.Store) (map[string][]string, error) {
	profiles, err := db.ListModelProfiles(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string][]string, len(profiles))
	for _, p := range profiles {
		out[p.Provider] = append(out[p.Provider], p.KnownModels...)
	}
	return out, nil
}

// startModelCatalogRefresher warms the catalog and starts the hourly refresh
// loop exactly once per daemon. Non-blocking: the initial refresh runs inside
// the goroutine so boot never waits on a provider listing. Asserted by
// TestServeBootWiresModelCatalog.
func startModelCatalogRefresher(ctx context.Context, r *models.Refresher) {
	if r == nil {
		return
	}
	modelCatalogOnce.Do(func() {
		go r.Run(ctx) // Run refreshes immediately, then ticks on the cadence.
		modelCatalogMu.Lock()
		modelCatalogStartedFlag = true
		modelCatalogMu.Unlock()
		slog.Info("model catalog: live refresher started")
	})
}

// modelCatalogStarted reports whether the refresh loop was started. Used by
// the boot-wiring test.
func modelCatalogStarted() bool {
	modelCatalogMu.Lock()
	defer modelCatalogMu.Unlock()
	return modelCatalogStartedFlag
}

// resetModelCatalogSingleton resets the once-guard for tests.
func resetModelCatalogSingleton() {
	modelCatalogMu.Lock()
	defer modelCatalogMu.Unlock()
	modelCatalogOnce = sync.Once{}
	modelCatalogStartedFlag = false
}
