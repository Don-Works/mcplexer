// router/capabilities.ts — compile classifier tool intents into MCPlexer
// capability bundles. NEVER passes classifier strings through as permissions;
// maps through trusted, curated bundles only.

import type { RouteDecision, CapabilityBundle, TaskKind } from "./types.ts";

/**
 * Trusted capability presets. Each preset maps to an MCPlexer capability_preset
 * that the gateway already knows about. The tool_allowlist is the explicit set
 * of MCP tools the delegation may use — not a glob, not a classifier string.
 */
const PRESET_BUNDLES: Record<string, { profile: string; tools: string[] }> = {
  code_readonly: {
    profile: "code-readonly",
    tools: [
      "mcpx__search_tools",
      "mcpx__execute_code",
      "read",
      "glob",
      "grep",
      "bash",
      "lsp",
    ],
  },
  code_readwrite: {
    profile: "code-readwrite",
    tools: [
      "mcpx__search_tools",
      "mcpx__execute_code",
      "read",
      "glob",
      "grep",
      "bash",
      "lsp",
      "edit",
      "write",
      "multiedit",
    ],
  },
  code_full: {
    profile: "code-full",
    tools: [
      "mcpx__search_tools",
      "mcpx__execute_code",
      "read",
      "glob",
      "grep",
      "bash",
      "lsp",
      "edit",
      "write",
      "multiedit",
      "task",
      "memory",
    ],
  },
  research: {
    profile: "research",
    tools: [
      "mcpx__search_tools",
      "mcpx__execute_code",
      "websearch",
      "webfetch",
      "read",
      "glob",
      "grep",
    ],
  },
  review: {
    profile: "review",
    tools: [
      "mcpx__search_tools",
      "mcpx__execute_code",
      "read",
      "glob",
      "grep",
      "bash",
      "lsp",
    ],
  },
  chat: {
    profile: "minimal",
    tools: ["mcpx__search_tools", "mcpx__execute_code"],
  },
};

/**
 * Dangerous tool intents that must NEVER be auto-granted.
 * If the classifier detects these, the bundle is downgraded to read-only
 * and the risk flag is set to "dangerous" regardless of classifier output.
 */
const DANGEROUS_INTENTS = new Set([
  "secret_write",
  "secret_read",
  "admin_write",
  "admin_read",
  "force_push",
  "delete_production",
  "modify_config",
]);

/**
 * Intent-to-capability mapping. Each tool intent maps to a set of presets
 * that satisfy it. The intersection of all required presets selects the
 * minimal sufficient bundle.
 */
const INTENT_TO_PRESETS: Record<string, string[]> = {
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

/** Preset ranking: higher index = more capable. */
const PRESET_RANK: Record<string, number> = {
  chat: 0,
  code_readonly: 1,
  review: 2,
  research: 2,
  code_readwrite: 3,
  code_full: 4,
};

/**
 * Map a task_kind to the default baseline preset.
 */
const TASK_DEFAULT_PRESET: Record<TaskKind, string> = {
  coding: "code_readwrite",
  research: "research",
  review: "review",
  chat: "chat",
};

/**
 * Compile a RouteDecision's tool intents into a CapabilityBundle.
 *
 * - Starts from the task_kind default preset.
 * - For each tool intent, finds the presets that support it.
 * - Picks the highest-ranked preset that satisfies all intents.
 * - If dangerous intents are detected, downgrades to code_readonly.
 * - Returns the explicit tool_allowlist (never a glob or classifier string).
 */
export function compileCapabilities(decision: RouteDecision): CapabilityBundle {
  // Check for dangerous intents
  const hasDangerous = decision.tool_intents.some((i) => DANGEROUS_INTENTS.has(i));

  // Start from the task default
  const baselinePreset = TASK_DEFAULT_PRESET[decision.task_kind];

  // Find the minimal preset that satisfies all tool intents
  let bestPreset = baselinePreset;

  for (const intent of decision.tool_intents) {
    if (DANGEROUS_INTENTS.has(intent)) continue; // handled by downgrade
    const supporting = INTENT_TO_PRESETS[intent];
    if (!supporting) continue; // unknown intent — use baseline
    // Pick the lowest-ranked preset that satisfies this intent (minimal sufficient).
    const minForIntent = supporting.reduce((best, p) => {
      const bestRank = PRESET_RANK[best] ?? 0;
      const pRank = PRESET_RANK[p] ?? 0;
      return pRank < bestRank ? p : best;
    }, supporting[0]);

    // The overall preset must be at least as capable as each intent's minimum.
    const currentRank = PRESET_RANK[bestPreset] ?? 0;
    const requiredRank = PRESET_RANK[minForIntent] ?? 0;
    if (requiredRank > currentRank) {
      bestPreset = minForIntent;
    }
  }

  // Downgrade if dangerous
  if (hasDangerous) {
    bestPreset = "code_readonly";
  }

  const bundle = PRESET_BUNDLES[bestPreset] ?? PRESET_BUNDLES.chat;

  return {
    preset: bestPreset,
    profile: bundle.profile,
    tool_allowlist: [...bundle.tools],
  };
}
