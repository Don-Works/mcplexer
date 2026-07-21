package gateway

import "encoding/json"

// monitoringNamespaceToolDefinitions declares the workspace-scoped
// monitoring.* namespace (design: docs/design/remote-log-intelligence.md).
// Read tools serve the log-watch worker's zero-spend gate + digests;
// monitoring__notify remains the low-level send path; commit_triage is the
// worker-facing transaction that calls it after durable dedupe/occurrence
// recording. Admin CRUD stays on the CWD-gated mcplexer__* tools.
func monitoringNamespaceToolDefinitions() []Tool {
	ro := func(title string) map[string]json.RawMessage {
		return withAnnotations(ToolAnnotations{
			Title: title, ReadOnlyHint: boolPtr(true),
			DestructiveHint: boolPtr(false), IdempotentHint: boolPtr(true),
			OpenWorldHint: boolPtr(false),
		})
	}
	return []Tool{
		{
			Name:        "monitoring__hosts",
			Description: "List the Monitoring remote hosts in this workspace with health + host-key pin state. Read-only view; config changes go through the admin surface or the Monitoring page.",
			InputSchema: json.RawMessage(`{"type": "object", "properties": {
				"workspace_id": {"type": "string", "description": "Override current workspace."}
			}}`),
			Extras: ro("List Monitoring Hosts"),
		},
		{
			Name:        "monitoring__sources",
			Description: "List the Monitoring log sources in this workspace: container selector, pull cadence, cursor position, consecutive_failures health counter.",
			InputSchema: json.RawMessage(`{"type": "object", "properties": {
				"workspace_id": {"type": "string"}
			}}`),
			Extras: ro("List Log Sources"),
		},
		{
			Name:        "monitoring__channels",
			Description: "List the Monitoring alert channels (kind, min_severity floor, enabled). Config secret refs are not returned. An incident fans out to every enabled channel whose floor admits its severity.",
			InputSchema: json.RawMessage(`{"type": "object", "properties": {
				"workspace_id": {"type": "string"}
			}}`),
			Extras: ro("List Alert Channels"),
		},
		{
			Name:        "monitoring__stats",
			Description: "Cheap counters for the zero-spend gate: lines, templates, new_templates, pending_templates (durable untriaged queue), and error_delta. Scheduled log-watch AI wakes for pending_templates; omitted digest entries remain queued instead of starving.",
			InputSchema: json.RawMessage(`{"type": "object", "properties": {
				"window": {"type": "string", "description": "Go duration, default 10m."},
				"source_ids": {"type": "array", "items": {"type": "string"}, "description": "Default: all sources in workspace."},
				"workspace_id": {"type": "string"}
			}}`),
			Extras: ro("Monitoring Stats"),
		},
		{
			Name:        "monitoring__digest",
			Description: "Budget-bounded digest of a log window: counts × masked templates, priority-ordered (new critical/error → new → error-class → busiest). This is what a triage worker reads INSTEAD of raw logs — a 10k-line window renders in well under 1k tokens. Drill down with monitoring__raw.",
			InputSchema: json.RawMessage(`{"type": "object", "properties": {
				"window": {"type": "string", "description": "Go duration, default 15m."},
				"budget_tokens": {"type": "integer", "description": "Render budget, default 2000."},
				"max_samples": {"type": "integer", "minimum": 1, "maximum": 3, "description": "Samples per template, default 3. Use 1 for a compact code-mode-safe digest."},
				"pending_only": {"type": "boolean", "description": "Return the durable untriaged queue, including entries older than the rolling window. Use true for scheduled triage."},
				"min_severity": {"type": "string", "description": "info|warn|error|critical floor. Default info."},
				"source_ids": {"type": "array", "items": {"type": "string"}},
				"workspace_id": {"type": "string"}
			}}`),
			Extras: ro("Log Digest"),
		},
		{
			Name:        "monitoring__commit_triage",
			Description: "Commit one complete triage decision. Deterministically classifies by correlation_key then template id, elects/reuses one canonical task under a DB uniqueness constraint, stores an idempotent occurrence, notifies only for a new warn+ class or verified severity escalation, marks templates complete, and writes the worker effect receipt. Use this instead of separate task list/create/update/ack/notify calls.",
			InputSchema: json.RawMessage(`{"type": "object", "properties": {
				"disposition": {"type": "string", "enum": ["actionable", "uncertain", "evidence-gap", "benign"]},
				"severity": {"type": "string", "enum": ["info", "warn", "error", "critical"]},
				"title": {"type": "string", "description": "Required except for benign."},
				"body": {"type": "string", "description": "Self-contained observed evidence, verified facts, and labelled hypotheses. For benign, becomes the acknowledgement note."},
				"template_ids": {"type": "array", "minItems": 1, "maxItems": 50, "items": {"type": "string"}},
				"correlation_key": {"type": "string", "description": "Copy the digest value exactly when present; omit otherwise."},
				"source_name": {"type": "string"},
				"remote_host_id": {"type": "string"},
				"workspace_id": {"type": "string"}
			}, "required": ["disposition", "severity", "template_ids"]}`),
			Extras: withAnnotations(ToolAnnotations{
				Title: "Commit Monitoring Triage", ReadOnlyHint: boolPtr(false),
				DestructiveHint: boolPtr(false), IdempotentHint: boolPtr(true),
				OpenWorldHint: boolPtr(true),
			}),
		},
		{
			Name:        "monitoring__triage_effect",
			Description: "Check whether a worker run committed a complete Monitoring triage effect. Used by the log-watch post-execute gate so a blank/model-only response cannot be recorded as success.",
			InputSchema: json.RawMessage(`{"type": "object", "properties": {
				"run_id": {"type": "string", "description": "Defaults to the current worker run."},
				"workspace_id": {"type": "string"}
			}}`),
			Extras: ro("Check Monitoring Triage Effect"),
		},
		{
			Name:        "monitoring__search",
			Description: "Substring-search one source's redacted raw ring buffer, newest first, capped. For targeted drill-down after the digest points somewhere.",
			InputSchema: json.RawMessage(`{"type": "object", "properties": {
				"source_id": {"type": "string"},
				"q": {"type": "string", "description": "Substring to match."},
				"limit": {"type": "integer", "description": "Default 100, max 500."},
				"workspace_id": {"type": "string"}
			}, "required": ["source_id", "q"]}`),
			Extras: ro("Search Log Lines"),
		},
		{
			Name:        "monitoring__raw",
			Description: "Recent redacted raw lines for one template id — the drill-down behind every digest entry and filed task.",
			InputSchema: json.RawMessage(`{"type": "object", "properties": {
				"template_id": {"type": "string"},
				"limit": {"type": "integer", "description": "Default 50, max 500."},
				"workspace_id": {"type": "string"}
			}, "required": ["template_id"]}`),
			Extras: ro("Raw Lines For Template"),
		},
		{
			Name:        "monitoring__ack",
			Description: "Mark a template known/expected: it stops counting toward novelty wake-ups (still appears in digests). Use for noisy-but-harmless shapes; add a note for teammates.",
			InputSchema: json.RawMessage(`{"type": "object", "properties": {
				"template_id": {"type": "string"},
				"note": {"type": "string"},
				"workspace_id": {"type": "string"}
			}, "required": ["template_id"]}`),
			Extras: withAnnotations(ToolAnnotations{
				Title: "Ack Template", ReadOnlyHint: boolPtr(false),
				DestructiveHint: boolPtr(false), IdempotentHint: boolPtr(true),
				OpenWorldHint: boolPtr(false),
			}),
		},
		{
			Name:        "monitoring__notify",
			Description: "THE send path for monitoring incidents. The daemon renders a compact deterministic alert with system, severity, source/host, and an optional clickable MCPlexer task id; resolves channel secret refs internally; enforces per-template cooldown + hourly caps; and fans out to every enabled channel whose min_severity admits. A new critical incident also enters the durable Signal + PWA Web Push path exactly once. Call ONCE per incident after triage; storm-safe by construction. Pass template_id so repeat incidents dedupe.",
			InputSchema: json.RawMessage(`{"type": "object", "properties": {
				"severity": {"type": "string", "description": "info|warn|error|critical (required)."},
				"title": {"type": "string", "description": "One-line incident headline (required)."},
				"body": {"type": "string", "description": "Short triage summary + drill-down pointers."},
				"task_id": {"type": "string", "description": "Optional canonical MCPlexer task id. When public_url is configured, Chat renders the id itself as the clickable link."},
				"new_incident": {"type": "boolean", "description": "True only when this call created a new canonical incident task. Enables one-time critical human/PWA escalation; false for evidence updates."},
				"source_name": {"type": "string", "description": "Optional human-readable source/service name copied from the digest."},
				"remote_host_id": {"type": "string", "description": "Optional opaque host id copied exactly from monitoring__hosts. Omit it when you only have a host, source, service, or container name; those names are not ids."},
				"template_id": {"type": "string", "description": "Template that triggered this — enables per-template throttle dedup."},
				"workspace_id": {"type": "string"}
			}, "required": ["severity", "title"]}`),
			Extras: withAnnotations(ToolAnnotations{
				Title: "Send Monitoring Notification", ReadOnlyHint: boolPtr(false),
				DestructiveHint: boolPtr(false), IdempotentHint: boolPtr(false),
				OpenWorldHint: boolPtr(true),
			}),
		},
	}
}
