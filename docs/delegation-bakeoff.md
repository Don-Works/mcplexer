# Delegation Bakeoff: Cheap/Free Provider Evaluation

**Status**: Design doc — not yet implemented
**Created**: 2026-06-12
**Goal**: Systematically compare cheap/free AI providers for mcplexer worker delegation, so the delegation ranker can route work to the best cost/quality/speed provider.

---

## 1. Providers Under Test

### Tier 1 — Free / Subscription (no marginal cost)

| Provider | Adapter | Model(s) | Billing | Auth | Notes |
|----------|---------|----------|---------|------|-------|
| `gemini_cli` | `gemini_cli` (new) | `gemini-2.5-pro`, `gemini-2.5-flash` | Free tier (Google AI Studio) | OAuth / API key | Needs new adapter — shells out to `gemini` CLI like `grok_cli` pattern |
| `codex_cli` | `codex_cli` (new) | `o4-mini`, `codex-mini` | ChatGPT Plus/Pro subscription | OAuth | OpenAI's Codex CLI — `codex --quiet --json` |
| `grok_cli` | existing | `grok-4`, `grok-4-mini` | X Premium+ subscription | OAuth | Already wired — `grok --output-format json` |
| `mimo_cli` | existing | `mimo-v2.5-pro`, `mimo-v2.5-flash` | Metered (cheap) | API key via `mimo providers login` | Already wired — `mimo run --format json` |

### Tier 2 — Metered (cheap, via OpenRouter)

| Provider | Adapter | Model(s) | Billing | Cost (in/out per M) | Notes |
|----------|---------|----------|---------|---------------------|-------|
| `openai_compat` | existing | `minimax/minimax-m3` | Metered | $0.30 / $1.20 | Strong code, very cheap |
| `openai_compat` | existing | `deepseek/deepseek-v4-flash` | Metered | $0.10 / $0.20 | Cheapest serious model |
| `openai_compat` | existing | `qwen/qwen3.7-plus` | Metered | $0.40 / $1.60 | Good generalist |
| `openai_compat` | existing | `z-ai/glm-5.1` | Metered | $0.98 / $3.08 | Zhipu coding specialist |
| `openai_compat` | existing | `moonshotai/kimi-k2.6` | Metered | $0.68 / $3.41 | Good reasoning |
| `openai_compat` | existing | `qwen/qwen3-coder:free` | Free | $0 / $0 | Free tier, rate-limited |
| `openai_compat` | existing | `openai/gpt-oss-120b:free` | Free | $0 / $0 | Free tier, rate-limited |

### Frontier Baseline (quality ceiling, NOT delegation targets)

| Provider | Model | Cost (in/out per M) | Purpose |
|----------|-------|---------------------|---------|
| `anthropic` | `claude-sonnet-4-6` | $3.00 / $15.00 | Quality reference |
| `openai` | `gpt-5.4` | $2.50 / $15.00 | Quality reference |

---

## 2. Task Matrix

Five canonical task types that cover the delegation workload surface. Each task is designed to be self-contained (no repo context needed) so the bakeoff runs against a synthetic repo.

### Task 1: Simple Edit

```
Edit the file `src/utils.ts` and add a `debounce(fn, ms)` function that:
- Takes a function and a delay in milliseconds
- Returns a debounced version that only executes after ms of inactivity
- Properly handles `this` context
- Includes JSDoc

Write the complete file.
```

**Expected output**: Correct, compilable TypeScript with JSDoc.
**Success criteria**: Function compiles, handles edge cases, has JSDoc.

### Task 2: Code Review

```
Review this Go function for bugs, performance issues, and style violations:

```go
func processUsers(users []User) []Result {
    var results []Result
    for i := 0; i < len(users); i++ {
        if users[i].Active {
            r := Result{}
            r.Name = users[i].Name
            r.Score = users[i].Points * 1.5
            if r.Score > 100 {
                r.Grade = "A"
            } else if r.Score > 80 {
                r.Grade = "B"
            } else if r.Score > 60 {
                r.Grade = "C"
            } else {
                r.Grade = "D"
            }
            results = append(results, r)
        }
    }
    return results
}
```

Provide a numbered list of issues with severity (critical/medium/low) and a fixed version.
```

**Expected output**: Finds at least: nil-slice pattern, range-vs-index, magic numbers, potential overflow.
**Success criteria**: Identifies 3+ real issues, provides correct fix.

### Task 3: Bug Fix

```
This Python function is supposed to merge two sorted lists into one sorted list, but it has a bug. Find and fix it.

```python
def merge_sorted(a, b):
    result = []
    i, j = 0, 0
    while i < len(a) and j < len(b):
        if a[i] <= b[j]:
            result.append(a[i])
            i += 1
        else:
            result.append(b[j])
            j += 1
    result.extend(a[i:])
    return result
```

What's the bug? Write the corrected version and a test that proves it works.
```

**Expected output**: Identifies missing `result.extend(b[j:])`, provides fix + test.
**Success criteria**: Correctly identifies the bug, fix passes, test covers it.

### Task 4: Architecture Decision

```
I'm building a real-time chat application. Compare these three approaches for message delivery:

1. WebSocket with pub/sub (Redis)
2. Server-Sent Events (SSE) with HTTP POST for sending
3. gRPC bidirectional streaming

For each, describe: scalability ceiling, implementation complexity, browser compatibility, reconnection handling, and operational overhead.

Give a recommendation with a one-paragraph justification.
```

**Expected output**: Structured comparison covering all 5 axes, clear recommendation.
**Success criteria**: Covers all axes, recommendation is defensible, no factual errors.

### Task 5: Test Writing

```
Write comprehensive tests for this TypeScript function:

```typescript
export function parseDuration(s: string): number {
  const match = s.match(/^(\d+)(ms|s|m|h|d)$/);
  if (!match) throw new Error(`Invalid duration: ${s}`);
  const n = parseInt(match[1], 10);
  switch (match[2]) {
    case "ms": return n;
    case "s":  return n * 1000;
    case "m":  return n * 60 * 1000;
    case "h":  return n * 60 * 60 * 1000;
    case "d":  return n * 24 * 60 * 60 * 1000;
  }
}
```

Use a test framework of your choice. Cover: valid inputs for each unit, invalid inputs, edge cases (0, large numbers, missing unit).
```

**Expected output**: 10+ test cases covering all units, error cases, edge cases.
**Success criteria**: Tests are correct, comprehensive, runnable.

---

## 3. Measurement Framework

### 3.1 Metrics

| Metric | Type | Source | Description |
|--------|------|--------|-------------|
| `success` | bool | runner loop | Did the adapter return a non-empty response without error? |
| `wall_clock_ms` | int | `time.Since(start)` | End-to-end wall time from Send to response |
| `input_tokens` | int | adapter response | Tokens consumed by input |
| `output_tokens` | int | adapter response | Tokens produced |
| `cost_usd` | float64 | adapter or EstimateCostUSD | Dollar cost (0 for free/subscription) |
| `real_cost_usd` | float64 | `RealCostUSD()` | Out-of-pocket cost (0 for subscription) |
| `billing_model` | string | `ClassifyBilling()` | "metered", "subscription", "free" |
| `tool_calls` | int | runner dispatch count | Number of MCP tool calls made |
| `iterations` | int | loop counter | Model turns before completion |
| `quality_score` | float64 | LLM judge (see 3.2) | 0.0–1.0 quality assessment |
| `stop_reason` | string | adapter | "end_turn", "max_tokens", "tool_use", etc. |

### 3.2 Quality Scoring

Use a **frontier LLM judge** (Claude Sonnet 4.6 via `claude_cli` or `anthropic` API) to score each task output on a 0–10 rubric:

| Score | Meaning |
|-------|---------|
| 9–10 | Perfect or near-perfect. Would accept without changes. |
| 7–8 | Good. Minor issues that don't affect correctness. |
| 5–6 | Acceptable. Correct but with notable gaps in style/completeness. |
| 3–4 | Poor. Has correctness issues or misses key requirements. |
| 1–2 | Unacceptable. Wrong, incomplete, or incoherent. |
| 0 | Failed. No usable output. |

**Judge prompt template**:

```
You are evaluating an AI-generated response for a software engineering task.

TASK: {task_prompt}

EXPECTED: {task_expectations}

RESPONSE:
{model_output}

Score this response 0-10 on:
1. Correctness (does it work?)
2. Completeness (does it cover all requirements?)
3. Quality (style, clarity, best practices)

Return JSON: {"correctness": N, "completeness": N, "quality": N, "overall": N, "notes": "..."}
```

The `overall` score normalizes to 0.0–1.0 via `overall / 10`.

### 3.3 Composite Score

The delegation ranker needs a single number. Weighted formula:

```
delegation_score = (
    0.50 * quality_score +        // correctness is king
    0.25 * speed_score +           // wall clock (normalized: 1.0 = fastest, 0.0 = timeout)
    0.15 * cost_score +            // cost efficiency (1.0 = free, 0.0 = frontier-priced)
    0.10 * reliability_score       // success rate across runs
)
```

Where:
- `speed_score = 1.0 - (wall_clock_ms / max_wall_clock_ms)` (capped at 0)
- `cost_score = 1.0 - min(1.0, real_cost_usd / frontier_cost_usd)`
- `reliability_score = success_count / total_runs`

---

## 4. Automation: `mcplexer bakeoff`

### 4.1 Command Surface

New MCP tool: `mcplexer__bakeoff`

```
mcplexer__bakeoff({
    "action": "run",           // run | status | results | compare
    "providers": ["mimo_cli", "openai_compat/deepseek/deepseek-v4-flash"],
    "tasks": ["simple_edit", "code_review", "bug_fix", "architecture", "test_writing"],
    "runs_per_task": 3,        // each (provider, task) pair runs N times for reliability
    "timeout_sec": 120,        // per-run wall clock cap
    "concurrency": 4,          // parallel runs (bounded to avoid rate limits)
    "workspace_id": "",        // optional — run against a specific workspace
    "judge": true              // run LLM quality judge on outputs
})
```

**Actions**:

| Action | Description |
|--------|-------------|
| `run` | Execute the bakeoff matrix. Returns a `bakeoff_id`. |
| `status` | Poll a running bakeoff. Returns progress + partial results. |
| `results` | Full results for a completed `bakeoff_id`. |
| `compare` | Side-by-side comparison table for N providers. |

### 4.2 Implementation Architecture

```
cmd/mcplexer/bakeoff.go          — CLI entry: `mcplexer bakeoff run --providers ...`
internal/bakeoff/
    runner.go                     — orchestrates the matrix
    tasks.go                      — task definitions + expectations
    judge.go                      — LLM judge scoring
    results.go                    — result aggregation + comparison
    store.go                      — persistence (bakeoff_runs table)
```

**Execution flow**:

1. **Validate** — check that each provider has a working adapter (probe with a trivial call).
2. **Generate worktrees** — create N isolated git worktrees (one per concurrent provider).
3. **Dispatch** — for each `(provider, task, run_index)`, create a `Worker` with:
   - `model_provider`: the provider under test
   - `model_id`: the model under test
   - `prompt`: the task prompt (from `tasks.go`)
   - `caps`: `{max_iterations: 3, max_tool_calls: 10, max_wall_clock: timeout_sec}`
   - `exec_mode`: "autonomous" (no approval gates)
4. **Collect** — poll each worker run until terminal. Record all metrics from §3.1.
5. **Judge** — if `judge=true`, send each output + expectations to the LLM judge.
6. **Aggregate** — compute composite scores per §3.3. Write to `bakeoff_results` table.
7. **Report** — emit a comparison table + markdown summary.

### 4.3 Rate Limit Management

| Provider | Est. Rate Limit | Strategy |
|----------|-----------------|----------|
| `gemini_cli` | 10 RPM (free tier) | Semaphore: max 1 concurrent |
| `codex_cli` | 50 RPM (Plus) | Semaphore: max 4 concurrent |
| `grok_cli` | 20 RPM (Premium+) | Semaphore: max 2 concurrent |
| `mimo_cli` | 60 RPM | Semaphore: max 6 concurrent |
| `openai_compat` (paid) | varies by model | Semaphore: max 4 concurrent |
| `openai_compat` (free) | 1–5 RPM | Semaphore: max 1 concurrent, 20s backoff |

Global concurrency cap: 4 parallel runs (configurable). Each provider gets a sub-semaphore.

### 4.4 Isolation

Each run operates in its own **git worktree** (per `mcplexer-parallel-worktrees` pattern):
- Worktree path: `.claude/worktrees/bakeoff-<bakeoff_id>-<provider>-<task>-<run>`
- Branch: `bakeoff/<bakeoff_id>/<provider>/<task>/<run>`
- Cleaned up after results collected
- No shared state between runs

---

## 5. Storage Schema

### 5.1 `bakeoff_runs` table

```sql
CREATE TABLE bakeoff_runs (
    id              TEXT PRIMARY KEY,
    started_at      DATETIME NOT NULL,
    finished_at     DATETIME,
    config_json     TEXT NOT NULL,       -- full run config (providers, tasks, etc.)
    status          TEXT NOT NULL DEFAULT 'running',  -- running | complete | failed
    summary_json    TEXT                 -- aggregated results after completion
);
```

### 5.2 `bakeoff_results` table

```sql
CREATE TABLE bakeoff_results (
    id              TEXT PRIMARY KEY,
    bakeoff_id      TEXT NOT NULL REFERENCES bakeoff_runs(id),
    provider        TEXT NOT NULL,
    model_id        TEXT NOT NULL,
    task_name       TEXT NOT NULL,
    run_index       INTEGER NOT NULL,    -- 0-indexed run within (provider, task)
    worker_run_id   TEXT,                -- FK to worker_runs if using worker infra
    success         BOOLEAN NOT NULL,
    wall_clock_ms   INTEGER NOT NULL,
    input_tokens    INTEGER NOT NULL,
    output_tokens   INTEGER NOT NULL,
    cost_usd        REAL NOT NULL,
    real_cost_usd   REAL NOT NULL,
    billing_model   TEXT NOT NULL,
    tool_calls      INTEGER NOT NULL,
    iterations      INTEGER NOT NULL,
    stop_reason     TEXT,
    output_text     TEXT,                -- full model output for judge + review
    quality_score   REAL,                -- 0.0–1.0 from LLM judge
    quality_notes   TEXT,                -- judge's reasoning
    delegation_score REAL,               -- weighted composite
    error_text      TEXT,                -- if success=false
    created_at      DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_bakeoff_results_bakeoff ON bakeoff_results(bakeoff_id);
CREATE INDEX idx_bakeoff_results_provider ON bakeoff_results(provider, task_name);
```

### 5.3 Dashboard Surface

New dashboard page: `/dashboard/bakeoff`

- **Overview tile**: latest bakeoff date, winning provider, total cost
- **Comparison table**: providers as rows, tasks as columns, cells show composite score (color-coded: green > 0.7, yellow 0.4–0.7, red < 0.4)
- **Cost chart**: bar chart of `real_cost_usd` per provider per task
- **Speed chart**: bar chart of `wall_clock_ms` per provider per task
- **Quality chart**: bar chart of `quality_score` per provider per task
- **History**: past bakeoffs with diff view (how rankings changed)
- **Provider detail drill-down**: per-task breakdown with full output text

---

## 6. Cost Estimates

### Per-provider cost for one full bakeoff (5 tasks × 3 runs = 15 runs)

Assuming ~2K input tokens, ~1K output tokens per run (conservative for these simple tasks):

| Provider | Per-run cost | 15 runs | Billing model |
|----------|-------------|---------|---------------|
| `gemini_cli` (flash) | $0.00 | $0.00 | Free tier |
| `codex_cli` (o4-mini) | $0.00 | $0.00 | ChatGPT Plus subscription |
| `grok_cli` (grok-4-mini) | $0.00 | $0.00 | X Premium+ subscription |
| `mimo_cli` (mimo-v2.5-flash) | ~$0.003 | ~$0.05 | Metered |
| `openai_compat` deepseek-v4-flash | ~$0.0004 | ~$0.006 | Metered |
| `openai_compat` minimax-m3 | ~$0.002 | ~$0.03 | Metered |
| `openai_compat` qwen3.7-plus | ~$0.004 | ~$0.06 | Metered |
| `openai_compat` qwen3-coder:free | $0.00 | $0.00 | Free tier |
| `openai_compat` gpt-oss-120b:free | $0.00 | $0.00 | Free tier |
| **Judge** (claude-sonnet-4-6) | ~$0.05 | ~$0.75 | Metered |

**Total cost per full bakeoff**: ~$0.90 (mostly the judge).
**Without judge**: ~$0.15.
**Free-only run**: $0.00 (judge can use a free model with quality tradeoff).

---

## 7. New Adapters Required

### 7.1 `gemini_cli` adapter

Follows the `grok_cli` pattern exactly:

```go
// internal/models/gemini_cli.go
const (
    geminiCLIDefaultBinary = "gemini"
    geminiCLIProvider      = "gemini_cli"
)

type geminiCLIAdapter struct {
    binaryPath string
    modelID    string
    runner     geminiCLIRunner
}

func (a *geminiCLIAdapter) Send(ctx context.Context, req SendRequest) (*SendResponse, error) {
    // Shell out: gemini --model <model> --format json --prompt-file <tmpfile>
    // Parse JSON response
    // Return SendResponse
}
```

Env gate: `MCPLEXER_ALLOW_GEMINI_CLI=1`

### 7.2 `codex_cli` adapter

Follows the `opencode_cli` pattern:

```go
// internal/models/codex_cli.go
const (
    codexCLIDefaultBinary = "codex"
    codexCLIProvider      = "codex_cli"
)

type codexCLIAdapter struct {
    binaryPath string
    modelID    string
    runner     codexCLIRunner
}

func (a *codexCLIAdapter) Send(ctx context.Context, req SendRequest) (*SendResponse, error) {
    // Shell out: codex --quiet --json --model <model> < prompt
    // Parse NDJSON response (similar to opencode_cli)
    // Return SendResponse
}
```

Env gate: `MCPLEXER_ALLOW_CODEX_CLI=1`

---

## 8. Re-runnability

The bakeoff is designed to be re-runnable as providers evolve:

1. **Provider registry**: `bakeoff/providers.json` lists available providers + models + rate limits. Adding a new provider = adding a JSON entry + (optionally) a new adapter.
2. **Task registry**: `bakeoff/tasks.json` lists task prompts + expectations. Adding a task = adding a JSON entry.
3. **Versioned results**: every bakeoff run has a timestamped `bakeoff_id`. The dashboard shows historical trends.
4. **Config override**: `mcplexer bakeoff run --config bakeoff/config.json` loads a full config. CI can run this on a schedule.
5. **Diff mode**: `mcplexer bakeoff compare --ids <id1>,<id2>` shows how rankings shifted between two runs.

### Recommended re-run schedule

| Frequency | Purpose |
|-----------|---------|
| Weekly | Track provider quality drift |
| After provider announcement | New model release, price change |
| After adapter change | Verify adapter fix didn't regress |
| Before delegation ranker update | Validate new routing weights |

---

## 9. Acceptance Criteria

A completed bakeoff produces:

- [ ] `bakeoff_runs` row with `status=complete`
- [ ] `bakeoff_results` rows for every `(provider, task, run_index)` combination
- [ ] Each result has: `success`, `wall_clock_ms`, `input_tokens`, `output_tokens`, `cost_usd`, `quality_score`
- [ ] Composite `delegation_score` computed for every result
- [ ] Comparison table in the dashboard at `/dashboard/bakeoff`
- [ ] Markdown summary exported to `docs/bakeoff-results/<bakeoff_id>.md`
- [ ] Provider ranking: ordered list of providers by mean `delegation_score` across all tasks

---

## 10. Implementation Plan

| Phase | Effort | Deliverable |
|-------|--------|-------------|
| Phase 1 | 2d | `bakeoff/tasks.go` + `bakeoff/runner.go` — core matrix runner using existing worker infra |
| Phase 2 | 1d | `bakeoff/judge.go` — LLM judge scoring via `claude_cli` adapter |
| Phase 3 | 1d | `bakeoff/store.go` — SQLite tables + queries |
| Phase 4 | 2d | `gemini_cli` adapter + `codex_cli` adapter |
| Phase 5 | 1d | Dashboard `/dashboard/bakeoff` page |
| Phase 6 | 0.5d | `mcplexer bakeoff` CLI command |
| Phase 7 | 0.5d | `bakeoff/providers.json` + `bakeoff/tasks.json` registries |

**Total**: ~8d

---

## Appendix A: Quick-start (manual, no code)

Until the automated `mcplexer bakeoff` command exists, run manually:

```bash
# For each provider, create a worker and run it:
mcplexer__create_worker({
    name: "bakeoff-mimo-simple-edit",
    model_provider: "mimo_cli",
    model_id: "mimo-v2.5-pro",
    prompt: "<task 1 prompt>",
    exec_mode: "autonomous",
    max_iterations: 3,
    max_wall_clock: 120
})

mcplexer__run_now({id: "<worker_id>"})

# Then collect results from worker_runs
mcplexer__list_runs({worker_id: "<worker_id>"})
```

## Appendix B: Provider CLI Install Requirements

| Provider | Binary | Install | Auth |
|----------|--------|---------|------|
| `gemini_cli` | `gemini` | `npm i -g @anthropic-ai/gemini-cli` or equivalent | `gemini auth login` |
| `codex_cli` | `codex` | `npm i -g @openai/codex` | OpenAI API key or ChatGPT auth |
| `grok_cli` | `grok` | `brew install grok` or download | `grok login` |
| `mimo_cli` | `mimo` | `npm i -g @anthropic-ai/mimo` or `bun add -g mimo` | `mimo providers login` |
