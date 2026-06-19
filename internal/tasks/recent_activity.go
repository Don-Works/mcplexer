package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// ActivityEntry is one row in the per-workspace recent-activity feed.
// Derived from task rows + their status_history JSON; the feed is a
// flat chronological view of "what just happened here" so an agent
// joining a session can catch up without scrolling 75 mesh broadcasts.
//
// See task 01KSJ053RRTDBW1AQBVVVSJX26.
type ActivityEntry struct {
	At        time.Time `json:"at"`
	TaskID    string    `json:"task_id"`
	TaskTitle string    `json:"task_title"`
	Status    string    `json:"status"`         // current status of the task at fetch time
	Evt       string    `json:"evt"`            // status_changed | assigned | closed | composed | lease_expired
	From      string    `json:"from,omitempty"` // old value (status, assignee, etc.)
	To        string    `json:"to,omitempty"`   // new value
	BySession string    `json:"by_session,omitempty"`
	ByPeer    string    `json:"by_peer,omitempty"`
	Note      string    `json:"note,omitempty"`
}

// RecentActivity returns a chronological feed (newest first) of status
// transitions in the given workspace since `since`. Caps at `limit`
// entries; default 50, max 500. Pass zero-time `since` to mean
// "last hour".
//
// Implementation: fetches tasks updated since `since` (cheap filter
// already in TaskFilter), reads each row's StatusHistoryJSON in Go,
// keeps entries where `entry.At >= since`, flattens, sorts by At desc.
// No new DB schema, no new index — the per-task status_history
// column carries everything the feed needs.
//
// Not included this milestone: note_appended events (would require a
// second query against task_notes). Filed as a followup if it becomes
// useful — most agents care about status flow, not the comments.
func (s *Service) RecentActivity(ctx context.Context, workspaceID string, since time.Time, limit int) ([]ActivityEntry, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	if since.IsZero() {
		since = time.Now().UTC().Add(-1 * time.Hour)
	}
	sinceCopy := since
	f := store.TaskFilter{
		WorkspaceID:  workspaceID,
		UpdatedAfter: &sinceCopy,
		Limit:        500, // upper bound on tasks scanned; per-task entries get filtered below
	}
	rows, err := s.store.ListTasks(ctx, f)
	if err != nil {
		return nil, fmt.Errorf("list tasks for recent activity: %w", err)
	}
	out := make([]ActivityEntry, 0, len(rows))
	for _, t := range rows {
		if len(t.StatusHistoryJSON) == 0 {
			continue
		}
		var history []store.TaskStatusHistoryEntry
		if err := json.Unmarshal(t.StatusHistoryJSON, &history); err != nil {
			// Bad row — skip rather than fail the whole feed.
			continue
		}
		for _, h := range history {
			if h.At.Before(since) {
				continue
			}
			out = append(out, ActivityEntry{
				At:        h.At,
				TaskID:    t.ID,
				TaskTitle: t.Title,
				Status:    t.Status,
				Evt:       h.Evt,
				From:      h.From,
				To:        h.To,
				BySession: h.BySession,
				ByPeer:    h.ByPeer,
				Note:      h.Note,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
