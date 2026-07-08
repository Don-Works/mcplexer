package gateway

import "encoding/json"

// monitoringNamespaceToolDefinitions declares the workspace-scoped
// monitoring.* namespace (design: docs/design/remote-log-intelligence.md).
// Read tools serve the log-watch worker's zero-spend gate + digests;
// monitoring__notify is the ONLY send path — the daemon-side dispatcher
// renders the deterministic envelope, resolves channel secret refs, and
// enforces throttles. Admin CRUD stays on the CWD-gated mcplexer__* tools.
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
			Description: "Cheap window counters for the zero-spend gate: lines, templates, new_templates (unacked, first seen in window), error_delta (error+critical lines). A log-watch worker's pre_execute_script calls this and abort('quiet')s when new_templates and error_delta are both zero.",
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
				"min_severity": {"type": "string", "description": "info|warn|error|critical floor. Default info."},
				"source_ids": {"type": "array", "items": {"type": "string"}},
				"workspace_id": {"type": "string"}
			}}`),
			Extras: ro("Log Digest"),
		},
		{
			Name:        "monitoring__search",
			Description: "Substring-search one source's redacted raw ring buffer, newest first, capped. For targeted drill-down after the digest points somewhere.",
			InputSchema: json.RawMessage(`{"type": "object", "properties": {
				"source_id": {"type": "string"},
				"q": {"type": "string", "description": "Substring to match."},
				"limit": {"type": "integer", "description": "Default 100, max 500."}
			}, "required": ["source_id", "q"]}`),
			Extras: ro("Search Log Lines"),
		},
		{
			Name:        "monitoring__raw",
			Description: "Recent redacted raw lines for one template id — the drill-down behind every digest entry and filed task.",
			InputSchema: json.RawMessage(`{"type": "object", "properties": {
				"template_id": {"type": "string"},
				"limit": {"type": "integer", "description": "Default 50, max 500."}
			}, "required": ["template_id"]}`),
			Extras: ro("Raw Lines For Template"),
		},
		{
			Name:        "monitoring__ack",
			Description: "Mark a template known/expected: it stops counting toward novelty wake-ups (still appears in digests). Use for noisy-but-harmless shapes; add a note for teammates.",
			InputSchema: json.RawMessage(`{"type": "object", "properties": {
				"template_id": {"type": "string"},
				"note": {"type": "string"}
			}, "required": ["template_id"]}`),
			Extras: withAnnotations(ToolAnnotations{
				Title: "Ack Template", ReadOnlyHint: boolPtr(false),
				DestructiveHint: boolPtr(false), IdempotentHint: boolPtr(true),
				OpenWorldHint: boolPtr(false),
			}),
		},
		{
			Name:        "monitoring__notify",
			Description: "THE send path for monitoring incidents. The daemon renders the deterministic envelope '[workspace · via gateway-host] SEVERITY · remote-host', resolves channel secret refs internally, enforces per-template cooldown + hourly caps, and fans out to every enabled channel whose min_severity admits. Call ONCE per incident after triage; storm-safe by construction. Pass template_id so repeat incidents dedupe.",
			InputSchema: json.RawMessage(`{"type": "object", "properties": {
				"severity": {"type": "string", "description": "info|warn|error|critical (required)."},
				"title": {"type": "string", "description": "One-line incident headline (required)."},
				"body": {"type": "string", "description": "Short triage summary + drill-down pointers."},
				"remote_host_id": {"type": "string", "description": "Host having the issue — resolved into the envelope."},
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
