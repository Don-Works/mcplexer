package admin

import (
	"context"
	"sort"
	"strings"
)

type DelegationCapacityListInput struct {
	WorkspaceID string `json:"workspace_id,omitempty"`
	TaskKind    string `json:"task_kind,omitempty"`
	Limit       int    `json:"limit,omitempty"`
	// DisabledProviders populated by handler from settings; candidates
	// matching a disabled group are marked unavailable (or dropped for
	// actual selection).
	DisabledProviders map[string]bool `json:"-"`
}

type DelegationModelCapacity struct {
	Rank              int      `json:"rank"`
	Label             string   `json:"label,omitempty"`
	ModelProfileID    string   `json:"model_profile_id,omitempty"`
	ModelProvider     string   `json:"model_provider"`
	ModelID           string   `json:"model_id"`
	ModelKey          string   `json:"model_key"`
	CapabilityTags    []string `json:"capability_tags,omitempty"`
	InputModalities   []string `json:"input_modalities,omitempty"`
	OutputModalities  []string `json:"output_modalities,omitempty"`
	Available         bool     `json:"available"`
	UnavailableReason string   `json:"unavailable_reason,omitempty"`
	CapacityScore     float64  `json:"capacity_score"`
	Runs              int      `json:"runs"`
	Success           int      `json:"success"`
	Failure           int      `json:"failure"`
	Running           int      `json:"running"`
	// OperationalFailures counts runs whose adapter/launch died before
	// the model produced any output, plus dispatch-failed workers. This
	// is RELIABILITY data — it does not move the avg review score or
	// rank position, but it lets the operator see (and the ranker
	// potentially demote) a model that frequently can't get off the
	// launch pad. Mirrors DelegationModelStat.OperationalFailures.
	OperationalFailures int     `json:"operational_failures,omitempty"`
	ReviewCount         int     `json:"review_count"`
	ReviewScore         float64 `json:"review_score"`
	// SuccessRate is computed over runs with known accounting. If
	// AccountingKnown is false, a zero SuccessRate means no successful run
	// reported usage telemetry, not that every run failed.
	SuccessRate float64 `json:"success_rate"`
	// OperationalSuccessRate is success over terminal runs (success+failure),
	// excluding in-flight workers. Use this when AccountingKnown is false.
	OperationalSuccessRate float64 `json:"operational_success_rate,omitempty"`
	// AccountingKnown reports whether at least one run had usable token/cost
	// telemetry. CLI adapters can succeed without reporting usage.
	AccountingKnown bool    `json:"accounting_known"`
	CostUSD         float64 `json:"cost_usd"`
	AvgDurationMS   int64   `json:"avg_duration_ms"`
	// ExplorationBonus is the UCB-style optimism (1/sqrt(runs+1)) folded into
	// CapacityScore for an under-sampled candidate. It is the portion of the
	// score that exists to get a new/rarely-tried model SCHEDULED rather than
	// to measure its proven quality, and decays to ~0 as runs accrue. Withheld
	// once the anti-thrash guard trips (operational failures, no success).
	ExplorationBonus float64 `json:"exploration_bonus,omitempty"`
	// Exploring marks a candidate the ranker is still optimistically boosting:
	// under explorationSettledRuns runs and not demoted by the anti-thrash
	// guard. The UI + agents use it to present the model as "new / promising"
	// — a good option to try, not yet a proven default.
	Exploring bool `json:"exploring,omitempty"`
}

func (s *Service) ListDelegationModelCapacity(
	ctx context.Context,
	in DelegationCapacityListInput,
) ([]DelegationModelCapacity, error) {
	in.WorkspaceID = strings.TrimSpace(in.WorkspaceID)
	in.TaskKind = normaliseDelegationTaskKind(in.TaskKind)
	if in.DisabledProviders == nil {
		in.DisabledProviders = map[string]bool{}
	}
	raw, err := s.registeredDelegationModelCandidates(ctx, &DelegationInput{
		WorkspaceID:       in.WorkspaceID,
		TaskKind:          in.TaskKind,
		DisabledProviders: in.DisabledProviders,
	})
	if err != nil {
		return nil, err
	}
	ranks := s.delegationCandidateRanks(ctx, in.WorkspaceID, in.TaskKind)
	rows := make([]DelegationModelCapacity, 0, len(raw))
	for i, candidate := range raw {
		resolved, err := s.resolveDelegationModelCandidate(ctx, candidate, i, len(raw))
		row := DelegationModelCapacity{
			Label:          strings.TrimSpace(candidate.Label),
			ModelProfileID: strings.TrimSpace(candidate.ModelProfileID),
			ModelProvider:  strings.TrimSpace(candidate.ModelProvider),
			ModelID:        strings.TrimSpace(candidate.ModelID),
			ModelKey:       strings.TrimSpace(candidate.ModelProvider) + "/" + strings.TrimSpace(candidate.ModelID),
		}
		if err != nil {
			row.UnavailableReason = err.Error()
			rows = append(rows, row)
			continue
		}
		if isProviderGroupDisabled(in.DisabledProviders, resolved.ModelProvider, resolved.ModelID, resolved.ModelEndpointURL, resolved.Label) {
			row.UnavailableReason = "provider group disabled by operator"
			rows = append(rows, row)
			continue
		}
		if err := validateModelProvider(resolved.ModelProvider, resolved.ModelEndpointURL); err != nil {
			row.ModelProvider = resolved.ModelProvider
			row.ModelID = resolved.ModelID
			row.ModelKey = resolved.ModelProvider + "/" + resolved.ModelID
			row.UnavailableReason = err.Error()
			rows = append(rows, row)
			continue
		}
		key := resolved.ModelProvider + "/" + resolved.ModelID
		rank := ranks[key]
		row.Label = resolved.Label
		row.ModelProfileID = resolved.ModelProfileID
		row.ModelProvider = resolved.ModelProvider
		row.ModelID = resolved.ModelID
		row.ModelKey = key
		row.CapabilityTags = resolved.CapabilityTags
		row.InputModalities = resolved.InputModalities
		row.OutputModalities = resolved.OutputModalities
		row.Available = true
		row.CapacityScore = capacityScoreForCandidate(resolved, rank, in.TaskKind)
		// Surface the explore/exploit signal so the UI + agents can mark a
		// fresh model as "new / promising". A nil rank is a never-seen
		// candidate — treat it as fully under-sampled.
		row.ExplorationBonus = capacityExplorationBonus(rank)
		row.Exploring = rank == nil || rank.underSampled()
		if rank != nil {
			row.Runs = rank.runs
			row.Success = rank.success
			row.Failure = rank.failure
			row.Running = rank.running
			row.OperationalFailures = rank.operationalFailures
			row.ReviewCount = rank.reviewCount
			// Report the same score family used for capacity/ranked selection.
			row.ReviewScore = rank.reviewScoreForTaskKind(in.TaskKind)
			row.SuccessRate = rank.successRate()
			row.OperationalSuccessRate = rank.operationalSuccessRate()
			row.AccountingKnown = rank.costKnown()
			row.CostUSD = rank.costUSD
			row.AvgDurationMS = rank.averageDuration()
		}
		rows = append(rows, row)
	}
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Available != rows[j].Available {
			return rows[i].Available
		}
		if rows[i].CapacityScore != rows[j].CapacityScore {
			return rows[i].CapacityScore > rows[j].CapacityScore
		}
		return rows[i].ModelKey < rows[j].ModelKey
	})
	limit := in.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	if len(rows) > limit {
		rows = rows[:limit]
	}
	for i := range rows {
		rows[i].Rank = i + 1
	}
	return rows, nil
}
