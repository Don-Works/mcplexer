package control

import "github.com/don-works/mcplexer/internal/gateway"

func usageToolDefs() []gateway.Tool {
	return []gateway.Tool{
		{
			Name:        "get_usage_dashboard",
			Description: "Read the unified AI subscription allowance and observed-usage dashboard. Missing provider data is reported explicitly rather than treated as zero.",
			InputSchema: schema(props{
				"days": propInt("Observed local usage window in days (default 30, maximum 365)."),
			}, nil),
		},
		{
			Name:        "refresh_usage_dashboard",
			Description: "Refresh configured first-party usage sources and return the unified dashboard. Provider failures are isolated and returned as per-provider status rows.",
			InputSchema: schema(props{
				"days": propInt("Observed local usage window in days (default 30, maximum 365)."),
			}, nil),
		},
		{
			Name:        "configure_usage_source",
			Description: "Add or replace non-secret configuration for an AI usage source. API credentials stay in the encrypted auth-scope store; pass only auth_scope_id and the secret key name.",
			InputSchema: schema(props{
				"provider":       propStr("claude, codex, minimax, zai, grok, mimo, or openrouter."),
				"label":          propStr("Optional display label."),
				"plan":           propStr("Optional subscription plan label."),
				"harness":        propStr("OpenRouter attribution label, for example opencode or mimo."),
				"auth_scope_id":  propStr("AuthScope containing the provider token; never the token itself."),
				"secret_key":     propStr("Key name inside auth_scope_id containing the provider token."),
				"base_url":       propStr("Optional approved first-party HTTPS API root."),
				"limit":          map[string]any{"type": "number", "description": "Optional manual allowance limit."},
				"unit":           propStr("Manual limit unit: percent, requests, credits, usd, or tokens."),
				"window_label":   propStr("Optional manual allowance window label."),
				"window_minutes": propInt("Optional manual allowance duration in minutes."),
			}, []string{"provider"}),
		},
		{
			Name:        "remove_usage_source",
			Description: "Remove usage-source configuration. Omitting harness for openrouter removes every configured OpenRouter source.",
			InputSchema: schema(props{
				"provider": propStr("Provider to remove."),
				"harness":  propStr("Optional OpenRouter harness label to remove only that source."),
			}, []string{"provider"}),
		},
	}
}
