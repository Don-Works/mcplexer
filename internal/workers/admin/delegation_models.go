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
	// explorationWeight is the UCB-style optimism granted to an unsampled
	// candidate, decayed by 1/sqrt(runs+1). A freshly-registered model
	// (0 runs) gets the full bonus so it floats ABOVE proven incumbents in
	// capacity ranking and is scheduled soon after registration; the bonus
	// fades as runs accrue (≈+60 at 0 runs, ≈+27 at 4 runs, ≈+19 at 9 runs,
	// ≈+11 at 30 runs) so exploit naturally takes over from explore. The
	// weight is sized so a 0-run candidate (base ≈54 after the unreviewed
	// penalty) clears a strongly-reviewed, fully-reliable incumbent (EWMA
	// ≈85 + reliability ≈+10 + its own small ≈+11 decayed bonus ≈106): the
	// newcomer scores ≈114, a comfortable ~8pt lift. Without this a 0-run
	// unreviewed candidate scores ~46-54 while a proven reviewed model
	// scores its 70-90 EWMA, so the newcomer is never SELECTED → never
	// accrues runs/reviews → stuck forever.
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
	}
	if len(raw) == 0 {
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
		idx := s.bestReviewedDelegationCandidate(ctx, in.WorkspaceID, in.TaskKind, resolved)
		resolved = []delegationResolvedModelCandidate{resolved[idx]}
	}
	if mode == delegationModelSelectionCapacity {
		available := make([]delegationResolvedModelCandidate, 0, len(resolved))
		var firstErr error
		for _, c := range resolved {
			if err := validateModelProvider(c.ModelProvider, c.ModelEndpointURL); err != nil {
				if firstErr == nil {
					firstErr = err
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
		resolved = s.rankCapacityDelegationCandidates(ctx, in.WorkspaceID, in.TaskKind, resolved)
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
) int {
	byKey := s.delegationCandidateRanks(ctx, workspaceID, taskKind)
	best := 0
	var bestRank *delegationCandidateRank
	for i, c := range candidates {
		r := byKey[c.ModelProvider+"/"+c.ModelID]
		if r == nil || r.reviews == 0 {
			continue
		}
		if bestRank == nil || r.betterThan(bestRank, taskKind) {
			best = i
			bestRank = r
		}
	}
	return best
}

func (s *Service) rankCapacityDelegationCandidates(
	ctx context.Context,
	workspaceID string,
	taskKind string,
	candidates []delegationResolvedModelCandidate,
) []delegationResolvedModelCandidate {
	byKey := s.delegationCandidateRanks(ctx, workspaceID, taskKind)
	out := append([]delegationResolvedModelCandidate(nil), candidates...)
	sort.SliceStable(out, func(i, j int) bool {
		left := capacityScoreForCandidate(out[i], byKey[out[i].ModelProvider+"/"+out[i].ModelID], taskKind)
		right := capacityScoreForCandidate(out[j], byKey[out[j].ModelProvider+"/"+out[j].ModelID], taskKind)
		if left != right {
			return left > right
		}
		return out[i].ModelProvider+"/"+out[i].ModelID < out[j].ModelProvider+"/"+out[j].ModelID
	})
	return out
}

func (s *Service) delegationCandidateRanks(
	ctx context.Context,
	workspaceID string,
	taskKind string,
) map[string]*delegationCandidateRank {
	rows, err := s.ListDelegations(ctx, DelegationListInput{WorkspaceID: workspaceID, Limit: 200})
	if err != nil {
		return map[string]*delegationCandidateRank{}
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
			r.unknownDurationMS += stat.UnknownDurationMS
			r.costUSD += stat.CostUSD
			r.durationMS += stat.DurationMS
			// Operational failures are RELIABILITY data, not quality.
			// The modelStatsForDelegation change already gated review
			// attribution on the operational-only check, so the
			// recency EWMA above naturally excludes these stats; this
			// counter is just for the operator-facing capacity row.
			r.operationalFailures += stat.OperationalFailures
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
	return byKey
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
	// unknownCostRuns counts successful runs whose adapter reported no
	// usage at all (accounting_missing). Excluded from the cheaper-wins
	// tiebreak + the capacity cost penalty so missing telemetry never
	// outranks honestly-reported spend.
	unknownCostRuns int
	costUSD         float64
	durationMS      int64
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
	// operationalFailures counts runs that died at the adapter/launch
	// stage before the model produced any output, plus dispatch-failed
	// workers. Reliability data, NOT quality data — modelStatsForDelegation
	// already suppresses review-score attribution when every run for the
	// model in a delegation was operational, so the rank builder sees
	// ReviewCount=0 for those stats. This counter is surfaced on the
	// capacity row so the operator can spot a chronically-unreliable
	// model even though the avg review score is unaffected.
	operationalFailures int
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
	if r.reviewScoreForTaskKind(taskKind) != other.reviewScoreForTaskKind(taskKind) {
		return r.reviewScoreForTaskKind(taskKind) > other.reviewScoreForTaskKind(taskKind)
	}
	if r.averageScore() != other.averageScore() {
		return r.averageScore() > other.averageScore()
	}
	if r.reviews != other.reviews {
		return r.reviews > other.reviews
	}
	if r.costKnown() && other.costKnown() &&
		r.reliabilityRateForRanking() != other.reliabilityRateForRanking() {
		return r.reliabilityRateForRanking() > other.reliabilityRateForRanking()
	}
	// Cheaper-wins tiebreak applies only when BOTH sides have known
	// cost. If either side's accounting is missing, the comparison is
	// meaningless (its $0 is absence of data) — skip to duration.
	if r.costKnown() && other.costKnown() && r.costUSD != other.costUSD {
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
		if score := r.categoryScores[taskKind]; score > 0 {
			return score
		}
	}
	if r.recencyScore > 0 {
		return r.recencyScore
	}
	return r.averageScore()
}

func (r delegationCandidateRank) successRate() float64 {
	known := r.knownAccountingRuns()
	if known == 0 {
		return 0
	}
	success := r.success - r.unknownCostRuns
	if success < 0 {
		success = 0
	}
	if success > known {
		success = known
	}
	return float64(success) / float64(known)
}

func (r delegationCandidateRank) knownAccountingRuns() int {
	known := r.runs - r.unknownCostRuns - r.operationalFailures
	if known < 0 {
		return 0
	}
	return known
}

// operationalSuccessRate is success over terminal runs (success+failure).
// Running workers are excluded. Used when accounting telemetry is missing
// so a CLI adapter's successful runs are not reported as 0% reliable.
func (r delegationCandidateRank) operationalSuccessRate() float64 {
	terminal := r.success + r.failure
	if terminal == 0 {
		return 0
	}
	return float64(r.success) / float64(terminal)
}

// reliabilityRateForRanking picks the success signal used for capacity
// scoring and ranked tiebreaks. Known-accounting runs use the stricter
// accounted rate. When every run lacks usage telemetry, return the
// neutral midpoint so missing CLI accounting cannot fake 0% or 100%.
func (r delegationCandidateRank) reliabilityRateForRanking() float64 {
	if r.costKnown() {
		return r.successRate()
	}
	return 0.5
}

func (r delegationCandidateRank) averageDuration() int64 {
	known := r.knownAccountingRuns()
	if known == 0 {
		return 0
	}
	return (r.durationMS - r.unknownDurationMS) / int64(known)
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

// explorationBonus returns the UCB-style optimism added to the capacity
// score for an under-sampled candidate. It decays as 1/sqrt(runs+1) so a
// brand-new (0-run) model floats near the top of the ranking and is
// scheduled soon, then the bonus fades as the model accrues its own runs.
// The bonus is suppressed entirely once the anti-thrash guard trips
// (operational failures with no success), so a broken adapter is not
// force-explored forever.
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
		score = r.reviewScoreForTaskKind(taskKind)
	}
	if r == nil || r.runs == 0 {
		score += 4
	} else {
		score += (r.reliabilityRateForRanking() - 0.5) * 20
		score -= minFloat(18, float64(r.running)*7)
		score -= minFloat(12, float64(r.failure)*3)
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
	// Explore/exploit: lift under-sampled candidates with UCB-style optimism
	// so a freshly-registered model is scheduled soon after registration
	// instead of being starved by proven incumbents (the cold-start trap).
	// The bonus decays with runs (1/sqrt(runs+1)) and is withheld entirely
	// once the anti-thrash guard trips, so a broken adapter is not explored
	// forever. Added on top of every existing nuanced term — it changes WHEN
	// a model is tried, not how its proven quality is measured.
	score += capacityExplorationBonus(r)
	return score
}

// capacityExplorationBonus returns the exploration optimism for a candidate
// rank, treating a nil rank (a candidate the ledger has never seen) the same
// as a 0-run rank so a brand-new model still floats up.
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
