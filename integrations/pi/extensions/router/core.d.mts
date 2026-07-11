import type {
  CapabilityBundle,
  ModelCandidate,
  RankedCandidate,
  RouteDecision,
  TaskKind,
} from "./types.ts";

export const ROUTER_SENTINEL: string;
export const ROUTER_ROW_SENTINEL: string;
export const ROUTER_META_SENTINEL: string;
export const ROUTER_OUTPUT_SENTINEL: string;
export const ROUTER_ERROR_SENTINEL: string;
export function parseClassifierOutput(raw: string): RouteDecision | null;
export function buildClassifierPrompt(text: string): string;
export function parseCandidateOverrides(raw: string | unknown): Array<Record<string, unknown>>;
export function capacityRowsToCandidates(
  rows: unknown,
  overrides?: Array<Record<string, unknown>>,
): ModelCandidate[];
export function rankCandidates(candidates: ModelCandidate[], decision: RouteDecision): RankedCandidate[];
export function compileCapabilities(decision: RouteDecision): CapabilityBundle;
export function shouldInterceptInput(enabled: boolean, busy: boolean, meta: unknown): boolean;
export function parseSentinelJSON(text: string): unknown;
export function parseSentinelJSONLines(text: string, prefix: string): unknown[];
export function parseDelegationPollText(text: string): {
  status: string;
  terminal: boolean;
  successful: boolean;
  output: string;
  error: string;
} | null;
export function delegationSnapshot(payload: unknown): {
  status: string;
  terminal: boolean;
  successful: boolean;
  output: string;
  error: string;
} | null;
