package control

import (
	"github.com/don-works/mcplexer/internal/gateway"
)

// spawnSubagentToolDef declares mcplexer__spawn_subagent — a one-call
// convenience that creates a single-shot Worker AND triggers its first
// run, intended for "concierge dispatches a heavier coding agent into
// a workspace" flows. Saves the caller from chaining create_worker →
// run_worker_now and provides safe defaults (manual schedule,
// autonomous exec, mesh output channel with reply_to_trigger so the
// sub-agent's reply chains back to the conversation that asked for it).
func spawnSubagentToolDef() gateway.Tool {
	return gateway.Tool{
		Name: "spawn_subagent",
		Description: "Create + immediately run a one-shot sub-agent Worker. " +
			"Use to delegate heavy lifting (coding, multi-file review, " +
			"large research) from a lightweight router worker (e.g. the " +
			"Telegram concierge) to a stronger model with its own workspace " +
			"context. The sub-agent runs asynchronously; the call returns " +
			"{worker_id, run_id, status:'running'} so the caller can poll " +
			"via mcplexer__get_worker_run or watch the mesh for its reply. " +
			"Defaults: schedule_spec='manual', exec_mode='autonomous', " +
			"output_channels=[mesh,reply_to_trigger=true]. " +
			"Sub-agents inherit mcplexer's sandbox (no writes to " +
			"~/.mcplexer or ~/.claude, no shell except via mcplexer tools) " +
			"so the host stays protected.",
		InputSchema: schema(props{
			"workspace_id": propStr("Workspace the sub-agent will run in. Its root_path becomes the subprocess CWD."),
			"name":         propStr("Worker name (must be unique within the workspace). Tip: include a short purpose suffix + a timestamp, e.g. 'code-task-truncation-1748213412'."),
			"prompt":       propStr("The full task instruction for the sub-agent. Will be set as prompt_template verbatim."),
			"model_provider": propStr("Model provider. Same model often works through multiple providers — " +
				"pick the one that matches the credentials you have. Options:\n" +
				"  • claude_cli     — Host's `claude` binary via OAuth subscription. " +
				"Models: any Claude (claude-opus-4-7, claude-sonnet-4-6, claude-haiku-4-5). " +
				"secret_scope_id NOT needed. Requires MCPLEXER_ALLOW_CLAUDE_CLI=1 on the daemon.\n" +
				"  • opencode_cli   — Host's `opencode` binary, routes via opencode's provider config. " +
				"Models: anything opencode supports — Claude (anthropic/*), GPT (openai/*), " +
				"MiniMax (minimax/MiniMax-M3), OpenRouter (openrouter/*), LM Studio, MLX, " +
				"gpt-oss, ~30 backends. secret_scope_id NOT needed (opencode reads its own creds). " +
				"Requires MCPLEXER_ALLOW_OPENCODE_CLI=1 on the daemon.\n" +
				"  • grok_cli       — Host's `grok` binary (xAI Grok Build). " +
				"Models: grok-build and any model accepted by `grok -m`. " +
				"secret_scope_id NOT needed (grok login / XAI_API_KEY owns auth). " +
				"Requires MCPLEXER_ALLOW_GROK_CLI=1 on the daemon.\n" +
				"  • mimo_cli       — Host's `mimo` binary (Xiaomi MiMo / mimocode). " +
				"Models: xiaomi/mimo-v2.5-pro (preferred), xiaomi/mimo-v2.5, mimo/mimo-auto. " +
				"secret_scope_id NOT needed (mimo providers login owns auth). " +
				"Requires MCPLEXER_ALLOW_MIMO_CLI=1 on the daemon.\n" +
				"  • pi_cli         — Host's `pi` binary (the Pi coding harness, pi.dev). " +
				"Models + providers are configured in ~/.pi/agent/models.json; pass the model id Pi resolves. " +
				"secret_scope_id NOT needed (Pi owns its own creds). " +
				"Requires MCPLEXER_ALLOW_PI_CLI=1 on the daemon.\n" +
				"  • anthropic      — Direct Anthropic API. Models: any Claude. " +
				"REQUIRES secret_scope_id holding ANTHROPIC_API_KEY.\n" +
				"  • openai         — Direct OpenAI API. Models: gpt-4o, gpt-4.1, o1, o3, etc. " +
				"REQUIRES secret_scope_id holding OPENAI_API_KEY.\n" +
				"  • openai_compat  — Any OpenAI-format endpoint (Groq, Together, Mistral, vLLM, " +
				"Ollama, MiniMax direct, custom proxies). Models: whatever the endpoint serves. " +
				"REQUIRES secret_scope_id holding both api_key AND base_url.\n" +
				"\n" +
				"Same-model examples: Claude → claude_cli (free, OAuth) OR anthropic (API key) " +
				"OR opencode_cli. MiniMax → opencode_cli (free, opencode's config) OR " +
				"openai_compat (point at their endpoint with your key)."),
			"model_id": propStr("Provider-specific model id. Examples by provider:\n" +
				"  • claude_cli:    claude-opus-4-7 | claude-sonnet-4-6 | claude-haiku-4-5\n" +
				"  • opencode_cli:  anthropic/claude-opus-4-7 | minimax/MiniMax-M3 | " +
				"openrouter/meta-llama/llama-3.3-70b | mlx/llama-3.2-3b\n" +
				"  • grok_cli:      grok-build | grok-composer-2.5-fast\n" +
				"  • mimo_cli:      xiaomi/mimo-v2.5-pro | xiaomi/mimo-v2.5 | mimo/mimo-auto\n" +
				"  • pi_cli:        whatever model id is registered in ~/.pi/agent/models.json\n" +
				"  • anthropic:     claude-opus-4-7 | claude-sonnet-4-6 | claude-haiku-4-5\n" +
				"  • openai:        gpt-4o | gpt-4.1 | o1 | o3-mini\n" +
				"  • openai_compat: whatever the endpoint advertises (e.g. MiniMax-M3, " +
				"llama-3.3-70b-versatile)"),
			"secret_scope_id": propStr("AuthScope id holding model credentials. " +
				"OPTIONAL for claude_cli / opencode_cli / grok_cli / mimo_cli / gemini_cli / codex_cli / pi_cli (gateway auto-picks a placeholder scope since " +
				"those providers read host-installed creds). " +
				"REQUIRED for anthropic / openai / openai_compat. " +
				"List candidate scopes with mcplexer__list_auth_scopes (admin-gated; CWD must be ⊆ ~/.mcplexer)."),
			"reply_to_trigger":       map[string]any{"type": "boolean", "description": "When true (default), the sub-agent's output is posted as a mesh reply to whatever triggered the calling worker. Set false for fire-and-forget tasks."},
			"max_wall_clock_seconds": propInt("Wall-clock cap (default 600s for sub-agents — they tend to need more headroom than chat-style workers)."),
			"max_tool_calls":         propInt("Tool-call cap (default 80)."),
			"tool_allowlist_json":    propStr("JSON array of tool-name globs the sub-agent may call. Default ['mcpx__execute_code','mcpx__search_tools','mesh__send','mesh__receive','memory__save','memory__recall','task__*']."),
			"trigger_message_id":     propStr("Optional: the mesh message that motivated this spawn. When called from inside an in-process worker run (e.g. the Telegram concierge), the gateway auto-inherits the parent run's trigger_message_id so you can usually omit this and reply_to_trigger=true will still chain the reply back correctly."),
		}, []string{"workspace_id", "name", "prompt", "model_provider", "model_id"}),
	}
}
