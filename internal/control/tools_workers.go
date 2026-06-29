package control

import (
	"encoding/json"

	"github.com/don-works/mcplexer/internal/gateway"
)

// workerListToolDef declares the mcplexer__list_workers tool. Optional
// filters narrow the scan to one workspace, enabled-only, or a
// case-insensitive name substring.
func workerListToolDef() gateway.Tool {
	return gateway.Tool{
		Name:        "list_workers",
		Description: "List Workers (scheduled in-process AI agents). Returns one summary row per worker — id, name, model, schedule, enabled flag, and last-run status — across the matching workspace(s). Workers are configured via mcplexer__create_worker; runs are inspected via mcplexer__list_worker_runs / mcplexer__get_worker_run.",
		InputSchema: schema(props{
			"enabled_only": map[string]any{
				"type":        "boolean",
				"description": "When true, only enabled workers are returned. Default: false.",
			},
			"workspace_id": propStr("Optional: limit listing to this workspace. Omit to scan all workspaces."),
			"name_pattern": propStr("Optional: case-insensitive substring match on worker name."),
		}, nil),
	}
}

// workerGetToolDef declares mcplexer__get_worker. Lookup is by id, or by
// (name + workspace_id) — at least one must be present.
func workerGetToolDef() gateway.Tool {
	return gateway.Tool{
		Name:        "get_worker",
		Description: "Get a Worker by id, or by (name + workspace_id). Returns the full Worker config plus its 5 most-recent runs (summaries). Provide either `id` OR both `name` and `workspace_id`.",
		InputSchema: schema(props{
			"id":           propStr("Worker ID (e.g. wkr-...). Optional when name+workspace_id are set."),
			"name":         propStr("Worker name (used with workspace_id when id is empty)."),
			"workspace_id": propStr("Workspace ID (used with name when id is empty)."),
		}, nil),
	}
}

// workerCreateToolDef is the heaviest schema — every Worker field shows
// up with a description so an agent can fill it in cleanly. Required
// fields mirror admin.validateCreate.
func workerCreateToolDef() gateway.Tool {
	return gateway.Tool{
		Name:        "create_worker",
		Description: "Create a Worker (scheduled in-process AI agent). The runner renders prompt_template with parameters_json, dispatches to the configured model, optionally invokes tools constrained by tool_allowlist_json, and emits output to output_channels_json sinks. Validation requires name, model_provider (anthropic|openai|openai_compat|claude_cli|opencode_cli|grok_cli|mimo_cli|gemini_cli|codex_cli|pi_cli), model_id, secret_scope_id (points at the AuthScope holding the model API key; CLI providers can use a placeholder scope and inherit host credentials), prompt_template, schedule_spec, and workspace_id. Defaults: exec_mode=propose, concurrency_policy=skip, tool_allowlist_json=[], output_channels_json=[{type:mesh,priority:normal}], enabled=true. When model_provider=openai_compat, model_endpoint_url is required.",
		InputSchema: workerCreateInputSchema(),
	}
}

// workerCreateInputSchema is the input_schema JSON for create. Pulled
// into its own function so create + update can share descriptions.
//
// auto_paused_reason and source_template_name / source_template_version
// are deliberately omitted — they're either runner-managed
// (auto_paused_reason is only clearable on update) or
// install-flow-managed (source_template_* set only by the internal
// createFromTemplate path, never by operator payloads).
func workerCreateInputSchema() json.RawMessage {
	properties := workerCommonProps()
	required := []string{
		"name", "model_provider", "model_id", "secret_scope_id",
		"prompt_template", "schedule_spec", "workspace_id",
	}
	return schema(properties, required)
}

// workerCommonProps returns the property map shared by create + update.
// update_worker layers on `id` plus `auto_paused_reason` (the operator-
// clearable audit field); every other property remains optional and is
// only applied when present in args.
func workerCommonProps() props {
	return props{
		"name":               propStr("Worker name; unique within the workspace."),
		"description":        propStr("Human-readable description."),
		"model_provider":     propStr("LLM provider: anthropic | openai | openai_compat | claude_cli | opencode_cli | grok_cli | mimo_cli | gemini_cli | codex_cli | pi_cli."),
		"model_id":           propStr("Provider-specific model identifier (e.g. claude-opus-4-7)."),
		"model_endpoint_url": propStr("Required when model_provider=openai_compat. Base URL of the OpenAI-compatible endpoint."),
		"secret_scope_id":    propStr("AuthScope id holding the model API key. Read at run time only; the key never crosses the runner boundary."),
		"skill_name":         propStr("Optional (legacy single-skill): skill name whose body prepends the rendered prompt. Use skill_refs for multi-skill workers."),
		"skill_version":      propStr("Optional (legacy single-skill): skill version pin (empty = latest stable)."),
		"skill_refs": map[string]any{
			"type":        "array",
			"description": "Ordered list of skills whose bodies prepend the prompt (joined with a markdown separator). Replaces legacy skill_name / skill_version (still accepted for single-skill workers).",
			"items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name":    map[string]any{"type": "string", "description": "Skill name."},
					"version": map[string]any{"type": "string", "description": "Skill version pin (empty = latest stable)."},
				},
				"required": []string{"name"},
			},
		},
		"prompt_template":      propStr("Mustache-style template; placeholders are interpolated from parameters_json."),
		"parameters_json":      propStr("JSON object literal injected into prompt_template. Default {}."),
		"schedule_spec":        propStr("Cron expression (e.g. \"0 9 * * *\") or Go duration string (\"5m\", \"1h\"). Parsed by the scheduler."),
		"tool_allowlist_json":  propStr("JSON array of tool-name globs the runner may call. Default []."),
		"pre_execute_script":   propStr("Optional JavaScript gate run in the code-mode sandbox BEFORE any model/CLI spend. Runs with this worker's own tool allowlist; `hook` exposes {phase,worker,run,params}. Throw or call abort(reason) to BLOCK the run (status=blocked, zero spend); return cleanly to proceed. There is no JS fetch — reach an HTTP endpoint via a downstream tool the allowlist permits, e.g. fetch.fetch({url}). Example: const r=fetch.fetch({url:\"https://api.example.com/gate\"}); if(!/\\\"go\\\":true/.test(r.content[0].text)) abort(\"gate said no\");"),
		"post_execute_script":  propStr("Optional JavaScript run in the code-mode sandbox AFTER output is produced. `hook.run` adds {status,output,error,input_tokens,output_tokens,cost_usd,tool_calls}. Throw or abort(reason) on an otherwise-successful run to REJECT the output (status=blocked, channel emission suppressed). Use for output validation or post-run notifications."),
		"output_channels_json": propStr("JSON array describing output sinks. Default [{type:mesh,priority:normal}]."),
		"exec_mode":            propStr("propose (emit a draft + mesh message — default) or autonomous (execute tool calls directly, subject to allowlist)."),
		"concurrency_policy":   propStr("skip (default; drop the tick when a run is in flight) or queue (start a parallel run, audit-only)."),
		"memory_scope_id":      propStr("Reserved for the future memory subsystem; persisted but unused in M0."),
		"enabled":              map[string]any{"type": "boolean", "description": "When false, the scheduler skips this worker. Default true."},
		"workspace_id":         propStr("Workspace this worker belongs to."),
		"workspace_access": map[string]any{
			"type":        "array",
			"description": "Explicit workspace grants this worker can see/access. access=read permits reads only; access=write permits reads and mutations. The preferred workspace_id is always retained with write access.",
			"items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"workspace_id": map[string]any{"type": "string", "description": "Workspace ID this worker can access."},
					"access": map[string]any{
						"type":        "string",
						"enum":        []string{"read", "write"},
						"description": "read or write; write implies read.",
					},
				},
				"required": []string{"workspace_id", "access"},
			},
		},

		// M1 safety caps. 0 = use the runner default (or no cap for the
		// budget + failure-streak caps).
		"max_input_tokens":         propInt("Aggregate input-token cap across the whole run. 0 = runner default (200000)."),
		"max_output_tokens":        propInt("Aggregate output-token cap across the whole run. 0 = use runner default (4096 per turn)."),
		"max_tool_calls":           propInt("Cap on total tool dispatches across the whole run. 0 = runner default (50)."),
		"max_wall_clock_seconds":   propInt("Wall-clock cap in seconds. 0 = runner default (300)."),
		"max_monthly_cost_usd":     map[string]any{"type": "number", "description": "Monthly budget in USD. When exceeded, the worker auto-pauses + a critical mesh alert fires. 0 = no cap."},
		"max_consecutive_failures": propInt("Pause + high mesh alert when the last N runs are all failures. 0 = no auto-pause."),
	}
}

// workerUpdateToolDef declares mcplexer__update_worker. Only id is
// required; every other field is optional and applied iff present.
// auto_paused_reason is exposed here (and only here) because update is
// where operators dismiss the auto-pause banner after manual review.
func workerUpdateToolDef() gateway.Tool {
	properties := workerCommonProps()
	properties["id"] = propStr("Worker ID.")
	properties["auto_paused_reason"] = propStr("Set to \"\" to dismiss the auto-pause banner after manual review. Read-only audit field otherwise.")
	return gateway.Tool{
		Name:        "update_worker",
		Description: "Update a Worker. Only fields explicitly present in the request are applied — omit a field to leave it unchanged. Validates the same way as create_worker; rejects invalid model_provider (anthropic|openai|openai_compat|claude_cli|opencode_cli|grok_cli|mimo_cli|gemini_cli|codex_cli|pi_cli), missing model_endpoint_url on openai_compat, malformed output_channels_json / parameters_json, and unrecognised exec_mode / concurrency_policy values.",
		InputSchema: schema(properties, []string{"id"}),
	}
}

// workerDeleteToolDef declares mcplexer__delete_worker. Hard delete;
// runs are intentionally preserved (M0.1 documented this — the audit
// ledger survives a Worker rename or recreate).
func workerDeleteToolDef() gateway.Tool {
	return gateway.Tool{
		Name:        "delete_worker",
		Description: "Hard-delete a Worker by id. The worker_runs ledger is intentionally NOT cascade-deleted so the audit history survives. Returns {\"deleted\": true} on success.",
		InputSchema: schema(props{"id": propStr("Worker ID.")}, []string{"id"}),
	}
}

// workerPauseToolDef declares mcplexer__pause_worker. Sets enabled=false.
func workerPauseToolDef() gateway.Tool {
	return gateway.Tool{
		Name:        "pause_worker",
		Description: "Pause a Worker (sets enabled=false) and hard-stop any currently running runs for that worker. The scheduler stops firing it. Idempotent — pausing an already-paused worker returns it unchanged.",
		InputSchema: schema(props{"id": propStr("Worker ID.")}, []string{"id"}),
	}
}

// workerResumeToolDef declares mcplexer__resume_worker. Sets enabled=true.
func workerResumeToolDef() gateway.Tool {
	return gateway.Tool{
		Name:        "resume_worker",
		Description: "Resume a paused Worker (sets enabled=true). The next scheduled tick will dispatch it. Idempotent.",
		InputSchema: schema(props{"id": propStr("Worker ID.")}, []string{"id"}),
	}
}

// workerRunNowToolDef declares mcplexer__run_worker_now. Returns a
// run_id immediately; the run executes asynchronously.
func workerRunNowToolDef() gateway.Tool {
	return gateway.Tool{
		Name:        "run_worker_now",
		Description: "Fire an ad-hoc Worker run immediately, bypassing its schedule. Returns {run_id, status:\"running\"} once the run row is persisted. The run executes asynchronously — poll with mcplexer__get_worker_run to track it. Subject to the Worker's concurrency_policy.",
		InputSchema: schema(props{"id": propStr("Worker ID.")}, []string{"id"}),
	}
}

// workerListRunsToolDef declares mcplexer__list_worker_runs.
func workerListRunsToolDef() gateway.Tool {
	return gateway.Tool{
		Name:        "list_worker_runs",
		Description: "List recent runs for one Worker, ordered started_at DESC. Returns bounded prompt/output/error previews; call get_worker_run for full text. Optional status filter (running | success | failure | paused | cap_exceeded | awaiting_approval | rejected). Default limit 25, hard cap 100.",
		InputSchema: schema(props{
			"worker_id": propStr("Worker ID."),
			"limit":     propInt("Max rows to return (default 25, hard cap 100)."),
			"status":    propStr("Optional status filter."),
		}, []string{"worker_id"}),
	}
}

// workerGetRunToolDef declares mcplexer__get_worker_run.
func workerGetRunToolDef() gateway.Tool {
	return gateway.Tool{
		Name:        "get_worker_run",
		Description: "Get a Worker run by id. Returns the full WorkerRun record including prompt_rendered, output_text, token + cost counters, tool_calls_count, mesh + audit cross-references.",
		InputSchema: schema(props{"run_id": propStr("Run ID.")}, []string{"run_id"}),
	}
}

// workerCancelRunToolDef declares mcplexer__cancel_worker_run.
func workerCancelRunToolDef() gateway.Tool {
	return gateway.Tool{
		Name:        "cancel_worker_run",
		Description: "Force-finalise a stuck Worker run as status=failure. Use when a run is visibly stranded in running and the live process is gone or wedged. Optional reason is written to the run error field and audit ledger.",
		InputSchema: schema(props{
			"run_id": propStr("Run ID."),
			"reason": propStr("Optional operator reason."),
		}, []string{"run_id"}),
	}
}

// workerListApprovalsToolDef declares mcplexer__list_worker_approvals.
func workerListApprovalsToolDef() gateway.Tool {
	return gateway.Tool{
		Name:        "list_worker_approvals",
		Description: "List propose-first WorkerApproval rows (write-class tool dispatches stopped pending operator decision). Optional status filter (pending|approved|rejected) — empty returns all. Default limit 50.",
		InputSchema: schema(props{
			"status": propStr("Optional: pending|approved|rejected."),
			"limit":  propInt("Max rows (default 50, hard cap 500)."),
		}, nil),
	}
}

// workerApproveApprovalToolDef declares mcplexer__approve_worker_approval.
// Approve fires a NEW run with the named tool pre-cleared.
func workerApproveApprovalToolDef() gateway.Tool {
	return gateway.Tool{
		Name:        "approve_worker_approval",
		Description: "Approve a pending WorkerApproval and fire a NEW run with the originally-blocked tool pre-cleared (PreApprovedTools). The previous run row stays as awaiting_approval — mid-run resume isn't supported in M1, so the new run reruns the worker from the start with the write tool unlocked once. Returns {approval_id, status:'approved', resumed_run_id, original_run_id}.",
		InputSchema: schema(props{
			"id":         propStr("WorkerApproval id (wapp-...)."),
			"decided_by": propStr("Optional actor label recorded on the row (default 'agent')."),
		}, []string{"id"}),
	}
}

// workerRejectApprovalToolDef declares mcplexer__reject_worker_approval.
func workerRejectApprovalToolDef() gateway.Tool {
	return gateway.Tool{
		Name:        "reject_worker_approval",
		Description: "Reject a pending WorkerApproval. The previous run row is stamped status='rejected'. No new run is fired.",
		InputSchema: schema(props{
			"id":         propStr("WorkerApproval id (wapp-...)."),
			"decided_by": propStr("Optional actor label recorded on the row (default 'agent')."),
		}, []string{"id"}),
	}
}
