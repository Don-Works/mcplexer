// router/ranker.ts — deterministic model-candidate ranker.
//
// Pipeline: eligibility gates → task-specific prior + review + reliability +
// latency + cost scoring → deterministic tiebreak. Missing evidence is neutral
// (50 for priors, 80 for reliability), never zero or free.

import type {
  ModelCandidate,
  RouteDecision,
  RankedCandidate,
  ScoreBreakdown,
  TaskKind,
} from "./types.ts";

// --- Eligibility gates ---

/** Capability requirements that each task_kind implies. */
const TASK_CAPABILITIES: Record<TaskKind, string[]> = {
  coding: ["code"],
  research: ["reasoning"],
  review: ["code", "reasoning"],
  chat: [],
};

function hasCapabilities(candidate: ModelCandidate, required: string[]): boolean {
  return required.every((cap) => candidate.capabilities.includes(cap));
}

function meetsRequirements(candidate: ModelCandidate, requirements: string[]): boolean {
  return requirements.every((req) => candidate.capabilities.includes(req));
}

/** Gate: candidate must have the capabilities the task_kind and classifier require. */
function isEligible(candidate: ModelCandidate, decision: RouteDecision): boolean {
  const taskCaps = TASK_CAPABILITIES[decision.task_kind];
  return hasCapabilities(candidate, taskCaps) && meetsRequirements(candidate, decision.requirements);
}

// --- Scoring components (each 0–100) ---

const NEUTRAL_PRIOR = 50;
const NEUTRAL_RELIABILITY = 80;

function scoreTaskPrior(candidate: ModelCandidate, taskKind: TaskKind): number {
  return candidate.task_priors[taskKind] ?? NEUTRAL_PRIOR;
}

function scoreReliability(candidate: ModelCandidate): number {
  return candidate.reliability || NEUTRAL_RELIABILITY;
}

function scoreLatency(candidate: ModelCandidate): number {
  // Unknown latency (0) → neutral 50
  if (!candidate.latency_ms) return 50;
  // Lower is better. Map 0–10000ms to 100–0 linearly, clamped.
  return Math.max(0, Math.min(100, 100 - (candidate.latency_ms / 10000) * 100));
}

const COST_SCORES: Record<string, number> = { low: 90, medium: 60, high: 30 };

function scoreCost(candidate: ModelCandidate): number {
  return COST_SCORES[candidate.cost_tier] ?? 50;
}

/** Boost for models that have been reviewed/validated for this task kind. */
function scoreReviewBoost(_candidate: ModelCandidate, _decision: RouteDecision): number {
  // No review history yet — neutral. Operators can inject review data later.
  return 0;
}

// --- Weights ---

interface RankerWeights {
  task_prior: number;
  review_boost: number;
  reliability: number;
  latency: number;
  cost: number;
}

const DEFAULT_WEIGHTS: RankerWeights = {
  task_prior: 0.40,
  review_boost: 0.15,
  reliability: 0.20,
  latency: 0.15,
  cost: 0.10,
};

// Quality multipliers amplify/de-emphasize prior importance
const QUALITY_MULTIPLIER: Record<string, number> = {
  high: 1.2,
  medium: 1.0,
  low: 0.8,
};

// --- Main ranker ---

/**
 * Rank model candidates for a given RouteDecision.
 *
 * Returns candidates sorted by score descending. The first element is the
 * best pick. Ties are broken deterministically by candidate id (lexicographic).
 *
 * @param candidates Full model catalog.
 * @param decision   Classifier output.
 * @param weights    Optional weight overrides (for testing/tuning).
 */
export function rankCandidates(
  candidates: ModelCandidate[],
  decision: RouteDecision,
  weights: RankerWeights = DEFAULT_WEIGHTS,
): RankedCandidate[] {
  const eligible = candidates.filter((c) => isEligible(c, decision));
  if (eligible.length === 0) return [];

  const qualityMult = QUALITY_MULTIPLIER[decision.quality] ?? 1.0;

  const scored: RankedCandidate[] = eligible.map((candidate) => {
    const taskPrior = scoreTaskPrior(candidate, decision.task_kind);
    const reviewBoost = scoreReviewBoost(candidate, decision);
    const reliability = scoreReliability(candidate);
    const latencyScore = scoreLatency(candidate);
    const costScore = scoreCost(candidate);

    const total =
      taskPrior * weights.task_prior * qualityMult +
      reviewBoost * weights.review_boost +
      reliability * weights.reliability +
      latencyScore * weights.latency +
      costScore * weights.cost;

    const breakdown: ScoreBreakdown = {
      task_prior: taskPrior,
      review_boost: reviewBoost,
      reliability,
      latency_score: latencyScore,
      cost_score: costScore,
      total: Math.round(total * 100) / 100,
    };

    return { candidate, score: Math.round(total * 100) / 100, breakdown };
  });

  // Sort by score descending, then by id ascending for deterministic ties.
  scored.sort((a, b) => {
    if (b.score !== a.score) return b.score - a.score;
    return a.candidate.id.localeCompare(b.candidate.id);
  });

  return scored;
}
