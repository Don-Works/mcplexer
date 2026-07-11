// MCPlexer delegation dispatch and bounded polling.

import type {
  CapabilityBundle,
  DispatchResult,
  RankedCandidate,
  RouteDecision,
  ShimRunner,
} from "./types.ts";
import {
  ROUTER_ERROR_SENTINEL,
  ROUTER_META_SENTINEL,
  ROUTER_OUTPUT_SENTINEL,
  ROUTER_SENTINEL,
  parseDelegationPollText,
  parseSentinelJSON,
} from "./core.mjs";

function aborted(signal?: AbortSignal): boolean {
  return Boolean(signal?.aborted);
}

function wait(ms: number, signal?: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    if (signal?.aborted) {
      reject(new Error("aborted"));
      return;
    }
    const timer = setTimeout(resolve, ms);
    signal?.addEventListener("abort", () => {
      clearTimeout(timer);
      reject(new Error("aborted"));
    }, { once: true });
  });
}

export async function dispatch(
  runShim: ShimRunner,
  task: string,
  chosen: RankedCandidate,
  bundle: CapabilityBundle,
  decision: RouteDecision,
  pollAttempts = 180,
  pollIntervalMs = 2000,
  signal?: AbortSignal,
): Promise<DispatchResult> {
  if (
    bundle.blocked_reason ||
    !bundle.capability_preset ||
    !bundle.capability_profile ||
    !bundle.tool_allowlist ||
    !bundle.worker_mode
  ) {
    return {
      delegation_id: null,
      started: false,
      output: "",
      error: bundle.blocked_reason ?? "invalid capability bundle",
      status: "blocked",
    };
  }

  const args = {
    objective: task,
    handoff: `Pi router classification: ${decision.reason}`,
    task_kind: decision.task_kind,
    worker_mode: bundle.worker_mode,
    model_provider: chosen.candidate.provider,
    model_id: chosen.candidate.model_id,
    model_profile_id: chosen.candidate.model_profile_id,
    capability_preset: bundle.capability_preset,
    capability_profile: bundle.capability_profile,
    tool_allowlist_json: JSON.stringify(bundle.tool_allowlist),
    review_required: true,
    parallelism: 1,
    max_tool_calls: decision.quality === "high" ? 180 : 100,
    max_wall_clock_seconds: decision.quality === "high" ? 3600 : 1200,
  };
  const createCode = [
    `const result=mcpx.delegate_worker(${JSON.stringify(args)});`,
    `print(${JSON.stringify(ROUTER_SENTINEL)}+JSON.stringify({delegation_id:result.delegation_id}));`,
  ].join("\n");
  const created = await runShim("mcpx__execute_code", { code: createCode }, signal);
  if (!created.ok) {
    return {
      delegation_id: null,
      started: false,
      output: "",
      error: `delegation creation failed: ${created.text}`,
      status: "create_failed",
    };
  }

  const createPayload = parseSentinelJSON(created.text) as Record<string, unknown> | null;
  const delegationId = typeof createPayload?.delegation_id === "string"
    ? createPayload.delegation_id
    : null;
  if (!delegationId) {
    return {
      delegation_id: null,
      started: false,
      output: "",
      error: "delegation creation returned no delegation id",
      status: "create_failed",
    };
  }

  for (let attempt = 0; attempt < pollAttempts; attempt += 1) {
    if (aborted(signal)) break;
    try {
      await wait(pollIntervalMs, signal);
      const pollCode = [
        "const result=mcpx.list_delegations({limit:200});",
        "const rows=Array.isArray(result)?result:(result.delegations||result.items||[]);",
        `let found=null;for(let n=0;n<rows.length;n+=1){if(rows[n].id===${JSON.stringify(delegationId)}){found=rows[n];break;}}`,
        "const aggregate=found&&found.aggregate?found.aggregate:{};",
        `print(${JSON.stringify(ROUTER_META_SENTINEL)}+JSON.stringify({status:found?found.status:\"missing\",success:aggregate.success||0,failure:aggregate.failure||0}));`,
        "let remaining=16000;if(found&&Array.isArray(found.workers)){for(let w=0;w<found.workers.length&&remaining>0;w+=1){const run=found.workers[w].latest_run;if(!run)continue;const output=typeof run.output_text===\"string\"?run.output_text.slice(0,remaining):\"\";remaining-=output.length;for(let p=0;p<output.length;p+=300){",
        `print(${JSON.stringify(ROUTER_OUTPUT_SENTINEL)}+JSON.stringify(output.slice(p,p+300)));}`,
        "const error=typeof run.error===\"string\"?run.error.slice(0,2000):\"\";for(let p=0;p<error.length;p+=300){",
        `print(${JSON.stringify(ROUTER_ERROR_SENTINEL)}+JSON.stringify(error.slice(p,p+300)));}}}`,
      ].join("\n");
      const polled = await runShim("mcpx__execute_code", { code: pollCode }, signal);
      if (!polled.ok) continue;
      const snapshot = parseDelegationPollText(polled.text);
      if (!snapshot?.terminal) continue;
      if (snapshot.successful || snapshot.output) {
        return {
          delegation_id: delegationId,
          started: true,
          output: snapshot.output,
          error: snapshot.successful ? null : snapshot.error || `delegation ended as ${snapshot.status}`,
          status: snapshot.status,
        };
      }
      return {
        delegation_id: delegationId,
        started: true,
        output: "",
        error: snapshot.error || `delegation ended as ${snapshot.status}`,
        status: snapshot.status,
      };
    } catch {
      return {
        delegation_id: delegationId,
        started: true,
        output: "",
        error: aborted(signal)
          ? "delegation continues in MCPlexer after local wait was cancelled"
          : "delegation continues in MCPlexer after a polling error",
        status: aborted(signal) ? "cancelled_wait" : "poll_failed",
      };
    }
  }

  return {
    delegation_id: delegationId,
    started: true,
    output: "",
    error: aborted(signal)
      ? "delegation continues in MCPlexer after local wait was cancelled"
      : "delegation continues in MCPlexer after the router polling window elapsed",
    status: aborted(signal) ? "cancelled_wait" : "timed_out",
  };
}

export async function reviewDelegation(
  runShim: ShimRunner,
  delegationId: string,
  score: number,
  signal?: AbortSignal,
): Promise<{ ok: boolean; text: string }> {
  const bounded = Math.max(0, Math.min(100, Math.round(score)));
  const outcome = bounded >= 80 ? "accepted" : bounded >= 50 ? "partial" : "rejected";
  const code = [
    `const result=mcpx.review_delegation({delegation_id:${JSON.stringify(delegationId)},score:${bounded},outcome:${JSON.stringify(outcome)},notes:"Pi router user score"});`,
    `print(${JSON.stringify(ROUTER_SENTINEL)}+JSON.stringify({reviewed:result.reviewed!==false}));`,
  ].join("\n");
  const response = await runShim("mcpx__execute_code", { code }, signal);
  return { ok: response.ok && parseSentinelJSON(response.text) !== null, text: response.text };
}
