package tasks

import (
	"context"
	"fmt"
	"sort"

	"github.com/don-works/mcplexer/internal/store"
)

// TouchesFilesMetaKey is the canonical meta key agents use to declare
// which file paths a task intends to modify. The coordination check
// queries this key via meta_match array-contains (see migration 072
// for the meta_json infrastructure). Plain strings, repo-relative,
// no glob expansion — exact match is the contract.
const TouchesFilesMetaKey = "touches_files"

// CoordinationWarning surfaces an overlap with another in-progress
// task. Returned by Service.CheckCoordinationOverlap; the gateway
// handler attaches the slice to its response envelope under the key
// "coordination_warnings" so the LLM caller can see collisions in
// the same round-trip as the status transition.
type CoordinationWarning struct {
	TaskID            string   `json:"task_id"`
	Title             string   `json:"title"`
	Status            string   `json:"status"`
	AssigneeSessionID string   `json:"assignee_session_id,omitempty"`
	AssigneePeerID    string   `json:"assignee_peer_id,omitempty"`
	OverlappingPaths  []string `json:"overlapping_paths"`
}

// CheckCoordinationOverlap returns warnings for other in-progress
// tasks in the same workspace whose meta.touches_files entries
// overlap with `t`'s declared paths. Returns (nil, nil) when:
//
//   - `t` is nil
//   - `t.Meta` has no touches_files key (the caller didn't opt in)
//   - `t.Status` isn't a working status (the collision-risk moment is
//     specifically "I'm about to start editing X")
//   - no other open task touches any of the same paths
//
// The query path is meta_match per path via the existing
// array-contains predicate in metaMatchSQL — no schema change, no new
// migration. Self is excluded by ID.
func (s *Service) CheckCoordinationOverlap(ctx context.Context, t *store.Task) ([]CoordinationWarning, error) {
	if t == nil {
		return nil, nil
	}
	paths := MetaListGet(t.Meta, TouchesFilesMetaKey)
	if len(paths) == 0 {
		return nil, nil
	}
	if !s.isWorkingStatus(ctx, t.WorkspaceID, t.Status) {
		return nil, nil
	}

	// One ListTasks per path. Each path is short, the meta_match
	// predicate hits a JSON1 index path, and the typical number of
	// declared paths per task is single-digit — the row budget is
	// fine. Merge in-memory by other-task-id.
	openOnly := false
	seen := map[string]*CoordinationWarning{}
	for _, p := range paths {
		f := store.TaskFilter{
			WorkspaceID:  t.WorkspaceID,
			OnlyTerminal: &openOnly,
			MetaMatch:    map[string]string{TouchesFilesMetaKey: p},
			Limit:        100,
		}
		rows, err := s.store.ListTasks(ctx, f)
		if err != nil {
			return nil, fmt.Errorf("list tasks touching %q: %w", p, err)
		}
		for i := range rows {
			other := &rows[i]
			if other.ID == t.ID {
				continue
			}
			if !s.isWorkingStatus(ctx, other.WorkspaceID, other.Status) {
				continue
			}
			w, ok := seen[other.ID]
			if !ok {
				w = &CoordinationWarning{
					TaskID:            other.ID,
					Title:             other.Title,
					Status:            other.Status,
					AssigneeSessionID: other.AssigneeSessionID,
					AssigneePeerID:    other.AssigneePeerID,
				}
				seen[other.ID] = w
			}
			w.OverlappingPaths = append(w.OverlappingPaths, p)
		}
	}
	if len(seen) == 0 {
		return nil, nil
	}

	// Stable order: by TaskID — keeps test fixtures deterministic and
	// keeps the LLM-facing output reproducible across daemon restarts.
	out := make([]CoordinationWarning, 0, len(seen))
	for _, w := range seen {
		sort.Strings(w.OverlappingPaths)
		out = append(out, *w)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TaskID < out[j].TaskID })
	return out, nil
}
