package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/don-works/mcplexer/internal/models"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/toolgate"
	"github.com/don-works/mcplexer/internal/workers/runner"
)

// Caller-supplied prompt-section budgets. Before these existed the only
// backstop was runner.maxUserPromptBytes (128 KB ≈ 32k tokens), which a
// 100k-window local model (Qwen-class behind pi_cli) cannot absorb — the
// prompt alone would eat a third of its context before the worker read a
// single file. The split favours instructions over pre-computed context:
// a handoff the parent actually wrote is denser and more load-bearing per
// byte than an index pack, and an over-long repo brief is the failure mode
// seen in practice.
//
// Sizing rationale (≈4 bytes/token, the estimateBriefTokens heuristic):
//   - repo brief 24 KB ≈ 6,000 tokens — comfortably fits a generous
//     index.context pack plus a file tree.
//   - handoff 16 KB ≈ 4,000 tokens — several pages of prose; a brief
//     longer than this belongs in a task work-context the worker fetches.
//
// Combined worst case ≈10k tokens, leaving a 100k-window worker ~90% of
// its budget for actual work. Over-budget input is TRUNCATED, never
// rejected: a delegation that runs on a trimmed brief beats one that
// never starts, and the drop is reported in warnings + logs so the parent
// can re-scope.
const (
	maxRepoBriefBytes = 24 * 1024
	maxHandoffBytes   = 16 * 1024
)

const (
	delegationWorkerPrefix               = "delegate-"
	delegationMetaKey                    = "_mcplexer_delegation"
	defaultDelegationMaxWallClockSeconds = 60 * 60
	defaultDelegationMaxToolCalls        = 80
	defaultDelegationTools               = `["mcpx__execute_code","mcpx__search_tools","mcpx__skill_search","mcpx__skill_get","mcpx__workspace_read_file","mcpx__workspace_list_directory","mcpx__workspace_write_file","mcpx__workspace_edit_file","mesh__send","mesh__receive","mesh__list_peers","mesh__list_agents","memory__save","memory__recall","memory__list","task__create","task__get","task__list","task__update","task__append_note"]`
	// defaultDelegationToolsReview is the hardened surface for worker_mode=review.
	// It omits mutating operations (task create/update, memory save) so a review
	// worker cannot make state changes unless the operator explicitly supplies a
	// broader allowlist (and the handoff authorizes it). This is the "role filter"
	// defaulting path: review role gets a narrower tool allowlist by construction.
	defaultDelegationToolsReview = `["mcpx__execute_code","mcpx__search_tools","mcpx__skill_search","mcpx__skill_get","mcpx__workspace_read_file","mcpx__workspace_list_directory","mesh__send","mesh__receive","mesh__list_peers","mesh__list_agents","memory__recall","memory__list","task__get","task__list","task__append_note"]`
)

// DelegationInput is the product-facing wrapper for creating one or more
// one-shot workers. It deliberately avoids requiring callers to know the
// low-level Worker JSON fields unless they need to override them.
type DelegationInput struct {
	WorkspaceID string `json:"workspace_id,omitempty"`
	Objective   string `json:"objective"`
	Handoff     string `json:"handoff,omitempty"`
	Name        string `json:"name,omitempty"`
	TaskID      string `json:"task_id,omitempty"`
	TaskKind    string `json:"task_kind,omitempty"`
	TaskShape   string `json:"task_shape,omitempty"`
	WorkerMode  string `json:"worker_mode,omitempty"`
	// NoAutoContext opts out of automatic memory/mesh context injection.
	NoAutoContext bool `json:"no_auto_context,omitempty"`
	// taskKindInferred records that TaskKind was derived from the shape
	// classifier rather than supplied by the caller (provenance for
	// ranking history; reviews can override the kind).
	taskKindInferred bool
	// autoContext holds the computed auto-injected context packet
	// (memory + recent mesh), rendered into the worker prompt. Not a
	// caller input — populated in Delegate.
	autoContext string
	// truncationWarnings records over-budget RepoBrief/Handoff clipping
	// done by normalizeDelegationInput, so Delegate can surface it in the
	// delegation's warnings alongside the frontier/overlap advisories.
	// Not a caller input.
	truncationWarnings []string

	ModelProfileID      string                     `json:"model_profile_id,omitempty"`
	ModelProvider       string                     `json:"model_provider,omitempty"`
	ModelID             string                     `json:"model_id,omitempty"`
	ModelEndpointURL    string                     `json:"model_endpoint_url,omitempty"`
	SecretScopeID       string                     `json:"secret_scope_id,omitempty"`
	ModelSelectionMode  string                     `json:"model_selection_mode,omitempty"`
	ModelCandidateIndex int                        `json:"model_candidate_index,omitempty"`
	ModelCandidates     []DelegationModelCandidate `json:"model_candidates,omitempty"`
	ToolAllowlistJSON   string                     `json:"tool_allowlist_json,omitempty"`
	// PreExecuteScript / PostExecuteScript are optional JS hooks run in the
	// code-mode sandbox around this delegate's loop. Pre gates the run
	// (throw / abort(reason) blocks before any model spend); post can reject
	// output. Validated + executed identically to a hand-built worker's.
	PreExecuteScript  string `json:"pre_execute_script,omitempty"`
	PostExecuteScript string `json:"post_execute_script,omitempty"`
	ReviewRequired    *bool  `json:"review_required,omitempty"`
	// WorkerIsolation defaults to "worktree". "none" is an explicit trusted
	// escape, including the required mode for CLI-backed delegates.
	WorkerIsolation string `json:"worker_isolation,omitempty"`
	// TouchesFiles declares concrete workspace-relative write paths. Runtime
	// enforcement resolves them inside the isolated worktree.
	TouchesFiles []string `json:"touches_files,omitempty"`

	// CapabilityPreset sizes the delegate's allowed tool surface +
	// mcplexer features to its trust ("full"|"coder"|"researcher"|
	// "minimal"). Empty = today's behavior (only tool_allowlist_json gates).
	// CapabilityProfile is a fine-grained override merged ON TOP of the
	// preset. Pointer so "absent" is distinguishable from "empty {}".
	CapabilityPreset  string                      `json:"capability_preset,omitempty"`
	CapabilityProfile *toolgate.CapabilityProfile `json:"capability_profile,omitempty"`

	// capabilityProfileJSON is the resolved+validated+marshalled profile,
	// computed by normalizeDelegationInput and threaded into the created
	// worker by buildDelegationWorkerInput. Empty when no profile is set.
	capabilityProfileJSON string `json:"-"`

	ParentContextID        string  `json:"parent_context_id,omitempty"`
	ParentSessionID        string  `json:"parent_session_id,omitempty"`
	ParentModel            string  `json:"parent_model,omitempty"`
	ParentInputTokens      int     `json:"parent_input_tokens,omitempty"`
	ParentOutputTokens     int     `json:"parent_output_tokens,omitempty"`
	ParentCostUSD          float64 `json:"parent_cost_usd,omitempty"`
	BaselineTokensEstimate int     `json:"baseline_tokens_estimate,omitempty"`
	BaselineCostUSD        float64 `json:"baseline_cost_usd,omitempty"`

	// RepoBrief is an optional pre-computed repository context to inject
	// into the worker prompt, eliminating cold-start token spend. When set,
	// it is prepended as a "Repository Context (pre-computed)" section
	// before the Objective.
	RepoBrief string `json:"repo_brief,omitempty"`

	Parallelism         int     `json:"parallelism,omitempty"`
	MaxInputTokens      int     `json:"max_input_tokens,omitempty"`
	MaxOutputTokens     int     `json:"max_output_tokens,omitempty"`
	MaxToolCalls        int     `json:"max_tool_calls,omitempty"`
	MaxWallClockSeconds int     `json:"max_wall_clock_seconds,omitempty"`
	MaxMonthlyCostUSD   float64 `json:"max_monthly_cost_usd,omitempty"`

	// ProgressInterval controls how often the worker emits a progress
	// finding on the mesh (every N tool calls). 0 = use runner default (10).
	ProgressInterval int `json:"progress_interval,omitempty"`

	// DisabledProviders is populated by the HTTP layer from current
	// settings.DelegationDisabledProviders. Candidates whose provider or
	// logical group is present (true) are excluded from routing choices.
	// Not part of the public JSON contract for createDelegation.
	DisabledProviders map[string]bool `json:"-"`

	resolvedModelCandidates []delegationResolvedModelCandidate `json:"-"`
}

type DelegationModelCandidate struct {
	Label            string   `json:"label,omitempty"`
	ModelProfileID   string   `json:"model_profile_id,omitempty"`
	ModelProvider    string   `json:"model_provider,omitempty"`
	ModelID          string   `json:"model_id,omitempty"`
	ModelEndpointURL string   `json:"model_endpoint_url,omitempty"`
	SecretScopeID    string   `json:"secret_scope_id,omitempty"`
	CapabilityTags   []string `json:"capability_tags,omitempty"`
	InputModalities  []string `json:"input_modalities,omitempty"`
	OutputModalities []string `json:"output_modalities,omitempty"`
}

type delegationResolvedModelCandidate struct {
	DelegationModelCandidate
	Index int
	Total int
}

type DelegationModelChoice struct {
	Label            string   `json:"label,omitempty"`
	ModelProfileID   string   `json:"model_profile_id,omitempty"`
	ModelProvider    string   `json:"model_provider,omitempty"`
	ModelID          string   `json:"model_id,omitempty"`
	CapabilityTags   []string `json:"capability_tags,omitempty"`
	InputModalities  []string `json:"input_modalities,omitempty"`
	OutputModalities []string `json:"output_modalities,omitempty"`
	CandidateIndex   int      `json:"candidate_index,omitempty"`
	CandidateTotal   int      `json:"candidate_total,omitempty"`
}

// DelegationDispatch reports the worker rows created by Delegate. RunID is
// filled only when the service can create a stub row synchronously; normal
// daemon dispatch hydrates the run via ListDelegations after the detached
// runner persists it.
type DelegationDispatch struct {
	WorkerID      string `json:"worker_id"`
	RunID         string `json:"run_id,omitempty"`
	Status        string `json:"status"`
	Name          string `json:"name"`
	ParallelIndex int    `json:"parallel_index"`
	ParallelTotal int    `json:"parallel_total"`
}

// DelegationOutput is returned by Delegate.
type DelegationOutput struct {
	DelegationID       string               `json:"delegation_id"`
	WorkspaceID        string               `json:"workspace_id"`
	Objective          string               `json:"objective"`
	TaskKind           string               `json:"task_kind,omitempty"`
	TaskShape          string               `json:"task_shape,omitempty"`
	WorkerMode         string               `json:"worker_mode"`
	ModelSelectionMode string               `json:"model_selection_mode,omitempty"`
	ReviewRequired     bool                 `json:"review_required"`
	Parent             DelegationParent     `json:"parent"`
	Baseline           DelegationBaseline   `json:"baseline"`
	Dispatches         []DelegationDispatch `json:"dispatches"`
	// BriefTokens is the estimated token count of the injected repo brief
	// (0 when no brief was provided). Reported so callers can measure the
	// cold-start savings from brief injection.
	BriefTokens int `json:"brief_tokens,omitempty"`
	// ContextTokens is the estimated token count of the auto-injected
	// context packet (recalled memory + recent mesh). 0 when nothing was
	// injected or the caller opted out.
	ContextTokens int `json:"context_tokens,omitempty"`
	// Warnings carries non-blocking advisories about the delegation —
	// e.g. that a frontier-class model was chosen as a worker. The
	// delegation still runs; the warning is signal for the caller.
	Warnings []string `json:"warnings,omitempty"`
}

type DelegationParent struct {
	ContextID    string  `json:"context_id,omitempty"`
	SessionID    string  `json:"session_id,omitempty"`
	Model        string  `json:"model,omitempty"`
	InputTokens  int     `json:"input_tokens,omitempty"`
	OutputTokens int     `json:"output_tokens,omitempty"`
	CostUSD      float64 `json:"cost_usd,omitempty"`
}

type DelegationBaseline struct {
	TokensEstimate int     `json:"tokens_estimate,omitempty"`
	CostUSD        float64 `json:"cost_usd,omitempty"`
}

type DelegationReview struct {
	Reviewed          bool                    `json:"reviewed,omitempty"`
	Score             int                     `json:"score"`
	Outcome           string                  `json:"outcome,omitempty"`
	Notes             string                  `json:"notes,omitempty"`
	ReviewerContextID string                  `json:"reviewer_context_id,omitempty"`
	ReviewerModel     string                  `json:"reviewer_model,omitempty"`
	TaskKind          string                  `json:"task_kind,omitempty"`
	Scores            map[string]int          `json:"scores,omitempty"`
	ModelScores       []DelegationModelReview `json:"model_scores,omitempty"`
	ReviewedAt        time.Time               `json:"reviewed_at,omitempty"`
}

type DelegationModelReview struct {
	ModelKey string         `json:"model_key,omitempty"`
	WorkerID string         `json:"worker_id,omitempty"`
	Score    int            `json:"score"`
	Outcome  string         `json:"outcome,omitempty"`
	Notes    string         `json:"notes,omitempty"`
	Scores   map[string]int `json:"scores,omitempty"`
}

type DelegationReviewInput struct {
	DelegationID      string                  `json:"delegation_id,omitempty"`
	WorkspaceID       string                  `json:"workspace_id,omitempty"`
	Score             int                     `json:"score"`
	Outcome           string                  `json:"outcome,omitempty"`
	Notes             string                  `json:"notes,omitempty"`
	ReviewerContextID string                  `json:"reviewer_context_id,omitempty"`
	ReviewerModel     string                  `json:"reviewer_model,omitempty"`
	TaskKind          string                  `json:"task_kind,omitempty"`
	Scores            map[string]int          `json:"scores,omitempty"`
	ModelScores       []DelegationModelReview `json:"model_scores,omitempty"`
}

type DelegationListInput struct {
	WorkspaceID string `json:"workspace_id,omitempty"`
	Limit       int    `json:"limit,omitempty"`
}

type DelegationContext struct {
	ID                 string                    `json:"id"`
	WorkspaceID        string                    `json:"workspace_id"`
	Objective          string                    `json:"objective"`
	Handoff            string                    `json:"handoff,omitempty"`
	TaskID             string                    `json:"task_id,omitempty"`
	TaskKind           string                    `json:"task_kind,omitempty"`
	TaskShape          string                    `json:"task_shape,omitempty"`
	WorkerMode         string                    `json:"worker_mode"`
	ModelSelectionMode string                    `json:"model_selection_mode,omitempty"`
	ReviewRequired     bool                      `json:"review_required"`
	Parent             DelegationParent          `json:"parent"`
	Baseline           DelegationBaseline        `json:"baseline"`
	Review             DelegationReview          `json:"review,omitempty"`
	ParallelTotal      int                       `json:"parallel_total"`
	CreatedAt          time.Time                 `json:"created_at"`
	UpdatedAt          time.Time                 `json:"updated_at"`
	Status             string                    `json:"status"`
	Aggregate          DelegationAggregate       `json:"aggregate"`
	ModelStats         []DelegationModelStat     `json:"model_stats,omitempty"`
	Workers            []DelegationWorkerContext `json:"workers"`
	// Warnings replays creation-time advisories (frontier-class model,
	// file-claim overlaps) on every list/wait read.
	Warnings []string `json:"warnings,omitempty"`
}

type DelegationWorkerContext struct {
	Worker        *store.Worker      `json:"worker"`
	LatestRun     *store.WorkerRun   `json:"latest_run,omitempty"`
	RecentRuns    []*store.WorkerRun `json:"recent_runs"`
	ParallelIndex int                `json:"parallel_index"`
	ParallelTotal int                `json:"parallel_total"`
	// DispatchFailed mirrors the delegation metadata flag stamped by
	// dispatchDelegationRun when the detached RunNow errored before a
	// run row existed. Without it the worker shows "dispatched" forever.
	DispatchFailed bool   `json:"dispatch_failed,omitempty"`
	DispatchError  string `json:"dispatch_error,omitempty"`
}

type DelegationAggregate struct {
	Workers int `json:"workers"`
	Running int `json:"running"`
	Success int `json:"success"`
	Failure int `json:"failure"`
	Blocked int `json:"blocked"`
	// Cancelled counts operator hard-stopped runs. Tracked separately so
	// they never inflate Failure — a human cancelling a delegation is not
	// a worker failure and must not flip the delegation to "failed" or
	// gate it into "needs_review".
	Cancelled                  int     `json:"cancelled"`
	Interrupted                int     `json:"interrupted"`
	Dispatched                 int     `json:"dispatched"`
	InputTokens                int     `json:"input_tokens"`
	OutputTokens               int     `json:"output_tokens"`
	TotalTokens                int     `json:"total_tokens"`
	CostUSD                    float64 `json:"cost_usd"`
	ToolCalls                  int     `json:"tool_calls"`
	DurationMS                 int64   `json:"duration_ms"`
	ParentTokens               int     `json:"parent_tokens"`
	CombinedTokens             int     `json:"combined_tokens"`
	ParentCostUSD              float64 `json:"parent_cost_usd"`
	CombinedCostUSD            float64 `json:"combined_cost_usd"`
	BaselineTokens             int     `json:"baseline_tokens"`
	BaselineCostUSD            float64 `json:"baseline_cost_usd"`
	FrontierTokensAvoided      int     `json:"frontier_tokens_avoided"`
	FrontierCostAvoidedUSD     float64 `json:"frontier_cost_avoided_usd"`
	WorkerTokenDelta           int     `json:"worker_token_delta"`
	EstimatedParentTokensSaved int     `json:"estimated_parent_tokens_saved"`
	NetTokensDelta             int     `json:"net_tokens_delta"`
	EstimatedCostSavedUSD      float64 `json:"estimated_cost_saved_usd"`
	SavingsBasis               string  `json:"savings_basis,omitempty"`
	SavingsConfidence          string  `json:"savings_confidence,omitempty"`
	ParentTokensKnown          bool    `json:"parent_tokens_known"`
	ReviewScore                int     `json:"review_score"`
	// UnknownCostRuns counts runs whose adapter reported zero usage telemetry,
	// including CLI cap/output-gate outcomes. Their $0 cost is MISSING data,
	// not free compute — savings math excludes them.
	UnknownCostRuns        int            `json:"unknown_cost_runs,omitempty"`
	UnknownDurationMS      int64          `json:"unknown_duration_ms,omitempty"`
	CostAllMissing         bool           `json:"cost_all_missing,omitempty"`
	RealDollarsSpent       float64        `json:"real_dollars_spent"`
	QuotaTokensByBucket    map[string]int `json:"quota_tokens_by_bucket,omitempty"`
	FrontierQuotaPreserved int            `json:"frontier_quota_preserved"`
	FrontierQuotaBurned    int            `json:"frontier_quota_burned"`
	RealCostSavedUSD       float64        `json:"real_cost_saved_usd"`
}

type DelegationModelStat struct {
	ModelProvider string `json:"model_provider"`
	ModelID       string `json:"model_id"`
	ModelKey      string `json:"model_key"`
	Runs          int    `json:"runs"`
	Success       int    `json:"success"`
	Failure       int    `json:"failure"`
	Running       int    `json:"running"`
	// Cancelled counts operator hard-stopped runs for this model. They
	// are EXCLUDED from Runs/Success/Failure (and from the token/cost
	// roll-up) so an operator cancel never perturbs the model rank — it
	// is neither a failure penalty nor a success/cost-skew advantage.
	Cancelled    int     `json:"cancelled,omitempty"`
	Interrupted  int     `json:"interrupted,omitempty"`
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	TotalTokens  int     `json:"total_tokens"`
	CostUSD      float64 `json:"cost_usd"`
	// UnknownCostRuns counts runs whose adapter reported no usage telemetry,
	// including CLI cap/output-gate outcomes. Their $0 cost is MISSING data,
	// not free compute — the ranker excludes them from
	// cheaper-wins comparisons so missing accounting never becomes a
	// ranking advantage.
	UnknownCostRuns int `json:"unknown_cost_runs,omitempty"`
	// UnknownSuccessRuns is the subset of UnknownCostRuns whose terminal
	// outcome was success. The ranker subtracts only this subset from the
	// success numerator; missing telemetry on a cap/output failure must not
	// erase a different, known-accounting success.
	UnknownSuccessRuns int   `json:"unknown_success_runs,omitempty"`
	DurationMS         int64 `json:"duration_ms"`
	AvgDurationMS      int64 `json:"avg_duration_ms"`
	// UnknownDurationMS is the sum of durations of accounting_missing runs.
	UnknownDurationMS int64 `json:"unknown_duration_ms,omitempty"`
	// OperationalDurationMS is adapter/launch time for operational failures.
	// It is excluded from model execution speed so infrastructure latency is
	// not double-counted as both reliability and slow generation.
	OperationalDurationMS int64 `json:"operational_duration_ms,omitempty"`
	// OperationalFailures counts runs whose adapter/launch died before
	// the model produced any output (status=failure, zero tokens, error
	// prefixed with "adapter send:") plus dispatch-failed workers that
	// never created a run row. These runs are RELIABILITY data, not
	// QUALITY data: the model never ran, so a parent review score of
	// (e.g.) 20 from a "model unavailable" judgement must NOT be folded
	// into the per-model avg review score that drives capacity ranking.
	// modelStatsForDelegation keeps this counter for the operator to
	// see (and for the ranker to demote a chronically-unreliable model),
	// but suppresses stat.ReviewCount/ReviewScore when every matching
	// worker in a delegation was operational-only.
	OperationalFailures int `json:"operational_failures,omitempty"`
	// DispatchFailures are the operational failures that happened before a
	// WorkerRun row existed. They are tracked separately so reliability uses
	// them in its attempt denominator without pretending they consumed a run.
	DispatchFailures int `json:"dispatch_failures,omitempty"`
	// BudgetFailures are terminal cap outcomes (tool, wall-clock, token, or
	// cost limits). They remain in Failure for backwards compatibility but
	// are excluded from coding-quality failure counts.
	BudgetFailures int `json:"budget_failures,omitempty"`
	// DeliverabilityFailures reached the model but were rejected by a
	// post-execute/output gate. Pre-execute policy blocks are excluded because
	// the model never ran.
	DeliverabilityFailures int `json:"deliverability_failures,omitempty"`
	// QualitySuccess and QualityFailure exclude operational failures
	// so callers can compute a pure coding-ability rate without
	// adapter/launch noise inflating the denominator.
	QualitySuccess int `json:"quality_success,omitempty"`
	QualityFailure int `json:"quality_failure,omitempty"`
	// QualityRate is qualitySuccess / (qualitySuccess + qualityFailure).
	// 0 when no quality terminal runs exist.
	QualityRate float64 `json:"quality_rate"`
	// ReliabilityRate is 1 - (operationalFailures / terminal runs).
	// 1 when no terminal runs exist (nothing broken = reliable).
	ReliabilityRate  float64        `json:"reliability_rate"`
	ReviewCount      int            `json:"review_count,omitempty"`
	ReviewScore      int            `json:"review_score"`
	TaskKind         string         `json:"task_kind,omitempty"`
	TaskShape        string         `json:"task_shape,omitempty"`
	CapabilityScores map[string]int `json:"capability_scores,omitempty"`
	WorkerIDs        []string       `json:"worker_ids,omitempty"`
}

type delegationMetadata struct {
	ID                 string                `json:"id"`
	Kind               string                `json:"kind"`
	Objective          string                `json:"objective"`
	Handoff            string                `json:"handoff,omitempty"`
	TaskID             string                `json:"task_id,omitempty"`
	TaskKind           string                `json:"task_kind,omitempty"`
	TaskKindInferred   bool                  `json:"task_kind_inferred,omitempty"`
	TaskShape          string                `json:"task_shape,omitempty"`
	WorkerMode         string                `json:"worker_mode,omitempty"`
	WorkerIsolation    string                `json:"worker_isolation,omitempty"`
	TouchesFiles       []string              `json:"touches_files,omitempty"`
	ModelSelectionMode string                `json:"model_selection_mode,omitempty"`
	ModelChoice        DelegationModelChoice `json:"model_choice,omitempty"`
	ProgressInterval   int                   `json:"progress_interval,omitempty"`
	// Warnings persists creation-time advisories (frontier-class model,
	// file-claim overlaps) so list/wait/dashboard surfaces can show them
	// after the one-shot Delegate response is gone.
	Warnings []string `json:"warnings,omitempty"`
	// CapabilityPreset is the resolved preset name copied here for the
	// Delegations UI. The enforcement source of truth is the worker's
	// capability_profile_json column, not this display-only blob.
	CapabilityPreset       string           `json:"capability_preset,omitempty"`
	ReviewRequired         *bool            `json:"review_required,omitempty"`
	ParentContextID        string           `json:"parent_context_id,omitempty"`
	ParentSessionID        string           `json:"parent_session_id,omitempty"`
	ParentModel            string           `json:"parent_model,omitempty"`
	ParentInputTokens      int              `json:"parent_input_tokens,omitempty"`
	ParentOutputTokens     int              `json:"parent_output_tokens,omitempty"`
	ParentCostUSD          float64          `json:"parent_cost_usd,omitempty"`
	BaselineTokensEstimate int              `json:"baseline_tokens_estimate,omitempty"`
	BaselineCostUSD        float64          `json:"baseline_cost_usd,omitempty"`
	Review                 DelegationReview `json:"review,omitempty"`
	ParallelIndex          int              `json:"parallel_index"`
	ParallelTotal          int              `json:"parallel_total"`
	CreatedAt              time.Time        `json:"created_at"`
	// DispatchFailed + DispatchError record a detached-dispatch failure
	// (RunNowWithOpts errored before a run row existed) so parents stop
	// seeing a stuck "dispatched". Stamped by dispatchDelegationRun.
	DispatchFailed bool   `json:"dispatch_failed,omitempty"`
	DispatchError  string `json:"dispatch_error,omitempty"`
	// ArchivedAt is stamped by the retention sweep when the delegation
	// worker is auto-disabled N days after its last terminal run.
	// Archived workers are excluded from ListDelegations.
	ArchivedAt *time.Time `json:"archived_at,omitempty"`
}

func (s *Service) Delegate(ctx context.Context, in DelegationInput) (DelegationOutput, error) {
	if err := s.normalizeDelegationInput(ctx, &in); err != nil {
		return DelegationOutput{}, err
	}
	if err := s.preflightDelegation(ctx, &in); err != nil {
		return DelegationOutput{}, err
	}
	// Runtime startup can mutate external process state. Keep it after the
	// read-only preflight so an invalid workspace/scope never starts a managed
	// OpenCode server for a delegation that cannot be created.
	if err := s.ensureDelegationOpenCodeRuntime(ctx, &in); err != nil {
		return DelegationOutput{}, err
	}
	reviewRequired := delegationReviewRequired(in)
	delegationID := "del-" + uuid.NewString()
	createdAt := s.clock.Now().UTC()
	plan := buildDelegationModelPlan(in)
	warnings := frontierWorkerWarnings(in.WorkerMode, plan)
	for _, msg := range warnings {
		slog.WarnContext(ctx, "delegation: frontier-class worker discouraged",
			"delegation_objective", in.Objective, "advisory", msg)
	}
	// Over-budget repo_brief/handoff was clipped in normalizeDelegationInput.
	// Report it: a parent that silently loses half its handoff has no way to
	// tell whether the worker under-performed or was under-briefed.
	for _, msg := range in.truncationWarnings {
		slog.WarnContext(ctx, "delegation: input truncated to fit worker context budget",
			"delegation_id", delegationID, "advisory", msg)
	}
	warnings = append(warnings, in.truncationWarnings...)
	// Coordination: surface overlap warnings before creating anything.
	claimRepo := ""
	if len(in.TouchesFiles) > 0 {
		claimRepo = s.delegationClaimRepo(ctx, in.WorkspaceID)
		warnings = append(warnings, s.checkFileClaimOverlap(ctx, in.TouchesFiles, delegationID, claimRepo)...)
	}
	// Context sharing: recall memory + recent mesh once per delegation
	// (not per parallel candidate) so workers skip cold-start
	// re-exploration. Best-effort — never blocks or fails the delegation.
	contextTokens := 0
	in.autoContext, contextTokens = s.buildAutoContext(ctx, &in)
	total := len(plan)
	pending := make([]delegationPendingWorker, 0, total)
	for i, candidate := range plan {
		meta := delegationMetadata{
			ID:                     delegationID,
			Kind:                   "token_preserving_delegation",
			Objective:              in.Objective,
			Handoff:                in.Handoff,
			TaskID:                 in.TaskID,
			TaskKind:               in.TaskKind,
			TaskKindInferred:       in.taskKindInferred,
			TaskShape:              in.TaskShape,
			WorkerMode:             in.WorkerMode,
			WorkerIsolation:        in.WorkerIsolation,
			TouchesFiles:           append([]string(nil), in.TouchesFiles...),
			ModelSelectionMode:     in.ModelSelectionMode,
			ModelChoice:            delegationModelChoice(candidate),
			CapabilityPreset:       delegationCapabilityPresetLabel(in),
			ReviewRequired:         &reviewRequired,
			ParentContextID:        in.ParentContextID,
			ParentSessionID:        in.ParentSessionID,
			ParentModel:            in.ParentModel,
			ParentInputTokens:      in.ParentInputTokens,
			ParentOutputTokens:     in.ParentOutputTokens,
			ParentCostUSD:          in.ParentCostUSD,
			BaselineTokensEstimate: in.BaselineTokensEstimate,
			BaselineCostUSD:        in.BaselineCostUSD,
			ProgressInterval:       in.ProgressInterval,
			Warnings:               warnings,
			ParallelIndex:          i + 1,
			ParallelTotal:          total,
			CreatedAt:              createdAt,
		}
		createIn, err := buildDelegationWorkerInput(in, meta, candidate)
		if err != nil {
			s.rollbackDelegationWorkers(ctx, pending)
			return DelegationOutput{}, err
		}
		w, err := s.Create(ctx, createIn)
		if err != nil {
			s.rollbackDelegationWorkers(ctx, pending)
			return DelegationOutput{}, err
		}
		pending = append(pending, delegationPendingWorker{worker: w, parallelIndex: i})
	}
	// Coordination: claim files BEFORE dispatch so a sibling delegation's
	// overlap check cannot race past a worker that is already running.
	if len(in.TouchesFiles) > 0 {
		s.claimDelegationFiles(ctx, delegationID, &in, claimRepo)
	}
	dispatches := make([]DelegationDispatch, 0, total)
	for _, item := range pending {
		dispatches = append(dispatches, DelegationDispatch{
			WorkerID:      item.worker.ID,
			Status:        "dispatched",
			Name:          item.worker.Name,
			ParallelIndex: item.parallelIndex,
			ParallelTotal: total,
		})
		s.dispatchDelegationRun(ctx, item.worker.ID, item.worker.MaxWallClockSeconds)
	}
	briefTokens := estimateBriefTokens(in.RepoBrief)
	if briefTokens > 0 {
		slog.InfoContext(ctx, "delegation: repo brief injected",
			"delegation_id", delegationID, "brief_tokens", briefTokens)
	}
	return DelegationOutput{
		DelegationID:       delegationID,
		WorkspaceID:        in.WorkspaceID,
		Objective:          in.Objective,
		TaskKind:           in.TaskKind,
		TaskShape:          in.TaskShape,
		WorkerMode:         in.WorkerMode,
		ModelSelectionMode: in.ModelSelectionMode,
		ReviewRequired:     reviewRequired,
		BriefTokens:        briefTokens,
		ContextTokens:      contextTokens,
		Parent: DelegationParent{
			ContextID:    in.ParentContextID,
			SessionID:    in.ParentSessionID,
			Model:        in.ParentModel,
			InputTokens:  in.ParentInputTokens,
			OutputTokens: in.ParentOutputTokens,
			CostUSD:      in.ParentCostUSD,
		},
		Baseline: DelegationBaseline{
			TokensEstimate: in.BaselineTokensEstimate,
			CostUSD:        in.BaselineCostUSD,
		},
		Dispatches: dispatches,
		Warnings:   warnings,
	}, nil
}

// frontierWorkerWarnings returns one advisory per distinct frontier-class
// model in the plan. Frontier models (opus, fable, gpt-5.5, o1-class)
// should only be delegated to in exceptional cases: the first-12h ledger
// audit found one frontier worker family was 10% of runs but 97% of
// spend, for a quality edge parent review closes for free. The warning is
// non-blocking — the delegation still runs — so genuine exceptional use
// (a frontier judge/final-reviewer role) stays possible.
//
// review-mode delegations are exempt: a frontier model as a read-only
// critic/judge is exactly the sanctioned exceptional use.
func frontierWorkerWarnings(workerMode string, plan []delegationResolvedModelCandidate) []string {
	if strings.EqualFold(strings.TrimSpace(workerMode), "review") {
		return nil
	}
	seen := make(map[string]bool)
	var out []string
	for _, c := range plan {
		if !models.IsFrontierClass(c.ModelProvider, c.ModelID) {
			continue
		}
		key := c.ModelProvider + "/" + c.ModelID
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, fmt.Sprintf(
			"frontier-class worker %q is discouraged for execute work — prefer a workhorse model "+
				"(glm, minimax, deepseek, sonnet) and let parent review close the quality gap. "+
				"Reserve frontier models for judge/final-review roles or genuinely exceptional tasks.",
			key,
		))
	}
	return out
}

func (s *Service) normalizeDelegationInput(ctx context.Context, in *DelegationInput) error {
	if in == nil {
		return errors.New("delegation input required")
	}
	in.WorkspaceID = strings.TrimSpace(in.WorkspaceID)
	in.Objective = strings.TrimSpace(in.Objective)
	in.Handoff = strings.TrimSpace(in.Handoff)
	in.Name = strings.TrimSpace(in.Name)
	in.TaskID = strings.TrimSpace(in.TaskID)
	in.RepoBrief = strings.TrimSpace(in.RepoBrief)
	// Bound the two caller-supplied prompt sections BEFORE classification so
	// everything downstream (shape classifier, metadata, prompt) sees the same
	// bytes the worker will actually read.
	in.truncationWarnings = nil
	var truncWarn string
	if in.RepoBrief, truncWarn = truncateSection("repo_brief", in.RepoBrief, maxRepoBriefBytes); truncWarn != "" {
		in.truncationWarnings = append(in.truncationWarnings, truncWarn)
	}
	if in.Handoff, truncWarn = truncateSection("handoff", in.Handoff, maxHandoffBytes); truncWarn != "" {
		in.truncationWarnings = append(in.truncationWarnings, truncWarn)
	}
	if in.TaskShape = normalizeTaskShape(in.TaskShape); in.TaskShape == "" {
		in.TaskShape = classifyTaskShape(in.Objective, in.Handoff)
	}
	in.TaskKind = normaliseDelegationTaskKind(in.TaskKind)
	if err := normalizeDelegationIsolationInput(in); err != nil {
		return err
	}
	in.WorkerMode = strings.ToLower(strings.TrimSpace(in.WorkerMode))
	if in.WorkerMode == "" {
		in.WorkerMode = "execute"
	}
	switch in.WorkerMode {
	case "execute", "review":
	default:
		return errors.New("worker_mode must be execute or review")
	}
	if in.WorkerMode == "review" {
		in.TaskShape = taskShapeReview
	}
	// Shape-aware routing: an omitted task_kind is inferred from the
	// shape so the existing kind-keyed ranking pipeline (review weight,
	// category EWMA, capability bonus) engages instead of scoring on the
	// overall-only prior. Recorded as provenance; a parent review's
	// task_kind override corrects a misinference.
	if in.TaskKind == "" {
		if kind := inferredTaskKindForShape(in.TaskShape); kind != "" {
			in.TaskKind = normaliseDelegationTaskKind(kind)
			in.taskKindInferred = true
		}
	}
	if in.WorkspaceID == "" {
		return errors.New("workspace_id required")
	}
	if in.Objective == "" {
		return errors.New("objective required")
	}
	if in.Parallelism <= 0 {
		in.Parallelism = 1
	}
	if in.Parallelism > 20 {
		return errors.New("parallelism max 20")
	}
	if in.ParentInputTokens < 0 || in.ParentOutputTokens < 0 || in.BaselineTokensEstimate < 0 {
		return errors.New("token estimates must be >= 0")
	}
	if in.ParentCostUSD < 0 || in.BaselineCostUSD < 0 {
		return errors.New("cost estimates must be >= 0")
	}
	in.ModelProvider = strings.TrimSpace(in.ModelProvider)
	in.ModelID = strings.TrimSpace(in.ModelID)
	in.ModelEndpointURL = strings.TrimSpace(in.ModelEndpointURL)
	in.SecretScopeID = strings.TrimSpace(in.SecretScopeID)
	candidates, err := s.resolveDelegationModelCandidates(ctx, in)
	if err != nil {
		return err
	}
	in.resolvedModelCandidates = candidates
	totalDispatches := in.Parallelism
	if in.ModelSelectionMode == delegationModelSelectionSideBySide {
		totalDispatches *= len(candidates)
	}
	if totalDispatches > 20 {
		return errors.New("delegation dispatches max 20")
	}
	if in.MaxWallClockSeconds == 0 {
		if in.WorkerMode == "review" {
			in.MaxWallClockSeconds = 600
		} else {
			in.MaxWallClockSeconds = defaultDelegationMaxWallClockSeconds
		}
	}
	// Streaming progress defaults ON for delegations (every 10 tool
	// calls); a negative value is the explicit off switch. Plain workers
	// stay silent — the runner has no default of its own.
	switch {
	case in.ProgressInterval == 0:
		in.ProgressInterval = 10
	case in.ProgressInterval < 0:
		in.ProgressInterval = 0
	}
	if in.MaxToolCalls == 0 {
		in.MaxToolCalls = defaultDelegationMaxToolCalls
	}
	if in.ToolAllowlistJSON == "" {
		if in.WorkerMode == "review" {
			in.ToolAllowlistJSON = defaultDelegationToolsReview
		} else {
			in.ToolAllowlistJSON = defaultDelegationTools
		}
	}
	if err := validateDelegationAllowlistJSON(in.ToolAllowlistJSON); err != nil {
		return err
	}
	if err := resolveDelegationCapabilityProfile(in); err != nil {
		return err
	}
	if err := requireIsolatedExecuteFileSurface(in); err != nil {
		return err
	}
	return nil
}

// preflightDelegation validates that every external dependency the
// delegation will need at run time is resolvable BEFORE any WorkerRun
// rows are created. normalizeDelegationInput already validates model
// candidate fields and allowlist JSON syntax; preflight catches the
// remaining class of "doomed before first token" failures: missing
// workspace, missing secret scope, and empty resolved candidates.
func (s *Service) preflightDelegation(ctx context.Context, in *DelegationInput) error {
	if in.WorkerIsolation == workerIsolationWorktree {
		for _, candidate := range in.resolvedModelCandidates {
			if models.IsCLIProvider(candidate.ModelProvider) {
				return fmt.Errorf("preflight: worktree isolation is unavailable for CLI provider %q until a per-run MCP relay can deny direct access to the main daemon socket; use an API provider or explicitly select worker_isolation=none", candidate.ModelProvider)
			}
		}
	}
	if err := s.preflightWorkspace(ctx, in.WorkspaceID); err != nil {
		return err
	}
	if err := s.preflightSecretScopes(ctx, in.resolvedModelCandidates); err != nil {
		return err
	}
	if err := s.preflightKnownModelIDs(ctx, in.resolvedModelCandidates); err != nil {
		return err
	}
	if len(in.resolvedModelCandidates) == 0 {
		return errors.New("preflight: no resolved model candidates after normalization")
	}
	if in.ToolAllowlistJSON != "" {
		if err := validateDelegationAllowlistJSON(in.ToolAllowlistJSON); err != nil {
			return fmt.Errorf("preflight: %w", err)
		}
	}
	return nil
}

// preflightWorkspace verifies the workspace ID maps to a real workspace
// when a workspace lister is available. A nil lister skips the check
// (same semantic as collectWorkspaceIDs).
func (s *Service) preflightWorkspace(ctx context.Context, workspaceID string) error {
	if s.workspaces == nil {
		return nil
	}
	workspaces, err := s.workspaces.ListWorkspaces(ctx)
	if err != nil {
		return fmt.Errorf("preflight: list workspaces: %w", err)
	}
	for _, ws := range workspaces {
		if ws.ID == workspaceID {
			return nil
		}
	}
	return fmt.Errorf("preflight: workspace %q not found", workspaceID)
}

// preflightSecretScopes verifies that every resolved candidate's
// SecretScopeID maps to an existing auth scope. CLI providers
// (claude_cli, opencode_cli, etc.) are exempt — they inherit host
// credentials and never read a secret.
func (s *Service) preflightSecretScopes(ctx context.Context, candidates []delegationResolvedModelCandidate) error {
	if s.authScopes == nil {
		return nil
	}
	seen := make(map[string]struct{})
	for _, c := range candidates {
		scopeID := c.SecretScopeID
		if scopeID == "" || delegationProviderIgnoresScope(c.ModelProvider) {
			continue
		}
		if _, ok := seen[scopeID]; ok {
			continue
		}
		seen[scopeID] = struct{}{}
		if _, err := s.authScopes.GetAuthScope(ctx, scopeID); err != nil {
			return fmt.Errorf("preflight: secret scope %q not found for candidate %s/%s: %w",
				scopeID, c.ModelProvider, c.ModelID, err)
		}
	}
	return nil
}

func (s *Service) ensureDelegationOpenCodeRuntime(ctx context.Context, in *DelegationInput) error {
	managedEndpoint := ""
	if s.openCodeRuntime != nil {
		managedEndpoint = strings.TrimRight(strings.TrimSpace(s.openCodeRuntime.Endpoint()), "/")
	}
	needsManaged := false
	for i := range in.resolvedModelCandidates {
		c := &in.resolvedModelCandidates[i]
		if c.ModelProvider != providerOpenCodeCLI {
			continue
		}
		currentEndpoint := strings.TrimRight(strings.TrimSpace(c.ModelEndpointURL), "/")
		if currentEndpoint != "" && !isHTTPURLString(currentEndpoint) && delegationHasOpenCodeFanout(in) {
			return fmt.Errorf("opencode_cli fan-out requires an HTTP attach endpoint (for example http://127.0.0.1:4096); raw CLI endpoint %q would race OpenCode local state", currentEndpoint)
		}
		if currentEndpoint == "" && s.openCodeRuntime == nil && delegationHasOpenCodeFanout(in) {
			return errors.New("opencode_cli fan-out requires a managed OpenCode runtime or explicit HTTP attach endpoint")
		}
		if s.openCodeRuntime != nil && (currentEndpoint == "" || currentEndpoint == managedEndpoint) {
			needsManaged = true
		}
	}
	if !needsManaged {
		return nil
	}
	// Launch Start asynchronously. Blocking on cold `opencode serve` (or other
	// managed CLI runtime) made the Delegations page POST /delegations (and
	// equivalent mcpx__delegate_worker) time out after ~15s in the browser.
	// Delegate must return promptly (202 + delegation_id + dispatches); the
	// caller observes live progress via list polling + WorkerLiveTail / run bus.
	// We still pre-fill the endpoint so the just-created delegation workers
	// carry a usable target immediately.
	go func() {
		// Start on the daemon LIFECYCLE context, never a short timeout.
		// Start launches a supervisor goroutine (see internal/opencode
		// supervisor.go) that owns restart-on-crash for `opencode serve`,
		// and that goroutine lives exactly as long as this context. A
		// WithTimeout here was a latent bug: the paired defer cancel()
		// fired the instant Start() returned (a few seconds in), so the
		// supervisor saw its parentCtx cancelled and refused to restart
		// the server the first time `opencode serve` crashed (~10-12
		// turns into a run). The recycled/dead server then failed every
		// later `opencode run --attach` with "Error: Session not found".
		// Start bounds its own readiness wait via readinessTimeout, so a
		// long-lived context here cannot hang the dispatch.
		if err := s.openCodeRuntime.Start(s.lifecycleContext()); err != nil {
			slog.Warn("delegation: async managed opencode runtime start failed (subsequent run may fail or self-heal)", "error", err)
		}
	}()
	for i := range in.resolvedModelCandidates {
		c := &in.resolvedModelCandidates[i]
		if c.ModelProvider != providerOpenCodeCLI {
			continue
		}
		currentEndpoint := strings.TrimRight(strings.TrimSpace(c.ModelEndpointURL), "/")
		if currentEndpoint == "" {
			c.ModelEndpointURL = managedEndpoint
		}
	}
	return nil
}

func delegationHasOpenCodeFanout(in *DelegationInput) bool {
	if in == nil {
		return false
	}
	if maxInt(1, in.Parallelism) > 1 {
		return true
	}
	if in.ModelSelectionMode == delegationModelSelectionSideBySide {
		opencodeCandidates := 0
		for _, c := range in.resolvedModelCandidates {
			if c.ModelProvider == providerOpenCodeCLI {
				opencodeCandidates++
			}
		}
		return opencodeCandidates > 1
	}
	return false
}

func isHTTPURLString(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

func buildDelegationWorkerInput(
	in DelegationInput,
	meta delegationMetadata,
	candidate delegationResolvedModelCandidate,
) (CreateInput, error) {
	name := delegationWorkerName(in.Name, in.Objective, meta.ID, meta.ParallelIndex, meta.ParallelTotal)
	params, err := json.Marshal(map[string]any{delegationMetaKey: meta})
	if err != nil {
		return CreateInput{}, fmt.Errorf("marshal delegation metadata: %w", err)
	}
	desc := fmt.Sprintf("Token-preserving delegated context %s", meta.ID)
	if meta.TaskID != "" {
		desc += " for task " + meta.TaskID
	}
	enabled := true
	return CreateInput{
		Name:                   name,
		Description:            desc,
		ModelProvider:          candidate.ModelProvider,
		ModelID:                candidate.ModelID,
		ModelEndpointURL:       candidate.ModelEndpointURL,
		SecretScopeID:          candidate.SecretScopeID,
		PromptTemplate:         delegationPrompt(in, meta),
		ParametersJSON:         string(params),
		ScheduleSpec:           "manual",
		ToolAllowlistJSON:      in.ToolAllowlistJSON,
		CapabilityProfileJSON:  in.capabilityProfileJSON,
		PreExecuteScript:       in.PreExecuteScript,
		PostExecuteScript:      in.PostExecuteScript,
		OutputChannelsJSON:     buildDelegationOutputChannels(),
		ExecMode:               runner.ExecModeAutonomous,
		ConcurrencyPolicy:      "skip",
		Enabled:                &enabled,
		WorkspaceID:            in.WorkspaceID,
		WorkspaceAccess:        []store.WorkerWorkspaceAccess{{WorkspaceID: in.WorkspaceID, Access: store.WorkerWorkspaceAccessWrite}},
		MaxInputTokens:         in.MaxInputTokens,
		MaxOutputTokens:        in.MaxOutputTokens,
		MaxToolCalls:           in.MaxToolCalls,
		MaxWallClockSeconds:    in.MaxWallClockSeconds,
		MaxMonthlyCostUSD:      in.MaxMonthlyCostUSD,
		MaxConsecutiveFailures: 3,
	}, nil
}

type delegationPendingWorker struct {
	worker        *store.Worker
	parallelIndex int
}

func (s *Service) rollbackDelegationWorkers(ctx context.Context, pending []delegationPendingWorker) {
	for _, item := range pending {
		if item.worker == nil || strings.TrimSpace(item.worker.ID) == "" {
			continue
		}
		if err := s.Delete(ctx, item.worker.ID); err != nil {
			slog.Warn("delegation: rollback delete failed",
				"worker_id", item.worker.ID, "error", err)
		}
	}
}

func delegationPrompt(in DelegationInput, meta delegationMetadata) string {
	var b strings.Builder
	b.WriteString("You are a delegated coding worker spawned to preserve the parent model's context budget.\n\n")
	if meta.WorkerMode == "review" {
		b.WriteString("Operate as a review-only worker. Inspect the scoped implementation, run focused checks if needed, and report concrete bugs, risks, or missing tests. Do not edit files unless the handoff explicitly says to.\n\n")
	} else {
		b.WriteString("Operate autonomously inside this workspace. Do the token-heavy execution work; keep final output concise and auditable. Do not ask the parent to re-send broad context unless you are genuinely blocked.\n\n")
	}
	b.WriteString("Delegation:\n")
	b.WriteString("- id: " + meta.ID + "\n")
	b.WriteString("- worker mode: " + meta.WorkerMode + "\n")
	if delegationMetadataReviewRequired(meta) {
		b.WriteString("- parent review: required before this delegation is considered complete\n")
	} else {
		b.WriteString("- parent review: optional\n")
	}
	if meta.ParentContextID != "" {
		b.WriteString("- parent context: " + meta.ParentContextID + "\n")
	}
	if meta.ParentModel != "" {
		b.WriteString("- parent model: " + meta.ParentModel + "\n")
	}
	if meta.TaskID != "" {
		b.WriteString("- task: " + meta.TaskID + "\n")
	}
	if meta.TaskKind != "" {
		b.WriteString("- task kind: " + meta.TaskKind + "\n")
	}
	if meta.ModelSelectionMode != "" {
		b.WriteString("- model selection: " + meta.ModelSelectionMode + "\n")
	}
	if meta.ModelChoice.ModelID != "" {
		b.WriteString("- assigned model: " + meta.ModelChoice.ModelProvider + "/" + meta.ModelChoice.ModelID + "\n")
		if len(meta.ModelChoice.CapabilityTags) > 0 {
			b.WriteString("- model capability tags: " + strings.Join(meta.ModelChoice.CapabilityTags, ", ") + "\n")
		}
	}
	if meta.ParallelTotal > 1 {
		b.WriteString(fmt.Sprintf("- parallel instance: %d of %d\n", meta.ParallelIndex, meta.ParallelTotal))
		b.WriteString("Coordinate by claiming/creating narrowly scoped tasks or sending mesh status before editing. Prefer distinct file or problem areas to avoid duplicate work.\n")
	}
	if strings.TrimSpace(in.RepoBrief) != "" {
		b.WriteString("## Repository Context (pre-computed)\n")
		b.WriteString(in.RepoBrief)
		b.WriteString("\n\n")
	}
	if strings.TrimSpace(in.autoContext) != "" {
		b.WriteString(in.autoContext)
	}
	b.WriteString("## Objective\n")
	b.WriteString(in.Objective)
	b.WriteString("\n\n")
	if strings.TrimSpace(in.Handoff) != "" {
		b.WriteString("## Handoff\n")
		b.WriteString(in.Handoff)
		b.WriteString("\n\n")
	}
	b.WriteString("## Required final response format\n")
	b.WriteString("STATUS: success | blocked | partial\n")
	b.WriteString("CHANGED: concise file/task summary\n")
	b.WriteString("TESTED: commands run and result, or why not run\n")
	b.WriteString("TOKENS_SAVED: what you handled so the parent did not have to\n")
	b.WriteString("RISKS: remaining risk or follow-up\n")
	b.WriteString("HANDOFF: exact next action for the parent model\n")
	return b.String()
}

func buildDelegationOutputChannels() string {
	b, _ := json.Marshal([]map[string]any{{
		"type":        "mesh",
		"priority":    "high",
		"tags":        "delegation_reply,token_preservation",
		"notify_user": false,
	}})
	return string(b)
}

// dispatchDelegationRun fires the worker on a detached goroutine. The
// run context derives from the daemon's lifecycle context (when wired
// via SetLifecycleContext) so daemon shutdown cancels in-flight
// delegation runs instead of leaking them; context.Background is only
// the fallback for non-daemon callers (tests, stdio mode).
//
// Dispatch failures are recorded into the worker's delegation metadata
// (dispatch_failed + dispatch_error) so ListDelegations stops reporting
// a stuck "dispatched" for a worker whose run never started.
func (s *Service) dispatchDelegationRun(parent context.Context, workerID string, wallSecs int) {
	timeout := time.Duration(wallSecs+60) * time.Second
	if wallSecs <= 0 {
		timeout = time.Duration(defaultDelegationMaxWallClockSeconds+60) * time.Second
	}
	go func() {
		ctx, cancel := context.WithTimeout(s.lifecycleContext(), timeout)
		defer cancel()
		if _, err := s.RunNowWithOpts(ctx, workerID, runner.RunOpts{TriggerKind: "manual"}); err != nil {
			slog.Warn("delegation: detached run failed", "worker_id", workerID, "error", err)
			// Best-effort metadata stamp on a short, shutdown-immune ctx —
			// the run ctx may already be cancelled (that can be WHY the
			// dispatch failed).
			recordCtx, recordCancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			defer recordCancel()
			s.recordDelegationDispatchFailure(recordCtx, workerID, err)
		}
	}()
	_ = parent
}

// recordDelegationDispatchFailure stamps dispatch_failed=true +
// dispatch_error onto the worker's delegation metadata. Best-effort:
// failures are logged, never propagated (the dispatch error itself is
// already logged and there's no caller left to return to).
func (s *Service) recordDelegationDispatchFailure(ctx context.Context, workerID string, dispatchErr error) {
	w, err := s.store.GetWorker(ctx, workerID)
	if err != nil {
		slog.Warn("delegation: record dispatch failure: get worker",
			"worker_id", workerID, "error", err)
		return
	}
	meta, ok := parseDelegationMetadata(w.ParametersJSON)
	if !ok {
		return
	}
	meta.DispatchFailed = true
	meta.DispatchError = dispatchErr.Error()
	params, err := updateDelegationMetadataJSON(w.ParametersJSON, meta)
	if err != nil {
		slog.Warn("delegation: record dispatch failure: marshal metadata",
			"worker_id", workerID, "error", err)
		return
	}
	if _, err := s.Update(ctx, UpdateInput{ID: workerID, ParametersJSON: &params}); err != nil {
		slog.Warn("delegation: record dispatch failure: update worker",
			"worker_id", workerID, "error", err)
	}
}

func (s *Service) ReviewDelegation(ctx context.Context, in DelegationReviewInput) (DelegationContext, error) {
	in.DelegationID = strings.TrimSpace(in.DelegationID)
	in.WorkspaceID = strings.TrimSpace(in.WorkspaceID)
	in.Outcome = normaliseDelegationOutcome(in.Outcome, in.Score)
	in.Notes = strings.TrimSpace(in.Notes)
	in.ReviewerContextID = strings.TrimSpace(in.ReviewerContextID)
	in.ReviewerModel = strings.TrimSpace(in.ReviewerModel)
	in.TaskKind = normaliseDelegationTaskKind(in.TaskKind)
	scores, err := normaliseDelegationReviewScores(in.Scores)
	if err != nil {
		return DelegationContext{}, err
	}
	modelScores, err := normaliseDelegationModelReviews(in.ModelScores)
	if err != nil {
		return DelegationContext{}, err
	}
	if in.DelegationID == "" {
		return DelegationContext{}, errors.New("delegation_id required")
	}
	if in.Score < 0 || in.Score > 100 {
		return DelegationContext{}, errors.New("score must be 0..100")
	}
	review := DelegationReview{
		Reviewed:          true,
		Score:             in.Score,
		Outcome:           in.Outcome,
		Notes:             in.Notes,
		ReviewerContextID: in.ReviewerContextID,
		ReviewerModel:     in.ReviewerModel,
		TaskKind:          in.TaskKind,
		Scores:            scores,
		ModelScores:       modelScores,
		ReviewedAt:        s.clock.Now().UTC(),
	}
	updated := 0
	var workerIDs []string
	rows, err := s.List(ctx, ListInput{WorkspaceID: in.WorkspaceID, NamePattern: delegationWorkerPrefix})
	if err != nil {
		return DelegationContext{}, err
	}
	for _, row := range rows {
		got, err := s.Get(ctx, GetInput{ID: row.ID})
		if err != nil || got == nil || got.Worker == nil {
			continue
		}
		meta, ok := parseDelegationMetadata(got.Worker.ParametersJSON)
		if !ok || meta.ID != in.DelegationID {
			continue
		}
		meta.Review = review
		params, err := updateDelegationMetadataJSON(got.Worker.ParametersJSON, meta)
		if err != nil {
			return DelegationContext{}, err
		}
		if _, err := s.Update(ctx, UpdateInput{ID: got.Worker.ID, ParametersJSON: &params}); err != nil {
			return DelegationContext{}, err
		}
		updated++
		workerIDs = append(workerIDs, got.Worker.ID)
	}
	if updated == 0 {
		return DelegationContext{}, store.ErrNotFound
	}
	s.archiveDelegationWorkerMessages(ctx, workerIDs)
	s.releaseDelegationFileClaims(ctx, in.DelegationID)
	delegations, err := s.ListDelegations(ctx, DelegationListInput{WorkspaceID: in.WorkspaceID, Limit: 200})
	if err != nil {
		return DelegationContext{}, err
	}
	for _, d := range delegations {
		if d.ID == in.DelegationID {
			return d, nil
		}
	}
	return DelegationContext{}, store.ErrNotFound
}

func normaliseDelegationOutcome(raw string, score int) string {
	raw = strings.ToLower(strings.TrimSpace(raw))
	switch raw {
	case "accepted", "partial", "rejected":
		return raw
	}
	if score >= 80 {
		return "accepted"
	}
	if score >= 50 {
		return "partial"
	}
	return "rejected"
}

func updateDelegationMetadataJSON(params string, meta delegationMetadata) (string, error) {
	env := map[string]any{}
	if strings.TrimSpace(params) != "" {
		if err := json.Unmarshal([]byte(params), &env); err != nil {
			return "", fmt.Errorf("parse parameters_json: %w", err)
		}
	}
	env[delegationMetaKey] = meta
	b, err := json.Marshal(env)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func parseDelegationMetadata(params string) (delegationMetadata, bool) {
	var env map[string]json.RawMessage
	if err := json.Unmarshal([]byte(params), &env); err != nil {
		return delegationMetadata{}, false
	}
	raw, ok := env[delegationMetaKey]
	if !ok {
		return delegationMetadata{}, false
	}
	var meta delegationMetadata
	if err := json.Unmarshal(raw, &meta); err != nil {
		return delegationMetadata{}, false
	}
	if meta.ID == "" {
		return delegationMetadata{}, false
	}
	if meta.ParallelTotal == 0 {
		meta.ParallelTotal = 1
	}
	if meta.ParallelIndex == 0 {
		meta.ParallelIndex = 1
	}
	if meta.WorkerMode == "" {
		meta.WorkerMode = "execute"
	}
	if meta.WorkerIsolation == "" {
		meta.WorkerIsolation = "worktree"
	}
	if !meta.Review.Reviewed && !meta.Review.ReviewedAt.IsZero() {
		meta.Review.Reviewed = true
	}
	return meta, true
}

func aggregateDelegation(d DelegationContext) (DelegationAggregate, string) {
	agg := DelegationAggregate{
		Workers:             len(d.Workers),
		QuotaTokensByBucket: map[string]int{},
	}
	status := "dispatched"
	knownCostRuns := 0
	usedClaude := false
	hasPostExecuteBlock := false
	for _, w := range d.Workers {
		// A detached dispatch failure is authoritative even if a raced/stale
		// run row exists. The model never ran, so do not attribute that row's
		// status, usage, or duration to the delegation result.
		if w.DispatchFailed {
			agg.Failure++
			continue
		}
		run := w.LatestRun
		if run == nil {
			agg.Dispatched++
			continue
		}
		switch run.Status {
		case "running", "awaiting_approval":
			agg.Running++
		case "success":
			agg.Success++
		case "failure", "cap_exceeded", "paused", "rejected":
			agg.Failure++
		case "blocked":
			agg.Blocked++
			if isPostExecuteBlocked(run) {
				hasPostExecuteBlock = true
			}
		case "cancelled":
			agg.Cancelled++
		case "interrupted":
			agg.Interrupted++
		default:
			agg.Dispatched++
		}
		agg.InputTokens += run.InputTokens
		agg.OutputTokens += run.OutputTokens
		agg.CostUSD += run.CostUSD
		agg.ToolCalls += run.ToolCallsCount
		agg.DurationMS += run.DurationMS
		provider := strings.TrimSpace(run.ModelProvider)
		if provider == "" && w.Worker != nil {
			provider = strings.TrimSpace(w.Worker.ModelProvider)
		}
		if delegationRunAccountingMissing(run, provider) {
			agg.UnknownCostRuns++
			agg.UnknownDurationMS += run.DurationMS
		} else if run.InputTokens != 0 || run.OutputTokens != 0 || run.CostUSD != 0 {
			// Failed/capped/blocked runs can still carry real adapter usage.
			// Count that evidence so one telemetry-less CLI sibling does not
			// make a mixed fan-out look as though all cost data is missing.
			knownCostRuns++
		}
		tok := run.InputTokens + run.OutputTokens
		real := models.RealCostUSD(run.ModelProvider, run.ModelID, run.InputTokens, run.OutputTokens, run.CostUSD)
		agg.RealDollarsSpent += real
		cl := models.ClassifyBilling(run.ModelProvider, run.ModelID)
		if cl.Model == models.BillingSubscription {
			agg.QuotaTokensByBucket[string(cl.Bucket)] += tok
			if cl.Bucket == models.BucketClaude {
				agg.FrontierQuotaBurned += tok
				usedClaude = true
			}
		}
	}
	agg.CostAllMissing = agg.UnknownCostRuns > 0 && knownCostRuns == 0
	agg.TotalTokens = agg.InputTokens + agg.OutputTokens
	agg.ParentTokens = d.Parent.InputTokens + d.Parent.OutputTokens
	agg.CombinedTokens = agg.ParentTokens + agg.TotalTokens
	agg.ParentCostUSD = d.Parent.CostUSD
	agg.CombinedCostUSD = agg.ParentCostUSD + agg.CostUSD
	agg.BaselineTokens = d.Baseline.TokensEstimate
	agg.BaselineCostUSD = d.Baseline.CostUSD
	agg.ParentTokensKnown = agg.ParentTokens > 0
	agg.SavingsBasis = "missing_baseline"
	agg.SavingsConfidence = "missing"
	if agg.BaselineTokens > 0 {
		agg.FrontierTokensAvoided = agg.BaselineTokens
		agg.WorkerTokenDelta = agg.BaselineTokens - agg.TotalTokens
		agg.EstimatedParentTokensSaved = agg.FrontierTokensAvoided
		agg.NetTokensDelta = agg.WorkerTokenDelta
		agg.SavingsBasis = "baseline_estimate"
		agg.SavingsConfidence = "estimated"
	}
	if agg.BaselineCostUSD > 0 {
		agg.FrontierCostAvoidedUSD = agg.BaselineCostUSD
		agg.EstimatedCostSavedUSD = agg.BaselineCostUSD - agg.CostUSD
		agg.SavingsBasis = "baseline_estimate"
		agg.SavingsConfidence = "estimated"
	}
	// When ALL worker cost is from runs whose adapter reported no usage
	// telemetry, the $0 CostUSD is missing data, not free compute, so
	// any cost-saved math overstates savings. Demote confidence.
	if agg.CostAllMissing {
		if agg.BaselineCostUSD > 0 {
			agg.EstimatedCostSavedUSD = 0
		}
		agg.SavingsConfidence = "missing"
	}
	if agg.BaselineTokens == 0 && agg.BaselineCostUSD == 0 && agg.ParentTokensKnown {
		agg.SavingsBasis = "parent_usage_only"
	}
	agg.ReviewScore = d.Review.Score

	if usedClaude {
		agg.FrontierQuotaPreserved = 0
	} else {
		agg.FrontierQuotaPreserved = agg.BaselineTokens
	}
	agg.RealCostSavedUSD = agg.BaselineCostUSD - agg.RealDollarsSpent

	// hasRun distinguishes "no worker run row exists yet" (freshly dispatched/queued)
	// from "at least one run has started or finished". needs_review must only be
	// reported after a run exists; otherwise wait_for_delegation would return
	// premature terminal needs_review before the worker even starts.
	hasRun := false
	for _, w := range d.Workers {
		if !w.DispatchFailed && w.LatestRun != nil {
			hasRun = true
			break
		}
	}

	if agg.Running > 0 {
		status = "running"
	} else if agg.Dispatched > 0 {
		// At least one worker has not started yet. Do not let an early failure
		// or output rejection from a sibling make the whole fan-out appear
		// terminal while useful work is still pending.
		status = "dispatched"
	} else if agg.Failure > 0 {
		// An all-operational failure never reached the model — every worker
		// died at the adapter/launch stage with zero tokens either way. There
		// is no model output for a parent to score, so gating it into
		// needs_review parks the delegation forever demanding a quality
		// judgement about a model that produced nothing. Same discrimination
		// the Blocked branch below already makes via hasPostExecuteBlock, and
		// the same predicate modelStatsForDelegation uses to keep launch
		// crashes out of model ranking. A MIX of operational and genuine
		// failures is not operational-only, so it still honours the gate.
		if d.ReviewRequired && !d.Review.Reviewed && hasRun &&
			!delegationIsOperationalOnly(d.Workers) {
			status = "needs_review"
		} else if agg.Success > 0 {
			status = "partial"
		} else {
			status = "failure"
		}
	} else if agg.Blocked > 0 {
		if d.ReviewRequired && !d.Review.Reviewed && (agg.Success > 0 || hasPostExecuteBlock) {
			// A success or post-execute rejection reached the model and must
			// honor the review gate. Pre/legacy blocks did not run the model,
			// so an all-pre-blocked delegation remains simply blocked.
			status = "needs_review"
		} else if agg.Success > 0 {
			status = "partial"
		} else {
			status = "blocked"
		}
	} else if agg.Cancelled > 0 {
		// Operator hard-stopped one or more workers and nothing failed.
		// Cancelled is terminal and is NOT a failure, so it does NOT gate
		// into needs_review. A mix of success + cancelled is "partial";
		// all-cancelled is its own terminal "cancelled".
		if agg.Success > 0 {
			status = "partial"
		} else {
			status = "cancelled"
		}
	} else if agg.Interrupted > 0 {
		if agg.Success > 0 {
			status = "partial"
		} else {
			status = "interrupted"
		}
	} else if d.ReviewRequired && !d.Review.Reviewed && hasRun {
		status = "needs_review"
	} else if agg.Success == agg.Workers && agg.Workers > 0 {
		status = "success"
	}
	return agg, status
}

func modelStatsForDelegation(d DelegationContext) []DelegationModelStat {
	byKey := map[string]*DelegationModelStat{}
	// modelWorkers tracks, per model key, the set of worker contexts that
	// contributed to that stat in this delegation. We need the original
	// worker context (not just the stat) at the bottom of this function
	// to decide whether every worker was operational-only, so we keep a
	// parallel slice here. (A worker belongs to exactly one stat by
	// model key — see provider/modelID assignment above.)
	modelWorkers := map[string][]DelegationWorkerContext{}
	for _, w := range d.Workers {
		provider := ""
		modelID := ""
		if w.Worker != nil {
			provider = strings.TrimSpace(w.Worker.ModelProvider)
			modelID = strings.TrimSpace(w.Worker.ModelID)
		}
		if w.LatestRun != nil {
			if strings.TrimSpace(w.LatestRun.ModelProvider) != "" {
				provider = strings.TrimSpace(w.LatestRun.ModelProvider)
			}
			if strings.TrimSpace(w.LatestRun.ModelID) != "" {
				modelID = strings.TrimSpace(w.LatestRun.ModelID)
			}
		}
		if provider == "" && modelID == "" {
			continue
		}
		key := provider + "/" + modelID
		stat := byKey[key]
		if stat == nil {
			stat = &DelegationModelStat{
				ModelProvider: provider,
				ModelID:       modelID,
				ModelKey:      key,
				TaskKind:      d.TaskKind,
				TaskShape:     d.TaskShape,
			}
			byKey[key] = stat
		}
		if w.Worker != nil && strings.TrimSpace(w.Worker.ID) != "" {
			stat.WorkerIDs = append(stat.WorkerIDs, w.Worker.ID)
		}
		modelWorkers[key] = append(modelWorkers[key], w)
		// DispatchFailed is authoritative even if a raced/stale run row is
		// present. The model never ran, so record a dispatch-only reliability
		// attempt and exclude the row's status, usage, and duration.
		if w.DispatchFailed {
			stat.OperationalFailures++
			stat.DispatchFailures++
			continue
		}
		run := w.LatestRun
		if run == nil {
			continue
		}
		// A pre-execute (or otherwise non-post) block did not invoke the
		// model. Keep it visible in delegation aggregation but exclude it from
		// model runs, accounting, exploration settlement, and review quality.
		if run.Status == "blocked" && !isPostExecuteBlocked(run) {
			continue
		}
		// Operator hard-stops are fully excluded from this model's rank
		// stats: counted separately and NOT folded into Runs / Success /
		// Failure or the token/cost roll-up. A human cancelling a run
		// must not move the model's success rate, failure penalty, or
		// per-run cost in EITHER direction. Delegation-level real spend
		// is still reflected in aggregateDelegation.
		if run.Status == "cancelled" {
			stat.Cancelled++
			continue
		}
		if run.Status == "interrupted" {
			stat.Interrupted++
			continue
		}
		// Operational failures (adapter/launch died before the model
		// produced any output) ARE counted in Runs/Failure — a launch
		// failure is still a real attempt that didn't deliver compute —
		// but the operational counter surfaces the distinction for the
		// operator. Most importantly, when EVERY worker of a model in
		// this delegation was operational-only, the review score
		// attribution below is suppressed so the parent's "no usable
		// output" judgement doesn't poison the per-model avg.
		//
		isOperational := isOperationalFailure(run)
		stat.Runs++
		switch run.Status {
		case "running", "awaiting_approval":
			stat.Running++
		case "success":
			stat.Success++
			if !isOperational {
				stat.QualitySuccess++
			}
		case "failure":
			stat.Failure++
			if !isOperational {
				stat.QualityFailure++
			}
		case "cap_exceeded":
			stat.Failure++
			stat.BudgetFailures++
		case "blocked":
			stat.Failure++
			stat.DeliverabilityFailures++
		case "paused", "rejected":
			// Policy/operator outcomes remain visible in the legacy Failure
			// total but do not describe model coding quality.
			stat.Failure++
		}
		if run.Status == "running" || run.Status == "awaiting_approval" {
			// In-flight rows are attempts for concurrency/exploration, but they
			// have no terminal accounting sample yet. Exclude any partial row
			// values from cost and speed statistics until the run finalizes.
			continue
		}
		if isOperational {
			stat.OperationalFailures++
			stat.OperationalDurationMS += run.DurationMS
		}
		stat.InputTokens += run.InputTokens
		stat.OutputTokens += run.OutputTokens
		stat.CostUSD += run.CostUSD
		stat.DurationMS += run.DurationMS
		if delegationRunAccountingMissing(run, provider) {
			stat.UnknownCostRuns++
			stat.UnknownDurationMS += run.DurationMS
			if run.Status == "success" {
				stat.UnknownSuccessRuns++
			}
		}
	}

	out := make([]DelegationModelStat, 0, len(byKey))
	for _, stat := range byKey {
		stat.TotalTokens = stat.InputTokens + stat.OutputTokens
		if qTerminal := stat.QualitySuccess + stat.QualityFailure; qTerminal > 0 {
			stat.QualityRate = float64(stat.QualitySuccess) / float64(qTerminal)
		}
		if terminal := stat.Success + stat.Failure + stat.DispatchFailures; terminal > 0 {
			opFails := stat.OperationalFailures
			if opFails > terminal {
				opFails = terminal
			}
			stat.ReliabilityRate = 1 - float64(opFails)/float64(terminal)
		} else {
			stat.ReliabilityRate = 1
		}
		if stat.Runs > 0 {
			runOperationalFailures := stat.OperationalFailures - stat.DispatchFailures
			if runOperationalFailures < 0 {
				runOperationalFailures = 0
			}
			if known := stat.Runs - stat.Running - stat.UnknownCostRuns - runOperationalFailures; known > 0 {
				duration := stat.DurationMS - stat.UnknownDurationMS - stat.OperationalDurationMS
				if duration < 0 {
					duration = 0
				}
				stat.AvgDurationMS = duration / int64(known)
			} else {
				stat.AvgDurationMS = 0
			}
		}
		if d.Review.Reviewed {
			// Suppress review attribution when every worker matching
			// this model key in this delegation was operational-only.
			// The model never produced any output, so a parent score
			// of (e.g.) 20 is a judgement about the adapter/launch,
			// not about model quality — folding it into the per-model
			// avg would corrupt capacity ranking for every model that
			// ever had a launch crash. The counter above preserves the
			// data for the operator; the review simply doesn't move
			// the quality number.
			//
			// Mixed delegations (one operational worker + one
			// success worker on the same model key) ARE attributable:
			// the model did run on the success worker, so the parent's
			// review applies to the model's quality. The pathological
			// case is rare in practice (most operational failures
			// affect the whole delegation, not a single worker).
			if !delegationIsOperationalOnly(modelWorkers[stat.ModelKey]) {
				review := reviewForDelegationModel(d.Review, stat.ModelKey, stat.WorkerIDs)
				stat.ReviewCount = 1
				stat.ReviewScore = review.Score
				stat.CapabilityScores = review.Scores
			}
			if d.Review.TaskKind != "" {
				stat.TaskKind = d.Review.TaskKind
			}
		}
		out = append(out, *stat)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].ModelKey < out[j].ModelKey
	})
	return out
}

func delegationReviewRequired(in DelegationInput) bool {
	if in.ReviewRequired == nil {
		return false
	}
	return *in.ReviewRequired
}

func delegationMetadataReviewRequired(meta delegationMetadata) bool {
	if meta.ReviewRequired == nil {
		return false
	}
	return *meta.ReviewRequired
}

func delegationProviderIgnoresScope(provider string) bool {
	return provider == providerClaudeCLI || provider == providerOpenCodeCLI ||
		provider == providerGrokCLI || provider == providerMiMoCLI ||
		provider == providerPiCLI
}

func (s *Service) firstAuthScopeID(ctx context.Context) string {
	if s.authScopes == nil {
		return ""
	}
	scopes, err := s.authScopes.ListAuthScopes(ctx)
	if err != nil || len(scopes) == 0 {
		return ""
	}
	for _, sc := range scopes {
		if !looksLikeUUID(sc.ID) {
			return sc.ID
		}
	}
	return scopes[0].ID
}

func looksLikeUUID(id string) bool {
	_, err := uuid.Parse(id)
	return err == nil
}

func delegationWorkerName(base, objective, delegationID string, idx, total int) string {
	slug := slugForWorkerName(base)
	if slug == "" {
		slug = slugForWorkerName(objective)
	}
	if slug == "" {
		slug = "task"
	}
	if len(slug) > 36 {
		slug = strings.Trim(slug[:36], "-")
	}
	short := delegationID
	if len(short) > 12 {
		short = short[len(short)-12:]
	}
	if total > 1 {
		return fmt.Sprintf("%s%s-%02d-of-%02d-%s", delegationWorkerPrefix, slug, idx, total, short)
	}
	return fmt.Sprintf("%s%s-%s", delegationWorkerPrefix, slug, short)
}

func slugForWorkerName(in string) string {
	in = strings.ToLower(strings.TrimSpace(in))
	var b strings.Builder
	lastDash := false
	for _, r := range in {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if ok {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

// estimateBriefTokens returns a rough token count for a repo brief string.
// Uses ~4 chars per token as a heuristic (standard for English prose + code).
func estimateBriefTokens(brief string) int {
	if brief == "" {
		return 0
	}
	return (len(brief) + 3) / 4
}

// truncateMarker is appended in place of dropped bytes so the worker can
// see that its context was clipped rather than silently ending mid-thought.
const truncateMarker = "\n\n[…truncated by mcplexer: %s exceeded %d bytes, %d bytes (~%d tokens) dropped. Ask the parent for the rest, or fetch it from the task work-context.]"

// truncateSection clips s to at most maxBytes, appending a marker that names
// the field and the drop size. The cut is UTF-8-safe (never splits a rune) and
// prefers the last newline in the final 10% of the kept window so the worker
// reads whole lines. Returns the (possibly clipped) string and a warning that
// is empty when nothing was dropped.
func truncateSection(field, s string, maxBytes int) (string, string) {
	if maxBytes <= 0 || len(s) <= maxBytes {
		return s, ""
	}
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	// Snap back to a line boundary when one is close, so the tail of the
	// kept window isn't a half-sentence.
	if nl := strings.LastIndexByte(s[:cut], '\n'); nl > cut-maxBytes/10 {
		cut = nl + 1 // keep the newline so the last kept line is whole
	}
	dropped := len(s) - cut
	warn := fmt.Sprintf(
		"%s truncated: %d bytes supplied, %d byte cap, %d bytes (~%d tokens) dropped — "+
			"the worker will not see the remainder",
		field, len(s), maxBytes, dropped, (dropped+3)/4,
	)
	return s[:cut] + fmt.Sprintf(truncateMarker, field, maxBytes, dropped, (dropped+3)/4), warn
}

func (s *Service) archiveDelegationWorkerMessages(ctx context.Context, workerIDs []string) {
	if s.meshStore == nil || len(workerIDs) == 0 {
		return
	}
	senderIDs := make([]string, len(workerIDs))
	for i, wid := range workerIDs {
		senderIDs[i] = "worker:" + wid
	}
	archived, err := s.meshStore.ArchiveMessagesBySenderAndKinds(ctx, senderIDs, []string{"finding", "reply"})
	if err != nil {
		slog.Warn("delegation: auto-ack worker messages failed",
			"error", err, "worker_count", len(workerIDs))
		return
	}
	if archived > 0 {
		slog.Info("delegation: auto-acked worker mesh messages",
			"archived", archived, "worker_count", len(workerIDs))
	}
}
