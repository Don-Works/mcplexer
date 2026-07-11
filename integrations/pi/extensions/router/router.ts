// Stateful orchestration: classify -> live catalog -> rank -> capability
// policy -> MCPlexer delegation. Router mode remains opt-in and fail-open
// until a delegation has actually been created.

import type {
  InputMeta,
  RouteResult,
  RouterConfig,
  RouterState,
  ShimRunner,
} from "./types.ts";
import { classify } from "./classifier.ts";
import { loadCandidates, candidatesFromEnv } from "./catalog.ts";
import { rankCandidates } from "./ranker.ts";
import { compileCapabilities } from "./capabilities.ts";
import { dispatch } from "./dispatch.ts";
import { shouldInterceptInput } from "./core.mjs";

export type CompleteFn = (prompt: string) => Promise<string>;

let enabled = false;
let busy = false;
let lastRoute: string | null = null;
let lastDelegationId: string | null = null;
let config: RouterConfig | null = null;

function positiveInt(value: string | undefined, fallback: number): number {
  const parsed = Number(value);
  return Number.isInteger(parsed) && parsed > 0 ? parsed : fallback;
}

export function getRouterState(): RouterState {
  return {
    enabled,
    busy,
    last_route: lastRoute,
    last_delegation_id: lastDelegationId,
  };
}

export function setRouterEnabled(value: boolean): void {
  enabled = value;
}

export function getConfig(): RouterConfig {
  if (!config) {
    config = {
      candidate_overrides: candidatesFromEnv(),
      max_poll_attempts: positiveInt(process.env.MCPLEXER_ROUTER_POLL_ATTEMPTS, 180),
      poll_interval_ms: positiveInt(process.env.MCPLEXER_ROUTER_POLL_MS, 2000),
    };
  }
  return config;
}

export function setConfig(value: Partial<RouterConfig>): void {
  config = { ...getConfig(), ...value };
}

export function resetRouter(): void {
  enabled = false;
  busy = false;
  lastRoute = null;
  lastDelegationId = null;
  config = null;
}

export function shouldIntercept(meta: InputMeta): boolean {
  return shouldInterceptInput(enabled, busy, meta);
}

export function formatRouteResult(result: RouteResult): string {
  const bd = result.chosen.breakdown;
  const lines = [result.output || result.error || "Delegation completed without text output", "", "---"];
  lines.push(`Route: ${result.chosen.candidate.label} · score ${result.chosen.score}`);
  lines.push(`Task: ${result.decision.task_kind} · ${result.decision.quality} · ${result.decision.worker_mode}`);
  lines.push(`Why: ${result.decision.reason}`);
  lines.push(
    `Evidence: task=${bd.task_quality} review=${bd.review} reliability=${bd.reliability} ` +
    `speed=${bd.speed} cost=${bd.cost} load=${bd.load} priority=${bd.priority}`,
  );
  lines.push(`Capability: ${result.capabilities.capability_preset} · ${result.status}`);
  if (result.delegation_id) lines.push(`Delegation: ${result.delegation_id}`);
  if (result.error && result.output) lines.push(`Warning: ${result.error}`);
  return lines.join("\n");
}

export function formatBusyResponse(): string {
  return "Router is already waiting on a delegation. Wait for it to finish, or use normal Pi after /router off.";
}

export async function route(
  input: string,
  meta: InputMeta,
  completePrompt: CompleteFn,
  runShim: ShimRunner,
  signal?: AbortSignal,
): Promise<RouteResult | null> {
  if (!shouldIntercept(meta)) return null;
  busy = true;
  try {
    const decision = await classify(meta, completePrompt);
    if (!decision || decision.action === "passthrough") return null;

    const capabilityBundle = compileCapabilities(decision);
    if (capabilityBundle.blocked_reason) {
      throw new Error(capabilityBundle.blocked_reason);
    }

    const currentConfig = getConfig();
    const candidates = await loadCandidates(
      runShim,
      decision.task_kind,
      currentConfig.candidate_overrides,
      signal,
    );
    const ranked = rankCandidates(candidates, decision);
    if (ranked.length === 0) {
      throw new Error("no eligible configured MCPlexer delegation model");
    }
    const chosen = ranked[0];
    const dispatched = await dispatch(
      runShim,
      input,
      chosen,
      capabilityBundle,
      decision,
      currentConfig.max_poll_attempts,
      currentConfig.poll_interval_ms,
      signal,
    );

    // Before dispatch, fail-open is safe. After a delegation exists, surface
    // its state and do not let normal Pi duplicate the same work.
    if (dispatched.error && !dispatched.started) {
      throw new Error(dispatched.error);
    }

    lastRoute = chosen.candidate.id;
    lastDelegationId = dispatched.delegation_id;
    return {
      decision,
      chosen,
      capabilities: capabilityBundle,
      delegation_id: dispatched.delegation_id,
      output: dispatched.output,
      status: dispatched.status,
      error: dispatched.error ?? undefined,
    };
  } finally {
    busy = false;
  }
}
