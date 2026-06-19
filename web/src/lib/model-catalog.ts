// Curated lists of model identifiers per provider. Used to populate the
// worker-form model picker and to seed the `known_models` field on a new
// ModelProfile. Free-text input remains supported everywhere — these
// are *suggestions*, not an allowlist.

export type ModelProvider =
  | 'anthropic'
  | 'openai'
  | 'openai_compat'
  | 'claude_cli'
  | 'opencode_cli'
  | 'grok_cli'
  | 'mimo_cli'
  | 'gemini_cli'
  | 'codex_cli'
  | 'pi_cli'

// Anthropic Messages API model IDs (direct API, with an API key).
// Includes both family aliases (`claude-*-latest`) and pinned versions.
// Pinned versions are the right default for production; aliases are for
// development convenience.
//
// Order matters beyond UX: delegation resolves a profile with no pinned
// model to `known_models[0]`, so the newest model per family sits first.
export const ANTHROPIC_API_MODELS: string[] = [
  // Current generation (latest first)
  'claude-fable-5',
  'claude-opus-4-8',
  'claude-sonnet-4-6',
  'claude-sonnet-4-6-20251115',
  'claude-haiku-4-5',
  'claude-haiku-4-5-20251001',
  // Family aliases
  'claude-fable-latest',
  'claude-opus-latest',
  'claude-sonnet-latest',
  'claude-haiku-latest',
  // Previous generation (still callable)
  'claude-opus-4-7',
  'claude-opus-4-6',
  'claude-sonnet-4-5',
  'claude-3-5-haiku-latest',
]

// `claude -p` (the subprocess provider) accepts a smaller set of names
// because the CLI itself maps them to API IDs. Aliases first (most
// commonly typed), full IDs second for users who want pinning.
export const CLAUDE_CLI_MODELS: string[] = [
  // Aliases
  'fable',
  'opus',
  'sonnet',
  'haiku',
  // Full IDs the CLI accepts via --model
  'claude-fable-5',
  'claude-opus-4-8',
  'claude-sonnet-4-6',
  'claude-haiku-4-5-20251001',
]

// OpenAI Chat Completions API. Includes reasoning models (`o*`) which
// don't accept `system` messages the same way — the runner adapter
// already handles that asymmetry.
export const OPENAI_MODELS: string[] = [
  // GPT-5 family (latest first)
  'gpt-5.5',
  'gpt-5.4',
  'gpt-5.4-mini',
  'gpt-5.4-nano',
  'gpt-5.3-codex',
  'gpt-5.1-codex-max',
  // Previous generation (still callable)
  'gpt-4o',
  'gpt-4o-mini',
  'o3-mini',
  'o1',
]

// OpenRouter's catalogue is huge — we ship the most-used picks as a
// starting suggestion list. Full catalogue is fetched live at runtime
// when the profile's `Provider = openai_compat` + endpoint is OpenRouter.
// Grouped: frontier, then discounted-but-strong (the delegation sweet
// spot), then zero-cost `:free` tiers (rate-limited, great for bulk and
// experimentation without touching the frontier budget).
export const OPENROUTER_HIGHLIGHTS: string[] = [
  // Frontier
  'anthropic/claude-fable-5',
  'anthropic/claude-opus-4.8',
  'anthropic/claude-sonnet-4.6',
  'openai/gpt-5.5',
  'openai/gpt-5.4',
  'google/gemini-3.5-flash',
  'x-ai/grok-4.3',
  // Discounted heavy hitters (<= ~$2/M output as of 2026-06)
  'minimax/minimax-m3',
  'deepseek/deepseek-v4-pro',
  'deepseek/deepseek-v4-flash',
  'z-ai/glm-5.1',
  'z-ai/glm-5',
  'qwen/qwen3.7-plus',
  'moonshotai/kimi-k2.6',
  // Free tiers ($0 per token; throughput-limited)
  'nvidia/nemotron-3-ultra-550b-a55b:free',
  'nvidia/nemotron-3-super-120b-a12b:free',
  'nex-agi/nex-n2-pro:free',
  'qwen/qwen3-coder:free',
  'openai/gpt-oss-120b:free',
]

// OpenCode CLI starter list — heavy hitters across the providers
// opencode supports. The full catalogue is fetched live from the daemon
// (/api/v1/opencode/models) when the profile/worker form needs it.
// Grouped: subscription-billed plans first (zero marginal cost), then
// opencode's free hosted models, then pay-per-token OpenRouter routes.
export const OPENCODE_CLI_HIGHLIGHTS: string[] = [
  // Subscription-billed (latest per plan first)
  'zai-coding-plan/glm-5.1',
  'minimax/MiniMax-M3',
  'opencode/big-pickle',
  // Free hosted by opencode
  'opencode/deepseek-v4-flash-free',
  'opencode/nemotron-3-ultra-free',
  'opencode/mimo-v2.5-free',
  'opencode/north-mini-code-free',
  // Pay-per-token via opencode's OpenRouter auth
  'openrouter/anthropic/claude-fable-5',
  'openrouter/anthropic/claude-opus-4.8',
  'openrouter/anthropic/claude-sonnet-4.6',
  'openrouter/minimax/minimax-m3',
  'openrouter/deepseek/deepseek-v4-pro',
  'openrouter/nvidia/nemotron-3-ultra-550b-a55b:free',
  'openrouter/qwen/qwen3-coder:free',
  'openrouter/openai/gpt-5.5',
]

// Grok CLI suggestions come from `grok models` on a logged-in CLI.
// Free-text stays open because xAI can change the available catalogue
// without a mcplexer release. Default model first.
export const GROK_CLI_MODELS: string[] = [
  'grok-composer-2.5-fast',
  'grok-build',
]

// Native Xiaomi MiMo / mimocode CLI suggestions (`mimo models`).
export const MIMO_CLI_MODELS: string[] = [
  'mimo/mimo-auto',
  'xiaomi/mimo-v2.5',
  'xiaomi/mimo-v2.5-pro',
  'xiaomi/mimo-v2.5-pro-ultraspeed',
  'xiaomi/mimo-v2-flash',
  'xiaomi/mimo-v2-pro',
  'xiaomi/mimo-v2-omni',
]

// Google Gemini CLI suggestions. Free-text stays open because the CLI's
// accepted model ids are controlled by the local install and account.
export const GEMINI_CLI_MODELS: string[] = [
  'gemini-3.5-pro',
  'gemini-3.5-flash',
  'gemini-2.5-pro',
  'gemini-2.5-flash',
]

// OpenAI Codex CLI suggestions. The codex CLI supports the same models
// as the OpenAI API. Free-text is always allowed.
export const CODEX_CLI_MODELS: string[] = [
  'o3',
  'o3-mini',
  'o4-mini',
  'gpt-4o',
  'gpt-4o-mini',
  'codex-mini',
]

// Pi CLI model ids are resolved by the host's ~/.pi/agent/models.json.
// These are examples only; operators should type the exact local alias.
export const PI_CLI_MODELS: string[] = [
  'qwen-local',
  'local-model',
]

export function defaultKnownModelsFor(provider: ModelProvider): string[] {
  switch (provider) {
    case 'anthropic':
      return ANTHROPIC_API_MODELS
    case 'openai':
      return OPENAI_MODELS
    case 'claude_cli':
      return CLAUDE_CLI_MODELS
    case 'opencode_cli':
      return OPENCODE_CLI_HIGHLIGHTS
    case 'grok_cli':
      return GROK_CLI_MODELS
    case 'mimo_cli':
      return MIMO_CLI_MODELS
    case 'gemini_cli':
      return GEMINI_CLI_MODELS
    case 'codex_cli':
      return CODEX_CLI_MODELS
    case 'pi_cli':
      return PI_CLI_MODELS
    case 'openai_compat':
      return OPENROUTER_HIGHLIGHTS
  }
}

// `humanProvider` returns a short user-facing label for a provider enum.
export function humanProvider(provider: ModelProvider): string {
  switch (provider) {
    case 'anthropic':
      return 'Anthropic'
    case 'openai':
      return 'OpenAI'
    case 'openai_compat':
      return 'OpenAI-compatible'
    case 'claude_cli':
      return 'Claude CLI'
    case 'opencode_cli':
      return 'OpenCode CLI'
    case 'grok_cli':
      return 'xAI Grok CLI'
    case 'mimo_cli':
      return 'Xiaomi MiMo CLI'
    case 'gemini_cli':
      return 'Google Gemini CLI'
    case 'codex_cli':
      return 'OpenAI Codex CLI'
    case 'pi_cli':
      return 'Pi CLI'
  }
}
