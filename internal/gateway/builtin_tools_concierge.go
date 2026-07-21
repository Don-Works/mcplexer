package gateway

import "encoding/json"

// conciergeToolDefinitions returns the concierge__* MCP tools — the
// thin agent-facing surface for the self-improving cross-channel chat
// subsystem. The concierge worker (a chat-fronting Worker like the
// Telegram concierge) calls these from inside its turn handler so
// every interaction leaves a structured signal behind.
//
// Today the surface is small (one tool, record_signal). Wider surfaces
// — recall last K signals, summarise frictions, surface A/B arms —
// live on the REST API; agents that need richer reads call those via
// the workspace's standard fetch path rather than expanding the MCP
// surface.
func conciergeToolDefinitions() []Tool {
	return []Tool{
		{
			Name:        "concierge__record_signal",
			Description: "Record one per-turn feedback signal for the self-improving chat loop. Call this AFTER you've produced an assistant reply, when the user replies to it — pass the user's reply as `user_message`, your prior reply as `assistant_message`, and the channel + worker context. The gateway runs a rule-based classifier (correction|frustration|confirmation|redirect|escalation|neutral) unless you supply an explicit `label`. Returns the persisted row, including the chosen label, so your next-turn prompt can adapt. The friction-extractor worker (B2) reads negative signals from this log + proposes prompt refinements; the A/B telemetry (B4) aggregates by prompt_version. Don't call this on the FIRST inbound from a user — there's no prior turn to score.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"worker_id":         {"type": "string", "description": "Worker id (wkr-...) producing the turn. Required."},
					"workspace_id":      {"type": "string", "description": "Workspace id the worker belongs to. Optional — caller may leave blank when the worker runs cross-workspace."},
					"user_id_external":  {"type": "string", "description": "Opaque per-channel user id, e.g. 'telegram:12345' or 'gchat:user@org.com'. Lets the per-user calibration layer (B5) scope memories."},
					"channel":           {"type": "string", "description": "Channel identifier: 'telegram', 'gchat', 'web', etc. Required."},
					"prompt_version":    {"type": "integer", "description": "Prompt template version active for this turn. Lets the A/B telemetry slice signals by arm. 0 = unknown."},
					"turn_id":           {"type": "string", "description": "Identifier of the turn being judged (worker_run id or mesh message id). Lets a friction proposal point back at exactly which assistant_message went wrong."},
					"user_message":      {"type": "string", "description": "The user's reply text. Required."},
					"assistant_message": {"type": "string", "description": "The prior assistant reply this signal judges. Highly recommended — the friction extractor reads this when proposing fixes."},
					"label":             {"type": "string", "enum": ["confirmation", "correction", "frustration", "redirect", "escalation", "neutral"], "description": "Explicit label override. Omit to let the rule-based classifier decide."}
				},
				"required": ["worker_id", "channel", "user_message"]
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "Record Chat Turn Signal",
				ReadOnlyHint:    boolPtr(false),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(false),
				OpenWorldHint:   boolPtr(false),
			}),
		},
	}
}
