// router/index.ts — public surface for the Pi MCPlexer router.
// Re-exports everything the extension needs.

export { classify, parseClassifierOutput } from "./classifier.ts";
export { rankCandidates } from "./ranker.ts";
export { compileCapabilities } from "./capabilities.ts";
export { dispatch } from "./dispatch.ts";
export { DEFAULT_CANDIDATES, resolveCatalog, candidatesFromEnv } from "./catalog.ts";
export {
  route,
  getRouterState,
  setRouterEnabled,
  getConfig,
  setConfig,
  resetRouter,
  shouldIntercept,
  formatRouteResult,
  formatBusyResponse,
} from "./router.ts";
export type { CompleteFn } from "./router.ts";
export type {
  RouteDecision,
  RouteAction,
  TaskKind,
  WorkerMode,
  ModelCandidate,
  CapabilityBundle,
  ScoreBreakdown,
  RankedCandidate,
  RouteResult,
  RouterState,
  RouterConfig,
  InputMeta,
  ShimRunner,
} from "./types.ts";
