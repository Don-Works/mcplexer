// logwatch_autoinstall.go — optional daemon-startup wiring installing
// the Monitoring log-watch triage worker in every workspace that has
// enabled log sources. Same contract as consolidator_autoinstall.go:
//
//   - Gated behind MCPLEXER_AUTO_INSTALL_LOG_WATCH=1
//   - Idempotent: skips workspaces that already have a "log-watch"
//     worker (operator tuning is never clobbered)
//   - Skips workspaces with no ENABLED log sources — nothing to watch
//   - Skips entirely when no auth scope exists yet
//
// Post-install it stamps the zero-spend pre_execute_script gate, tight
// budgets, and the mesh trigger (tag:logwatch) that lets anomaly
// alerts wake the worker between ticks.
package main

import (
	"context"
	"log"
	"os"
	"strings"

	"github.com/don-works/mcplexer/internal/store"
	workersadmin "github.com/don-works/mcplexer/internal/workers/admin"
)

const (
	autoLogWatchName     = "log-watch"
	autoLogWatchTemplate = "log-watch"
	autoLogWatchSchedule = "2m"

	// logWatchGate aborts before any model spend on quiet ticks —
	// the ratified wake floor: error-class novelty/delta immediate,
	// info novelty batches into the next non-quiet tick's digest.
	logWatchGate = `const s = monitoring.stats({window: "10m"});
if (s.new_templates === 0 && s.error_delta === 0) abort("quiet");`
)

func autoInstallLogWatchEnabled() bool {
	v := strings.TrimSpace(strings.ToLower(os.Getenv("MCPLEXER_AUTO_INSTALL_LOG_WATCH")))
	return v == "1" || v == "true" || v == "yes" || v == "on"
}

// autoInstallLogWatch ensures the log-watch worker exists in every
// workspace with enabled log sources. Called once at daemon startup
// after the worker admin service is wired. Best-effort per workspace.
func autoInstallLogWatch(ctx context.Context, db store.Store, workers *workersadmin.Service) {
	if db == nil || workers == nil || !autoInstallLogWatchEnabled() {
		return
	}
	scopeID, ok := pickConsolidatorScope(ctx, db)
	if !ok {
		return // no auth scopes yet; next boot retries
	}
	wss, err := db.ListWorkspaces(ctx)
	if err != nil {
		log.Printf("log-watch autoinstall: list workspaces: %v", err)
		return
	}
	for _, ws := range wss {
		if !workspaceHasEnabledSources(ctx, db, ws.ID) {
			continue
		}
		if installed, err := workers.Get(ctx, workersadmin.GetInput{
			Name: autoLogWatchName, WorkspaceID: ws.ID,
		}); err == nil && installed != nil && installed.Worker != nil {
			// Present — converge the wake trigger (a prior boot may
			// have installed the worker but failed on the trigger)
			// without touching operator-tuned schedule/enabled state.
			if err := ensureLogWatchTrigger(ctx, workers, installed.Worker.ID); err != nil {
				log.Printf("log-watch autoinstall: trigger for workspace %s: %v", ws.ID, err)
			}
			continue
		}
		if err := installLogWatchWorker(ctx, workers, ws.ID, scopeID); err != nil {
			log.Printf("log-watch autoinstall: workspace %s: %v", ws.ID, err)
			continue
		}
		log.Printf("log-watch autoinstall: installed in workspace %s", ws.ID)
	}
}

// ensureLogWatchTrigger creates the tag:logwatch wake trigger when the
// worker has none — idempotent convergence, never duplicates.
func ensureLogWatchTrigger(ctx context.Context, workers *workersadmin.Service, workerID string) error {
	triggers, err := workers.ListMeshTriggers(ctx, workerID)
	if err != nil {
		return err
	}
	for _, t := range triggers {
		if t.TagMatch == "logwatch" {
			return nil
		}
	}
	_, err = workers.CreateMeshTrigger(ctx, workersadmin.MeshTriggerInput{
		WorkerID:        workerID,
		KindMatch:       "alert",
		TagMatch:        "logwatch",
		ThrottleSeconds: 120,
	})
	return err
}

func workspaceHasEnabledSources(ctx context.Context, db store.Store, workspaceID string) bool {
	sources, err := db.ListLogSources(ctx, workspaceID)
	if err != nil {
		return false
	}
	for _, s := range sources {
		if s.Enabled {
			return true
		}
	}
	return false
}

// installLogWatchWorker installs from the seed template, then stamps
// the gate script + budgets + the wake trigger. Model overrides come
// from env so an operator can run the triage worker on their own
// provider (e.g. GLM/Z.AI via openai_compat) instead of the template's
// claude_cli default:
//
//	MCPLEXER_LOGWATCH_MODEL_PROVIDER=openai_compat
//	MCPLEXER_LOGWATCH_MODEL_ID=glm-5.2
//	MCPLEXER_LOGWATCH_MODEL_ENDPOINT=https://api.z.ai/api/coding/paas/v4
//	MCPLEXER_LOGWATCH_SECRET_SCOPE=<scope id holding api_key>  (optional; else auto-picked)
func installLogWatchWorker(ctx context.Context, workers *workersadmin.Service, workspaceID, scopeID string) error {
	enabled := true
	if s := strings.TrimSpace(os.Getenv("MCPLEXER_LOGWATCH_SECRET_SCOPE")); s != "" {
		scopeID = s
	}
	installed, err := workers.InstallFromTemplate(ctx, workersadmin.InstallFromTemplateInput{
		TemplateName:     autoLogWatchTemplate,
		WorkerName:       autoLogWatchName,
		WorkspaceID:      workspaceID,
		SecretScopeID:    scopeID,
		ScheduleSpec:     autoLogWatchSchedule,
		ExecMode:         "autonomous",
		Enabled:          &enabled,
		ModelProvider:    strings.TrimSpace(os.Getenv("MCPLEXER_LOGWATCH_MODEL_PROVIDER")),
		ModelID:          strings.TrimSpace(os.Getenv("MCPLEXER_LOGWATCH_MODEL_ID")),
		ModelEndpointURL: strings.TrimSpace(os.Getenv("MCPLEXER_LOGWATCH_MODEL_ENDPOINT")),
	})
	if err != nil {
		return err
	}
	gate := logWatchGate
	maxTools := 15
	maxWall := 120
	maxFail := 5
	if _, err := workers.Update(ctx, workersadmin.UpdateInput{
		ID:                     installed.ID,
		PreExecuteScript:       &gate,
		MaxToolCalls:           &maxTools,
		MaxWallClockSeconds:    &maxWall,
		MaxConsecutiveFailures: &maxFail,
	}); err != nil {
		return err
	}
	// Anomaly alerts (tag:logwatch) wake the worker between ticks; the
	// throttle bounds re-fires per source.
	return ensureLogWatchTrigger(ctx, workers, installed.ID)
}
