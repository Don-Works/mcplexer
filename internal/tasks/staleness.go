// staleness.go — non-blocking "finish what you start" aging signals.
// The gateway attaches the StaleTasksSummary to task__list and
// task__create response envelopes (same advisory pattern as
// coordination_warnings) so an agent opening a workspace sees its own
// abandoned doing/review/blocked rows without an explicit sweep call.
package tasks

import (
	"context"
	"fmt"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// Aging thresholds. Constants for now — per-workspace tuning is a
// future hook. The clock basis is updated_at (every status transition
// bumps it, so "age" reads as "time since this row last moved").
const (
	// StaleReviewAfter — a review-kind task older than this should have
	// been verified and closed (or bounced back to doing) by now.
	StaleReviewAfter = 24 * time.Hour
	// StaleBlockedAfter — a blocked task older than this has been
	// waiting on its unblock too long; re-triage it.
	StaleBlockedAfter = 72 * time.Hour
	// StaleAssignedOpenAfter — an open-kind task WITH an assignee that
	// hasn't moved in this long was claimed and abandoned.
	StaleAssignedOpenAfter = 7 * 24 * time.Hour
)

// staleTasksScanLimit caps how many open rows one summary scan reads.
const staleTasksScanLimit = 500

// StaleTaskRef is the compact pointer to the single oldest stale task.
type StaleTaskRef struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Status   string `json:"status"`
	AgeHours int    `json:"age_hours"`
}

// StaleTasksSummary is the envelope payload: {count, oldest, hint}.
type StaleTasksSummary struct {
	Count  int          `json:"count"`
	Oldest StaleTaskRef `json:"oldest"`
	Hint   string       `json:"hint"`
}

// StaleTasks scans the workspace's open tasks and summarises the ones
// that have sat in a lifecycle state past its threshold:
//
//   - review-kind  > StaleReviewAfter (24h)
//   - blocked-kind > StaleBlockedAfter (72h)
//   - open-kind with an assignee > StaleAssignedOpenAfter (7d)
//
// Working-kind rows are excluded — the lease sweep already reclaims
// abandoned working rows. Returns (nil, nil) when nothing is stale.
func (s *Service) StaleTasks(ctx context.Context, workspaceID string) (*StaleTasksSummary, error) {
	openOnly := false
	rows, err := s.store.ListTasks(ctx, store.TaskFilter{
		WorkspaceID:  workspaceID,
		OnlyTerminal: &openOnly,
		Limit:        staleTasksScanLimit,
	})
	if err != nil {
		return nil, fmt.Errorf("stale tasks scan: %w", err)
	}
	kinds := s.workspaceStatusKinds(ctx, workspaceID)
	now := time.Now().UTC()
	count := 0
	var oldest *store.Task
	var oldestAge time.Duration
	for i := range rows {
		t := &rows[i]
		age := now.Sub(t.UpdatedAt)
		if !staleByKind(kinds[t.Status], t, age) {
			continue
		}
		count++
		if oldest == nil || age > oldestAge {
			oldest, oldestAge = t, age
		}
	}
	if count == 0 || oldest == nil {
		return nil, nil
	}
	return &StaleTasksSummary{
		Count: count,
		Oldest: StaleTaskRef{
			ID:       oldest.ID,
			Title:    oldest.Title,
			Status:   oldest.Status,
			AgeHours: int(oldestAge.Hours()),
		},
		Hint: fmt.Sprintf(
			"%d stale task(s) in this workspace (review-kind > 24h, blocked > 72h, assigned-open > 7d). Resume them, hand off via task__assign, mark blocked with a reason, or close them.",
			count),
	}, nil
}

// staleByKind applies the per-kind threshold to one open row.
func staleByKind(kind string, t *store.Task, age time.Duration) bool {
	switch kind {
	case KindReview:
		return age >= StaleReviewAfter
	case KindBlocked:
		return age >= StaleBlockedAfter
	case KindOpen:
		if t.AssigneeSessionID == "" && t.AssigneePeerID == "" && t.AssigneeUserID == "" {
			return false
		}
		return age >= StaleAssignedOpenAfter
	default:
		return false
	}
}
