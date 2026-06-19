# Cheap Provider Profiles Cookbook

How to configure cheap and free LLM providers as mcplexer model profiles using
the `openai_compat` adapter. Every provider below speaks the OpenAI Chat
Completions wire format — mcplexer's `openai_compat` adapter handles them all.

**Last updated:** 2026-06-12

---

## Quick Reference

| Provider      | Endpoint                                  | Auth required | Cheapest model                    | ~Input / Output per 1M tokens |
|---------------|-------------------------------------------|---------------|-----------------------------------|-------------------------------|
| OpenRouter    | `https://openrouter.ai/api/v1`            | Yes           | `:free` tier models               | $0 / $0                      |
| OpenRouter    | `https://openrouter.ai/api/v1`            | Yes           | `deepseek/deepseek-v4-flash`      | $0.10 / $0.20                |
| Together.ai   | `https://api.together.xyz/v1`             | Yes           | `meta-llama/Llama-3.3-70B-Instruct-Turbo` | ~$0.20 / $0.20       |
| Groq          | `https://api.groq.com/openai/v1`          | Yes           | `llama-3.3-70b-versatile`         | ~$0.59 / $0.79               |
| Ollama (local)| `http://localhost:11434/v1`               | No            | Any downloaded model              | $0 / $0                      |
| LMStudio      | `http://localhost:1234/v1`                | No            | Any loaded model                  | $0 / $0                      |

### Cost comparison (100k input + 50k output tokens)

| Provider + Model                              | Estimated cost |
|-----------------------------------------------|----------------|
| Local (Ollama/LMStudio)                       | $0.00          |
| OpenRouter `:free` tier                       | $0.00          |
| OpenRouter `deepseek/deepseek-v4-flash`       | $0.02          |
| OpenRouter `deepseek/deepseek-chat`           | $0.08          |
| OpenRouter `google/gemini-2.0-flash`          | ~$0.03         |
| OpenRouter `qwen/qwen3.7-plus`               | $0.12          |
| Together.ai `Llama-3.3-70B-Instruct-Turbo`   | ~$0.03         |
| Groq `llama-3.3-70b-versatile`               | ~$0.09         |
| OpenRouter `minimax/minimax-m3`               | $0.12          |
| DeepSeek direct `deepseek-chat`               | $0.08          |
| *Reference: Claude Sonnet 4.6*                | *$1.05*        |
| *Reference: GPT-5.4-mini*                     | *$0.30*        |

---

## 1. OpenRouter

OpenRouter aggregates hundreds of models behind a single API. Best for
experimentation — cheapest paid models and free-tier options.

### Get an API key

1. Go to https://openrouter.ai and sign up.
2. Navigate to **Keys** and create a new API key.
3. Add credits (or use free-tier models that need no credits).

### Store the API key as a secret

From the mcplexer dashboard (http://localhost:3333) or via MCP:

```
secret__prompt:
  label: OPENROUTER_API_KEY
  reason: OpenRouter API access for cheap model delegation
```

Or add it directly to an auth scope via the dashboard under **Auth Scopes**.

### Create a model profile

Via the dashboard (**Workers → Model Profiles → New**) or via the REST API:

```bash
curl -X POST http://localhost:3333/api/model-profiles \
  -H 'Content-Type: application/json' \
  -d '{
    "name": "openrouter-cheap",
    "provider": "openai_compat",
    "endpoint_url": "https://openrouter.ai/api/v1",
    "secret_scope_id": "<your-auth-scope-id>",
    "known_models": [
      "google/gemini-2.0-flash",
      "meta-llama/llama-3.3-70b",
      "deepseek/deepseek-chat",
      "deepseek/deepseek-v4-flash",
      "deepseek/deepseek-v4-pro",
      "qwen/qwen3.7-plus",
      "minimax/minimax-m3",
      "nvidia/nemotron-3-ultra-550b-a55b:free",
      "qwen/qwen3-coder:free"
    ]
  }'
```

### Configure as a delegation worker

When delegating work, reference the profile:

```json
{
  "objective": "Search the codebase for all TODO comments",
  "model_profile_id": "<profile-id-from-above>",
  "model_id": "deepseek/deepseek-v4-flash"
}
```

Or let the delegation system pick from `known_models` automatically.

### Recommended models

| Model                                 | Use case                          | Notes                                      |
|---------------------------------------|-----------------------------------|--------------------------------------------|
| `deepseek/deepseek-v4-flash`         | Code tasks, fast iteration        | Cheapest capable model                     |
| `deepseek/deepseek-v4-pro`           | Harder code tasks                 | Strong reasoning, still cheap              |
| `google/gemini-2.0-flash`            | General tasks, summarisation      | Fast, good context window                  |
| `qwen/qwen3.7-plus`                  | Code + reasoning                  | Strong on code benchmarks                  |
| `qwen/qwen3-coder:free`              | Code tasks (free)                 | Free tier, rate-limited                    |
| `nvidia/nemotron-3-ultra-550b-a55b:free` | Heavy reasoning (free)        | Free tier, may be slow/queued              |
| `minimax/minimax-m3`                 | General delegation workhorse      | Very cheap, good tool-use support          |

### Limitations

- **Rate limits:** Free-tier models (`:free` suffix) are heavily rate-limited and may queue.
- **Context window:** Varies per model. OpenRouter lists each model's context on their /models page.
- **Quality:** Free-tier models are noticeably weaker on complex multi-step tasks. Use paid models for anything load-bearing.
- **Latency:** OpenRouter adds a routing hop. Expect ~100-300ms extra vs direct provider APIs.

---

## 2. Together.ai

Fast inference for open-source models. Competitive pricing, good throughput.

### Get an API key

1. Go to https://api.together.xyz and sign up.
2. Navigate to **API Keys** in settings.
3. Create a key. New accounts get $5 free credit.

### Store the API key

```
secret__prompt:
  label: TOGETHER_API_KEY
  reason: Together.ai API access for cheap delegation
```

### Create a model profile

```bash
curl -X POST http://localhost:3333/api/model-profiles \
  -H 'Content-Type: application/json' \
  -d '{
    "name": "together-llama",
    "provider": "openai_compat",
    "endpoint_url": "https://api.together.xyz/v1",
    "secret_scope_id": "<your-auth-scope-id>",
    "known_models": [
      "meta-llama/Llama-3.3-70B-Instruct-Turbo",
      "meta-llama/Meta-Llama-3.1-8B-Instruct-Turbo",
      "deepseek-ai/DeepSeek-V3",
      "Qwen/Qwen2.5-72B-Instruct-Turbo"
    ]
  }'
```

### Configure as a delegation worker

```json
{
  "objective": "Refactor the error handling in pkg/api",
  "model_profile_id": "<profile-id>",
  "model_id": "meta-llama/Llama-3.3-70B-Instruct-Turbo"
}
```

### Recommended models

| Model                                        | Use case                   | ~Price per 1M (in/out) |
|----------------------------------------------|----------------------------|------------------------|
| `meta-llama/Meta-Llama-3.1-8B-Instruct-Turbo` | Simple tasks, triage     | ~$0.02 / $0.02         |
| `meta-llama/Llama-3.3-70B-Instruct-Turbo`   | General delegation        | ~$0.20 / $0.20         |
| `Qwen/Qwen2.5-72B-Instruct-Turbo`           | Code-heavy tasks          | ~$0.30 / $0.30         |
| `deepseek-ai/DeepSeek-V3`                    | Reasoning + code          | ~$0.30 / $0.30         |

### Limitations

- **Rate limits:** Tiered by spend. Free-tier accounts are limited. Check https://api.together.xyz/settings/rate-limits.
- **Context window:** Most models support 8k-32k. Some support 128k (check model card).
- **Tool use:** Llama models have basic tool-use support. For complex multi-tool delegation, prefer DeepSeek or Qwen.
- **Latency:** Generally fast (Together optimises for throughput). Sub-second for most models.

---

## 3. Groq

Ultra-fast inference using custom LPU hardware. Best when speed matters more than model variety.

### Get an API key

1. Go to https://console.groq.com and sign up.
2. Navigate to **API Keys** and create one.
3. Free tier includes generous rate limits for experimentation.

### Store the API key

```
secret__prompt:
  label: GROQ_API_KEY
  reason: Groq API access for fast delegation
```

### Create a model profile

```bash
curl -X POST http://localhost:3333/api/model-profiles \
  -H 'Content-Type: application/json' \
  -d '{
    "name": "groq-fast",
    "provider": "openai_compat",
    "endpoint_url": "https://api.groq.com/openai/v1",
    "secret_scope_id": "<your-auth-scope-id>",
    "known_models": [
      "llama-3.3-70b-versatile",
      "llama-3.1-8b-instant",
      "mixtral-8x7b-32768",
      "gemma2-9b-it"
    ]
  }'
```

### Configure as a delegation worker

```json
{
  "objective": "Summarise the last 10 git commits",
  "model_profile_id": "<profile-id>",
  "model_id": "llama-3.3-70b-versatile"
}
```

### Recommended models

| Model                     | Use case                | ~Price per 1M (in/out) |
|---------------------------|-------------------------|------------------------|
| `llama-3.1-8b-instant`   | Fast triage, simple ops | ~$0.05 / $0.08         |
| `llama-3.3-70b-versatile`| General delegation      | ~$0.59 / $0.79         |
| `mixtral-8x7b-32768`     | Long-context tasks      | ~$0.24 / $0.24         |
| `gemma2-9b-it`            | Lightweight tasks       | ~$0.20 / $0.20         |

### Limitations

- **Rate limits:** Free tier: 30 requests/min for 70B models, higher for smaller. Paid tiers available.
- **Context window:** `llama-3.3-70b-versatile` supports 128k context. `mixtral` supports 32k.
- **Model variety:** Smaller catalogue than OpenRouter/Together. Primarily Llama + Mixtral.
- **Tool use:** Supported on `llama-3.3-70b-versatile` and `llama-3.1-8b-instant`. Quality varies.
- **Availability:** Popular models can hit capacity limits during peak hours.

---

## 4. Local (Ollama / LMStudio)

Zero-cost inference running on your own hardware. No API key needed.

### Ollama

1. Install: `brew install ollama` (macOS) or see https://ollama.ai.
2. Pull a model: `ollama pull llama3.3` or `ollama pull qwen2.5-coder`.
3. Ollama runs on `http://localhost:11434` by default.

### LMStudio

1. Download from https://lmstudio.ai.
2. Load a model in the UI.
3. Start the local server (defaults to `http://localhost:1234`).

### Create a model profile (Ollama)

```bash
curl -X POST http://localhost:3333/api/model-profiles \
  -H 'Content-Type: application/json' \
  -d '{
    "name": "ollama-local",
    "provider": "openai_compat",
    "endpoint_url": "http://localhost:11434/v1",
    "secret_scope_id": "",
    "known_models": [
      "llama3.3",
      "qwen2.5-coder",
      "deepseek-coder-v2",
      "mistral"
    ]
  }'
```

> **Note:** `secret_scope_id` can be empty for local endpoints — no auth is
> needed. The validator allows empty secrets for `openai_compat` when the
> endpoint is clearly localhost, but you may need to create a dummy auth scope
> if the API rejects an empty string. In that case, create an empty scope and
> reference its ID.

### Create a model profile (LMStudio)

```bash
curl -X POST http://localhost:3333/api/model-profiles \
  -H 'Content-Type: application/json' \
  -d '{
    "name": "lmstudio-local",
    "provider": "openai_compat",
    "endpoint_url": "http://localhost:1234/v1",
    "secret_scope_id": "",
    "known_models": [
      "llama-3.3-70b-instruct",
      "qwen2.5-coder-32b-instruct"
    ]
  }'
```

### Configure as a delegation worker

```json
{
  "objective": "Find all unused imports in the codebase",
  "model_profile_id": "<profile-id>",
  "model_id": "qwen2.5-coder"
}
```

### Recommended models (Ollama)

| Model              | Use case                   | VRAM needed |
|--------------------|----------------------------|-------------|
| `llama3.3`         | General tasks              | ~8 GB       |
| `qwen2.5-coder`    | Code tasks                 | ~8 GB       |
| `deepseek-coder-v2`| Code + reasoning           | ~16 GB      |
| `mistral`          | Fast general tasks         | ~8 GB       |

### Limitations

- **Speed:** Depends entirely on your hardware. Apple Silicon (M-series) is fast; CPU-only is slow.
- **Quality:** Local models are significantly weaker than frontier cloud models. Fine for simple, well-scoped tasks (grep, format, summarise). Not recommended for complex multi-step reasoning.
- **Context window:** Ollama defaults to 2k-8k context. Set `OLLAMA_NUM_CTX` higher (e.g. 32768) for delegation workloads.
- **Concurrency:** Ollama handles one request at a time by default. Set `OLLAMA_NUM_PARALLEL` for concurrent workers.
- **No streaming:** The `openai_compat` adapter does not stream — all output arrives in one response. Large outputs may time out (60s default HTTP client timeout).

---

## Using Profiles with Delegation

### Via `mcpx__delegate_worker`

```javascript
// Inside mcpx__execute_code
mcpx.delegate_worker({
  workspace_id: "<ws-id>",
  objective: "List all exported functions in pkg/models",
  model_profile_id: "<cheap-profile-id>",
  model_id: "deepseek/deepseek-v4-flash",
  max_tool_calls: 20,
  max_wall_clock_seconds: 120,
})
```

### Via the dashboard

1. Open http://localhost:3333
2. Go to **Workers → Model Profiles** and create the profile.
3. Go to **Workers → Delegations** and start a delegation, selecting the profile.

### Model selection modes

When you supply `model_candidates` in a delegation, the system can pick the
first available model based on capability tags or cost. This lets you define a
cascade: try the cheapest model first, fall back to a stronger one if it fails.

---

## Troubleshooting

| Problem | Cause | Fix |
|---------|-------|-----|
| `openai_compat requires EndpointURL` | Missing `endpoint_url` in profile | Add the full base URL (e.g. `https://openrouter.ai/api/v1`) |
| `openai_compat requires SecretScopeID` | No auth scope configured | Create an auth scope with the provider's API key, then reference its ID |
| `500: Unauthorized` | Wrong or expired API key | Regenerate key on provider dashboard, update the secret in mcplexer |
| `429: Rate limit exceeded` | Too many requests | Wait, upgrade tier, or use a different provider |
| `context deadline exceeded` | Request timed out (60s default) | Use a faster model, reduce prompt size, or increase timeout |
| `connection refused` on localhost | Ollama/LMStudio not running | Start the service first |
| Model returns empty `tool_calls` | Model doesn't support function calling | Switch to a model with tool-use support (DeepSeek, Qwen, Llama 3.3+) |
| Delegation cost shows $0 | Model not in pricing table | Add pricing entry or accept $0 for unmetered local models |
