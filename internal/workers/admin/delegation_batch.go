package admin

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"
)

// batchMaxDelegations caps how many delegations one batch may contain.
// Each delegation independently enforces the 20-dispatch cap, so a full
// batch of fan-outs is bounded by batchMaxTotalDispatches below.
const (
	batchMaxDelegations     = 20
	batchMaxTotalDispatches = 60
)

// BatchDelegationInput groups multiple delegations that share context and
// are validated together before any worker is created.
type BatchDelegationInput struct {
	Delegations []DelegationInput `json:"delegations"`
	// SharedRepoBrief is prepended to every child delegation's RepoBrief
	// so common repository context is stated once, not per item.
	SharedRepoBrief string `json:"shared_repo_brief,omitempty"`
}

// BatchDelegationItem is one delegation's outcome within a batch. Error is
// set (and Output zero) when that item failed after the batch had already
// begun dispatching earlier items — items are independent, so one failure
// does not roll back the successes.
type BatchDelegationItem struct {
	Index  int              `json:"index"`
	Output DelegationOutput `json:"output,omitempty"`
	Error  string           `json:"error,omitempty"`
}

// BatchDelegationOutput is returned by DelegateBatch.
type BatchDelegationOutput struct {
	BatchID string                `json:"batch_id"`
	Items   []BatchDelegationItem `json:"items"`
	// Warnings carries batch-level advisories (e.g. cross-item file
	// overlaps detected before dispatch).
	Warnings []string `json:"warnings,omitempty"`
}

// DelegateBatch validates every delegation up front (fail-fast before any
// side effect), warns on cross-item touches_files overlaps that the
// per-item store check cannot see yet, then delegates each item through
// the normal Delegate path so coordination, context injection, shape
// routing, and dispatch behave identically to a single delegation. Items
// are independent: a mid-batch failure is reported per item and does not
// undo earlier successes.
//
// Perf note: capacity-mode items each re-scan recent delegations for
// ranking; a large capacity batch multiplies that scan. Callers running
// wide capacity batches should prefer explicit candidates.
func (s *Service) DelegateBatch(ctx context.Context, in BatchDelegationInput) (BatchDelegationOutput, error) {
	if len(in.Delegations) == 0 {
		return BatchDelegationOutput{}, errors.New("batch delegations required")
	}
	if len(in.Delegations) > batchMaxDelegations {
		return BatchDelegationOutput{}, fmt.Errorf("batch delegations max %d", batchMaxDelegations)
	}
	batchID := "batch-" + uuid.NewString()
	sharedBrief := strings.TrimSpace(in.SharedRepoBrief)

	// Phase 1: apply shared context, then normalize + preflight EVERY
	// item before creating anything. A single invalid item fails the
	// whole batch loudly rather than half-dispatching.
	totalDispatches := 0
	for i := range in.Delegations {
		del := &in.Delegations[i]
		if sharedBrief != "" {
			if del.RepoBrief != "" {
				del.RepoBrief = sharedBrief + "\n\n" + del.RepoBrief
			} else {
				del.RepoBrief = sharedBrief
			}
		}
		if err := s.normalizeDelegationInput(ctx, del); err != nil {
			return BatchDelegationOutput{}, fmt.Errorf("batch %s delegation[%d] normalize: %w", batchID, i, err)
		}
		if err := s.preflightDelegation(ctx, del); err != nil {
			return BatchDelegationOutput{}, fmt.Errorf("batch %s delegation[%d] preflight: %w", batchID, i, err)
		}
		// Use the exact per-mode plan size: random/capacity fan out to
		// parallelism workers, not candidates*parallelism, so a valid
		// large random batch is not wrongly rejected.
		totalDispatches += delegationPlanSize(*del)
	}
	if totalDispatches > batchMaxTotalDispatches {
		return BatchDelegationOutput{}, fmt.Errorf(
			"batch %s would dispatch %d workers, max %d", batchID, totalDispatches, batchMaxTotalDispatches)
	}

	warnings := crossItemOverlapWarnings(in.Delegations)

	// Phase 2: delegate each item through the normal path. These are
	// PRE-normalized; Delegate re-normalizes idempotently. Items are
	// independent — record per-item errors, keep going.
	items := make([]BatchDelegationItem, 0, len(in.Delegations))
	for i, del := range in.Delegations {
		out, err := s.Delegate(ctx, del)
		item := BatchDelegationItem{Index: i}
		if err != nil {
			item.Error = err.Error()
			slog.WarnContext(ctx, "batch delegation: item failed",
				"batch_id", batchID, "index", i, "error", err)
		} else {
			item.Output = out
		}
		items = append(items, item)
	}
	return BatchDelegationOutput{BatchID: batchID, Items: items, Warnings: warnings}, nil
}

// crossItemOverlapWarnings reports touches_files that appear in more than
// one item of the same batch. The per-item store overlap check cannot see
// this: item i's claim is not yet written when item j is checked, so a
// batch that has two items touching the same file would otherwise collide
// silently.
func crossItemOverlapWarnings(dels []DelegationInput) []string {
	firstOwner := make(map[string]int)
	seen := make(map[string]bool)
	var warnings []string
	for i, del := range dels {
		for _, f := range del.TouchesFiles {
			if owner, ok := firstOwner[f]; ok {
				if !seen[f] {
					seen[f] = true
					warnings = append(warnings, fmt.Sprintf(
						"file %q claimed by both delegation[%d] and delegation[%d] in this batch - potential duplicate work", f, owner, i))
				}
				continue
			}
			firstOwner[f] = i
		}
	}
	return warnings
}
