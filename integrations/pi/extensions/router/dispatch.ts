// router/dispatch.ts — dispatch a routed task through MCPlexer delegation.
//
// Uses the existing mcpx-shim / code-mode surface. Waits or polls for the
// delegate result. Returns the output text and delegation id.

import type { RankedCandidate, CapabilityBundle, RouteDecision, ShimRunner } from "./types.ts";

export interface DispatchResult {
  delegation_id: string | null;
  output: string;
  error: string | null;
}

/**
 * Dispatch a task to MCPlexer delegation with the chosen model and capabilities.
 *
 * @param runShim  Function that invokes the mcpx-shim (from the extension's runShim).
 * @param task     The user's original input text.
 * @param chosen   The ranked candidate to dispatch to.
 * @param bundle   Compiled capability bundle.
 * @param decision The classifier's route decision.
 * @param pollAttempts How many times to poll for completion (default: 60).
 * @param pollIntervalMs How long to wait between polls (default: 2000).
 */
export async function dispatch(
  runShim: ShimRunner,
  task: string,
  chosen: RankedCandidate,
  bundle: CapabilityBundle,
  decision: RouteDecision,
  pollAttempts = 60,
  pollIntervalMs = 2000,
): Promise<DispatchResult> {
  // Step 1: Create delegation
  const delegateArgs = {
    objective: task,
    task_kind: decision.task_kind,
    model_id: chosen.candidate.id,
    model_profile_id: chosen.candidate.profile,
    capability_preset: bundle.preset,
    capability_profile: bundle.profile,
    tool_allowlist_json: JSON.stringify(bundle.tool_allowlist),
    review_required: false,
    context: "none",
  };

  const createResult = await runShim("mcpx__execute_code", {
    code: `const r = mcpx.delegate_worker(${JSON.stringify(delegateArgs)}); print(JSON.stringify(r));`,
  });

  if (!createResult.ok) {
    return {
      delegation_id: null,
      output: "",
      error: `Delegation creation failed: ${createResult.text}`,
    };
  }

  // Parse delegation id from result
  let delegationId: string | null = null;
  try {
    const parsed = JSON.parse(createResult.text);
    delegationId = parsed?.delegation_id ?? parsed?.id ?? null;
  } catch {
    // If we can't parse the id, try to extract from text
    const match = createResult.text.match(/delegation_id["\s:]+([a-f0-9-]+)/i);
    if (match) delegationId = match[1];
  }

  if (!delegationId) {
    return {
      delegation_id: null,
      output: "",
      error: "Could not extract delegation ID from create response",
    };
  }

  // Step 2: Poll for completion
  for (let attempt = 0; attempt < pollAttempts; attempt++) {
    await sleep(pollIntervalMs);

    const pollResult = await runShim("mcpx__execute_code", {
      code: `const r = mcpx.list_delegations({id: ${JSON.stringify(delegationId)}}); print(JSON.stringify(r));`,
    });

    if (!pollResult.ok) continue;

    try {
      const status = JSON.parse(pollResult.text);
      const delegation = Array.isArray(status) ? status[0] : status;

      if (!delegation) continue;

      const state = delegation.status ?? delegation.state;
      if (state === "completed" || state === "success") {
        return {
          delegation_id: delegationId,
          output: delegation.output ?? delegation.result ?? delegation.text ?? "",
          error: null,
        };
      }
      if (state === "failed" || state === "error") {
        return {
          delegation_id: delegationId,
          output: "",
          error: delegation.error ?? "Delegation failed",
        };
      }
      if (state === "cancelled") {
        return {
          delegation_id: delegationId,
          output: "",
          error: "Delegation was cancelled",
        };
      }
      // Otherwise still running — continue polling
    } catch {
      // Parse error — continue polling
    }
  }

  return {
    delegation_id: delegationId,
    output: "",
    error: `Delegation timed out after ${pollAttempts * pollIntervalMs}ms`,
  };
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}
