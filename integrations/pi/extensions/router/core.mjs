// Pure, dependency-free core for the Pi router. Pi's TypeScript extension and
// the Node 20 test suite both import this file, so tests exercise production
// parsing, ranking, and policy code rather than a copied implementation.

export const ROUTER_SENTINEL = "MCPLEXER_ROUTER_JSON:";
export const ROUTER_ROW_SENTINEL = "MCPLEXER_ROUTER_ROW:";
export const ROUTER_META_SENTINEL = "MCPLEXER_ROUTER_META:";
export const ROUTER_OUTPUT_SENTINEL = "MCPLEXER_ROUTER_OUTPUT:";
export const ROUTER_ERROR_SENTINEL = "MCPLEXER_ROUTER_ERROR:";

const TASK_KINDS = new Set([
  "coding",
  "research",
  "review",
  "architecture",
  "tool_calling",
  "general",
]);
const QUALITIES = new Set(["low", "medium", "high"]);
const WORKER_MODES = new Set(["execute", "review"]);
const RISKS = new Set(["safe", "elevated", "dangerous"]);
const REQUIREMENTS = new Set([
  "code",
  "reasoning",
  "analysis",
  "tool_use",
  "vision",
  "long_context",
  "workspace_tools",
]);
const TOOL_INTENTS = new Set([
  "repo_read",
  "repo_write",
  "tests_run",
  "web_read",
  "github_read",
  "github_write",
  "task_read",
  "task_write",
  "memory_read",
  "memory_write",
  "secrets",
  "admin",
  "deploy",
  "external_write",
  "destructive_write",
]);
const BLOCKED_INTENTS = new Set([
  "github_write",
  "secrets",
  "admin",
  "deploy",
  "external_write",
  "destructive_write",
]);
const DECISION_KEYS = new Set([
  "action",
  "task_kind",
  "quality",
  "worker_mode",
  "tool_intents",
  "risk",
  "requirements",
  "reason",
]);

const TASK_CAPABILITIES = {
  coding: ["code", "tool_use"],
  research: ["reasoning"],
  review: ["code", "reasoning"],
  architecture: ["reasoning", "analysis"],
  tool_calling: ["tool_use"],
  general: [],
};

const WEIGHTS = {
  high: { task_quality: 0.42, review: 0.25, reliability: 0.15, speed: 0.06, cost: 0.03, load: 0.04, priority: 0.05 },
  medium: { task_quality: 0.35, review: 0.20, reliability: 0.15, speed: 0.10, cost: 0.07, load: 0.05, priority: 0.08 },
  low: { task_quality: 0.20, review: 0.10, reliability: 0.12, speed: 0.25, cost: 0.15, load: 0.08, priority: 0.10 },
};

const BASE_TOOLS = [
  "mcpx__execute_code",
  "mcpx__search_tools",
  "mcpx__skill_search",
  "mcpx__skill_get",
];
const INDEX_READ_TOOLS = [
  "index__context",
  "index__symbols",
  "index__deps",
  "index__tests_for",
  "index__map_failure",
  "index__summary",
  "index__recent_changes",
  "index__status",
];
const TASK_READ_TOOLS = ["task__get", "task__list", "task__recent_activity"];
const TASK_WRITE_TOOLS = ["task__create", "task__update", "task__append_note"];
const MEMORY_READ_TOOLS = ["memory__recall", "memory__get", "memory__list"];
const MEMORY_WRITE_TOOLS = ["memory__save"];

function isRecord(value) {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function uniqueStrings(values) {
  return [...new Set(values.filter((value) => typeof value === "string" && value.length > 0))];
}

function boundedScore(value, fallback = 50) {
  if (typeof value !== "number" || !Number.isFinite(value)) return fallback;
  return Math.max(0, Math.min(100, value));
}

function stringArray(value, allowed) {
  if (!Array.isArray(value) || !value.every((item) => typeof item === "string")) return null;
  const result = uniqueStrings(value);
  return result.every((item) => allowed.has(item)) ? result : null;
}

export function parseClassifierOutput(raw) {
  if (typeof raw !== "string") return null;
  const cleaned = raw
    .trim()
    .replace(/^```(?:json)?\s*/i, "")
    .replace(/\s*```$/, "")
    .trim();

  let parsed;
  try {
    parsed = JSON.parse(cleaned);
  } catch {
    return null;
  }
  if (!isRecord(parsed)) return null;
  if (Object.keys(parsed).some((key) => !DECISION_KEYS.has(key))) return null;
  if (parsed.action !== "delegate" && parsed.action !== "passthrough") return null;
  if (!TASK_KINDS.has(parsed.task_kind)) return null;
  if (!QUALITIES.has(parsed.quality)) return null;
  if (!WORKER_MODES.has(parsed.worker_mode)) return null;
  if (!RISKS.has(parsed.risk)) return null;
  if (typeof parsed.reason !== "string" || parsed.reason.length === 0 || parsed.reason.length > 240) return null;

  const toolIntents = stringArray(parsed.tool_intents, TOOL_INTENTS);
  const requirements = stringArray(parsed.requirements, REQUIREMENTS);
  if (!toolIntents || !requirements) return null;

  return {
    action: parsed.action,
    task_kind: parsed.task_kind,
    quality: parsed.quality,
    worker_mode: parsed.worker_mode,
    tool_intents: toolIntents,
    risk: parsed.risk,
    requirements,
    reason: parsed.reason,
  };
}

export function buildClassifierPrompt(text) {
  return `You are the input router for a coding-agent shell. Return exactly one JSON object and no markdown.

Schema:
{"action":"delegate|passthrough","task_kind":"coding|research|review|architecture|tool_calling|general","quality":"low|medium|high","worker_mode":"execute|review","tool_intents":["repo_read|repo_write|tests_run|web_read|github_read|github_write|task_read|task_write|memory_read|memory_write|secrets|admin|deploy|external_write|destructive_write"],"risk":"safe|elevated|dangerous","requirements":["code|reasoning|analysis|tool_use|vision|long_context|workspace_tools"],"reason":"one short sentence"}

Rules:
- Delegate substantive execution, research, code review, architecture, and tool work.
- Passthrough greetings, simple conversation, clarification requests, and tasks needing direct human interaction.
- execute means the worker may change its isolated workspace; review means read/report only.
- Mark any secret, deployment, external mutation, production mutation, destructive action, or admin request elevated/dangerous.
- Describe capabilities and intents only. Never output provider names, model ids, namespaces, or tool globs.

Examples:
Input: Review auth middleware for races without editing.
Output: {"action":"delegate","task_kind":"review","quality":"high","worker_mode":"review","tool_intents":["repo_read","tests_run"],"risk":"safe","requirements":["code","reasoning","tool_use"],"reason":"Deep read-only code review with focused checks"}
Input: hello
Output: {"action":"passthrough","task_kind":"general","quality":"low","worker_mode":"review","tool_intents":[],"risk":"safe","requirements":[],"reason":"Conversation does not benefit from delegation"}

Input: ${text}`;
}

function defaultPriority(provider, modelId) {
  const key = `${provider}/${modelId}`.toLowerCase();
  if (key.includes("mimo-v2.5-pro")) return 96;
  if (key.includes("glm-5.2")) return 93;
  if (key.includes("minimax-m3")) return 90;
  if (key.includes("sonnet")) return 82;
  if (key.includes("haiku")) return 78;
  if (key.includes("qwen") && (key.includes("local") || key.includes("lmstudio"))) return 76;
  if (key.includes("opus") || key.includes("fable")) return 35;
  return 60;
}

function defaultCapabilities(provider, modelId) {
  const capabilities = ["code", "reasoning", "analysis", "tool_use"];
  if (provider.endsWith("_cli")) capabilities.push("workspace_tools");
  const lower = modelId.toLowerCase();
  if (lower.includes("omni") || lower.includes("vision")) capabilities.push("vision");
  return capabilities;
}

function overrideKey(value) {
  if (!isRecord(value)) return "";
  if (typeof value.id === "string") return value.id;
  if (typeof value.provider === "string" && typeof value.model_id === "string") {
    return `${value.provider}/${value.model_id}`;
  }
  return "";
}

export function parseCandidateOverrides(raw) {
  if (!raw) return [];
  let parsed;
  try {
    parsed = typeof raw === "string" ? JSON.parse(raw) : raw;
  } catch {
    return [];
  }
  if (!Array.isArray(parsed)) return [];
  return parsed.filter((entry) => isRecord(entry) && overrideKey(entry));
}

export function capacityRowsToCandidates(rows, overrides = []) {
  const safeRows = Array.isArray(rows) ? rows.filter(isRecord) : [];
  const byKey = new Map(overrides.map((entry) => [overrideKey(entry), entry]));
  const candidates = [];
  const seen = new Set();

  for (const row of safeRows) {
    const provider = typeof row.model_provider === "string" ? row.model_provider : "";
    const modelId = typeof row.model_id === "string" ? row.model_id : "";
    if (!provider || !modelId || row.available === false) continue;
    const id = `${provider}/${modelId}`;
    const override = byKey.get(id) ?? byKey.get(modelId) ?? {};
    const capabilities = Array.isArray(override.capabilities)
      ? uniqueStrings(override.capabilities)
      : defaultCapabilities(provider, modelId);
    const modalities = Array.isArray(override.modalities)
      ? uniqueStrings(override.modalities)
      : capabilities.includes("vision") ? ["text", "image"] : ["text"];

    candidates.push({
      id,
      provider,
      model_id: modelId,
      model_profile_id: typeof row.model_profile_id === "string" ? row.model_profile_id : undefined,
      label: typeof override.label === "string"
        ? override.label
        : typeof row.label === "string" ? `${row.label} · ${modelId}` : id,
      capabilities,
      modalities,
      context_window: typeof override.context_window === "number" ? override.context_window : undefined,
      task_priors: isRecord(override.task_priors) ? override.task_priors : {},
      benchmark_scores: isRecord(override.benchmark_scores) ? override.benchmark_scores : {},
      speed_score: typeof override.speed_score === "number" ? override.speed_score : undefined,
      cost_score: typeof override.cost_score === "number" ? override.cost_score : undefined,
      operator_priority: typeof override.operator_priority === "number"
        ? override.operator_priority
        : defaultPriority(provider, modelId),
      tags: Array.isArray(override.tags) ? uniqueStrings(override.tags) : [],
      live: {
        review_score: row.review_score,
        review_count: row.review_count,
        success_rate: row.success_rate,
        operational_success_rate: row.operational_success_rate,
        avg_duration_ms: row.avg_duration_ms,
        cost_usd: row.cost_usd,
        running: row.running,
        accounting_known: row.accounting_known,
        capacity_score: row.capacity_score,
        exploring: row.exploring,
        available: row.available,
      },
    });
    seen.add(id);
  }

  // Explicit overrides can add a candidate absent from the live capacity
  // response. This is useful for a new model, but requires provider+model_id.
  for (const override of overrides) {
    const id = overrideKey(override);
    if (seen.has(id) || typeof override.provider !== "string" || typeof override.model_id !== "string") continue;
    candidates.push({
      id,
      provider: override.provider,
      model_id: override.model_id,
      model_profile_id: typeof override.model_profile_id === "string" ? override.model_profile_id : undefined,
      label: typeof override.label === "string" ? override.label : id,
      capabilities: Array.isArray(override.capabilities) ? uniqueStrings(override.capabilities) : defaultCapabilities(override.provider, override.model_id),
      modalities: Array.isArray(override.modalities) ? uniqueStrings(override.modalities) : ["text"],
      context_window: override.context_window,
      task_priors: isRecord(override.task_priors) ? override.task_priors : {},
      benchmark_scores: isRecord(override.benchmark_scores) ? override.benchmark_scores : {},
      speed_score: override.speed_score,
      cost_score: override.cost_score,
      operator_priority: typeof override.operator_priority === "number" ? override.operator_priority : defaultPriority(override.provider, override.model_id),
      tags: Array.isArray(override.tags) ? uniqueStrings(override.tags) : [],
      live: {},
    });
  }
  return candidates;
}

function hasRequirement(candidate, requirement) {
  if (requirement === "vision") return candidate.modalities?.includes("image") || candidate.capabilities.includes("vision");
  if (requirement === "long_context") return typeof candidate.context_window === "number" && candidate.context_window >= 100_000;
  return candidate.capabilities.includes(requirement);
}

function eligible(candidate, decision) {
  if (candidate.live?.available === false) return false;
  if (
    decision.tool_intents.some((intent) => intent === "repo_write" || intent === "tests_run") &&
    !candidate.capabilities.includes("workspace_tools")
  ) return false;
  const implied = TASK_CAPABILITIES[decision.task_kind] ?? [];
  return [...implied, ...decision.requirements].every((requirement) => hasRequirement(candidate, requirement));
}

function taskQuality(candidate, taskKind) {
  const benchmark = candidate.benchmark_scores?.[taskKind];
  const prior = candidate.task_priors?.[taskKind];
  if (typeof benchmark === "number" && typeof prior === "number") {
    return boundedScore(benchmark * 0.7 + prior * 0.3);
  }
  if (typeof benchmark === "number") return boundedScore(benchmark);
  if (typeof prior === "number") return boundedScore(prior);
  return 50;
}

function reviewScore(candidate) {
  return Number(candidate.live?.review_count) > 0 ? boundedScore(candidate.live?.review_score) : 50;
}

function reliabilityScore(candidate) {
  const value = candidate.live?.operational_success_rate ?? candidate.live?.success_rate;
  if (typeof value !== "number" || !Number.isFinite(value)) return 50;
  return boundedScore(value <= 1 ? value * 100 : value);
}

function durationScore(durationMs) {
  if (typeof durationMs !== "number" || durationMs <= 0) return 50;
  // 1s≈100, 10s≈67, 100s≈34, 1000s≈0.
  return boundedScore(100 - Math.log10(Math.max(1000, durationMs) / 1000) * 33);
}

function speedScore(candidate) {
  if (typeof candidate.speed_score === "number") return boundedScore(candidate.speed_score);
  return durationScore(candidate.live?.avg_duration_ms);
}

function costScore(candidate) {
  if (typeof candidate.cost_score === "number") return boundedScore(candidate.cost_score);
  // Live capacity cost_usd is cumulative, not per run. Treat it as neutral so
  // established models are not penalized merely for having more history.
  return 50;
}

function loadScore(candidate) {
  const running = candidate.live?.running;
  if (typeof running !== "number" || running < 0) return 50;
  return boundedScore(100 / (1 + running));
}

export function rankCandidates(candidates, decision) {
  const weights = WEIGHTS[decision.quality] ?? WEIGHTS.medium;
  const ranked = candidates.filter((candidate) => eligible(candidate, decision)).map((candidate) => {
    const breakdown = {
      task_quality: taskQuality(candidate, decision.task_kind),
      review: reviewScore(candidate),
      reliability: reliabilityScore(candidate),
      speed: speedScore(candidate),
      cost: costScore(candidate),
      load: loadScore(candidate),
      priority: boundedScore(candidate.operator_priority),
    };
    const total = Object.entries(weights).reduce((sum, [key, weight]) => sum + breakdown[key] * weight, 0);
    const score = Math.round(total * 100) / 100;
    return { candidate, score, breakdown: { ...breakdown, total: score } };
  });
  ranked.sort((left, right) => right.score - left.score || left.candidate.id.localeCompare(right.candidate.id));
  return ranked;
}

function profileFeatures(overrides) {
  return {
    may_create_subdelegation: false,
    may_offer_tasks: false,
    may_use_mesh: false,
    may_use_secrets: false,
    may_write_memory: Boolean(overrides.may_write_memory),
    may_write_tasks: Boolean(overrides.may_write_tasks),
  };
}

export function compileCapabilities(decision) {
  const blockedIntent = decision.tool_intents.find((intent) => BLOCKED_INTENTS.has(intent));
  if (decision.risk !== "safe" || blockedIntent) {
    return {
      blocked_reason: blockedIntent
        ? `tool intent ${blockedIntent} requires direct human-controlled handling`
        : `risk level ${decision.risk} requires direct human-controlled handling`,
    };
  }

  const tools = [...BASE_TOOLS];
  const namespaces = ["mcpx"];
  let preset = decision.worker_mode === "execute" ? "coder" : "researcher";
  let mayWriteTasks = false;
  let mayWriteMemory = false;

  if (["coding", "review", "architecture"].includes(decision.task_kind) || decision.tool_intents.some((intent) => intent.startsWith("repo_") || intent === "tests_run")) {
    tools.push(...INDEX_READ_TOOLS);
    namespaces.push("index");
  }
  if (decision.tool_intents.includes("task_read") || decision.tool_intents.includes("task_write")) {
    tools.push(...TASK_READ_TOOLS);
    namespaces.push("task");
  }
  if (decision.tool_intents.includes("task_write") && decision.worker_mode === "execute") {
    tools.push(...TASK_WRITE_TOOLS);
    mayWriteTasks = true;
  }
  if (decision.tool_intents.includes("memory_read") || decision.tool_intents.includes("memory_write")) {
    tools.push(...MEMORY_READ_TOOLS);
    namespaces.push("memory");
  }
  if (decision.tool_intents.includes("memory_write") && decision.worker_mode === "execute") {
    tools.push(...MEMORY_WRITE_TOOLS);
    mayWriteMemory = true;
  }
  if (decision.tool_intents.includes("web_read")) {
    tools.push("fetch__fetch");
    namespaces.push("fetch");
    preset = "researcher";
  }
  if (decision.tool_intents.includes("github_read")) {
    tools.push("github__get_*", "github__list_*", "github__search_*");
    namespaces.push("github");
    preset = "researcher";
  }

  const toolAllow = uniqueStrings(tools);
  return {
    capability_preset: preset,
    capability_profile: {
      namespace_allow: uniqueStrings(namespaces),
      tool_allow: toolAllow,
      features: profileFeatures({ may_write_memory: mayWriteMemory, may_write_tasks: mayWriteTasks }),
    },
    tool_allowlist: toolAllow,
    worker_mode: decision.worker_mode,
  };
}

export function shouldInterceptInput(enabled, busy, meta) {
  return Boolean(enabled) &&
    !busy &&
    meta?.origin !== "extension" &&
    !meta?.streaming &&
    !meta?.is_slash_command &&
    !meta?.has_images &&
    typeof meta?.text === "string" &&
    meta.text.trim().length > 0;
}

export function parseSentinelJSON(text) {
  if (typeof text !== "string") return null;
  const line = text.split(/\r?\n/).find((entry) => entry.startsWith(ROUTER_SENTINEL));
  if (!line) return null;
  try {
    return JSON.parse(line.slice(ROUTER_SENTINEL.length));
  } catch {
    return null;
  }
}

export function parseSentinelJSONLines(text, prefix) {
  if (typeof text !== "string") return [];
  const values = [];
  for (const line of text.split(/\r?\n/)) {
    if (!line.startsWith(prefix)) continue;
    try {
      values.push(JSON.parse(line.slice(prefix.length)));
    } catch {
      // A malformed/truncated row is ignored; other complete rows remain usable.
    }
  }
  return values;
}

export function parseDelegationPollText(text) {
  const meta = parseSentinelJSONLines(text, ROUTER_META_SENTINEL)[0];
  if (!isRecord(meta)) return null;
  const status = typeof meta.status === "string" ? meta.status : "unknown";
  const outputs = parseSentinelJSONLines(text, ROUTER_OUTPUT_SENTINEL).filter((value) => typeof value === "string");
  const errors = parseSentinelJSONLines(text, ROUTER_ERROR_SENTINEL).filter((value) => typeof value === "string");
  const running = status === "running" || status === "dispatched";
  return {
    status,
    terminal: !running,
    successful: Number(meta.success ?? 0) > 0 && Number(meta.failure ?? 0) === 0,
    output: outputs.join(""),
    error: errors.join(""),
  };
}

export function delegationSnapshot(payload) {
  if (!isRecord(payload)) return null;
  const status = typeof payload.status === "string" ? payload.status : "unknown";
  const workers = Array.isArray(payload.workers) ? payload.workers : [];
  const outputs = [];
  const errors = [];
  for (const row of workers) {
    const run = isRecord(row) && isRecord(row.latest_run) ? row.latest_run : null;
    if (!run) continue;
    if (typeof run.output_text === "string" && run.output_text.trim()) outputs.push(run.output_text.trim());
    if (typeof run.error === "string" && run.error.trim()) errors.push(run.error.trim());
  }
  const running = status === "running" || status === "dispatched";
  const successCount = Number(payload.aggregate?.success ?? 0);
  const failureCount = Number(payload.aggregate?.failure ?? 0);
  return {
    status,
    terminal: !running,
    successful: successCount > 0 && failureCount === 0,
    output: outputs.join("\n\n---\n\n"),
    error: errors.join("; "),
  };
}
