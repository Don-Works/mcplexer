// delegation_review_outcome.go — operational-failure detection used
// to keep adapter/launch crashes out of per-model quality ranking.
//
// When a delegated worker dies at the adapter/launch stage (subprocess
// crash, network blip, missing binary, …) before the model ever
// produces a turn, the parent sees "the model produced nothing" and
// typically gives a low review score. Folding that score into the
// per-model avg would corrupt capacity ranking for every model that
// ever suffered a launch crash, because the judgement is about the
// adapter, not the model.
//
// This file holds the two predicates that detect operational
// failures:
//
//   - isOperationalFailure(run) — single-run signature: status=failure,
//     zero tokens, error prefixed with the runner's canonical
//     "adapter send: " marker (the only place the runner stamps that
//     prefix is the FIRST adapter.Send call failing inside runLoop).
//
//   - delegationIsOperationalOnlyForModel(workers) — delegation-level
//     predicate: every worker matching the model key was operational
//     (either by run signature, or by DispatchFailed flag). When
//     true, modelStatsForDelegation suppresses review attribution.
package admin

import (
	"strings"

	"github.com/don-works/mcplexer/internal/store"
)

// isOperationalFailure reports whether a single worker run died at the
// adapter/launch stage before the model produced any output. The
// signature is intentionally narrow: status=failure, ZERO tokens in
// and out (the model never got a chance to respond), and the error
// text stamped with the loop's canonical "adapter send: " prefix (see
// internal/workers/runner/loop.go — the only site in the runner that
// produces that prefix is the FIRST adapter.Send call failing inside
// runLoop). Tokens > 0 means the model had at least one successful
// turn before a later failure, so it's a real quality event and
// stays in the quality aggregation.
//
// A nil run is NOT a launch failure per se (dispatch-failed workers
// never create a run row) — callers should consult
// DelegationWorkerContext.DispatchFailed for that.
func isOperationalFailure(run *store.WorkerRun) bool {
	if run == nil {
		return false
	}
	if run.Status != "failure" {
		return false
	}
	if run.InputTokens != 0 || run.OutputTokens != 0 {
		return false
	}
	return strings.HasPrefix(run.Error, "adapter send:")
}

// delegationIsOperationalOnlyForModel reports whether every worker
// context that contributed to a model's stat in a delegation was an
// operational failure (adapter/launch died or detached-dispatch
// failed). When true, the parent's review score must not be attributed
// to model quality because the model never ran.
//
// DispatchFailed short-circuits to "operational" even if a run row
// somehow exists alongside the flag (e.g. a stale run row from a
// previous dispatch attempt): the operator has explicitly marked the
// worker as launch-failed, and the per-model quality attribution
// must respect that. An empty worker list is treated as "not
// operational-only" so an unattributable stat doesn't silently get
// suppressed by a missing worker record.
func delegationIsOperationalOnlyForModel(workers []DelegationWorkerContext) bool {
	if len(workers) == 0 {
		return false
	}
	for _, w := range workers {
		if w.DispatchFailed {
			continue
		}
		run := w.LatestRun
		if run == nil {
			return false
		}
		if !isOperationalFailure(run) {
			return false
		}
	}
	return true
}
