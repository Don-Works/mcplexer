// logwatch_autoinstall.go — optional daemon-startup wiring installing
// the Monitoring log-watch triage worker in every workspace that has
// enabled log sources. Same contract as consolidator_autoinstall.go:
//
//   - Gated behind MCPLEXER_AUTO_INSTALL_LOG_WATCH=1
//   - Idempotent: installs missing workers and converges the fleet-wide
//     evidence/safety contract while preserving operator state/model/schedule
//   - Skips workspaces with no ENABLED log sources — nothing to watch
//   - Skips entirely when no auth scope exists yet
//
// Post-install it stamps the zero-spend pre_execute_script gate, tight
// budgets, and the mesh trigger (tag:logwatch) that lets anomaly
// alerts wake the worker between ticks.
package main

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"strings"

	"github.com/don-works/mcplexer/internal/store"
	workersadmin "github.com/don-works/mcplexer/internal/workers/admin"
	"github.com/don-works/mcplexer/internal/workertemplates"
)

const (
	autoLogWatchName     = "log-watch"
	autoLogWatchTemplate = "log-watch"
	autoLogWatchSchedule = "10m"

	// A first error-class template still wakes the worker immediately.
	// Subsequent novel shapes are grouped briefly and recovered by the
	// periodic sweep instead of starting overlapping model loops.
	autoLogWatchTriggerThrottleSeconds = 300
	// The prompt targets two outer calls (digest + commit). Six leaves one
	// raw drill-down and recovery room without allowing an expensive loop.
	autoLogWatchMaxToolCalls = 6
	// A real default ceiling prevents a forgotten worker from spending without
	// bound. Existing positive operator-configured caps remain untouched.
	autoLogWatchMaxMonthlyCostUSD = 5.0
	// Log-watch is a bounded, evidence-copying classifier. Deep-thinking
	// models can exhaust a whole completion on hidden reasoning before they
	// issue the acknowledgement/task call, so new and existing installs opt
	// out through the per-worker openai_compat extension.
	autoLogWatchModelThinking = "disabled"

	// logWatchGate aborts before any model spend on quiet ticks. Its window
	// deliberately matches the schedule: every new template is seen by one
	// periodic sweep without paying to re-triage the same rolling window.
	// Error-class novelty still wakes immediately through the mesh trigger.
	//
	// Note the prompt's digest reads a slightly WIDER window (15m) than this
	// 10m gate on purpose: the ~5m overlap means a template first seen in the
	// tail of one tick is still surfaced by the next tick's digest even though
	// it didn't itself trip this gate. Repeat observations are absorbed by the
	// worker's canonical-task dedupe (task__list meta_match), not re-filed.
	//
	// Optional hook.params.min_pending_severity (e.g. "error") further gates
	// spend for high-backlog workspaces: the tick only proceeds when the
	// durable pending queue still has at least one template at that floor.
	// Used by Embroidery after its historical info/warn flood; central clients
	// leave the param unset so warn novelty still gets classified.
	logWatchGate = `const s = monitoring.stats({window: "10m"});
const forced = hook.run.trigger_kind === "mesh" || hook.run.trigger_kind === "manual";
if (s.pending_templates === 0 && !s.evidence_gap && !forced) abort("quiet");
const minSev = (hook.params && hook.params.min_pending_severity) || "";
if (minSev && !forced) {
  const d = monitoring.digest({window: "15m", budget_tokens: 200, max_samples: 1, pending_only: true, min_severity: String(minSev)});
  const text = typeof d === "string" ? d : (d && d.text) || "";
  if (!String(text).includes("template_id:")) abort("quiet below " + minSev);
}`

	// A model response is not an effect. The post hook admits no-work manual /
	// mesh runs when the queue is genuinely empty, but blocks a blank or
	// reasoning-only success while any pending template remains. When
	// min_pending_severity is set, only pending items at that floor count.
	logWatchPostGate = `const e = monitoring.triage_effect({run_id: hook.run.id});
const s = monitoring.stats({window: "10m"});
if (!e.committed && s.pending_templates > 0) {
  const minSev = (hook.params && hook.params.min_pending_severity) || "";
  if (minSev) {
    const d = monitoring.digest({window: "15m", budget_tokens: 200, max_samples: 1, pending_only: true, min_severity: String(minSev)});
    const text = typeof d === "string" ? d : (d && d.text) || "";
    if (String(text).includes("template_id:")) abort("no monitoring triage effect committed");
  } else {
    abort("no monitoring triage effect committed");
  }
}`
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
			if err := convergeLogWatchWorker(ctx, workers, installed.Worker); err != nil {
				log.Printf("log-watch autoinstall: safety config for workspace %s: %v", ws.ID, err)
			}
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

// convergeLogWatchWorker upgrades the fleet-wide evidence/task contract and
// safety/cost invariants without changing an operator's enabled state,
// schedule, model, or positive monthly cap.
func convergeLogWatchWorker(
	ctx context.Context, workers *workersadmin.Service, worker *store.Worker,
) error {
	in := workersadmin.UpdateInput{ID: worker.ID}
	dirty := false
	if worker.PreExecuteScript != logWatchGate {
		gate := logWatchGate
		in.PreExecuteScript, dirty = &gate, true
	}
	if worker.PostExecuteScript != logWatchPostGate {
		gate := logWatchPostGate
		in.PostExecuteScript, dirty = &gate, true
	}
	if worker.PromptTemplate != workertemplates.HardenedLogWatchPrompt {
		prompt := workertemplates.HardenedLogWatchPrompt
		in.PromptTemplate, dirty = &prompt, true
	}
	allowlist := worker.ToolAllowlistJSON
	for _, required := range []string{"monitoring__commit_triage", "monitoring__triage_effect"} {
		updated, changed, err := ensureLogWatchTool(allowlist, required)
		if err != nil {
			return err
		}
		if changed {
			allowlist, dirty = updated, true
		}
	}
	if allowlist != worker.ToolAllowlistJSON {
		in.ToolAllowlistJSON = &allowlist
	}
	if params, changed, err := ensureLogWatchParameter(
		worker.ParametersJSON, "model_thinking", autoLogWatchModelThinking,
	); err != nil {
		return err
	} else if changed {
		in.ParametersJSON, dirty = &params, true
	}
	setCappedInt(&in.MaxToolCalls, worker.MaxToolCalls, autoLogWatchMaxToolCalls, &dirty)
	setCappedInt(&in.MaxWallClockSeconds, worker.MaxWallClockSeconds, 300, &dirty)
	setCappedInt(&in.MaxConsecutiveFailures, worker.MaxConsecutiveFailures, 5, &dirty)
	if worker.MaxMonthlyCostUSD <= 0 {
		capUSD := autoLogWatchMaxMonthlyCostUSD
		in.MaxMonthlyCostUSD, dirty = &capUSD, true
	}
	if !dirty {
		return nil
	}
	_, err := workers.Update(ctx, in)
	return err
}

// ensureLogWatchParameter adds a runtime parameter without replacing
// operator-authored values. Existing explicit choices win.
func ensureLogWatchParameter(raw, key, value string) (string, bool, error) {
	params := map[string]any{}
	if strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(raw), &params); err != nil {
			return "", false, err
		}
		if params == nil {
			params = map[string]any{}
		}
	}
	if _, exists := params[key]; exists {
		return raw, false, nil
	}
	params[key] = value
	out, err := json.Marshal(params)
	if err != nil {
		return "", false, err
	}
	return string(out), true, nil
}

// ensureLogWatchTool adds a newly required read-only task primitive without
// removing operator-added tools or reordering the existing allowlist.
func ensureLogWatchTool(raw, required string) (string, bool, error) {
	tools := []string{}
	if strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(raw), &tools); err != nil {
			return "", false, err
		}
	}
	for _, tool := range tools {
		if tool == required {
			return raw, false, nil
		}
	}
	tools = append(tools, required)
	out, err := json.Marshal(tools)
	if err != nil {
		return "", false, err
	}
	return string(out), true, nil
}

func setCappedInt(dst **int, current, ceiling int, dirty *bool) {
	if current > 0 && current <= ceiling {
		return
	}
	value := ceiling
	*dst, *dirty = &value, true
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
		ThrottleSeconds: autoLogWatchTriggerThrottleSeconds,
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
// from env so an operator can run the triage worker on a commercially
// permitted provider (e.g. a metered GLM/Z.AI API via openai_compat) instead
// of the template's claude_cli default. A personal coding-plan credential is
// not an automatic fit for customer automation:
//
//	MCPLEXER_LOGWATCH_MODEL_PROVIDER=openai_compat
//	MCPLEXER_LOGWATCH_MODEL_ID=glm-5.2
//	MCPLEXER_LOGWATCH_MODEL_ENDPOINT=https://api.z.ai/api/paas/v4/chat/completions
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
		Parameters:       map[string]string{"model_thinking": autoLogWatchModelThinking},
		Enabled:          &enabled,
		ModelProvider:    strings.TrimSpace(os.Getenv("MCPLEXER_LOGWATCH_MODEL_PROVIDER")),
		ModelID:          strings.TrimSpace(os.Getenv("MCPLEXER_LOGWATCH_MODEL_ID")),
		ModelEndpointURL: strings.TrimSpace(os.Getenv("MCPLEXER_LOGWATCH_MODEL_ENDPOINT")),
	})
	if err != nil {
		return err
	}
	gate := logWatchGate
	postGate := logWatchPostGate
	maxTools := autoLogWatchMaxToolCalls
	maxWall := 300
	maxFail := 5
	maxMonthlyCost := autoLogWatchMaxMonthlyCostUSD
	if _, err := workers.Update(ctx, workersadmin.UpdateInput{
		ID:                     installed.ID,
		PreExecuteScript:       &gate,
		PostExecuteScript:      &postGate,
		MaxToolCalls:           &maxTools,
		MaxWallClockSeconds:    &maxWall,
		MaxMonthlyCostUSD:      &maxMonthlyCost,
		MaxConsecutiveFailures: &maxFail,
	}); err != nil {
		return err
	}
	// Anomaly alerts (tag:logwatch) wake the worker between ticks; the
	// throttle bounds re-fires per source.
	return ensureLogWatchTrigger(ctx, workers, installed.ID)
}
