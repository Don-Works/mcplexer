// gossip_apply.go — apply remote TaskSyncEvent frames into the local
// task store using last-writer-wins per HLC.
//
// Authority rules:
//   - If the local row's HLC > remote.HLC → drop (stale; we already
//     have a newer version).
//   - If remote.HLC > local row's HLC → apply.
//   - If remote.HLC == local row's HLC → tiebreak by AssigneePeerID
//     (lexically smaller wins). This stays stable across reruns:
//     gossip is idempotent because (task_id, hlc, by_peer) uniquely
//     names an event.
//
// V1 NOT-IN-SCOPE — kept explicit so the next slice doesn't quietly
// expand the wire shape:
//   - task_notes bodies (events only — the dashboard reads notes
//     directly from the local store; cross-peer notes ride the
//     existing /mcplexer/task/1.0.0 protocol's payload phase).
//   - Soft-delete propagation. A remote delete is conveyed by the
//     receiver via a separate explicit DeleteTask call, not by an
//     HLC-stamped tombstone — until the v2 protocol adds a delete
//     frame, peers that purge a task locally do NOT broadcast the
//     deletion; the partner will keep seeing the row until they
//     re-sync from a fresh peer.
//   - Cross-workspace remapping. If the remote workspace_id doesn't
//     match a local workspace, the event is dropped (audit-only).
//     A future v2 will add workspace-binding-aware apply.
//
// The HLC tiebreak choice (lexically smaller peer wins) is arbitrary
// but stable — what matters is that both sides converge to the same
// state regardless of which side replays first.
package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/clock"
	"github.com/don-works/mcplexer/internal/p2p"
	"github.com/don-works/mcplexer/internal/store"
)

// RemoteTaskPatch is the JSON shape carried inside TaskSyncEvent
// FieldPatchesJSON. V1 ships the full field set on every event; a
// future v2 may shrink this to per-field diffs. The receiver applies
// fields whose JSON pointers are non-zero — empty strings on string
// fields mean "no patch" by convention so the v1 producer can emit a
// stable shape without nil-pointers.
//
// Clears is the list of field names that were explicitly cleared
// (assignee, due_at, meta, description). The receiver clears
// these AFTER applying the regular fields — clears win.
type RemoteTaskPatch struct {
	Title              string          `json:"title,omitempty"`
	Description        string          `json:"description,omitempty"`
	Status             string          `json:"status,omitempty"`
	Priority           string          `json:"priority,omitempty"`
	Meta               string          `json:"meta,omitempty"`
	TagsJSON           json.RawMessage `json:"tags,omitempty"`
	AssigneeSessionID  string          `json:"assignee_session_id,omitempty"`
	AssigneePeerID     string          `json:"assignee_peer_id,omitempty"`
	AssigneeOriginKind string          `json:"assignee_origin_kind,omitempty"`
	OriginPeerID       string          `json:"origin_peer_id,omitempty"`

	// Pointer fields use *time.Time so a nil patch means "no change",
	// matching the local store's null-vs-omit convention.
	DueAt    *time.Time `json:"due_at,omitempty"`
	ClosedAt *time.Time `json:"closed_at,omitempty"`

	// Clears lists fields that were explicitly cleared — these
	// are applied AFTER the regular fields so clears always win.
	Clears []string `json:"clears,omitempty"`
}

// ApplyRemoteEvent is the canonical receiver entry point — called by
// the p2p.TaskSyncService.ApplyRemoteEvent hook. Idempotent: replaying
// the same (task_id, hlc, by_peer) tuple twice is a no-op.
//
// fromPeerID is the libp2p peer that delivered the event; used in
// conflict tiebreaks and as the by_peer attribution if the event
// carries none.
func (s *Service) ApplyRemoteEvent(ctx context.Context, fromPeerID string, evt p2p.TaskSyncEvent) error {
	if evt.TaskID == "" || evt.HLC == "" {
		return errors.New("gossip apply: task_id + hlc required")
	}
	if evt.WorkspaceID == "" {
		return errors.New("gossip apply: workspace_id required")
	}
	// HLC receive rule: merge the remote stamp into the local clock
	// BEFORE the LWW comparison so our next local mutation stamps ahead
	// of a fast-wall-clock peer (otherwise such a peer permanently wins
	// conflicts). Best-effort — a non-canonical stamp (tests, older
	// peers) is skipped; the raw-string LWW comparison still runs.
	_ = clock.Observe(evt.HLC)
	// by_peer falls back to the delivering peer when the producer
	// omitted it (a daemon that doesn't know its own peer id).
	byPeer := evt.ByPeer
	if byPeer == "" {
		byPeer = fromPeerID
	}
	// Decode patch BEFORE the DB round-trip so a malformed event is
	// caught cheaply.
	var patch RemoteTaskPatch
	if len(evt.FieldPatchesJSON) > 0 {
		if err := json.Unmarshal(evt.FieldPatchesJSON, &patch); err != nil {
			return fmt.Errorf("gossip apply: decode patch: %w", err)
		}
	}

	existing, err := s.store.GetTask(ctx, evt.TaskID)
	if err == nil {
		return s.applyToExisting(ctx, existing, evt, &patch, byPeer)
	}
	if !errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("gossip apply: load existing: %w", err)
	}
	// Not present locally — materialize.
	return s.applyMaterialize(ctx, evt, &patch, byPeer)
}

// applyToExisting handles the common case: the local row exists. LWW
// per the HLC + by_peer tiebreak, and the existing status_history is
// appended to with a gossip-attributed entry (so the audit trail shows
// which mutation came from gossip).
func (s *Service) applyToExisting(
	ctx context.Context, existing *store.Task,
	evt p2p.TaskSyncEvent, patch *RemoteTaskPatch, byPeer string,
) error {
	if existing.WorkspaceID != evt.WorkspaceID {
		// Cross-workspace event — drop. The peer probably has a
		// stale workspace binding; we don't try to migrate here.
		return nil
	}
	cmp := compareHLCWithTiebreak(evt.HLC, byPeer, existing.HlcAt, existing.AssigneePeerID)
	switch {
	case cmp < 0:
		// Local is newer — drop.
		return nil
	case cmp == 0:
		// Exact duplicate by (hlc, by_peer) — idempotent no-op.
		return nil
	}
	now := time.Now().UTC()
	mergeFields(existing, patch)
	applyClears(existing, patch)
	existing.HlcAt = evt.HLC
	existing.UpdatedBySessionID = evt.BySession
	existing.OriginPeerID = firstNonEmptyStr(patch.OriginPeerID, existing.OriginPeerID, byPeer)
	appendGossipHistory(existing, evt, byPeer, now)
	if err := s.store.UpdateTask(ctx, existing); err != nil {
		return fmt.Errorf("gossip apply: update: %w", err)
	}
	s.publish(Event{
		Kind: EventTaskUpdated, WorkspaceID: existing.WorkspaceID,
		Task: existing, At: now,
	})
	return nil
}

// applyMaterialize creates a new local row from the gossip event. This
// is how a new task born on peer A first arrives at peer B. The new
// row is stamped with the remote HLC so subsequent gossip events from
// the same peer compare cleanly without us re-stamping with a local
// HLC.
func (s *Service) applyMaterialize(
	ctx context.Context, evt p2p.TaskSyncEvent,
	patch *RemoteTaskPatch, byPeer string,
) error {
	now := time.Now().UTC()
	t := &store.Task{
		ID:                 evt.TaskID,
		WorkspaceID:        evt.WorkspaceID,
		Title:              patch.Title,
		Description:        patch.Description,
		Status:             patch.Status,
		Priority:           patch.Priority,
		Meta:               patch.Meta,
		TagsJSON:           patch.TagsJSON,
		AssigneeSessionID:  patch.AssigneeSessionID,
		AssigneePeerID:     patch.AssigneePeerID,
		AssigneeOriginKind: patch.AssigneeOriginKind,
		OriginPeerID:       firstNonEmptyStr(patch.OriginPeerID, byPeer),
		DueAt:              patch.DueAt,
		ClosedAt:           patch.ClosedAt,
		SourceKind:         store.TaskSourcePeerImport,
		UpdatedBySessionID: evt.BySession,
		HlcAt:              evt.HLC,
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	applyClears(t, patch)
	if t.Title == "" {
		// A title-less remote row is a wire bug; refuse rather than
		// commit a row that fails Title-required invariants on the
		// next local edit.
		return fmt.Errorf("gossip apply: cannot materialize task %s without title", evt.TaskID)
	}
	if t.Status == "" {
		t.Status = "open"
	}
	// Seed history with one "received via gossip" entry so the audit
	// trail shows where the row came from.
	history := []store.TaskStatusHistoryEntry{{
		At: now, BySession: evt.BySession, ByPeer: byPeer,
		Evt: "received_gossip", To: t.Status,
	}}
	t.StatusHistoryJSON, _ = json.Marshal(history)
	if err := s.store.CreateTask(ctx, t); err != nil {
		return fmt.Errorf("gossip apply: materialize: %w", err)
	}
	s.publish(Event{
		Kind: EventTaskCreated, WorkspaceID: t.WorkspaceID,
		Task: t, At: now,
	})
	return nil
}

// mergeFields applies non-zero fields from patch onto existing. The
// "non-zero" check is per-field: pointer fields require nil-check;
// string fields require empty-check. Tags + descriptions are replaced
// wholesale (no per-token merge — gossip events carry the post-state).
func mergeFields(existing *store.Task, patch *RemoteTaskPatch) {
	if patch.Title != "" {
		existing.Title = patch.Title
	}
	if patch.Description != "" {
		existing.Description = patch.Description
	}
	if patch.Status != "" {
		existing.Status = patch.Status
	}
	if patch.Priority != "" {
		existing.Priority = patch.Priority
	}
	if patch.Meta != "" {
		existing.Meta = patch.Meta
	}
	if len(patch.TagsJSON) > 0 {
		existing.TagsJSON = patch.TagsJSON
	}
	if patch.AssigneeSessionID != "" {
		existing.AssigneeSessionID = patch.AssigneeSessionID
	}
	if patch.AssigneePeerID != "" {
		existing.AssigneePeerID = patch.AssigneePeerID
	}
	if patch.AssigneeOriginKind != "" {
		existing.AssigneeOriginKind = patch.AssigneeOriginKind
	}
	if patch.DueAt != nil {
		existing.DueAt = patch.DueAt
	}
	if patch.ClosedAt != nil {
		existing.ClosedAt = patch.ClosedAt
	}
}

// applyClears clears the named fields from the task row.
// This is called AFTER mergeFields so explicit clears win over
// any values set in the patch.
func applyClears(existing *store.Task, patch *RemoteTaskPatch) {
	for _, field := range patch.Clears {
		switch strings.ToLower(field) {
		case "assignee":
			existing.AssigneeSessionID = ""
			existing.AssigneePeerID = ""
			existing.AssigneeOriginKind = store.TaskAssigneeLocal
			existing.AssignedAt = nil
			existing.LeaseExpiresAt = nil
		case "due_at":
			existing.DueAt = nil
		case "meta":
			existing.Meta = ""
		case "description":
			existing.Description = ""
		case "closed_at":
			existing.ClosedAt = nil
		}
	}
}

// appendGossipHistory adds one row-local audit entry tagged with the
// event source (HLC + peer) so the dashboard's "history" view shows
// the gossip path next to local edits.
func appendGossipHistory(t *store.Task, evt p2p.TaskSyncEvent, byPeer string, now time.Time) {
	history := readHistory(t.StatusHistoryJSON)
	history = append(history, store.TaskStatusHistoryEntry{
		At:        now,
		BySession: evt.BySession,
		ByPeer:    byPeer,
		Evt:       "gossip_applied",
		Note:      "hlc=" + evt.HLC,
	})
	t.StatusHistoryJSON, _ = json.Marshal(history)
}

// compareHLCWithTiebreak returns >0 if (aHLC, aPeer) should win over
// (bHLC, bPeer) under LWW + smaller-peer tiebreak, <0 if it should
// lose, 0 if they're an exact duplicate.
//
// The tiebreak uses string compare on the peer id — libp2p peer ids
// are stable + comparable, so this gives deterministic convergence
// regardless of replay order.
func compareHLCWithTiebreak(aHLC, aPeer, bHLC, bPeer string) int {
	if aHLC > bHLC {
		return 1
	}
	if aHLC < bHLC {
		return -1
	}
	// Equal HLC — smaller peer wins.
	return strings.Compare(bPeer, aPeer)
}

// firstNonEmptyStr returns the first non-empty string in values.
func firstNonEmptyStr(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// BuildLocalEventForGossip is a helper for the wiring layer: given a
// local task row, marshal it into a TaskSyncEvent so the p2p source
// adapter can hand it to the protocol. Keeps the conversion logic in
// one place so source + tests stay in sync.
//
// Clears is populated by checking which nullable fields are empty/nil
// in the store row — this lets the receiver distinguish "never set" from
// "explicitly cleared". The receiver applies clears AFTER regular fields.
func BuildLocalEventForGossip(t *store.Task, selfPeerID string) p2p.TaskSyncEvent {
	patch := RemoteTaskPatch{
		Title:              t.Title,
		Description:        t.Description,
		Status:             t.Status,
		Priority:           t.Priority,
		Meta:               t.Meta,
		TagsJSON:           t.TagsJSON,
		AssigneeSessionID:  t.AssigneeSessionID,
		AssigneePeerID:     t.AssigneePeerID,
		AssigneeOriginKind: t.AssigneeOriginKind,
		OriginPeerID:       t.OriginPeerID,
		DueAt:              t.DueAt,
		ClosedAt:           t.ClosedAt,
	}
	var clears []string
	if t.AssigneeSessionID == "" && t.AssigneePeerID == "" {
		clears = append(clears, "assignee")
	}
	if t.DueAt == nil {
		clears = append(clears, "due_at")
	}
	if t.Meta == "" {
		clears = append(clears, "meta")
	}
	if t.Description == "" {
		clears = append(clears, "description")
	}
	if t.ClosedAt == nil {
		clears = append(clears, "closed_at")
	}
	patch.Clears = clears
	raw, _ := json.Marshal(&patch)
	byPeer := t.OriginPeerID
	if byPeer == "" {
		byPeer = selfPeerID
	}
	return p2p.TaskSyncEvent{
		Type:             "task_event",
		TaskID:           t.ID,
		WorkspaceID:      t.WorkspaceID,
		HLC:              t.HlcAt,
		BySession:        t.UpdatedBySessionID,
		ByPeer:           byPeer,
		FieldPatchesJSON: raw,
	}
}
