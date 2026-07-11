// router/router.ts — main router orchestrator.
//
// Ties together classifier → ranker → capabilities → dispatch.
// Disabled by default. Toggle via /router on|off|status.
// Fails open to normal Pi on any error.

import type {
  RouteResult,
  RouterState,
  RouterConfig,
  InputMeta,
  ShimRunner,
} from "./types.ts";
import { classify } from "./classifier.ts";
import { rankCandidates } from "./ranker.ts";
import { compileCapabilities } from "./capabilities.ts";
import { dispatch } from "./dispatch.ts";
import { resolveCatalog, candidatesFromEnv } from "./catalog.ts";

/** LLM completion function for the classifier. */
export type CompleteFn = (prompt: string) => Promise<string>;

// --- Module state ---

let _enabled = false;
let _busy = false;
let _lastRoute: string | null = null;
let _config: RouterConfig | null = null;

/** Get current router state (for /router status). */
export function getRouterState(): RouterState {
  return {
    enabled: _enabled,
    busy: _busy,
    last_route: _lastRoute,
  };
}

/** Enable or disable the router. */
export function setRouterEnabled(enabled: boolean): void {
  _enabled = enabled;
}

/** Get or initialize the router config. */
export function getConfig(): RouterConfig {
  if (_config) return _config;

  const envCandidates = candidatesFromEnv();
  const candidates = resolveCatalog(envCandidates);

  _config = {
    enabled: false,
    candidates,
    confidence_threshold: 0.6,
    max_poll_attempts: 60,
    poll_interval_ms: 2000,
  };
  return _config;
}

/** Override config (for testing). */
export function setConfig(config: Partial<RouterConfig>): void {
  _config = { ...getConfig(), ...config };
}

/** Reset all state (for testing). */
export function resetRouter(): void {
  _enabled = false;
  _busy = false;
  _lastRoute = null;
  _config = null;
}

/**
 * Check whether input should be intercepted by the router.
 * Returns true if the router should attempt to classify this input.
 */
export function shouldIntercept(meta: InputMeta): boolean {
  // Router must be enabled
  if (!_enabled) return false;

  // Already processing a route — serialize
  if (_busy) return false;

  // Bypass: extension-origin input (prevent recursion)
  if (meta.origin === "extension") return false;

  // Bypass: slash commands (handled by Pi natively)
  if (meta.is_slash_command) return false;

  // Bypass: images (not supported initially)
  if (meta.has_images) return false;

  // Bypass: empty or whitespace-only input
  if (!meta.text.trim()) return false;

  return true;
}

/**
 * Format the route result as a Pi custom message with expandable metadata.
 */
export function formatRouteResult(result: RouteResult): string {
  const lines: string[] = [];

  // Main output
  if (result.output) {
    lines.push(result.output);
  } else {
    lines.push("(No output from delegation)");
  }

  // Route metadata — compact, expandable
  lines.push("");
  lines.push("---");
  lines.push(`**Route**: ${result.chosen.candidate.label} (score: ${result.chosen.score})`);
  lines.push(`**Task**: ${result.decision.task_kind} | ${result.decision.quality} quality | ${result.decision.worker_mode}`);
  lines.push(`**Reason**: ${result.decision.reason}`);

  if (result.delegation_id) {
    lines.push(`**Delegation**: \`${result.delegation_id}\``);
  }

  // Score breakdown
  const bd = result.chosen.breakdown;
  lines.push(
    `**Score**: prior=${bd.task_prior} review=${bd.review_boost} rel=${bd.reliability} lat=${bd.latency_score} cost=${bd.cost_score}`,
  );

  if (result.decision.risk !== "safe") {
    lines.push(`**Risk**: ${result.decision.risk}`);
  }

  return lines.join("\n");
}

/**
 * Format a busy response when the router is already processing a route.
 */
export function formatBusyResponse(): string {
  return "Router is busy processing a previous request. Please wait for it to complete, or use Ctrl+C to cancel.";
}

/**
 * Main routing entry point. Classifies input, ranks candidates, dispatches
 * to the best model via MCPlexer delegation. On any failure, returns null
 * so the caller can fail-open to normal Pi.
 *
 * @param input    The user's input text.
 * @param meta     Input metadata (images, slash commands, origin).
 * @param complete LLM function for the classifier.
 * @param runShim  Shim runner for MCPlexer tool calls.
 */
export async function route(
  input: string,
  meta: InputMeta,
  complete: CompleteFn,
  runShim: ShimRunner,
): Promise<RouteResult | null> {
  if (!shouldIntercept(meta)) return null;

  _busy = true;
  _lastRoute = null;

  try {
    // Step 1: Classify
    const decision = await classify(meta, complete);
    if (!decision) {
      // Classifier failed — fail open
      console.error("[mcplexer-router] classifier failed, falling through to normal Pi");
      return null;
    }

    // Step 2: If passthrough, let normal Pi handle it
    if (decision.action === "passthrough") {
      return null;
    }

    // Step 3: Rank candidates
    const config = getConfig();
    const ranked = rankCandidates(config.candidates, decision);
    if (ranked.length === 0) {
      console.error("[mcplexer-router] no eligible model candidates for this task");
      return null;
    }

    const chosen = ranked[0];

    // Step 4: Compile capabilities
    const bundle = compileCapabilities(decision);

    // Step 5: Dispatch
    const dispatchResult = await dispatch(
      runShim,
      input,
      chosen,
      bundle,
      decision,
      config.max_poll_attempts,
      config.poll_interval_ms,
    );

    if (dispatchResult.error) {
      console.error(`[mcplexer-router] dispatch error: ${dispatchResult.error}`);
      return null;
    }

    const result: RouteResult = {
      decision,
      chosen,
      delegation_id: dispatchResult.delegation_id,
      output: dispatchResult.output,
    };

    _lastRoute = chosen.candidate.id;
    return result;
  } catch (err) {
    console.error(`[mcplexer-router] unexpected error: ${err}`);
    return null;
  } finally {
    _busy = false;
  }
}
