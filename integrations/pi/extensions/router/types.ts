// Shared TypeScript contracts for the Pi MCPlexer router.

export type RouteAction = "delegate" | "passthrough";
export type TaskKind =
  | "coding"
  | "research"
  | "review"
  | "architecture"
  | "tool_calling"
  | "general";
export type WorkerMode = "execute" | "review";
export type RouteQuality = "low" | "medium" | "high";
export type RouteRisk = "safe" | "elevated" | "dangerous";

export interface RouteDecision {
  action: RouteAction;
  task_kind: TaskKind;
  quality: RouteQuality;
  worker_mode: WorkerMode;
  tool_intents: string[];
  risk: RouteRisk;
  requirements: string[];
  reason: string;
}

export interface LiveModelEvidence {
  review_score?: number;
  review_count?: number;
  success_rate?: number;
  operational_success_rate?: number;
  avg_duration_ms?: number;
  cost_usd?: number;
  running?: number;
  accounting_known?: boolean;
  capacity_score?: number;
  exploring?: boolean;
  available?: boolean;
}

export interface ModelCandidate {
  id: string;
  provider: string;
  model_id: string;
  model_profile_id?: string;
  label: string;
  capabilities: string[];
  modalities: string[];
  context_window?: number;
  /** Operator quality prior for this workload, 0–100. */
  task_priors: Partial<Record<TaskKind, number>>;
  /** Evidence such as SWE-bench, mapped to the matching task kind, 0–100. */
  benchmark_scores: Partial<Record<TaskKind, number>>;
  /** Optional measured/static normalized evidence, 0–100. */
  speed_score?: number;
  cost_score?: number;
  operator_priority?: number;
  tags: string[];
  live: LiveModelEvidence;
}

export interface ScoreBreakdown {
  task_quality: number;
  review: number;
  reliability: number;
  speed: number;
  cost: number;
  load: number;
  priority: number;
  total: number;
}

export interface RankedCandidate {
  candidate: ModelCandidate;
  score: number;
  breakdown: ScoreBreakdown;
}

export interface DelegationCapabilityProfile {
  namespace_allow: string[];
  tool_allow: string[];
  features: {
    may_create_subdelegation: boolean;
    may_offer_tasks: boolean;
    may_use_mesh: boolean;
    may_use_secrets: boolean;
    may_write_memory: boolean;
    may_write_tasks: boolean;
  };
}

export interface CapabilityBundle {
  capability_preset?: "coder" | "researcher" | "minimal";
  capability_profile?: DelegationCapabilityProfile;
  tool_allowlist?: string[];
  worker_mode?: WorkerMode;
  blocked_reason?: string;
}

export interface DispatchResult {
  delegation_id: string | null;
  started: boolean;
  output: string;
  error: string | null;
  status: string;
}

export interface RouteResult {
  decision: RouteDecision;
  chosen: RankedCandidate;
  capabilities: CapabilityBundle;
  delegation_id: string | null;
  output: string;
  status: string;
  error?: string;
}

export interface RouterState {
  enabled: boolean;
  busy: boolean;
  last_route: string | null;
  last_delegation_id: string | null;
}

export interface RouterConfig {
  candidate_overrides: Array<Record<string, unknown>>;
  max_poll_attempts: number;
  poll_interval_ms: number;
}

export interface InputMeta {
  has_images: boolean;
  is_slash_command: boolean;
  origin: "interactive" | "rpc" | "extension";
  streaming: boolean;
  text: string;
}

export interface ShimResult {
  ok: boolean;
  text: string;
}

export type ShimRunner = (
  tool: string,
  args: unknown,
  signal?: AbortSignal,
) => Promise<ShimResult>;
