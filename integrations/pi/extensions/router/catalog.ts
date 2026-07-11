// router/catalog.ts — default model-candidate catalog.
//
// Operators override via MCPLEXER_ROUTER_CANDIDATES (JSON) or extend at runtime.
// Priors are intentionally neutral (50) until real review/evidence accumulates.
// Speed/cost tiers gate eligibility; they do NOT encode quality.

import type { ModelCandidate } from "./types.ts";

export const DEFAULT_CANDIDATES: ModelCandidate[] = [
  {
    id: "anthropic/claude-sonnet-4",
    provider: "anthropic",
    profile: "default",
    label: "Claude Sonnet 4",
    cost_tier: "medium",
    speed_tier: "medium",
    context_window: 200_000,
    capabilities: ["code", "reasoning", "analysis", "tool_use"],
    modalities: ["text"],
    task_priors: { coding: 70, research: 65, review: 70, chat: 60 },
    reliability: 90,
    latency_ms: 0,
  },
  {
    id: "anthropic/claude-haiku-3.5",
    provider: "anthropic",
    profile: "default",
    label: "Claude Haiku 3.5",
    cost_tier: "low",
    speed_tier: "fast",
    context_window: 200_000,
    capabilities: ["code", "reasoning", "tool_use"],
    modalities: ["text"],
    task_priors: { coding: 55, research: 50, review: 55, chat: 65 },
    reliability: 85,
    latency_ms: 0,
  },
  {
    id: "openai/gpt-4o",
    provider: "openai",
    profile: "default",
    label: "GPT-4o",
    cost_tier: "high",
    speed_tier: "medium",
    context_window: 128_000,
    capabilities: ["code", "reasoning", "analysis", "tool_use", "vision"],
    modalities: ["text", "image"],
    task_priors: { coding: 65, research: 70, review: 65, chat: 70 },
    reliability: 88,
    latency_ms: 0,
  },
  {
    id: "openai/gpt-4o-mini",
    provider: "openai",
    profile: "default",
    label: "GPT-4o Mini",
    cost_tier: "low",
    speed_tier: "fast",
    context_window: 128_000,
    capabilities: ["code", "reasoning", "tool_use"],
    modalities: ["text", "image"],
    task_priors: { coding: 50, research: 50, review: 50, chat: 60 },
    reliability: 82,
    latency_ms: 0,
  },
  {
    id: "openai/o3-mini",
    provider: "openai",
    profile: "default",
    label: "o3-mini",
    cost_tier: "medium",
    speed_tier: "medium",
    context_window: 200_000,
    capabilities: ["code", "reasoning", "analysis"],
    modalities: ["text"],
    task_priors: { coding: 75, research: 70, review: 75, chat: 50 },
    reliability: 80,
    latency_ms: 0,
  },
];

/**
 * Merge operator overrides (from env or config) into the default catalog.
 * Operator entries are matched by `id`; unknown ids are appended.
 */
export function resolveCatalog(overrides: ModelCandidate[] | null): ModelCandidate[] {
  if (!overrides || overrides.length === 0) return DEFAULT_CANDIDATES;
  const byId = new Map(DEFAULT_CANDIDATES.map((c) => [c.id, c]));
  for (const o of overrides) byId.set(o.id, o);
  return [...byId.values()];
}

/** Try to parse candidates from env. Returns null if unset or unparseable. */
export function candidatesFromEnv(): ModelCandidate[] | null {
  const raw = process.env.MCPLEXER_ROUTER_CANDIDATES;
  if (!raw) return null;
  try {
    const parsed = JSON.parse(raw);
    return Array.isArray(parsed) ? parsed : null;
  } catch {
    return null;
  }
}
