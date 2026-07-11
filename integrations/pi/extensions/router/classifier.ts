// router/classifier.ts — local LLM-based input classifier.
//
// Returns a strict RouteDecision. The classifier NEVER selects raw model IDs,
// providers, namespaces, or tool globs — only structured routing signals.
// On failure, returns null so the caller can fail-open to normal Pi.

import type { RouteDecision, InputMeta, TaskKind } from "./types.ts";

const CLASSIFIER_SYSTEM = `You are a routing classifier for a coding assistant. Analyze the user's input and return a JSON object with EXACTLY these fields:

{
  "action": "delegate" | "passthrough",
  "task_kind": "coding" | "research" | "review" | "chat",
  "quality": "low" | "medium" | "high",
  "worker_mode": "execute" | "explore",
  "tool_intents": ["<intent>", ...],
  "risk": "safe" | "elevated" | "dangerous",
  "requirements": ["<requirement>", ...],
  "reason": "<one sentence>"
}

Rules:
- "delegate" when the task benefits from a specialized model or isolated execution.
- "passthrough" for trivial chat, greetings, or when no delegation adds value.
- "quality" reflects how demanding the task is (high = complex multi-file changes, low = simple lookups).
- "worker_mode": "execute" for writes/edits/runs, "explore" for read-only investigation.
- "tool_intents": describe what tools the task needs (e.g., "file_read", "file_write", "shell_run", "web_search", "code_review", "test_run", "memory_read", "memory_write", "github_read", "github_write").
- "risk": "dangerous" if the task involves deleting files, force-pushing, modifying production configs, or accessing secrets. "elevated" for writes outside the current project. "safe" otherwise.
- "requirements": capabilities the model MUST have (e.g., "code", "reasoning", "vision", "tool_use").
- Be concise. Return ONLY the JSON object, no markdown fences or explanation.`;

const FEW_SHOTS: Array<{ input: string; output: RouteDecision }> = [
  {
    input: "Fix the type error in src/parser.ts",
    output: {
      action: "delegate",
      task_kind: "coding",
      quality: "medium",
      worker_mode: "execute",
      tool_intents: ["file_read", "file_write", "test_run"],
      risk: "safe",
      requirements: ["code", "tool_use"],
      reason: "Single-file code fix requiring read/write/test",
    },
  },
  {
    input: "hello",
    output: {
      action: "passthrough",
      task_kind: "chat",
      quality: "low",
      worker_mode: "execute",
      tool_intents: [],
      risk: "safe",
      requirements: [],
      reason: "Trivial greeting, no delegation needed",
    },
  },
  {
    input: "Research the best approach for migrating from Express to Hono",
    output: {
      action: "delegate",
      task_kind: "research",
      quality: "medium",
      worker_mode: "explore",
      tool_intents: ["web_search", "file_read"],
      risk: "safe",
      requirements: ["reasoning", "analysis"],
      reason: "Multi-source research task benefiting from focused model",
    },
  },
  {
    input: "Review the auth middleware for security vulnerabilities",
    output: {
      action: "delegate",
      task_kind: "review",
      quality: "high",
      worker_mode: "explore",
      tool_intents: ["file_read", "code_review"],
      risk: "safe",
      requirements: ["code", "reasoning", "analysis"],
      reason: "Security review requiring deep code analysis",
    },
  },
  {
    input: "Delete all test fixtures and regenerate them",
    output: {
      action: "delegate",
      task_kind: "coding",
      quality: "medium",
      worker_mode: "execute",
      tool_intents: ["file_read", "file_write", "shell_run"],
      risk: "dangerous",
      requirements: ["code", "tool_use"],
      reason: "Destructive file operations require careful delegation",
    },
  },
];

/**
 * Build the classifier prompt from user input and metadata.
 */
function buildPrompt(meta: InputMeta): string {
  const parts = [`User input:\n${meta.text}`];
  if (meta.has_images) parts.push("[Input contains images]");
  if (meta.is_slash_command) parts.push("[Input is a slash command]");
  if (meta.origin === "extension") parts.push("[Input originated from an extension]");
  return parts.join("\n");
}

/**
 * Parse and validate the raw classifier output into a RouteDecision.
 * Returns null if the output is malformed or missing required fields.
 */
export function parseClassifierOutput(raw: string): RouteDecision | null {
  // Strip markdown fences if present
  const cleaned = raw.replace(/^```(?:json)?\s*\n?/gm, "").replace(/\n?```\s*$/gm, "").trim();

  let parsed: unknown;
  try {
    parsed = JSON.parse(cleaned);
  } catch {
    return null;
  }

  if (!parsed || typeof parsed !== "object") return null;
  const obj = parsed as Record<string, unknown>;

  // Validate action
  if (obj.action !== "delegate" && obj.action !== "passthrough") return null;

  // Validate task_kind
  const validKinds: TaskKind[] = ["coding", "research", "review", "chat"];
  if (!validKinds.includes(obj.task_kind as TaskKind)) return null;

  // Validate quality
  if (obj.quality !== "low" && obj.quality !== "medium" && obj.quality !== "high") return null;

  // Validate worker_mode
  if (obj.worker_mode !== "execute" && obj.worker_mode !== "explore") return null;

  // Validate tool_intents
  if (!Array.isArray(obj.tool_intents)) return null;
  if (!obj.tool_intents.every((t: unknown) => typeof t === "string")) return null;

  // Validate risk
  if (obj.risk !== "safe" && obj.risk !== "elevated" && obj.risk !== "dangerous") return null;

  // Validate requirements
  if (!Array.isArray(obj.requirements)) return null;
  if (!obj.requirements.every((r: unknown) => typeof r === "string")) return null;

  // Validate reason
  if (typeof obj.reason !== "string") return null;

  return {
    action: obj.action,
    task_kind: obj.task_kind,
    quality: obj.quality,
    worker_mode: obj.worker_mode,
    tool_intents: obj.tool_intents,
    risk: obj.risk,
    requirements: obj.requirements,
    reason: obj.reason,
  };
}

export interface ClassifyFn {
  (prompt: string): Promise<string>;
}

/**
 * Classify user input into a RouteDecision using the provided LLM function.
 * Returns null on any failure (parse error, API error, timeout) so the caller
 * can fail-open to normal Pi behavior.
 */
export async function classify(
  meta: InputMeta,
  complete: ClassifyFn,
): Promise<RouteDecision | null> {
  try {
    const prompt = buildPrompt(meta);

    // Build few-shot context
    const fewShotBlock = FEW_SHOTS.map(
      (s) => `Input: ${s.input}\nOutput: ${JSON.stringify(s.output)}`,
    ).join("\n\n");

    const fullPrompt = `${CLASSIFIER_SYSTEM}\n\nExamples:\n${fewShotBlock}\n\nNow classify:\n${prompt}`;

    const raw = await complete(fullPrompt);
    return parseClassifierOutput(raw);
  } catch {
    return null;
  }
}
