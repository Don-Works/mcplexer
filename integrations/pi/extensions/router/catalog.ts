// Build the candidate catalog from MCPlexer's live capacity rows, then overlay
// operator-supplied benchmark/capability/speed metadata.

import type { ModelCandidate, ShimRunner, TaskKind } from "./types.ts";
import {
  ROUTER_ROW_SENTINEL,
  capacityRowsToCandidates,
  parseCandidateOverrides,
  parseSentinelJSONLines,
} from "./core.mjs";

export { capacityRowsToCandidates, parseCandidateOverrides } from "./core.mjs";

export function candidatesFromEnv(): Array<Record<string, unknown>> {
  return parseCandidateOverrides(process.env.MCPLEXER_ROUTER_CANDIDATES ?? "");
}

export async function loadCandidates(
  runShim: ShimRunner,
  taskKind: TaskKind,
  overrides: Array<Record<string, unknown>>,
  signal?: AbortSignal,
): Promise<ModelCandidate[]> {
  const code = [
    `const result=mcpx.list_delegation_model_capacity({task_kind:${JSON.stringify(taskKind)},limit:100});`,
    "const rows=Array.isArray(result)?result:(result.models||result.capacity||result.items||[]);",
    "for(let n=0;n<rows.length;n+=1){const x=rows[n];",
    `print(${JSON.stringify(ROUTER_ROW_SENTINEL)}+JSON.stringify({model_provider:x.model_provider,model_id:x.model_id,model_profile_id:x.model_profile_id,label:x.label,review_score:x.review_score,review_count:x.review_count,success_rate:x.success_rate,operational_success_rate:x.operational_success_rate,avg_duration_ms:x.avg_duration_ms,cost_usd:x.cost_usd,running:x.running,accounting_known:x.accounting_known,capacity_score:x.capacity_score,exploring:x.exploring,available:x.available}));}`,
  ].join("\n");
  const response = await runShim("mcpx__execute_code", { code }, signal);
  if (!response.ok) {
    // Explicit overrides can still route during a capacity-read outage.
    return capacityRowsToCandidates([], overrides);
  }
  return capacityRowsToCandidates(
    parseSentinelJSONLines(response.text, ROUTER_ROW_SENTINEL),
    overrides,
  );
}
