package gateway

import "encoding/json"

// withExamples returns a new Extras map carrying the given annotations
// plus an "x-examples" key. The x-examples payload is a JSON array of
// copy-pasteable call payloads so codemode's extractExamples helper
// (internal/gateway/handler_codemode.go) can surface them inline next
// to the tool description. Used by delegate_worker / invoke_model to
// give cheap models a working call template they can mirror.
func withExamples(a ToolAnnotations, examples []string) map[string]json.RawMessage {
	extras := withAnnotations(a)
	data, _ := json.Marshal(examples)
	extras["x-examples"] = data
	return extras
}

func delegationToolDefinitions() []Tool {
	return []Tool{
		{
			Name:        "mcpx__delegate_worker",
			Description: "Delegate token-heavy coding, research, file-inspection, or review work to one or more cheaper worker contexts while the current Claude/Codex context stays strategic. The worker runs in the current workspace by default, can use a model profile or explicit provider/model, and appears in the Delegations UI with model, status, tokens, cost, tool calls, prompt/output, real-dollar spend (real_dollars_spent) vs subscription-quota usage (frontier_quota_preserved/burned), measured cost saved (real_cost_saved_usd), caller-estimated baseline savings, model-level stats/rank inputs, and parent review score. Use baseline_tokens_estimate/baseline_cost_usd as the delegating model's CLAIM of frontier work avoided — these are caller estimates, not measured values; authoritative out-of-pocket and quota figures are reported per delegation. For model exploration fan-out, set model_candidates plus side_by_side, or use capacity to let mcplexer rank registered profiles by reviewed quality, reliability, active load, cost, and speed. Review every result so model rank becomes meaningful. For intra-delegation fan-out, set parallelism up to 20.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"objective": {"type":"string","description":"The concrete work the delegated worker should complete."},
					"handoff": {"type":"string","description":"Bounded context packet: relevant files, decisions, constraints, acceptance criteria, and what not to re-read."},
					"name": {"type":"string","description":"Optional short label for the worker names."},
					"task_id": {"type":"string","description":"Optional mcplexer task id this delegation is executing."},
					"task_kind": {"type":"string","description":"Optional task category used for model routing/review, e.g. coding, review, architecture, tool_calling, visual."},
					"workspace_id": {"type":"string","description":"Optional workspace override. Omit to use the current session workspace. Cross-workspace writes are rejected unless already in scope."},
					"worker_mode": {"type":"string","enum":["execute","review"],"description":"execute lets the worker make scoped changes; review asks it to inspect/report only unless the handoff explicitly permits edits. Default execute."},
					"review_required": {"type":"boolean","description":"When true, the delegation remains needs_review until mcpx__review_delegation records a parent score. Default false; set true only when parent review must gate completion or feed model-ranking telemetry."},
					"model_profile_id": {"type":"string","description":"Reusable model profile id. If set, fills provider, endpoint, secret scope, and default model when omitted."},
					"model_provider": {"type":"string","description":"anthropic | openai | openai_compat | claude_cli | opencode_cli | grok_cli | mimo_cli | gemini_cli | codex_cli | pi_cli. For native Xiaomi MiMo via the host CLI, use mimo_cli. For the Pi coding harness (pi.dev) via the host 'pi' CLI, use pi_cli."},
					"model_id": {"type":"string","description":"Provider-specific model id, e.g. grok-build, xiaomi/mimo-v2.5-pro, minimax/MiniMax-M3, or zai-coding-plan/glm-5.1. For pi_cli, the model id Pi resolves from ~/.pi/agent/models.json."},
					"model_endpoint_url": {"type":"string","description":"Optional endpoint or binary path override; required for openai_compat unless supplied by a model profile. For opencode_cli, prefer a running opencode server URL such as http://127.0.0.1:4096 so parallel workers attach through one server instead of racing the CLI database. For grok_cli, leave blank to discover 'grok' or set an absolute binary path. For mimo_cli, leave blank to discover 'mimo', set an absolute binary path, or set an HTTP(S) mimocode server URL for --attach. For pi_cli, leave blank to discover 'pi' or set an absolute binary path."},
					"secret_scope_id": {"type":"string","description":"Auth scope for direct API providers. Optional for claude_cli/opencode_cli/grok_cli/mimo_cli/gemini_cli/codex_cli/pi_cli when any placeholder scope exists."},
					"model_selection_mode": {"type":"string","enum":["single","ranked","random","side_by_side","capacity"],"description":"single chooses model_candidate_index, ranked chooses the best reviewed supplied candidate, random samples supplied candidates, side_by_side runs every supplied candidate, capacity expands registered model profiles and load-balances by quality/reliability/load/cost/speed."},
					"model_candidate_index": {"type":"integer","description":"0-based candidate index used only by single mode. Default 0."},
					"model_candidates": {
						"type":"array",
						"description":"Optional candidate models for ranked/random/side_by_side exploration. Omit in capacity mode to expand registered model profiles.",
						"items":{
							"type":"object",
							"properties":{
								"label":{"type":"string"},
								"model_profile_id":{"type":"string"},
								"model_provider":{"type":"string"},
								"model_id":{"type":"string"},
								"model_endpoint_url":{"type":"string"},
								"secret_scope_id":{"type":"string"},
								"capability_tags":{"type":"array","items":{"type":"string"}},
								"input_modalities":{"type":"array","items":{"type":"string"}},
								"output_modalities":{"type":"array","items":{"type":"string"}}
							}
						}
					},
					"parallelism": {"type":"integer","description":"Number of independent delegated contexts to start. Default 1, max 20."},
					"parent_context_id": {"type":"string","description":"Optional parent context id. Defaults to current MCP session id when available."},
					"parent_model": {"type":"string","description":"Optional parent model label, e.g. opus or gpt-5. Defaults to the client model hint when available."},
					"parent_input_tokens": {"type":"integer","description":"Optional current parent input-token spend for context accounting only. Parent clients may not report this reliably; do not depend on it for savings."},
					"parent_output_tokens": {"type":"integer","description":"Optional current parent output-token spend for context accounting only. Parent clients may not report this reliably; do not depend on it for savings."},
					"parent_cost_usd": {"type":"number","description":"Optional current parent cost estimate in USD. Parent clients may not report this reliably; baseline_cost_usd is the caller's savings claim, not a measured value."},
					"baseline_tokens_estimate": {"type":"integer","description":"Caller-supplied estimate of incremental frontier-model tokens this delegated slice would have consumed. This is the delegating model's claim, not a measured value. Authoritative measured metrics are real_dollars_spent, frontier_quota_preserved, and frontier_quota_burned reported per delegation."},
					"baseline_cost_usd": {"type":"number","description":"Caller-supplied estimate of incremental frontier-model cost (USD) this delegated slice would have consumed. This is the delegating model's claim, not a measured value. Authoritative measured metrics are real_dollars_spent, real_cost_saved_usd, frontier_quota_preserved, and frontier_quota_burned reported per delegation."},
					"max_input_tokens": {"type":"integer","description":"Worker input-token cap. 0 = runner default."},
					"max_output_tokens": {"type":"integer","description":"Worker lifetime output-token cap. 0 = runner default."},
					"max_tool_calls": {"type":"integer","description":"Tool-call cap. Default 80. API adapters enforce via the gateway loop; CLI adapters (claude_cli/opencode_cli/grok_cli/mimo_cli/gemini_cli/codex_cli/pi_cli) enforce via audit-derived child MCP tool counts at finalize."},
					"max_wall_clock_seconds": {"type":"integer","description":"Wall-clock cap. Default 3600 for execute, 600 for review."},
					"max_monthly_cost_usd": {"type":"number","description":"Optional per-worker monthly cap."},
					"tool_allowlist_json": {"type":"string","description":"Advanced override: JSON array of allowed tool globs. Defaults to code/search/mesh/memory/task tools."},
					"pre_execute_script": {"type":"string","description":"Optional JS gate run in the code-mode sandbox BEFORE any model spend, with this worker's tool allowlist. 'hook' exposes {phase,worker,run,params}. Throw or call abort(reason) to BLOCK the run (status=blocked, zero spend); return cleanly to proceed. There is no JS fetch — reach HTTP endpoints via an allowed downstream tool, e.g. fetch.fetch({url})."},
					"post_execute_script": {"type":"string","description":"Optional JS run in the code-mode sandbox AFTER output is produced; 'hook.run' adds {status,output,error,input_tokens,output_tokens,cost_usd,tool_calls}. Throw or abort(reason) on an otherwise-successful run to REJECT its output (status=blocked, channel emission suppressed)."},
					"capability_preset": {"type":"string","enum":["full","coder","researcher","minimal"],"description":"Sizes the delegate's allowed tool surface + mcplexer features to its trust. Omit for today's behavior (full default surface gated only by tool_allowlist_json). full = unrestricted; coder = execute_code/search + selected downstream + task/memory write, no mesh/secret/subdelegation; researcher = read-only (no writes, no subdelegation); minimal = bare slim surface (search+execute only, no downstream/builtins). Composes with tool_allowlist_json: both must pass, so a profile can only narrow."},
					"capability_profile": {
						"type":"object",
						"description":"Fine-grained override merged ON TOP of capability_preset. Features can only SUBTRACT (narrow), never widen; widen via namespace_allow/tool_allow. may_use_admin is rejected — delegates never get admin.",
						"properties":{
							"namespace_allow":{"type":"array","items":{"type":"string"},"description":"Default-DENY: when set, only these namespace segments are reachable (e.g. github, task, memory). mcpx is always allowed."},
							"namespace_deny":{"type":"array","items":{"type":"string"}},
							"tool_allow":{"type":"array","items":{"type":"string"},"description":"Per-tool globs layered on top of the namespace decision (path.Match)."},
							"tool_deny":{"type":"array","items":{"type":"string"}},
							"features":{
								"type":"object",
								"properties":{
									"may_write_memory":{"type":"boolean"},
									"may_create_subdelegation":{"type":"boolean"},
									"may_offer_tasks":{"type":"boolean"},
									"may_write_tasks":{"type":"boolean"},
									"may_use_mesh":{"type":"boolean"},
									"may_use_secrets":{"type":"boolean"}
								}
							}
						}
					}
				},
				"required": ["objective"]
			}`),
			Extras: withExamples(ToolAnnotations{
				Title:           "Delegate Worker",
				ReadOnlyHint:    boolPtr(false),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(false),
				OpenWorldHint:   boolPtr(false),
			}, []string{
				`{"objective":"summarise the last 3 commits in internal/codemode/", "model_provider":"opencode_cli", "model_id":"minimax/MiniMax-M3", "max_wall_clock_seconds":300}`,
				`{"objective":"review internal/gateway/handler_builtin.go for race conditions", "model_provider":"anthropic", "model_id":"claude-sonnet-4-5", "secret_scope_id":"scope-anthropic-prod", "worker_mode":"review"}`,
				`{"objective":"explore model rank on this workspace", "model_candidates":[{"model_provider":"opencode_cli","model_id":"minimax/MiniMax-M3"},{"model_provider":"grok_cli","model_id":"grok-build"},{"model_provider":"mimo_cli","model_id":"xiaomi/mimo-v2.5"}], "model_selection_mode":"side_by_side", "parallelism":2}`,
			}),
		},
		{
			Name:        "mcpx__list_delegations",
			Description: "List recent token-preserving delegation context trees in the current workspace. Returns parent context metadata, child worker contexts, latest run status, model, tokens, cost, tool calls, frontier tokens/cost avoided, worker token delta, estimated cost saved, review state/score, and model_stats entries used by the Delegations UI model-rank panel.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"workspace_id": {"type":"string","description":"Optional workspace override; omit for current workspace."},
					"limit": {"type":"integer","description":"Max delegations to return. Default 50, max 200."}
				}
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "List Delegations",
				ReadOnlyHint:    boolPtr(true),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(true),
				OpenWorldHint:   boolPtr(false),
			}),
		},
		{
			Name:        "mcpx__extend_delegation_budget",
			Description: "Increase runtime caps for currently running workers in one delegation. Use when a delegated worker is making useful progress but needs more tool calls, wall-clock seconds, or token budget. This is increase-only: set higher absolute max_* values or additive additional_* increments. It updates the delegation worker rows and refreshes live in-memory runner caps when the run is still active; terminal runs are not resumed.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"delegation_id": {"type":"string","description":"Delegation id returned by mcpx__delegate_worker / mcpx__invoke_model."},
					"workspace_id": {"type":"string","description":"Optional workspace override; omit for current workspace."},
					"max_tool_calls": {"type":"integer","description":"Higher absolute tool-call cap."},
					"additional_tool_calls": {"type":"integer","description":"Tool calls to add to the current explicit cap."},
					"max_wall_clock_seconds": {"type":"integer","description":"Higher absolute wall-clock cap in seconds from run start."},
					"additional_wall_clock_seconds": {"type":"integer","description":"Seconds to add to the current explicit wall-clock cap."},
					"max_input_tokens": {"type":"integer","description":"Higher absolute aggregate input-token cap."},
					"additional_input_tokens": {"type":"integer","description":"Input tokens to add to the current explicit cap."},
					"max_output_tokens": {"type":"integer","description":"Higher absolute lifetime output-token cap."},
					"additional_output_tokens": {"type":"integer","description":"Output tokens to add to the current explicit lifetime cap."},
					"reason": {"type":"string","description":"Optional short operator note for why the budget was increased."}
				},
				"required": ["delegation_id"]
			}`),
			Extras: withExamples(ToolAnnotations{
				Title:           "Extend Delegation Budget",
				ReadOnlyHint:    boolPtr(false),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(false),
				OpenWorldHint:   boolPtr(false),
			}, []string{
				`{"delegation_id":"del-abc123","additional_tool_calls":80,"additional_wall_clock_seconds":900,"reason":"worker is on the right path and needs more runway"}`,
				`{"delegation_id":"del-abc123","max_tool_calls":250,"max_wall_clock_seconds":3600}`,
			}),
		},
		{
			Name:        "mcpx__invoke_model",
			Description: "Fire a single prompt to a cheap model and collect the result (or timeout). Thin one-call wrapper over delegate_worker that waits up to wait_seconds (default 25, clamped <=600) for the run to terminate. Never errors on elapsed wait; always returns delegation_id + current status + timed_out:true so the id is never lost when the code-mode sandbox would reap a long poll. Poll after timeout with: mcpx__list_delegations({})  (or mcpx__wait_for_delegation with the id). Use for quick single-prompt work when you do not need the full delegation review lifecycle. Returns compact status, output_text, tokens, cost, tool calls, and selected model.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"objective": {"type":"string","description":"The concrete work the delegated worker should complete."},
					"handoff": {"type":"string","description":"Bounded context packet: relevant files, decisions, constraints, acceptance criteria, and what not to re-read."},
					"workspace_id": {"type":"string","description":"Optional workspace override. Omit to use the current session workspace."},
					"worker_mode": {"type":"string","enum":["execute","review"],"description":"execute lets the worker make scoped changes; review asks it to inspect/report only. Default execute."},
					"model_profile_id": {"type":"string","description":"Reusable model profile id."},
						"model_provider": {"type":"string","description":"anthropic | openai | openai_compat | claude_cli | opencode_cli | grok_cli | mimo_cli | gemini_cli | codex_cli | pi_cli."},
					"model_id": {"type":"string","description":"Provider-specific model id."},
					"model_endpoint_url": {"type":"string","description":"Optional endpoint or binary path override."},
					"secret_scope_id": {"type":"string","description":"Auth scope for direct API providers."},
					"wait_seconds": {"type":"integer","description":"Max seconds to wait/poll before returning success with timed_out:true (preserves delegation_id). Default 25, max 600 (clamped, never errors)."},
					"max_wall_clock_seconds": {"type":"integer","description":"Wall-clock cap for the *worker* (not this call's wait). Default 3600 for execute, 600 for review."},
					"max_tool_calls": {"type":"integer","description":"Tool-call cap. Default 80. API adapters enforce via the gateway loop; CLI adapters (claude_cli/opencode_cli/grok_cli/mimo_cli/gemini_cli/codex_cli/pi_cli) enforce via audit-derived child MCP tool counts at finalize."},
					"max_output_tokens": {"type":"integer","description":"Worker lifetime output-token cap. 0 = runner default."},
					"task_id": {"type":"string","description":"Optional mcplexer task id."},
					"task_kind": {"type":"string","description":"Optional task category, e.g. coding, review."},
					"tool_allowlist_json": {"type":"string","description":"Advanced override: JSON array of allowed tool globs."},
					"capability_preset": {"type":"string","enum":["full","coder","researcher","minimal"],"description":"Sizes the delegate's allowed tool surface + mcplexer features to its trust. Omit for today's behavior. full = unrestricted; coder = code/search + selected downstream + task/memory write; researcher = read-only; minimal = mcpx only. Composes with tool_allowlist_json (intersection)."},
					"capability_profile": {
						"type":"object",
						"description":"Fine-grained override merged ON TOP of capability_preset. Features only SUBTRACT; may_use_admin is rejected.",
						"properties":{
							"namespace_allow":{"type":"array","items":{"type":"string"}},
							"namespace_deny":{"type":"array","items":{"type":"string"}},
							"tool_allow":{"type":"array","items":{"type":"string"}},
							"tool_deny":{"type":"array","items":{"type":"string"}},
							"features":{
								"type":"object",
								"properties":{
									"may_write_memory":{"type":"boolean"},
									"may_create_subdelegation":{"type":"boolean"},
									"may_offer_tasks":{"type":"boolean"},
									"may_write_tasks":{"type":"boolean"},
									"may_use_mesh":{"type":"boolean"},
									"may_use_secrets":{"type":"boolean"}
								}
							}
						}
					}
				},
				"required": ["objective"]
			}`),
			Extras: withExamples(ToolAnnotations{
				Title:           "Invoke Model",
				ReadOnlyHint:    boolPtr(false),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(false),
				OpenWorldHint:   boolPtr(false),
			}, []string{
				`{"objective":"translate this go error string to plain English: 'context deadline exceeded'", "model_provider":"opencode_cli", "model_id":"minimax/MiniMax-M3", "wait_seconds":25, "max_wall_clock_seconds":120}`,
				`{"objective":"classify this customer message as billing|bug|feature_request|other", "model_provider":"grok_cli", "model_id":"grok-build", "wait_seconds":25}`,
				`{"objective":"summarise this short note in one sentence", "model_provider":"mimo_cli", "model_id":"xiaomi/mimo-v2.5-pro", "wait_seconds":25}`,
			}),
		},
		{
			Name:        "mcpx__wait_for_delegation",
			Description: "Block until a previously dispatched delegation reaches a terminal state (success, partial, failure, or needs_review), or until the timeout elapses. Returns compact aggregate worker counts, tokens, cost, tool calls, and timeout status.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"delegation_id": {"type":"string","description":"Delegation id returned by mcpx__delegate_worker or mcpx__invoke_model."},
					"workspace_id": {"type":"string","description":"Optional workspace override; omit for current workspace."},
					"timeout_seconds": {"type":"integer","description":"Max seconds to wait. Default 300, max 600."},
					"poll_interval_ms": {"type":"integer","description":"Milliseconds between polls. Default 2000, min 500, max 10000."}
				},
				"required": ["delegation_id"]
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "Wait For Delegation",
				ReadOnlyHint:    boolPtr(true),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(true),
				OpenWorldHint:   boolPtr(false),
			}),
		},
		{
			Name:        "mcpx__list_delegation_model_capacity",
			Description: "List registered delegation model candidates with the same capacity score used by model_selection_mode=capacity. Rows are derived from model profiles plus observed delegation history: parent reviews, task-kind/category scores when available, success/failure, active running load, cost, and speed. CLI-backed models can have accounting_known=false when they succeeded but did not report usage; success_rate is over known-accounting runs only, operational_success_rate reflects terminal run outcomes, and capacity ranking uses operational success when accounting is missing so zero-token CLI runs do not poison quality scores.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"workspace_id": {"type":"string","description":"Optional workspace override; omit for current workspace."},
					"task_kind": {"type":"string","description":"Optional task kind used for task-specific scoring, e.g. coding, review, architecture, tool_calling, visual."},
					"limit": {"type":"integer","description":"Max capacity rows to return. Default 50, max 200."}
				}
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "List Delegation Model Capacity",
				ReadOnlyHint:    boolPtr(true),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(true),
				OpenWorldHint:   boolPtr(false),
			}),
		},
		{
			Name:        "mcpx__review_delegation",
			Description: "Record the expensive parent model's review of a cheaper delegated worker result. Use after inspecting worker output/tests. Score 0-100 and notes are shown in the Delegations UI beside computed token/cost savings and are attributed back to each participating model for model ranking. Optional category scores are used for task_kind-specific model ranking.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"delegation_id": {"type":"string","description":"Delegation id returned by mcpx__delegate_worker."},
					"workspace_id": {"type":"string","description":"Optional workspace override; omit for current workspace."},
					"score": {"type":"integer","description":"0-100 quality/usefulness score from the parent model."},
					"outcome": {"type":"string","enum":["accepted","partial","rejected"],"description":"Optional review outcome. Defaults from score."},
					"notes": {"type":"string","description":"Concise parent-model judgment: what was correct, what failed, and whether it saved parent context."},
					"reviewer_context_id": {"type":"string","description":"Optional reviewer context id. Defaults to current MCP session id."},
					"reviewer_model": {"type":"string","description":"Optional reviewer model label. Defaults to current client model hint."},
					"task_kind": {"type":"string","description":"Optional task category for review attribution; defaults to the delegation task kind."},
					"scores": {"type":"object","additionalProperties":{"type":"integer"},"description":"Optional category scores 0-100, e.g. coding, review, architecture, tool_calling, visual."},
					"model_scores": {
						"type":"array",
						"description":"Optional per-model scores for side-by-side delegation. Identify rows by model_key or worker_id.",
						"items":{
							"type":"object",
							"properties":{
								"model_key":{"type":"string"},
								"worker_id":{"type":"string"},
								"score":{"type":"integer"},
								"outcome":{"type":"string","enum":["accepted","partial","rejected"]},
								"notes":{"type":"string"},
								"scores":{"type":"object","additionalProperties":{"type":"integer"}}
							},
							"required":["score"]
						}
					}
				},
				"required": ["delegation_id", "score"]
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "Review Delegation",
				ReadOnlyHint:    boolPtr(false),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(false),
				OpenWorldHint:   boolPtr(false),
			}),
		},
	}
}
