// consolidator_autoinstall.go — daemon-startup wiring that guarantees a
// memory-consolidator worker exists in every workspace whenever an
// api_key auth scope has been configured. The consolidator runs two
// passes per execution (global memory + workspace memory, scope-
// preserving) so the user never has to opt-in per workspace — matching
// the "global + local simultaneously the whole time" direction.
//
// Safe to call on every boot; idempotent:
//   - Skips workspaces that already have a worker named "memory-consolidator"
//   - Skips entirely when no api_key auth scope exists yet (consolidator
//     needs a model to call; the user will configure one via Settings →
//     Secrets, and the next daemon boot picks it up)
//
// Best-effort: a failed install logs + continues to the next workspace
// rather than aborting daemon startup.
package main

import (
	"context"
	"log"
	"strings"

	"github.com/don-works/mcplexer/internal/store"
	workersadmin "github.com/don-works/mcplexer/internal/workers/admin"
)

const (
	autoConsolidatorName     = "memory-consolidator"
	autoConsolidatorTemplate = "memory-consolidator"
	autoConsolidatorSchedule = "0 3 * * *"
)

// autoInstallConsolidator ensures the memory-consolidator worker exists
// in every workspace. Called once at daemon startup, after the worker
// admin service is wired. Idempotent; safe to no-op when prereqs are
// missing.
func autoInstallConsolidator(
	ctx context.Context,
	db store.Store,
	workers *workersadmin.Service,
) {
	if db == nil || workers == nil {
		return
	}
	scopeID, ok := pickConsolidatorScope(ctx, db)
	if !ok {
		// No api_key configured yet — silent skip. The user will configure
		// one via Settings → Secrets; next boot picks it up.
		return
	}
	wss, err := db.ListWorkspaces(ctx)
	if err != nil {
		log.Printf("consolidator autoinstall: list workspaces: %v", err)
		return
	}
	for _, ws := range wss {
		if installed, err := workers.Get(ctx, workersadmin.GetInput{
			Name: autoConsolidatorName, WorkspaceID: ws.ID,
		}); err == nil && installed != nil && installed.Worker != nil {
			// Already present — don't touch the existing schedule or
			// enabled state; operators may have tuned them.
			continue
		}
		enabled := true
		if _, err := workers.InstallFromTemplate(ctx, workersadmin.InstallFromTemplateInput{
			TemplateName:  autoConsolidatorTemplate,
			WorkerName:    autoConsolidatorName,
			WorkspaceID:   ws.ID,
			SecretScopeID: scopeID,
			ScheduleSpec:  autoConsolidatorSchedule,
			Enabled:       &enabled,
		}); err != nil {
			log.Printf("consolidator autoinstall: workspace %s: %v", ws.ID, err)
			continue
		}
		log.Printf("consolidator autoinstall: installed in workspace %s", ws.ID)
	}
}

// pickConsolidatorScope picks the scope_id the worker row needs to satisfy
// the NOT NULL SecretScopeID column. It PREFERS a most-recently-updated
// api_key scope (the consolidator's default claude_cli provider ignores it
// at runtime, but an operator who later switches to an API-key model wants a
// real key bound). When no api_key scope exists it FALLS BACK to any auth
// scope — previously the autoinstall skipped entirely here, which is the
// reason subscription-only setups (no api_key) never got a consolidator at
// all. Returns ok=false only when there are NO auth scopes whatsoever (then
// there's nothing to satisfy the placeholder and the next boot retries).
func pickConsolidatorScope(ctx context.Context, db store.Store) (string, bool) {
	scopes, err := db.ListAuthScopes(ctx)
	if err != nil || len(scopes) == 0 {
		return "", false
	}
	var bestAPIKey, bestAny *store.AuthScope
	for i := range scopes {
		s := &scopes[i]
		if bestAny == nil || s.UpdatedAt.After(bestAny.UpdatedAt) {
			bestAny = s
		}
		if strings.EqualFold(s.Type, "api_key") {
			if bestAPIKey == nil || s.UpdatedAt.After(bestAPIKey.UpdatedAt) {
				bestAPIKey = s
			}
		}
	}
	if bestAPIKey != nil {
		return bestAPIKey.ID, true
	}
	return bestAny.ID, true
}
