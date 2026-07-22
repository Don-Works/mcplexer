package gateway

import "encoding/json"

// usageSummaryToolDefinition declares mcpx__usage_summary — a read-only,
// non-admin view of AI-subscription allowance vs observed usage across every
// configured provider (Claude, Codex, Grok, MiniMax, Z.AI, MiMo, OpenRouter).
//
// It exists for delegation decisions: a model deciding whether to burn frontier
// quota or hand a job to a cheaper worker needs to see "how much of my Claude
// allowance is left?" without the admin CWD-gate the dashboard tools require.
//
// The read path is strictly cache-only (see handleUsageSummary): it never
// triggers a provider API/CLI refresh, so a delegated model cannot use it to
// drive external calls. Missing provider data is reported explicitly
// (allowance_missing / observed_missing) and must never be read as "0 remaining".
func usageSummaryToolDefinition() Tool {
	return Tool{
		Name: "mcpx__usage_summary",
		Description: "Read the CACHED AI-subscription usage dashboard: per-provider allowance/limit " +
			"windows, observed usage over the window, percent-used and remaining, for Claude, Codex, " +
			"Grok, MiniMax, Z.AI, MiMo and OpenRouter credits. Use this to make delegation decisions — " +
			"e.g. when frontier (Claude/Codex) quota is nearly burned, hand work to a cheaper worker. " +
			"Read-only and cache-only: it returns the last snapshot the dashboard assembled and NEVER " +
			"triggers a provider refresh or API call, so it is safe and free to poll. When a provider's " +
			"allowance or observed data is unavailable, its allowance_missing / observed_missing flag is " +
			"true — treat that as UNKNOWN, never as zero remaining. If no snapshot has been assembled yet " +
			"(available=false), open the dashboard to populate it; the numbers are not zero. Carries no " +
			"secrets — only allowance/spend numbers and provider labels.",
		InputSchema: json.RawMessage(`{"type": "object", "properties": {
			"days": {"type": "integer", "minimum": 1, "maximum": 365, "description": "Observed-usage window in days. Default 30, max 365."}
		}}`),
		Extras: withAnnotations(ToolAnnotations{
			Title:           "AI Usage Summary",
			ReadOnlyHint:    boolPtr(true),
			DestructiveHint: boolPtr(false),
			IdempotentHint:  boolPtr(true),
			OpenWorldHint:   boolPtr(false),
		}),
	}
}
