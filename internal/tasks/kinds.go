// kinds.go — canonical task-status-vocabulary kind helpers. The kind
// column (migration 070, review split out in 099) classifies each
// freeform per-workspace status word into one of six buckets so the
// service layer + UI generalise without hardcoding the six suggested
// default statuses.
//
// Semantics per kind:
//   - open      — not started; no lease.
//   - working   — an agent is actively driving this; auto-claim +
//     lease machinery apply (see isWorkingStatus).
//   - blocked   — waiting on an external unblock; no lease.
//   - review    — working phase finished, awaiting verification /
//     signoff. NOT working (no lease, never auto-claimed, never swept
//     by the lease reclaim) and NOT terminal (closed_at not stamped).
//   - done      — terminal.
//   - cancelled — terminal.
package tasks

import (
	"context"

	"github.com/don-works/mcplexer/internal/store"
)

// Canonical vocabulary kinds (task_status_vocabulary.kind).
const (
	KindOpen      = "open"
	KindWorking   = "working"
	KindBlocked   = "blocked"
	KindReview    = "review"
	KindDone      = "done"
	KindCancelled = "cancelled"
)

// fallbackStatusKinds maps the six suggested default statuses to their
// canonical kinds for workspaces that never declared a vocabulary.
// Mirrors the migration 070/099 seeds + ensureTaskStatusVocabKind.
var fallbackStatusKinds = map[string]string{
	"open":      KindOpen,
	"doing":     KindWorking,
	"blocked":   KindBlocked,
	"review":    KindReview,
	"done":      KindDone,
	"cancelled": KindCancelled,
}

// kindTerminal reports whether a kind is a terminal bucket.
func kindTerminal(kind string) bool {
	return kind == KindDone || kind == KindCancelled
}

// workspaceStatusKinds returns the status_text → kind map for a
// workspace: the six fallback defaults overlaid with every declared
// vocabulary entry. Store errors are swallowed (the fallback map still
// answers) — kind classification is advisory, never load-bearing for
// the mutation itself.
func (s *Service) workspaceStatusKinds(ctx context.Context, workspaceID string) map[string]string {
	out := make(map[string]string, len(fallbackStatusKinds))
	for k, v := range fallbackStatusKinds {
		out[k] = v
	}
	if vocab, err := s.store.ListTaskStatusVocab(ctx, workspaceID); err == nil {
		for _, v := range vocab {
			if v.StatusText != "" && v.Kind != "" {
				out[v.StatusText] = v.Kind
			}
		}
	}
	return out
}

// detectSkippedReview reports whether a status transition goes from a
// working-kind status straight to a terminal-kind status on a task
// whose status history never visited a review-kind status. Backs the
// non-blocking review_skipped nudge on task__update — flipping
// doing → done without ever passing through review is the failure
// mode the task-lifecycle rules exist to stop.
//
// history is the PRE-mutation status history (the entries already on
// the row before this transition is appended).
func (s *Service) detectSkippedReview(
	ctx context.Context, workspaceID, fromStatus, toStatus string,
	history []store.TaskStatusHistoryEntry,
) bool {
	kinds := s.workspaceStatusKinds(ctx, workspaceID)
	if kinds[fromStatus] != KindWorking {
		return false
	}
	if !kindTerminal(kinds[toStatus]) {
		return false
	}
	for _, e := range history {
		if e.Evt != "status_changed" && e.Evt != "created" {
			continue
		}
		if (e.From != "" && kinds[e.From] == KindReview) ||
			(e.To != "" && kinds[e.To] == KindReview) {
			return false
		}
	}
	return true
}
