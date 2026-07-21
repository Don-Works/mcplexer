// Strict local-model classification. The model emits task semantics only;
// trusted code chooses models and permissions afterwards.

import type { InputMeta, RouteDecision } from "./types.ts";
import { buildClassifierPrompt, parseClassifierOutput } from "./core.mjs";

export { buildClassifierPrompt, parseClassifierOutput } from "./core.mjs";

export type ClassifyFn = (prompt: string) => Promise<string>;

export async function classify(
  meta: InputMeta,
  completePrompt: ClassifyFn,
): Promise<RouteDecision | null> {
  try {
    const raw = await completePrompt(buildClassifierPrompt(meta.text));
    return parseClassifierOutput(raw);
  } catch {
    return null;
  }
}
