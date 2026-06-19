package gateway

import "encoding/json"

// skillRunsToolDefinitions returns the W2 skill telemetry tools:
// skill__run_start / skill__phase / skill__run_complete. Together they
// turn every agent invocation of a registry skill into one append-only
// row in skill_runs + (optionally) a task_create epic + per-phase
// children — so the dashboard, refinement loop (W3), and composition
// graph (W6) all share one durable signal.
//
// The trio is intentionally minimal: start, phase, complete. Anything
// richer (mid-run metadata mutation, multi-attempt phase rewind) is a
// follow-up signal in `metadata_json` rather than a new tool. Smaller
// surface area = lower LLM ergonomics cost.
func skillRunsToolDefinitions() []Tool {
	return []Tool{
		{
			Name: "skill__run_start",
			Description: "Begin telemetry for a skill invocation. Call this FIRST at the start of executing any registry skill. Returns a `run_id` you'll pass to subsequent skill__phase / skill__run_complete calls. " +
				"Pass `phases:[name, name, ...]` to declare the skill's phase sequence up-front — when provided AND `task_epic_id` is empty, the gateway auto-creates a task__create epic + N child tasks so the run is visible + resumable in the task dashboard mid-flight. " +
				"Without phases the run is recorded silently in skill_runs and surfaces only on the skill's detail page.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"skill": {
						"type": "string",
						"description": "Skill name (matches the registry entry's name field)."
					},
					"version": {
						"type": "integer",
						"description": "Skill version. Optional — defaults to 0 when the caller doesn't know (still queryable; just no version-pinned A/B signal for W3)."
					},
					"phases": {
						"type": "array",
						"items": {"type": "string"},
						"description": "Optional ordered list of phase names. Auto-creates a task epic + child tasks when provided AND task_epic_id is empty."
					},
					"task_epic_id": {
						"type": "string",
						"description": "Optional existing task ID to attach this run to instead of auto-creating a new epic."
					},
					"metadata": {
						"type": "object",
						"description": "Optional structured metadata stored verbatim in metadata_json (agent name, mesh-trigger id, parent run id, etc.)."
					}
				},
				"required": ["skill"]
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "Start Skill Run",
				ReadOnlyHint:    boolPtr(false),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(false),
				OpenWorldHint:   boolPtr(false),
			}),
		},
		{
			Name: "skill__phase",
			Description: "Record a phase event (started / completed / failed) on an active skill run. Calls accumulate in `phases_json` append-only — restarts/retries appear as repeated `started` events rather than overwriting, which is itself a refinement signal for W3. " +
				"When the run was created with a task epic, child task status mirrors the phase event (started → doing, completed → done+terminal, failed → blocked).",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"run_id": {"type": "string", "description": "Run id returned by skill__run_start."},
					"phase":  {"type": "string", "description": "Phase name; usually one of the names passed to skill__run_start."},
					"event":  {
						"type": "string",
						"enum": ["started", "completed", "failed"],
						"description": "Lifecycle event for this phase."
					},
					"note":   {"type": "string", "description": "Optional short note; surfaces in the run timeline."}
				},
				"required": ["run_id", "phase", "event"]
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "Record Skill Phase",
				ReadOnlyHint:    boolPtr(false),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(false),
				OpenWorldHint:   boolPtr(false),
			}),
		},
		{
			Name: "skill__run_complete",
			Description: "Mark a skill run finished. Stamps `completed_at` and the final `outcome`. When a task epic was attached, it's also marked done/terminal so the dashboard reflects completion without a second call. " +
				"This is the W3 signal: outcome + duration + phase history + tool usage together feed the refinement loop's A/B promote-or-discard decisions.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"run_id":  {"type": "string", "description": "Run id returned by skill__run_start."},
					"outcome": {
						"type": "string",
						"enum": ["success", "failure", "cancelled"],
						"description": "Terminal outcome of the run."
					},
					"summary": {"type": "string", "description": "Optional summary; appended as a final note on the task epic."}
				},
				"required": ["run_id", "outcome"]
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "Complete Skill Run",
				ReadOnlyHint:    boolPtr(false),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(true),
				OpenWorldHint:   boolPtr(false),
			}),
		},
	}
}
