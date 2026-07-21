package admin

import (
	"context"
	crand "crypto/rand"
	"errors"
	"fmt"
	"math"
	"math/big"
	"sort"
	"strings"
	"time"
)

const (
	delegationModelSelectionSingle     = "single"
	delegationModelSelectionRanked     = "ranked"
	delegationModelSelectionRandom     = "random"
	delegationModelSelectionSideBySide = "side_by_side"
	delegationModelSelectionCapacity   = "capacity"
)

const (
	// explorationWeight drives an informational UCB-style signal surfaced to
	// callers. It is deliberately NOT folded into default capacity ranking:
	// production capacity must exploit reviewed evidence, while explicit
	// side_by_side/random delegations are the safe place to explore a new ID.
	explorationWeight = 60.0
	// explorationSettledRuns is the run count past which a candidate is no
	// longer "under-sampled": the exploration flag clears and the decayed
	// bonus is small. Used to surface a "new / promising" marker on the
	// capacity row while a candidate is still in the explore phase.
	explorationSettledRuns = 8
	// explorationFailureCutoff is the anti-thrash guard. Once a candidate
	// has accumulated this many operational (adapter/launch) failures with
	// no successful run, the exploration bonus is suppressed entirely so a
	// genuinely-broken model (e.g. the mimo 400 "Not supported model"
	// dispatch deaths) stops being force-explored forever.
	explorationFailureCutoff = 3
	// operationalQuarantineFailureCutoff is the hard capacity circuit breaker.
	// Anti-thrash handles "never succeeded" models; this handles mostly-bad
	// transports with occasional successes (the shape that keeps re-entering
	// capacity waves and burning parallelism). When operational failures are at
	// least this count AND at least half of terminal attempts, capacity mode
	// stops selecting the model until healthier runs dilute the ratio.
	operationalQuarantineFailureCutoff = 5
	// reviewConfidencePriorStrength shrinks tiny review samples toward the
	// neutral capacity prior. One unusually good review must not outrank a
	// model with dozens of independently-reviewed successful runs.
	reviewConfidencePriorStrength = 4.0
)

func (s *Service) resolveDelegationModelCandidates(
	ctx context.Context,
	in *DelegationInput,
) ([]delegationResolvedModelCandidate, error) {
	mode := strings.ToLower(strings.TrimSpace(in.ModelSelectionMode))
	if mode == "" {
		if len(in.ModelCandidates) > 0 {
			mode = delegationModelSelectionSideBySide
		} else {
			mode = delegationModelSelectionSingle
		}
	}
	switch mode {
	case delegationModelSelectionSingle,
		delegationModelSelectionRanked,
		delegationModelSelectionRandom,
		delegationModelSelectionSideBySide,
		delegationModelSelectionCapacity:
	default:
		return nil, errors.New("model_selection_mode must be single, ranked, random, side_by_side, or capacity")
	}
	in.ModelSelectionMode = mode

	raw := in.ModelCandidates
	if len(raw) == 0 && mode == delegationModelSelectionCapacity {
		var err error
		raw, err = s.registeredDelegationModelCandidates(ctx, in)
		if err != nil {
			return nil, err
		}
		if len(raw) == 0 {
			return nil, errors.New("no registered delegation model candidates; " +
				"create a worker model profile in Workers > Model Profiles, " +
				"create an enabled worker with a model provider/model id, " +
				"or pass model_provider and model_id explicitly. " +
				"use mcpx__list_delegation_model_capacity to verify candidates before using model_selection_mode:\"capacity\"")
		}
	}
	if len(raw) == 0 {
		if delegationModelSelectionMissing(in) {
			return nil, errors.New("delegation model required; " +
				"pass model_profile_id, pass model_provider and model_id explicitly, " +
				"or register a worker model profile and set model_selection_mode:\"capacity\". " +
				"use mcpx__list_delegation_model_capacity to see whether this workspace has selectable models")
		}
		raw = []DelegationModelCandidate{{
			ModelProfileID:   in.ModelProfileID,
			ModelProvider:    in.ModelProvider,
			ModelID:          in.ModelID,
			ModelEndpointURL: in.ModelEndpointURL,
			SecretScopeID:    in.SecretScopeID,
		}}
	}
	resolved := make([]delegationResolvedModelCandidate, 0, len(raw))
	for i, candidate := range raw {
		c, err := s.resolveDelegationModelCandidate(ctx, candidate, i, len(raw))
		if err != nil {
			return nil, fmt.Errorf("model candidate %d: %w", i, err)
		}
		if isProviderGroupDisabled(in.DisabledProviders, c.ModelProvider, c.ModelID, c.ModelEndpointURL, c.Label) {
			return nil, fmt.Errorf("model candidate %d: provider %q is disabled by operator (delegation_disabled_providers)", i, c.ModelProvider)
		}
		resolved = append(resolved, c)
	}
	if len(resolved) == 0 {
		return nil, errors.New("at least one model candidate required")
	}
	if mode == delegationModelSelectionRanked {
		idx, err := s.bestReviewedDelegationCandidate(ctx, in.WorkspaceID, in.TaskKind, resolved)
		if err != nil {
			return nil, err
		}
		resolved = []delegationResolvedModelCandidate{resolved[idx]}
	}
	if mode == delegationModelSelectionCapacity {
		available := make([]delegationResolvedModelCandidate, 0, len(resolved))
		var firstErr error
		ranks, err := s.delegationCandidateRanks(ctx, in.WorkspaceID, in.TaskKind)
		if err != nil {
			return nil, err
		}
		for _, c := range resolved {
			if err := validateModelProvider(c.ModelProvider, c.ModelEndpointURL); err != nil {
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			if rank := ranks[c.ModelProvider+"/"+c.ModelID]; rank != nil && rank.operationalQuarantined() {
				if firstErr == nil {
					firstErr = fmt.Errorf("model %q quarantined after repeated operational failures", c.ModelProvider+"/"+c.ModelID)
				}
				continue
			}
			available = append(available, c)
		}
		if len(available) == 0 {
			if firstErr != nil {
				return nil, firstErr
			}
			return nil, errors.New("no available capacity model candidates")
		}
		resolved = available
		resolved, err = s.rankCapacityDelegationCandidates(ctx, in.WorkspaceID, in.TaskKind, resolved)
		if err != nil {
			return nil, err
		}
	}
	if mode == delegationModelSelectionSingle {
		idx := in.ModelCandidateIndex
		if idx < 0 || idx >= len(resolved) {
			return nil, fmt.Errorf("model_candidate_index must be 0..%d", len(resolved)-1)
		}
		resolved = []delegationResolvedModelCandidate{resolved[idx]}
	}
	return resolved, nil
}

func delegationModelSelectionMissing(in *DelegationInput) bool {
	if in == nil {
		return true
	}
	return strings.TrimSpace(in.ModelProfileID) == "" &&
		strings.TrimSpace(in.ModelProvider) == "" &&
		strings.TrimSpace(in.ModelID) == "" &&
		strings.TrimSpace(in.ModelEndpointURL) == "" &&
		strings.TrimSpace(in.SecretScopeID) == ""
}

func (s *Service) registeredDelegationModelCandidates(
	ctx context.Context,
	in *DelegationInput,
) ([]DelegationModelCandidate, error) {
	out := make([]DelegationModelCandidate, 0)
	seen := map[string]struct{}{}
	if err := s.appendWorkerDelegationModelCandidates(ctx, in, &out, seen); err != nil {
		return nil, err
	}
	if err := s.appendProfileDelegationModelCandidates(ctx, &out, seen); err != nil {
		return nil, err
	}
	if len(out) == 0 && (strings.TrimSpace(in.ModelProvider) != "" || strings.TrimSpace(in.ModelID) != "") {
		addDelegationModelCandidate(&out, seen, DelegationModelCandidate{
			ModelProfileID:   in.ModelProfileID,
			ModelProvider:    in.ModelProvider,
			ModelID:          in.ModelID,
			ModelEndpointURL: in.ModelEndpointURL,
			SecretScopeID:    in.SecretScopeID,
		})
	}
	// Apply operator disables (from settings via handler) so disabled
	// groups never become selectable/ranked candidates.
	if len(in.DisabledProviders) > 0 {
		filtered := make([]DelegationModelCandidate, 0, len(out))
		for _, c := range out {
			if isProviderGroupDisabled(in.DisabledProviders, c.ModelProvider, c.ModelID, c.ModelEndpointURL, c.Label) {
				continue
			}
			filtered = append(filtered, c)
		}
		out = filtered
	}
	return out, nil
}

func (s *Service) appendProfileDelegationModelCandidates(
	ctx context.Context,
	out *[]DelegationModelCandidate,
	seen map[string]struct{},
) error {
	if s.modelProfiles == nil {
		return nil
	}
	profiles, err := s.modelProfiles.ListModelProfiles(ctx)
	if err != nil {
		return fmt.Errorf("list model profiles: %w", err)
	}
	for _, p := range profiles {
		for _, modelID := range p.KnownModels {
			modelID = strings.TrimSpace(modelID)
			if modelID == "" {
				continue
			}
			addDelegationModelCandidate(out, seen, DelegationModelCandidate{
				Label:            p.Name,
				ModelProfileID:   p.ID,
				ModelProvider:    p.Provider,
				ModelID:          modelID,
				ModelEndpointURL: p.EndpointURL,
				SecretScopeID:    p.SecretScopeID,
			})
		}
	}
	return nil
}

func (s *Service) resolveDelegationModelCandidate(
	ctx context.Context,
	c DelegationModelCandidate,
	index int,
	total int,
) (delegationResolvedModelCandidate, error) {
	c.Label = strings.TrimSpace(c.Label)
	c.ModelProfileID = strings.TrimSpace(c.ModelProfileID)
	c.ModelProvider = strings.TrimSpace(c.ModelProvider)
	c.ModelID = strings.TrimSpace(c.ModelID)
	c.ModelEndpointURL = strings.TrimSpace(c.ModelEndpointURL)
	c.SecretScopeID = strings.TrimSpace(c.SecretScopeID)
	c.CapabilityTags = normaliseStringList(c.CapabilityTags)
	c.InputModalities = normaliseStringList(c.InputModalities)
	c.OutputModalities = normaliseStringList(c.OutputModalities)
	if c.ModelProfileID != "" {
		if s.modelProfiles == nil {
			return delegationResolvedModelCandidate{}, errors.New("model profile store not available")
		}
		p, err := s.modelProfiles.GetModelProfile(ctx, c.ModelProfileID)
		if err != nil {
			return delegationResolvedModelCandidate{}, fmt.Errorf("get model profile: %w", err)
		}
		if c.ModelProvider == "" {
			c.ModelProvider = p.Provider
		}
		if c.ModelEndpointURL == "" {
			c.ModelEndpointURL = p.EndpointURL
		}
		if c.SecretScopeID == "" {
			c.SecretScopeID = p.SecretScopeID
		}
		if c.ModelID == "" && len(p.KnownModels) > 0 {
			c.ModelID = p.KnownModels[0]
		}
	}
	if c.ModelProvider == "" {
		return delegationResolvedModelCandidate{}, errors.New(
			"model_provider required. Example: {\"objective\":\"summarise recent changes\", \"model_provider\":\"opencode_cli\", \"model_id\":\"minimax/MiniMax-M3\"}")
	}
	if c.ModelID == "" {
		return delegationResolvedModelCandidate{}, errors.New(
			"model_id required. Example: {\"objective\":\"summarise recent changes\", \"model_provider\":\"opencode_cli\", \"model_id\":\"minimax/MiniMax-M3\"}")
	}
	if c.SecretScopeID == "" && delegationProviderIgnoresScope(c.ModelProvider) {
		c.SecretScopeID = s.firstAuthScopeID(ctx)
	}
	if c.SecretScopeID == "" {
		return delegationResolvedModelCandidate{}, errors.New(
			"secret_scope_id required. Example: {\"objective\":\"summarise recent changes\", \"model_provider\":\"anthropic\", \"model_id\":\"claude-sonnet-4-5\", \"secret_scope_id\":\"scope-anthropic-prod\"}")
	}
	return delegationResolvedModelCandidate{
		DelegationModelCandidate: c,
		Index:                    index,
		Total:                    total,
	}, nil
}

// delegationPlanSize returns the exact number of workers
// buildDelegationModelPlan will dispatch for this input, without building
// the plan. Kept adjacent to buildDelegationModelPlan so the two stay in
// lock-step: random and capacity fan out to parallelism workers, every
// other mode replicates all candidates parallelism times.
func delegationPlanSize(in DelegationInput) int {
	p := maxInt(1, in.Parallelism)
	switch in.ModelSelectionMode {
	case delegationModelSelectionRandom, delegationModelSelectionCapacity:
		return p
	default:
		return len(in.resolvedModelCandidates) * p
	}
}

func buildDelegationModelPlan(in DelegationInput) []delegationResolvedModelCandidate {
	if in.ModelSelectionMode == delegationModelSelectionRandom {
		out := make([]delegationResolvedModelCandidate, 0, in.Parallelism)
		for i := 0; i < in.Parallelism; i++ {
			out = append(out, in.resolvedModelCandidates[randomIndex(len(in.resolvedModelCandidates))])
		}
		return out
	}
	if in.ModelSelectionMode == delegationModelSelectionCapacity {
		out := make([]delegationResolvedModelCandidate, 0, in.Parallelism)
		for i := 0; i < maxInt(1, in.Parallelism); i++ {
			out = append(out, in.resolvedModelCandidates[i%len(in.resolvedModelCandidates)])
		}
		return out
	}
	out := make([]delegationResolvedModelCandidate, 0,
		len(in.resolvedModelCandidates)*maxInt(1, in.Parallelism))
	for replicate := 0; replicate < maxInt(1, in.Parallelism); replicate++ {
		out = append(out, in.resolvedModelCandidates...)
	}
	return out
}

func (s *Service) bestReviewedDelegationCandidate(
	ctx context.Context,
	workspaceID string,
	taskKind string,
	candidates []delegationResolvedModelCandidate,
) (int, error) {
	byKey, err := s.delegationCandidateRanks(ctx, workspaceID, taskKind)
	if err != nil {
		return 0, err
	}
	best := -1
	var bestRank *delegationCandidateRank
	for i, c := range candidates {
		r := byKey[c.ModelProvider+"/"+c.ModelID]
		if r == nil || r.reviews == 0 {
			continue
		}
		if r.operationalQuarantined() {
			continue
		}
		if bestRank == nil || r.betterThan(bestRank, taskKind) {
			best = i
			bestRank = r
		}
	}
	if best < 0 {
		return 0, errors.New("ranked model selection requires at least one reviewed, non-quarantined candidate")
	}
	return best, nil
}

func (s *Service) rankCapacityDelegationCandidates(
	ctx context.Context,
	workspaceID string,
	taskKind string,
	candidates []delegationResolvedModelCandidate,
) ([]delegationResolvedModelCandidate, error) {
	byKey, err := s.delegationCandidateRanks(ctx, workspaceID, taskKind)
	if err != nil {
		return nil, err
	}
	out := append([]delegationResolvedModelCandidate(nil), candidates...)
	sort.SliceStable(out, func(i, j int) bool {
		left := capacityScoreForCandidate(out[i], byKey[out[i].ModelProvider+"/"+out[i].ModelID], taskKind)
		right := capacityScoreForCandidate(out[j], byKey[out[j].ModelProvider+"/"+out[j].ModelID], taskKind)
		if left != right {
			return left > right
		}
		return out[i].ModelProvider+"/"+out[i].ModelID < out[j].ModelProvider+"/"+out[j].ModelID
	})
	return out, nil
}

func (s *Service) delegationCandidateRanks(
	ctx context.Context,
	workspaceID string,
	taskKind string,
) (map[string]*delegationCandidateRank, error) {
	rows, err := s.ListDelegations(ctx, DelegationListInput{WorkspaceID: workspaceID, Limit: 200})
	if err != nil {
		return nil, fmt.Errorf("load delegation ranking history: %w", err)
	}
	byKey := map[string]*delegationCandidateRank{}
	// Collect review observations (with timestamps from delegation) for EWMA.
	// ListDelegations returns newest-first (by UpdatedAt then re-sort); we use
	// Review.ReviewedAt (fallback UpdatedAt) + collection order for recency.
	obsByKey := map[string][]reviewObs{}
	categoryObsByKey := map[string]map[string][]reviewObs{}
	for _, row := range rows {
		for _, stat := range row.ModelStats {
			key := stat.ModelKey
			r := byKey[key]
			if r == nil {
				r = &delegationCandidateRank{}
				byKey[key] = r
			}
			if stat.ReviewCount > 0 {
				r.reviewCount += stat.ReviewCount
				weight := stat.ReviewCount
				if taskKind != "" && stat.TaskKind == taskKind {
					weight *= 2
				}
				r.scoreTotal += stat.ReviewScore * weight
				r.reviews += weight

				at := row.Review.ReviewedAt
				if at.IsZero() {
					at = row.UpdatedAt
				}
				obsByKey[key] = append(obsByKey[key], reviewObs{
					score:      float64(stat.ReviewScore),
					weight:     weight,
					reviewedAt: at,
				})
				for category, score := range stat.CapabilityScores {
					category = normaliseDelegationTaskKind(category)
					if category == "" {
						continue
					}
					categoryWeight := stat.ReviewCount
					if stat.TaskKind == category {
						categoryWeight *= 2
					}
					byCategory := categoryObsByKey[key]
					if byCategory == nil {
						byCategory = map[string][]reviewObs{}
						categoryObsByKey[key] = byCategory
					}
					if r.categoryReviewCounts == nil {
						r.categoryReviewCounts = map[string]int{}
					}
					r.categoryReviewCounts[category] += stat.ReviewCount
					byCategory[category] = append(byCategory[category], reviewObs{
						score:      float64(score),
						weight:     categoryWeight,
						reviewedAt: at,
					})
				}
			}
			r.success += stat.Success
			r.failure += stat.Failure
			r.running += stat.Running
			r.runs += stat.Runs
			r.unknownCostRuns += stat.UnknownCostRuns
			r.unknownSuccessRuns += stat.UnknownSuccessRuns
			r.unknownDurationMS += stat.UnknownDurationMS
			r.costUSD += stat.CostUSD
			r.durationMS += stat.DurationMS
			// Operational failures are RELIABILITY data, not quality.
			// The modelStatsForDelegation change already gated review
			// attribution on the operational-only check, so the
			// recency EWMA above naturally excludes these stats; this
			// counter is just for the operator-facing capacity row.
			r.operationalFailures += stat.OperationalFailures
			r.dispatchFailures += stat.DispatchFailures
			r.budgetFailures += stat.BudgetFailures
			r.deliverabilityFailures += stat.DeliverabilityFailures
			r.operationalDurationMS += stat.OperationalDurationMS
			// Use the already-classified dimensions from the per-delegation
			// stat. Re-deriving quality from the legacy Failure total would
			// fold cap and policy outcomes back into model quality.
			r.qualitySuccess += stat.QualitySuccess
			r.qualityFailure += stat.QualityFailure
		}
	}
	// Compute recency-weighted (EWMA) score per model from observations,
	// oldest-first so the fold ends biased toward most recent reviews.
	// Task-kind boosted weight (already *2 in obs) pulls harder via effAlpha.
	for key, r := range byKey {
		if obs := obsByKey[key]; len(obs) > 0 {
			r.recencyScore = computeRecencyWeightedScore(obs)
		}
		for category, obs := range categoryObsByKey[key] {
			if len(obs) == 0 {
				continue
			}
			if r.categoryScores == nil {
				r.categoryScores = map[string]float64{}
			}
			r.categoryScores[category] = computeRecencyWeightedScore(obs)
		}
	}
	return byKey, nil
}

// computeRecencyWeightedScore folds observations (oldest to newest) with
// exponential decay (alpha ~0.70 favors recent). weight (incl. task-kind*2)
// scales the pull of that observation. Explicit EWMA so recent reviewed
// performance dominates stale history without DB changes.
func computeRecencyWeightedScore(obs []reviewObs) float64 {
	if len(obs) == 0 {
		return 0
	}
	// oldest first
	sort.SliceStable(obs, func(i, j int) bool {
		if obs[i].reviewedAt.Equal(obs[j].reviewedAt) {
			return i < j
		}
		return obs[i].reviewedAt.Before(obs[j].reviewedAt)
	})
	const alpha = 0.70
	ewma := obs[0].score
	for i := 1; i < len(obs); i++ {
		s := obs[i].score
		w := obs[i].weight
		if w < 1 {
			w = 1
		}
		// equivalent alpha after w repeated applications of same s
		effAlpha := 1 - math.Pow(1-alpha, float64(w))
		ewma = effAlpha*s + (1-effAlpha)*ewma
	}
	return ewma
}

type delegationCandidateRank struct {
	scoreTotal  int
	reviews     int
	reviewCount int
	success     int
	failure     int
	running     int
	runs        int
	// unknownCostRuns counts runs whose adapter reported no usage, including
	// CLI cap/output-gate outcomes. Excluded from the cheaper-wins tiebreak
	// and capacity cost penalty so missing telemetry never outranks honest
	// spend. unknownSuccessRuns is its success-only subset for the numerator.
	unknownCostRuns    int
	unknownSuccessRuns int
	costUSD            float64
	durationMS         int64
	// unknownDurationMS is the sum of durations of accounting_missing
	// runs, subtracted from average duration so missing telemetry
	// doesn't produce a misleadingly-low average.
	unknownDurationMS int64
	// recencyScore holds the EWMA (recency-weighted) review score computed
	// from ListDelegations order (newest-first) + delegation ReviewedAt/UpdatedAt
	// timestamps. Recent reviewed outcomes dominate via exponential decay.
	recencyScore float64
	// categoryScores holds per-task-kind EWMA scores from review capability
	// scores. When present for the requested task kind, this is the primary
	// quality signal for ranked/capacity selection.
	categoryScores map[string]float64
	// categoryReviewCounts tracks the independent sample size behind each
	// category EWMA. Overall reviewCount is not a valid confidence proxy when
	// only a subset of reviews scored the requested task kind.
	categoryReviewCounts map[string]int
	// operationalFailures counts runs that died at the adapter/launch
	// stage before the model produced any output, plus dispatch-failed
	// workers. Reliability data, NOT quality data — modelStatsForDelegation
	// already suppresses review-score attribution when every run for the
	// model in a delegation was operational, so the rank builder sees
	// ReviewCount=0 for those stats. This counter is surfaced on the
	// capacity row so the operator can spot a chronically-unreliable
	// model even though the avg review score is unaffected.
	operationalFailures    int
	dispatchFailures       int
	budgetFailures         int
	deliverabilityFailures int
	operationalDurationMS  int64
	// qualitySuccess and qualityFailure exclude operational failures
	// so capacity ranking sees the model's actual coding ability
	// without adapter/launch noise in the denominator. qualityRate
	// = qualitySuccess / (qualitySuccess + qualityFailure), 0 when
	// no quality terminal runs exist.
	qualitySuccess int
	qualityFailure int
}

// reviewObs is a single reviewed delegation contribution for a model key,
// carrying the score given at review time + the (task-kind boosted) weight +
// timestamp for ordering. Used only for recency EWMA computation.
type reviewObs struct {
	score      float64
	weight     int
	reviewedAt time.Time
}

// costKnown reports whether this candidate has at least one run with
// real cost accounting. A candidate whose every run is missing usage
// telemetry has UNKNOWN cost — its $0 total must not win cost
// comparisons.
func (r delegationCandidateRank) costKnown() bool {
	return r.knownAccountingRuns() > 0
}

func (r delegationCandidateRank) betterThan(other *delegationCandidateRank, taskKind string) bool {
	if other == nil {
		return true
	}
	// Primary: recency-weighted EWMA so recent reviewed performance dominates
	// stale history. Falls back to historical avg only on exact tie.
	if r.confidenceAdjustedReviewScore(taskKind) != other.confidenceAdjustedReviewScore(taskKind) {
		return r.confidenceAdjustedReviewScore(taskKind) > other.confidenceAdjustedReviewScore(taskKind)
	}
	if r.averageScore() != other.averageScore() {
		return r.averageScore() > other.averageScore()
	}
	if r.reviews != other.reviews {
		return r.reviews > other.reviews
	}
	if r.qualityRateForRanking() != other.qualityRateForRanking() {
		return r.qualityRateForRanking() > other.qualityRateForRanking()
	}
	if r.reliabilityRateForRanking() != other.reliabilityRateForRanking() {
		return r.reliabilityRateForRanking() > other.reliabilityRateForRanking()
	}
	// Prefer auditable accounting when all quality/reliability evidence ties.
	// Otherwise an all-missing candidate's apparent $0 / 0ms becomes a hidden
	// advantage over a model that honestly reports usage.
	if r.costKnown() != other.costKnown() {
		return r.costKnown()
	}
	if !r.costKnown() {
		return false
	}
	if r.costUSD != other.costUSD {
		return r.costUSD < other.costUSD
	}
	return r.averageDuration() < other.averageDuration()
}

func (r delegationCandidateRank) averageScore() float64 {
	if r.reviews == 0 {
		return 0
	}
	return float64(r.scoreTotal) / float64(r.reviews)
}

func (r delegationCandidateRank) reviewScoreForTaskKind(taskKind string) float64 {
	taskKind = normaliseDelegationTaskKind(taskKind)
	if taskKind != "" {
		if score, ok := r.categoryScores[taskKind]; ok {
			return score
		}
	}
	if r.recencyScore > 0 {
		return r.recencyScore
	}
	return r.averageScore()
}

func (r delegationCandidateRank) confidenceAdjustedReviewScore(taskKind string) float64 {
	raw := r.reviewScoreForTaskKind(taskKind)
	n := r.reviewCount
	taskKind = normaliseDelegationTaskKind(taskKind)
	if taskKind != "" {
		if _, ok := r.categoryScores[taskKind]; ok {
			if categoryCount := r.categoryReviewCounts[taskKind]; categoryCount > 0 {
				n = categoryCount
			}
		}
	}
	if n <= 0 {
		n = r.reviews
	}
	if n <= 0 {
		return 58
	}
	return (raw*float64(n) + 58*reviewConfidencePriorStrength) /
		(float64(n) + reviewConfidencePriorStrength)
}

func (r delegationCandidateRank) successRate() float64 {
	known := r.knownAccountingRuns()
	if known == 0 {
		return 0
	}
	success := r.success - r.unknownSuccessRuns
	if success < 0 {
		success = 0
	}
	if success > known {
		success = known
	}
	return float64(success) / float64(known)
}

// qualityRate returns success over quality terminal runs (success +
// qualityFailure), excluding operational failures entirely. This is the
// pure coding-ability signal: a model that adapter-crashes 50 times but
// codes well on the 50 runs that actually executed still has a high
// qualityRate. Returns 0 when no quality terminal runs exist.
func (r delegationCandidateRank) qualityRate() float64 {
	terminal := r.qualitySuccess + r.qualityFailure
	if terminal == 0 {
		return 0
	}
	return float64(r.qualitySuccess) / float64(terminal)
}

// qualityRateForRanking returns a neutral prior when no coding-quality
// terminal exists, so missing quality evidence neither beats known success
// nor falls below known failure by pretending to be a measured 0%.
func (r delegationCandidateRank) qualityRateForRanking() float64 {
	if r.qualitySuccess+r.qualityFailure == 0 {
		return 0.5
	}
	return r.qualityRate()
}

// reliabilityRate returns 1 - (operational failures / total terminal
// attempts). A model that crashes 3 out of 10 times at the adapter
// layer has reliabilityRate 0.7. Returns 1 when no terminal runs
// exist (nothing broken = reliable by default).
func (r delegationCandidateRank) reliabilityRate() float64 {
	terminal := r.success + r.failure + r.dispatchFailures
	if terminal == 0 {
		return 1
	}
	opFails := r.operationalFailures
	if opFails > terminal {
		opFails = terminal
	}
	return 1 - float64(opFails)/float64(terminal)
}

func (r delegationCandidateRank) knownAccountingRuns() int {
	// Dispatch failures are attempts but never contributed to Runs. Subtract
	// only operational failures backed by an actual run row; subtracting the
	// dispatch subset too would undercount healthy accounted runs.
	runOperationalFailures := r.operationalFailures - r.dispatchFailures
	if runOperationalFailures < 0 {
		runOperationalFailures = 0
	}
	known := r.runs - r.running - r.unknownCostRuns - runOperationalFailures
	if known < 0 {
		return 0
	}
	return known
}

// operationalSuccessRate is success over terminal runs (success+failure).
// Running workers are excluded. Used when accounting telemetry is missing
// so a CLI adapter's successful runs are not reported as 0% reliable.
func (r delegationCandidateRank) operationalSuccessRate() float64 {
	terminal := r.success + r.failure + r.dispatchFailures
	if terminal == 0 {
		return 0
	}
	return float64(r.success) / float64(terminal)
}

// reliabilityRateForRanking is independent of cost telemetry: it measures
// whether the adapter/launch reached a real model run. With no attempts it
// returns a neutral prior so an unseen CLI is neither promoted nor poisoned.
func (r delegationCandidateRank) reliabilityRateForRanking() float64 {
	if r.success+r.failure+r.dispatchFailures == 0 {
		return 0.5
	}
	return r.reliabilityRate()
}

func (r delegationCandidateRank) averageDuration() int64 {
	known := r.knownAccountingRuns()
	if known == 0 {
		return 0
	}
	duration := r.durationMS - r.unknownDurationMS - r.operationalDurationMS
	if duration < 0 {
		duration = 0
	}
	return duration / int64(known)
}

// explorationThrashed reports whether the anti-thrash guard has tripped:
// the candidate keeps dying at the adapter/launch stage (operational
// failures) and has produced no successful run. Such a model is genuinely
// broken — keep exploring it and we just burn parallelism on a launch that
// never gets off the pad. Once tripped, the exploration bonus is withheld
// so the candidate falls to its (low) base score and stops being scheduled.
func (r delegationCandidateRank) explorationThrashed() bool {
	return r.operationalFailures >= explorationFailureCutoff && r.success == 0
}

// operationalQuarantined reports whether capacity mode should stop selecting
// this model for now. Unlike explorationThrashed, this trips even when the
// model occasionally succeeds: repeated adapter/launch deaths are an operator
// reliability problem, not a quality datapoint to keep re-testing in live work.
func (r delegationCandidateRank) operationalQuarantined() bool {
	if r.operationalFailures < operationalQuarantineFailureCutoff {
		return false
	}
	terminal := r.success + r.failure + r.dispatchFailures
	if terminal <= 0 {
		return true
	}
	return float64(r.operationalFailures)/float64(terminal) >= 0.5
}

// explorationBonus returns the informational UCB-style optimism for an
// under-sampled candidate. It decays as 1/sqrt(runs+1) and lets callers
// identify useful explicit side_by_side/random experiments without allowing
// unseen models to displace reviewed production capacity. The signal is
// suppressed entirely once the anti-thrash guard trips.
func (r delegationCandidateRank) explorationBonus() float64 {
	if r.explorationThrashed() {
		return 0
	}
	return explorationWeight / math.Sqrt(float64(r.runs)+1)
}

// underSampled reports whether the candidate is still in the explore phase:
// it has fewer than explorationSettledRuns runs and has not been demoted by
// the anti-thrash guard. Surfaced on the capacity row so the UI + agents can
// mark a fresh model as "new / promising".
func (r delegationCandidateRank) underSampled() bool {
	if r.explorationThrashed() {
		return false
	}
	return r.runs < explorationSettledRuns
}

func capacityScoreForCandidate(
	c delegationResolvedModelCandidate,
	r *delegationCandidateRank,
	taskKind string,
) float64 {
	score := 58.0
	reviewed := r != nil && r.reviews > 0
	if reviewed {
		// Prefer category-specific EWMA for the requested task kind when
		// reviewers supplied capability scores; otherwise use overall EWMA.
		// Shrink small samples toward the neutral prior so one lucky review
		// cannot dominate a proven production model.
		score = r.confidenceAdjustedReviewScore(taskKind)
	}
	if r == nil || r.runs+r.dispatchFailures == 0 {
		score += 4
	} else {
		score += (r.reliabilityRateForRanking() - 0.5) * 20
		// Quality dimension: bonus/penalty from quality-only success
		// rate (excluding operational failures from denominator).
		// Only applied when quality terminal runs exist.
		if qr := r.qualityRate(); r.qualitySuccess+r.qualityFailure > 0 {
			score += (qr - 0.5) * 10
		}
		score -= minFloat(18, float64(r.running)*7)
		// Penalise genuine quality and budget-efficiency outcomes on their
		// own axes. Raw Failure also contains operational and operator-policy
		// outcomes, so using it here double-counts infrastructure failures.
		score -= minFloat(12, float64(r.qualityFailure)*3)
		score -= minFloat(8, float64(r.budgetFailures)*2)
		score -= minFloat(12, float64(r.deliverabilityFailures)*3)
		if r.operationalQuarantined() {
			score -= 100
		} else if r.operationalFailures > 0 {
			score -= minFloat(24, float64(r.operationalFailures)*4)
		}
		// Cost penalty over KNOWN-cost runs only. A candidate whose
		// every run is missing usage telemetry gets a flat midpoint
		// penalty instead — missing accounting must not score better
		// than honestly-reported spend.
		if known := r.knownAccountingRuns(); known > 0 && r.costUSD > 0 {
			score -= minFloat(8, (r.costUSD/float64(known))*4)
		} else if !r.costKnown() {
			score -= 4
		}
		if avg := r.averageDuration(); avg > 0 {
			score -= minFloat(8, float64(avg)/120000)
		}
	}
	if candidateMatchesTaskKind(c, taskKind) {
		score += 6
	}
	if !reviewed {
		// Operational success only proves that the adapter launched. Until
		// parent review records quality, capacity mode should treat the model
		// as exploratory rather than promote it as a trusted default.
		score -= 8
		if candidateNeedsConservativeRouting(c) {
			score -= 8
		}
	}
	// Exploration is surfaced separately and exercised through explicit
	// side_by_side/random calls. Do not let an unseen model's optimism bonus
	// outrank reviewed, reliable production capacity.
	return score
}

// capacityExplorationBonus returns the exploration signal for a candidate
// rank, treating a nil rank (a candidate the ledger has never seen) the same
// as a 0-run rank. It is reported separately from CapacityScore.
func capacityExplorationBonus(r *delegationCandidateRank) float64 {
	if r == nil {
		return explorationWeight
	}
	return r.explorationBonus()
}

func candidateNeedsConservativeRouting(c delegationResolvedModelCandidate) bool {
	groups := providerGroupSet(c.ModelProvider, c.ModelID, c.ModelEndpointURL, c.Label)
	if _, ok := groups["local"]; ok {
		return true
	}
	if _, ok := groups["pi"]; ok {
		return true
	}
	return false
}

func candidateMatchesTaskKind(c delegationResolvedModelCandidate, taskKind string) bool {
	taskKind = normaliseDelegationTaskKind(taskKind)
	if taskKind == "" {
		return false
	}
	for _, tag := range c.CapabilityTags {
		if normaliseDelegationTaskKind(tag) == taskKind {
			return true
		}
	}
	for _, modality := range c.InputModalities {
		if normaliseDelegationTaskKind(modality) == taskKind {
			return true
		}
	}
	for _, modality := range c.OutputModalities {
		if normaliseDelegationTaskKind(modality) == taskKind {
			return true
		}
	}
	return false
}

func delegationModelChoice(c delegationResolvedModelCandidate) DelegationModelChoice {
	return DelegationModelChoice{
		Label:            c.Label,
		ModelProfileID:   c.ModelProfileID,
		ModelProvider:    c.ModelProvider,
		ModelID:          c.ModelID,
		CapabilityTags:   c.CapabilityTags,
		InputModalities:  c.InputModalities,
		OutputModalities: c.OutputModalities,
		CandidateIndex:   c.Index,
		CandidateTotal:   c.Total,
	}
}

func normaliseStringList(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		v := strings.ToLower(strings.TrimSpace(value))
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

func randomIndex(n int) int {
	if n <= 1 {
		return 0
	}
	v, err := crand.Int(crand.Reader, big.NewInt(int64(n)))
	if err != nil {
		return int(time.Now().UnixNano() % int64(n))
	}
	return int(v.Int64())
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

// providerGroupSet returns the set of logical group names + raw provider
// that should be checked against a disabled map for a given candidate.
// Groups: "opencode", "claude", "grok", "mimo", "pi", "openrouter",
// "minimax", "local" plus the raw model_provider value. This lets a single UI toggle
// ("OpenCode") disable all opencode_cli traffic regardless of the backend it
// routes to, while "local" catches loopback / LM Studio / Ollama style routes.
func providerGroupSet(provider, modelID, endpoint, label string) map[string]struct{} {
	g := map[string]struct{}{}
	p := strings.ToLower(strings.TrimSpace(provider))
	m := strings.ToLower(strings.TrimSpace(modelID))
	e := strings.ToLower(strings.TrimSpace(endpoint))
	l := strings.ToLower(strings.TrimSpace(label))
	if p != "" {
		g[p] = struct{}{}
	}
	switch p {
	case providerOpenCodeCLI, "opencode":
		g["opencode"] = struct{}{}
	case providerClaudeCLI, providerAnthropic, "claude":
		g["claude"] = struct{}{}
	case providerGrokCLI, "grok":
		g["grok"] = struct{}{}
	case providerMiMoCLI, "mimo":
		g["mimo"] = struct{}{}
	case providerPiCLI, "pi":
		g["pi"] = struct{}{}
	}
	if strings.Contains(m, "claude") || strings.Contains(m, "anthropic/") ||
		strings.Contains(l, "claude") || strings.Contains(l, "anthropic") ||
		strings.Contains(e, "anthropic") {
		g["claude"] = struct{}{}
	}
	if strings.Contains(m, "minimax") || strings.HasPrefix(m, "minimax/") {
		g["minimax"] = struct{}{}
	}
	if strings.Contains(e, "openrouter") || strings.Contains(l, "openrouter") || strings.Contains(m, "openrouter") {
		g["openrouter"] = struct{}{}
	}
	if isLocalModelRoute(m, e, l) {
		g["local"] = struct{}{}
	}
	if strings.Contains(e, "minimax") || strings.Contains(l, "minimax") {
		g["minimax"] = struct{}{}
	}
	if strings.Contains(m, "mimo") || strings.Contains(e, "mimo") || strings.Contains(l, "mimo") ||
		strings.Contains(e, "xiaomi") || strings.Contains(l, "xiaomi") {
		g["mimo"] = struct{}{}
	}
	if strings.Contains(m, "pi_cli") || strings.Contains(e, "pi_cli") || strings.Contains(l, "pi cli") ||
		strings.Contains(l, "pi.dev") || strings.Contains(l, "pi harness") {
		g["pi"] = struct{}{}
	}
	return g
}

func isLocalModelRoute(modelID, endpoint, label string) bool {
	if strings.Contains(endpoint, "localhost") ||
		strings.Contains(endpoint, "127.0.0.1") ||
		strings.Contains(endpoint, "[::1]") ||
		strings.Contains(endpoint, "0.0.0.0") {
		return true
	}
	return strings.Contains(modelID, "lmstudio") ||
		strings.Contains(endpoint, "lmstudio") ||
		strings.Contains(label, "lmstudio") ||
		strings.Contains(modelID, "lm studio") ||
		strings.Contains(endpoint, "lm studio") ||
		strings.Contains(label, "lm studio") ||
		strings.Contains(modelID, "ollama") ||
		strings.Contains(endpoint, "ollama") ||
		strings.Contains(label, "ollama")
}

func isProviderGroupDisabled(disabled map[string]bool, provider, modelID, endpoint, label string) bool {
	if len(disabled) == 0 {
		return false
	}
	groups := providerGroupSet(provider, modelID, endpoint, label)
	for k := range groups {
		if disabled[k] {
			return true
		}
	}
	return false
}
