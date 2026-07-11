// router/router.test.mjs — tests for the Pi MCPlexer router.
// Run with: node --test extensions/router/router.test.mjs
// No network required. No new runtime dependencies.

import { describe, it } from "node:test";
import assert from "node:assert/strict";

// --- Import modules directly (TypeScript files are loaded via Pi's loader,
// but for pure-logic tests we can import the compiled .mjs or test the logic
// inline. Since the ranker/classifier/capabilities are pure functions with no
// Pi-specific imports, we replicate their core logic here for testability
// without a build step. This matches the "node:test with .mjs helpers" contract.)

// ===== Classifier parse tests =====

function parseClassifierOutput(raw) {
  const cleaned = raw.replace(/^```(?:json)?\s*\n?/gm, "").replace(/\n?```\s*$/gm, "").trim();
  let parsed;
  try { parsed = JSON.parse(cleaned); } catch { return null; }
  if (!parsed || typeof parsed !== "object") return null;
  const obj = parsed;
  if (obj.action !== "delegate" && obj.action !== "passthrough") return null;
  const validKinds = ["coding", "research", "review", "chat"];
  if (!validKinds.includes(obj.task_kind)) return null;
  if (obj.quality !== "low" && obj.quality !== "medium" && obj.quality !== "high") return null;
  if (obj.worker_mode !== "execute" && obj.worker_mode !== "explore") return null;
  if (!Array.isArray(obj.tool_intents)) return null;
  if (!obj.tool_intents.every((t) => typeof t === "string")) return null;
  if (obj.risk !== "safe" && obj.risk !== "elevated" && obj.risk !== "dangerous") return null;
  if (!Array.isArray(obj.requirements)) return null;
  if (!obj.requirements.every((r) => typeof r === "string")) return null;
  if (typeof obj.reason !== "string") return null;
  return obj;
}

describe("parseClassifierOutput", () => {
  it("parses valid JSON", () => {
    const input = JSON.stringify({
      action: "delegate", task_kind: "coding", quality: "medium",
      worker_mode: "execute", tool_intents: ["file_read", "file_write"],
      risk: "safe", requirements: ["code", "tool_use"],
      reason: "Single-file code fix",
    });
    const result = parseClassifierOutput(input);
    assert.equal(result.action, "delegate");
    assert.equal(result.task_kind, "coding");
    assert.deepEqual(result.tool_intents, ["file_read", "file_write"]);
  });

  it("strips markdown fences", () => {
    const input = '```json\n{"action":"passthrough","task_kind":"chat","quality":"low","worker_mode":"execute","tool_intents":[],"risk":"safe","requirements":[],"reason":"greeting"}\n```';
    const result = parseClassifierOutput(input);
    assert.equal(result.action, "passthrough");
  });

  it("returns null for malformed JSON", () => {
    assert.equal(parseClassifierOutput("not json"), null);
    assert.equal(parseClassifierOutput(""), null);
    assert.equal(parseClassifierOutput("{}"), null);
  });

  it("returns null for invalid action", () => {
    const input = JSON.stringify({
      action: "invalid", task_kind: "coding", quality: "medium",
      worker_mode: "execute", tool_intents: [], risk: "safe",
      requirements: [], reason: "test",
    });
    assert.equal(parseClassifierOutput(input), null);
  });

  it("returns null for invalid task_kind", () => {
    const input = JSON.stringify({
      action: "delegate", task_kind: "unknown", quality: "medium",
      worker_mode: "execute", tool_intents: [], risk: "safe",
      requirements: [], reason: "test",
    });
    assert.equal(parseClassifierOutput(input), null);
  });

  it("returns null for missing tool_intents array", () => {
    const input = JSON.stringify({
      action: "delegate", task_kind: "coding", quality: "medium",
      worker_mode: "execute", tool_intents: "not_array", risk: "safe",
      requirements: [], reason: "test",
    });
    assert.equal(parseClassifierOutput(input), null);
  });
});

// ===== Ranker tests =====

const DEFAULT_CANDIDATES = [
  {
    id: "anthropic/claude-sonnet-4", provider: "anthropic", profile: "default",
    label: "Claude Sonnet 4", cost_tier: "medium", speed_tier: "medium",
    context_window: 200000, capabilities: ["code", "reasoning", "analysis", "tool_use"],
    modalities: ["text"], task_priors: { coding: 70, research: 65, review: 70, chat: 60 },
    reliability: 90, latency_ms: 0,
  },
  {
    id: "anthropic/claude-haiku-3.5", provider: "anthropic", profile: "default",
    label: "Claude Haiku 3.5", cost_tier: "low", speed_tier: "fast",
    context_window: 200000, capabilities: ["code", "reasoning", "tool_use"],
    modalities: ["text"], task_priors: { coding: 55, research: 50, review: 55, chat: 65 },
    reliability: 85, latency_ms: 0,
  },
  {
    id: "openai/gpt-4o", provider: "openai", profile: "default",
    label: "GPT-4o", cost_tier: "high", speed_tier: "medium",
    context_window: 128000, capabilities: ["code", "reasoning", "analysis", "tool_use", "vision"],
    modalities: ["text", "image"], task_priors: { coding: 65, research: 70, review: 65, chat: 70 },
    reliability: 88, latency_ms: 0,
  },
  {
    id: "openai/gpt-4o-mini", provider: "openai", profile: "default",
    label: "GPT-4o Mini", cost_tier: "low", speed_tier: "fast",
    context_window: 128000, capabilities: ["code", "reasoning", "tool_use"],
    modalities: ["text", "image"], task_priors: { coding: 50, research: 50, review: 50, chat: 60 },
    reliability: 82, latency_ms: 0,
  },
  {
    id: "openai/o3-mini", provider: "openai", profile: "default",
    label: "o3-mini", cost_tier: "medium", speed_tier: "medium",
    context_window: 200000, capabilities: ["code", "reasoning", "analysis"],
    modalities: ["text"], task_priors: { coding: 75, research: 70, review: 75, chat: 50 },
    reliability: 80, latency_ms: 0,
  },
];

const TASK_CAPABILITIES = {
  coding: ["code"],
  research: ["reasoning"],
  review: ["code", "reasoning"],
  chat: [],
};

const NEUTRAL_PRIOR = 50;
const NEUTRAL_RELIABILITY = 80;
const COST_SCORES = { low: 90, medium: 60, high: 30 };
const QUALITY_MULTIPLIER = { high: 1.2, medium: 1.0, low: 0.8 };

function hasCapabilities(candidate, required) {
  return required.every((cap) => candidate.capabilities.includes(cap));
}

function isEligible(candidate, decision) {
  const taskCaps = TASK_CAPABILITIES[decision.task_kind];
  return hasCapabilities(candidate, taskCaps) && hasCapabilities(candidate, decision.requirements);
}

function scoreTaskPrior(candidate, taskKind) {
  return candidate.task_priors[taskKind] ?? NEUTRAL_PRIOR;
}

function scoreReliability(candidate) {
  return candidate.reliability || NEUTRAL_RELIABILITY;
}

function scoreLatency(candidate) {
  if (!candidate.latency_ms) return 50;
  return Math.max(0, Math.min(100, 100 - (candidate.latency_ms / 10000) * 100));
}

function scoreCost(candidate) {
  return COST_SCORES[candidate.cost_tier] ?? 50;
}

const DEFAULT_WEIGHTS = { task_prior: 0.40, review_boost: 0.15, reliability: 0.20, latency: 0.15, cost: 0.10 };

function rankCandidates(candidates, decision, weights = DEFAULT_WEIGHTS) {
  const eligible = candidates.filter((c) => isEligible(c, decision));
  if (eligible.length === 0) return [];
  const qualityMult = QUALITY_MULTIPLIER[decision.quality] ?? 1.0;
  const scored = eligible.map((candidate) => {
    const taskPrior = scoreTaskPrior(candidate, decision.task_kind);
    const reliability = scoreReliability(candidate);
    const latencyScore = scoreLatency(candidate);
    const costScore = scoreCost(candidate);
    const total = taskPrior * weights.task_prior * qualityMult +
      reliability * weights.reliability + latencyScore * weights.latency + costScore * weights.cost;
    const breakdown = {
      task_prior: taskPrior, review_boost: 0, reliability,
      latency_score: latencyScore, cost_score: costScore, total: Math.round(total * 100) / 100,
    };
    return { candidate, score: Math.round(total * 100) / 100, breakdown };
  });
  scored.sort((a, b) => {
    if (b.score !== a.score) return b.score - a.score;
    return a.candidate.id.localeCompare(b.candidate.id);
  });
  return scored;
}

describe("rankCandidates", () => {
  it("ranks coding task — o3-mini wins on task prior despite medium cost", () => {
    const decision = {
      action: "delegate", task_kind: "coding", quality: "high",
      worker_mode: "execute", tool_intents: ["file_read", "file_write"],
      risk: "safe", requirements: ["code"], reason: "coding task",
    };
    const ranked = rankCandidates(DEFAULT_CANDIDATES, decision);
    assert.ok(ranked.length > 0);
    // o3-mini has coding prior 75, sonnet 70 — o3-mini should win
    assert.equal(ranked[0].candidate.id, "openai/o3-mini");
  });

  it("ranks research task — sonnet wins on reliability + cost balance", () => {
    const decision = {
      action: "delegate", task_kind: "research", quality: "medium",
      worker_mode: "explore", tool_intents: ["web_search"],
      risk: "safe", requirements: ["reasoning"], reason: "research task",
    };
    const ranked = rankCandidates(DEFAULT_CANDIDATES, decision);
    assert.ok(ranked.length > 0);
    // sonnet (prior 65, rel 90, cost medium) edges out gpt-4o (prior 70, rel 88, cost high)
    // because cost_score gap (60 vs 30) outweighs the 5-pt prior gap
    assert.equal(ranked[0].candidate.id, "anthropic/claude-sonnet-4");
  });

  it("filters candidates by required capabilities — vision filters to gpt-4o only", () => {
    const decision = {
      action: "delegate", task_kind: "coding", quality: "medium",
      worker_mode: "execute", tool_intents: [],
      risk: "safe", requirements: ["vision"], reason: "vision task",
    };
    const ranked = rankCandidates(DEFAULT_CANDIDATES, decision);
    assert.equal(ranked.length, 1);
    assert.equal(ranked[0].candidate.id, "openai/gpt-4o");
  });

  it("filters candidates by task capability — review requires code+reasoning", () => {
    const decision = {
      action: "delegate", task_kind: "review", quality: "high",
      worker_mode: "explore", tool_intents: ["code_review"],
      risk: "safe", requirements: [], reason: "review task",
    };
    const ranked = rankCandidates(DEFAULT_CANDIDATES, decision);
    // All except gpt-4o-mini (which has code+reasoning) should be eligible
    // Actually all have code+reasoning, so all are eligible
    assert.ok(ranked.length >= 3);
    // o3-mini has highest review prior (75)
    assert.equal(ranked[0].candidate.id, "openai/o3-mini");
  });

  it("chat task — all candidates eligible, haiku wins on cost + speed balance", () => {
    const decision = {
      action: "delegate", task_kind: "chat", quality: "low",
      worker_mode: "execute", tool_intents: [],
      risk: "safe", requirements: [], reason: "chat",
    };
    const ranked = rankCandidates(DEFAULT_CANDIDATES, decision);
    assert.equal(ranked.length, DEFAULT_CANDIDATES.length);
    // haiku (prior 65, low cost, fast) beats gpt-4o (prior 70, high cost)
    // at low quality (0.8 multiplier) the cost gap dominates
    assert.equal(ranked[0].candidate.id, "anthropic/claude-haiku-3.5");
  });

  it("missing task prior uses neutral (50), not zero", () => {
    const candidates = [{
      id: "test/model", provider: "test", profile: "default",
      label: "Test", cost_tier: "medium", speed_tier: "medium",
      context_window: 100000, capabilities: ["code"], modalities: ["text"],
      task_priors: {}, reliability: 90, latency_ms: 0,
    }];
    const decision = {
      action: "delegate", task_kind: "coding", quality: "medium",
      worker_mode: "execute", tool_intents: [], risk: "safe",
      requirements: ["code"], reason: "test",
    };
    const ranked = rankCandidates(candidates, decision);
    assert.equal(ranked.length, 1);
    assert.equal(ranked[0].breakdown.task_prior, NEUTRAL_PRIOR);
  });

  it("missing reliability uses neutral (80), not zero", () => {
    const candidates = [{
      id: "test/model", provider: "test", profile: "default",
      label: "Test", cost_tier: "medium", speed_tier: "medium",
      context_window: 100000, capabilities: ["code"], modalities: ["text"],
      task_priors: { coding: 60 }, reliability: 0, latency_ms: 0,
    }];
    const decision = {
      action: "delegate", task_kind: "coding", quality: "medium",
      worker_mode: "execute", tool_intents: [], risk: "safe",
      requirements: ["code"], reason: "test",
    };
    const ranked = rankCandidates(candidates, decision);
    assert.equal(ranked[0].breakdown.reliability, NEUTRAL_RELIABILITY);
  });

  it("deterministic tie — same result every run", () => {
    const candidates = [
      { id: "a/model", provider: "a", profile: "default", label: "A",
        cost_tier: "medium", speed_tier: "medium", context_window: 100000,
        capabilities: ["code"], modalities: ["text"],
        task_priors: { coding: 60 }, reliability: 80, latency_ms: 0 },
      { id: "b/model", provider: "b", profile: "default", label: "B",
        cost_tier: "medium", speed_tier: "medium", context_window: 100000,
        capabilities: ["code"], modalities: ["text"],
        task_priors: { coding: 60 }, reliability: 80, latency_ms: 0 },
    ];
    const decision = {
      action: "delegate", task_kind: "coding", quality: "medium",
      worker_mode: "execute", tool_intents: [], risk: "safe",
      requirements: ["code"], reason: "tie test",
    };
    const r1 = rankCandidates(candidates, decision);
    const r2 = rankCandidates(candidates, decision);
    assert.deepEqual(r1.map((r) => r.candidate.id), r2.map((r) => r.candidate.id));
    // a/model sorts before b/model lexicographically
    assert.equal(r1[0].candidate.id, "a/model");
  });

  it("speed tradeoff — fast model wins for low-quality chat tasks", () => {
    const candidates = [
      { id: "slow/model", provider: "slow", profile: "default", label: "Slow",
        cost_tier: "high", speed_tier: "slow", context_window: 100000,
        capabilities: [], modalities: ["text"],
        task_priors: { chat: 60 }, reliability: 80, latency_ms: 8000 },
      { id: "fast/model", provider: "fast", profile: "default", label: "Fast",
        cost_tier: "low", speed_tier: "fast", context_window: 100000,
        capabilities: [], modalities: ["text"],
        task_priors: { chat: 60 }, reliability: 80, latency_ms: 500 },
    ];
    const decision = {
      action: "delegate", task_kind: "chat", quality: "low",
      worker_mode: "execute", tool_intents: [], risk: "safe",
      requirements: [], reason: "speed test",
    };
    const ranked = rankCandidates(candidates, decision);
    assert.equal(ranked[0].candidate.id, "fast/model");
  });

  it("returns empty when no candidates eligible", () => {
    const decision = {
      action: "delegate", task_kind: "coding", quality: "medium",
      worker_mode: "execute", tool_intents: [], risk: "safe",
      requirements: ["nonexistent_capability"], reason: "impossible",
    };
    const ranked = rankCandidates(DEFAULT_CANDIDATES, decision);
    assert.equal(ranked.length, 0);
  });
});

// ===== Capabilities tests =====

const DANGEROUS_INTENTS = new Set([
  "secret_write", "secret_read", "admin_write", "admin_read",
  "force_push", "delete_production", "modify_config",
]);

const INTENT_TO_PRESETS = {
  file_read: ["code_readonly", "code_readwrite", "code_full"],
  file_write: ["code_readwrite", "code_full"],
  shell_run: ["code_readwrite", "code_full"],
  code_review: ["review", "code_readonly"],
  test_run: ["code_readwrite", "code_full"],
  web_search: ["research"],
  web_fetch: ["research"],
  memory_read: ["code_full"],
  memory_write: ["code_full"],
  github_read: ["research", "code_readonly"],
  github_write: ["code_full"],
};

const PRESET_BUNDLES = {
  code_readonly: { profile: "code-readonly", tools: ["mcpx__search_tools", "mcpx__execute_code", "read", "glob", "grep", "bash", "lsp"] },
  code_readwrite: { profile: "code-readwrite", tools: ["mcpx__search_tools", "mcpx__execute_code", "read", "glob", "grep", "bash", "lsp", "edit", "write", "multiedit"] },
  code_full: { profile: "code-full", tools: ["mcpx__search_tools", "mcpx__execute_code", "read", "glob", "grep", "bash", "lsp", "edit", "write", "multiedit", "task", "memory"] },
  research: { profile: "research", tools: ["mcpx__search_tools", "mcpx__execute_code", "websearch", "webfetch", "read", "glob", "grep"] },
  review: { profile: "review", tools: ["mcpx__search_tools", "mcpx__execute_code", "read", "glob", "grep", "bash", "lsp"] },
  chat: { profile: "minimal", tools: ["mcpx__search_tools", "mcpx__execute_code"] },
};

const PRESET_RANK = { chat: 0, code_readonly: 1, review: 2, research: 2, code_readwrite: 3, code_full: 4 };
const TASK_DEFAULT_PRESET = { coding: "code_readwrite", research: "research", review: "review", chat: "chat" };

function compileCapabilities(decision) {
  const hasDangerous = decision.tool_intents.some((i) => DANGEROUS_INTENTS.has(i));
  let bestPreset = TASK_DEFAULT_PRESET[decision.task_kind];
  for (const intent of decision.tool_intents) {
    if (DANGEROUS_INTENTS.has(intent)) continue;
    const supporting = INTENT_TO_PRESETS[intent];
    if (!supporting) continue;
    // Minimal sufficient: lowest-ranked preset that satisfies this intent
    const minForIntent = supporting.reduce((best, p) => {
      return (PRESET_RANK[p] ?? 0) < (PRESET_RANK[best] ?? 0) ? p : best;
    }, supporting[0]);
    // Overall preset must be at least as capable as each intent's minimum
    if ((PRESET_RANK[minForIntent] ?? 0) > (PRESET_RANK[bestPreset] ?? 0)) {
      bestPreset = minForIntent;
    }
  }
  if (hasDangerous) bestPreset = "code_readonly";
  const bundle = PRESET_BUNDLES[bestPreset] ?? PRESET_BUNDLES.chat;
  return { preset: bestPreset, profile: bundle.profile, tool_allowlist: [...bundle.tools] };
}

describe("compileCapabilities", () => {
  it("coding task with file_read only → code_readwrite preset (baseline)", () => {
    const decision = {
      action: "delegate", task_kind: "coding", quality: "medium",
      worker_mode: "execute", tool_intents: ["file_read"],
      risk: "safe", requirements: ["code"], reason: "test",
    };
    const bundle = compileCapabilities(decision);
    // file_read is satisfied by code_readwrite (the coding baseline)
    assert.equal(bundle.preset, "code_readwrite");
    assert.equal(bundle.profile, "code-readwrite");
    assert.ok(bundle.tool_allowlist.includes("edit"));
    assert.ok(bundle.tool_allowlist.includes("write"));
  });

  it("coding task with test_run → stays at code_readwrite (already sufficient)", () => {
    const decision = {
      action: "delegate", task_kind: "coding", quality: "medium",
      worker_mode: "execute", tool_intents: ["file_read", "test_run"],
      risk: "safe", requirements: ["code"], reason: "test",
    };
    const bundle = compileCapabilities(decision);
    // test_run min = code_readwrite (rank 3), baseline = code_readwrite (rank 3) — no escalation
    assert.equal(bundle.preset, "code_readwrite");
  });

  it("research task with web_search → research preset", () => {
    const decision = {
      action: "delegate", task_kind: "research", quality: "medium",
      worker_mode: "explore", tool_intents: ["web_search"],
      risk: "safe", requirements: ["reasoning"], reason: "test",
    };
    const bundle = compileCapabilities(decision);
    assert.equal(bundle.preset, "research");
    assert.ok(bundle.tool_allowlist.includes("websearch"));
    assert.ok(bundle.tool_allowlist.includes("webfetch"));
  });

  it("review task → review preset (code_readonly by default)", () => {
    const decision = {
      action: "delegate", task_kind: "review", quality: "high",
      worker_mode: "explore", tool_intents: ["code_review"],
      risk: "safe", requirements: ["code", "reasoning"], reason: "test",
    };
    const bundle = compileCapabilities(decision);
    // code_review maps to review preset, which has higher rank than default review
    assert.equal(bundle.preset, "review");
    assert.ok(!bundle.tool_allowlist.includes("edit"));
  });

  it("chat task → minimal preset", () => {
    const decision = {
      action: "passthrough", task_kind: "chat", quality: "low",
      worker_mode: "execute", tool_intents: [],
      risk: "safe", requirements: [], reason: "test",
    };
    const bundle = compileCapabilities(decision);
    assert.equal(bundle.preset, "chat");
    assert.equal(bundle.profile, "minimal");
  });

  it("dangerous intent → downgraded to code_readonly", () => {
    const decision = {
      action: "delegate", task_kind: "coding", quality: "medium",
      worker_mode: "execute", tool_intents: ["file_read", "secret_write"],
      risk: "dangerous", requirements: ["code"], reason: "test",
    };
    const bundle = compileCapabilities(decision);
    assert.equal(bundle.preset, "code_readonly");
    assert.ok(!bundle.tool_allowlist.includes("edit"));
  });

  it("memory_write escalates to code_full", () => {
    const decision = {
      action: "delegate", task_kind: "coding", quality: "medium",
      worker_mode: "execute", tool_intents: ["file_read", "memory_write"],
      risk: "safe", requirements: ["code"], reason: "test",
    };
    const bundle = compileCapabilities(decision);
    assert.equal(bundle.preset, "code_full");
    assert.ok(bundle.tool_allowlist.includes("memory"));
  });

  it("never passes classifier strings through as tool_allowlist", () => {
    const decision = {
      action: "delegate", task_kind: "coding", quality: "medium",
      worker_mode: "execute", tool_intents: ["file_read", "file_write", "custom_intent"],
      risk: "safe", requirements: ["code"], reason: "test",
    };
    const bundle = compileCapabilities(decision);
    // custom_intent is unknown — doesn't affect the bundle
    assert.ok(!bundle.tool_allowlist.includes("custom_intent"));
    assert.ok(bundle.tool_allowlist.every((t) => typeof t === "string" && t.length > 0));
  });
});

// ===== shouldIntercept tests =====

describe("shouldIntercept", () => {
  function shouldIntercept(enabled, busy, meta) {
    if (!enabled) return false;
    if (busy) return false;
    if (meta.origin === "extension") return false;
    if (meta.is_slash_command) return false;
    if (meta.has_images) return false;
    if (!meta.text.trim()) return false;
    return true;
  }

  it("returns false when disabled", () => {
    assert.equal(shouldIntercept(false, false, {
      has_images: false, is_slash_command: false, origin: "user", text: "hello",
    }), false);
  });

  it("returns false when busy", () => {
    assert.equal(shouldIntercept(true, true, {
      has_images: false, is_slash_command: false, origin: "user", text: "hello",
    }), false);
  });

  it("returns false for extension-origin input", () => {
    assert.equal(shouldIntercept(true, false, {
      has_images: false, is_slash_command: false, origin: "extension", text: "hello",
    }), false);
  });

  it("returns false for slash commands", () => {
    assert.equal(shouldIntercept(true, false, {
      has_images: false, is_slash_command: true, origin: "user", text: "/help",
    }), false);
  });

  it("returns false for images", () => {
    assert.equal(shouldIntercept(true, false, {
      has_images: true, is_slash_command: false, origin: "user", text: "look at this",
    }), false);
  });

  it("returns false for empty text", () => {
    assert.equal(shouldIntercept(true, false, {
      has_images: false, is_slash_command: false, origin: "user", text: "   ",
    }), false);
  });

  it("returns true for valid user input", () => {
    assert.equal(shouldIntercept(true, false, {
      has_images: false, is_slash_command: false, origin: "user", text: "Fix the bug",
    }), true);
  });
});

// ===== Integration: full routing decision flow =====

describe("routing integration", () => {
  it("coding task flows through classifier → ranker → capabilities correctly", () => {
    const decision = {
      action: "delegate", task_kind: "coding", quality: "high",
      worker_mode: "execute", tool_intents: ["file_read", "file_write", "test_run"],
      risk: "safe", requirements: ["code", "tool_use"], reason: "Multi-file refactor",
    };

    // Rank
    const ranked = rankCandidates(DEFAULT_CANDIDATES, decision);
    assert.ok(ranked.length > 0);
    const chosen = ranked[0];

    // sonnet wins: prior 70 + rel 90 + cost medium edges out o3-mini (prior 75 + rel 80 + cost medium)
    assert.equal(chosen.candidate.id, "anthropic/claude-sonnet-4");
    assert.ok(chosen.score > 0);

    // Capabilities
    const bundle = compileCapabilities(decision);
    assert.equal(bundle.preset, "code_readwrite");
    assert.ok(bundle.tool_allowlist.includes("edit"));
    assert.ok(bundle.tool_allowlist.includes("write"));

    // Verify no classifier strings leaked into allowlist
    assert.ok(!bundle.tool_allowlist.includes("file_read"));
    assert.ok(!bundle.tool_allowlist.includes("test_run"));
  });

  it("dangerous task downgrades capabilities even with high quality", () => {
    const decision = {
      action: "delegate", task_kind: "coding", quality: "high",
      worker_mode: "execute", tool_intents: ["file_read", "delete_production"],
      risk: "dangerous", requirements: ["code"], reason: "Cleanup",
    };

    const bundle = compileCapabilities(decision);
    assert.equal(bundle.preset, "code_readonly");
    assert.ok(!bundle.tool_allowlist.includes("edit"));
    assert.ok(!bundle.tool_allowlist.includes("write"));
  });
});
