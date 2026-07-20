package admin

import (
	"github.com/don-works/mcplexer/internal/store"
)

// SetRunnerForTest swaps the bound runner so tests can inject a fake
// after New. Production wires the runner once at construction; tests
// build a baseline Service via newTestService and replace the runner
// on demand to keep table-driven cases small.
func (s *Service) SetRunnerForTest(r Runner) {
	s.runner = r
}

// SetAuditCounterForTest wires (or clears) the AuditCounter used for
// derive-at-read-time tool_calls_count on CLI adapter runs. Tests use
// it to inject a fake or the real store (which implements the count)
// so ListDelegations / Get surface derived counts instead of the
// persisted 0 for claude_cli / opencode_cli / grok_cli / mimo_cli workers.
func (s *Service) SetAuditCounterForTest(ac AuditCounter) {
	s.auditCounter = ac
}

// ProviderGroupDisabledForTest exposes the group disable predicate for
// focused tests without making the internal helper public.
func ProviderGroupDisabledForTest(disabled map[string]bool, provider, modelID, endpoint, label string) bool {
	return isProviderGroupDisabled(disabled, provider, modelID, endpoint, label)
}

// AggregateDelegationForTest exposes aggregateDelegation so black-box
// tests can assert how run statuses (notably operator-cancelled) roll up
// into the delegation aggregate + overall status.
func AggregateDelegationForTest(d DelegationContext) (DelegationAggregate, string) {
	return aggregateDelegation(d)
}

// DelegationStatusTerminalForTest exposes the wait-loop terminal predicate so
// blocked/interrupted aggregation regressions cannot turn into long polls.
func DelegationStatusTerminalForTest(status string) bool { return isTerminalStatus(status) }

// ModelStatsForDelegationForTest exposes modelStatsForDelegation so
// black-box tests can assert cancelled runs are excluded from per-model
// rank stats.
func ModelStatsForDelegationForTest(d DelegationContext) []DelegationModelStat {
	return modelStatsForDelegation(d)
}

// IsOperationalFailureForTest exposes isOperationalFailure for the
// launch-failure ranking tests. Returns true when a worker run died at
// the adapter/launch stage before the model produced any output.
func IsOperationalFailureForTest(run *store.WorkerRun) bool {
	return isOperationalFailure(run)
}

// DelegationIsOperationalOnlyForModelForTest exposes the suppression
// predicate used to decide whether a parent review score should be
// attributed to a model in a given delegation.
func DelegationIsOperationalOnlyForModelForTest(workers []DelegationWorkerContext) bool {
	return delegationIsOperationalOnly(workers)
}

// DelegationRankRatesForTest exposes success-rate helpers for table-driven
// unit tests without widening the production API surface.
func DelegationRankRatesForTest(runs, success, failure, unknownCostRuns int) (successRate, operationalRate, rankingRate float64, accountingKnown bool) {
	return DelegationRankRatesWithOperationalFailuresForTest(runs, success, failure, unknownCostRuns, 0)
}

// DelegationRankRatesWithOperationalFailuresForTest exposes the same helper
// with launch/adapter failures included for reliability-ranking tests.
func DelegationRankRatesWithOperationalFailuresForTest(runs, success, failure, unknownCostRuns, operationalFailures int) (successRate, operationalRate, rankingRate float64, accountingKnown bool) {
	unknownSuccessRuns := unknownCostRuns
	if unknownSuccessRuns > success {
		unknownSuccessRuns = success
	}
	return DelegationRankRatesWithAccountingOutcomesForTest(
		runs, success, failure, 0, unknownCostRuns, unknownSuccessRuns, operationalFailures,
	)
}

// DelegationRankRatesWithAccountingOutcomesForTest exposes the accounting
// denominator with missing successes and failures separated.
func DelegationRankRatesWithAccountingOutcomesForTest(
	runs, success, failure, running, unknownCostRuns, unknownSuccessRuns, operationalFailures int,
) (successRate, operationalRate, rankingRate float64, accountingKnown bool) {
	r := delegationCandidateRank{
		runs:                runs,
		success:             success,
		failure:             failure,
		running:             running,
		unknownCostRuns:     unknownCostRuns,
		unknownSuccessRuns:  unknownSuccessRuns,
		operationalFailures: operationalFailures,
	}
	return r.successRate(), r.operationalSuccessRate(), r.reliabilityRateForRanking(), r.costKnown()
}

// CapacityScoreForCandidateForTest exposes capacity scoring for rank tests.
func CapacityScoreForCandidateForTest(
	runs, success, failure, unknownCostRuns, reviews int, reviewScore float64,
	taskKind string,
) float64 {
	return CapacityScoreForModelCandidateForTest(
		"", "", "", "",
		runs, success, failure, unknownCostRuns, reviews, reviewScore,
		taskKind,
	)
}

func CapacityScoreForModelCandidateForTest(
	provider, modelID, endpoint, label string,
	runs, success, failure, unknownCostRuns, reviews int, reviewScore float64,
	taskKind string,
) float64 {
	r := &delegationCandidateRank{
		runs:            runs,
		success:         success,
		failure:         failure,
		qualitySuccess:  success,
		qualityFailure:  failure,
		unknownCostRuns: unknownCostRuns,
		reviews:         reviews,
		recencyScore:    reviewScore,
	}
	return capacityScoreForCandidate(delegationResolvedModelCandidate{
		DelegationModelCandidate: DelegationModelCandidate{
			Label:            label,
			ModelProvider:    provider,
			ModelID:          modelID,
			ModelEndpointURL: endpoint,
		},
	}, r, taskKind)
}

// ExplorationSettledRunsForTest exposes the run count past which a candidate
// leaves the explore phase, so surfacing tests don't hardcode the constant.
func ExplorationSettledRunsForTest() int { return explorationSettledRuns }

// ExplorationBonusForTest exposes the informational UCB-style signal so
// tests can verify explicit exploration guidance independently of the
// production capacity score.
func ExplorationBonusForTest(runs, success, operationalFailures int) float64 {
	r := &delegationCandidateRank{
		runs:                runs,
		success:             success,
		operationalFailures: operationalFailures,
	}
	return r.explorationBonus()
}

// ExplorationStateForTest exposes the explore-phase / anti-thrash predicates
// for the surfacing tests.
func ExplorationStateForTest(runs, success, operationalFailures int) (underSampled, thrashed bool) {
	r := delegationCandidateRank{
		runs:                runs,
		success:             success,
		operationalFailures: operationalFailures,
	}
	return r.underSampled(), r.explorationThrashed()
}

// OperationalQuarantinedForTest exposes the capacity circuit breaker for
// focused tests.
func OperationalQuarantinedForTest(runs, success, failure, operationalFailures int) bool {
	r := delegationCandidateRank{
		runs:                runs,
		success:             success,
		failure:             failure,
		operationalFailures: operationalFailures,
	}
	return r.operationalQuarantined()
}

// CapacityScoreWithReliabilityForTest exposes capacity scoring with the
// operational-failure and running counters set, so anti-thrash and
// broken-model demotion can be table-tested directly against the score.
func CapacityScoreWithReliabilityForTest(
	provider, modelID, endpoint, label string,
	runs, success, failure, running, operationalFailures, unknownCostRuns, reviews int,
	reviewScore float64,
	taskKind string,
) float64 {
	r := &delegationCandidateRank{
		runs:                runs,
		success:             success,
		failure:             failure,
		running:             running,
		operationalFailures: operationalFailures,
		qualitySuccess:      success,
		qualityFailure:      maxInt(0, failure-operationalFailures),
		unknownCostRuns:     unknownCostRuns,
		reviews:             reviews,
		recencyScore:        reviewScore,
	}
	return capacityScoreForCandidate(delegationResolvedModelCandidate{
		DelegationModelCandidate: DelegationModelCandidate{
			Label:            label,
			ModelProvider:    provider,
			ModelID:          modelID,
			ModelEndpointURL: endpoint,
		},
	}, r, taskKind)
}

// CapacityScoreWithDispatchFailuresForTest represents attempts that failed
// before a WorkerRun row existed, which therefore have runs=0 but must still
// be penalised by the production capacity score.
func CapacityScoreWithDispatchFailuresForTest(dispatchFailures int) float64 {
	r := &delegationCandidateRank{
		dispatchFailures:    dispatchFailures,
		operationalFailures: dispatchFailures,
	}
	return capacityScoreForCandidate(delegationResolvedModelCandidate{
		DelegationModelCandidate: DelegationModelCandidate{
			ModelProvider: "grok_cli",
			ModelID:       "dispatch-failed",
		},
	}, r, "coding")
}

// CapacityScoreWithDeliverabilityFailuresForTest represents model attempts
// that ran but failed to return an acceptable final report.
func CapacityScoreWithDeliverabilityFailuresForTest(failures int) float64 {
	r := &delegationCandidateRank{
		runs:                   failures,
		failure:                failures,
		deliverabilityFailures: failures,
		unknownCostRuns:        failures,
	}
	return capacityScoreForCandidate(delegationResolvedModelCandidate{
		DelegationModelCandidate: DelegationModelCandidate{
			ModelProvider: "grok_cli",
			ModelID:       "empty-reporter",
		},
	}, r, "coding")
}

func AnnotateDeliverableForTest(run *store.WorkerRun) {
	annotateDeliverable(run)
}

func AnnotateToolCallsCapForTest(run *store.WorkerRun, worker *store.Worker) {
	annotateToolCallsCap(run, worker)
}
