// router/types.ts — shared types for the Pi MCPlexer router.

/** What the classifier decided to do with the input. */
export type RouteAction = "delegate" | "passthrough";

/** Coarse task categories the classifier may emit. */
export type TaskKind = "coding" | "research" | "review" | "chat";

/** Worker dispatch mode passed to MCPlexer delegation. */
export type WorkerMode = "execute" | "explore";

/**
 * Strict output of the local classifier. Contains NO raw model IDs, providers,
 * namespaces, or tool globs — only structured routing signals that downstream
 * code compiles into explicit MCPlexer parameters.
 */
export interface RouteDecision {
  action: RouteAction;
  task_kind: TaskKind;
  quality: "low" | "medium" | "high";
  worker_mode: WorkerMode;
  tool_intents: string[];
  risk: "safe" | "elevated" | "dangerous";
  requirements: string[];
  reason: string;
}

/** Metadata for a model candidate in the operator-configurable catalog. */
export interface ModelCandidate {
  id: string;
  provider: string;
  profile: string;
  label: string;
  cost_tier: "low" | "medium" | "high";
  speed_tier: "fast" | "medium" | "slow";
  context_window: number;
  capabilities: string[];
  modalities: string[];
  /** Task-specific quality priors (0–100). Operator-configurable. */
  task_priors: Partial<Record<TaskKind, number>>;
  /** Reliability score 0–100 from observed delegation success rate. */
  reliability: number;
  /** Average latency in ms (0 = unknown). */
  latency_ms: number;
}

/** Capability bundle compiled from classifier tool intents. */
export interface CapabilityBundle {
  preset: string;
  profile: string;
  tool_allowlist: string[];
}

/** Auditable score breakdown returned by the ranker. */
export interface ScoreBreakdown {
  task_prior: number;
  review_boost: number;
  reliability: number;
  latency_score: number;
  cost_score: number;
  total: number;
}

/** A ranked candidate with its score and breakdown. */
export interface RankedCandidate {
  candidate: ModelCandidate;
  score: number;
  breakdown: ScoreBreakdown;
}

/** Full routing result including the decision, chosen model, and dispatch outcome. */
export interface RouteResult {
  decision: RouteDecision;
  chosen: RankedCandidate;
  delegation_id: string | null;
  output: string;
}

/** Router state observable via /router status. */
export interface RouterState {
  enabled: boolean;
  busy: boolean;
  last_route: string | null;
}

/** Configuration for the router. */
export interface RouterConfig {
  enabled: boolean;
  /** Override candidate catalog (else defaults). */
  candidates: ModelCandidate[];
  /** Confidence threshold below which we passthrough (0–1). */
  confidence_threshold: number;
  /** Max delegation polling attempts. */
  max_poll_attempts: number;
  /** Poll interval in ms. */
  poll_interval_ms: number;
}

/** Input metadata the classifier receives alongside the text. */
export interface InputMeta {
  has_images: boolean;
  is_slash_command: boolean;
  origin: "user" | "extension";
  text: string;
}
