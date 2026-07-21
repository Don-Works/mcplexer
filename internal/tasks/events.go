// events.go — emits kind="task_event" mesh messages for every task
// mutation. Implements PLAN.md "Mesh event shape" + "Notify
// suppression rules" + "Chain-depth propagation".
//
// Design notes:
//   - Goes through the mesh.Manager.Send pipeline (NOT raw
//     store.InsertMeshMessage) so workspace_path is auto-filled, p2p
//     dispatch fires, and notify subscribers wake up.
//   - Defines its own MeshSender interface so the tasks package
//     doesn't transitively pull in internal/mesh (and friends) into
//     test mocks. mesh.Manager satisfies the interface unchanged.
//   - Chain-depth flows from the triggering MeshMessage when the
//     mutation came from a mesh event (worker firing on a task
//     assignment, peer-import accepting an offer, etc). Missing
//     triggering message = depth 0 = stamp "chain-depth:1" on the
//     emission so a worker firing off it sees depth 1.
//   - The bus.Publish path (SSE fan-out) is unchanged and fires
//     independently. Emitter + Bus are complementary — Bus drives the
//     dashboard, Emitter drives mesh-aware peers + workers.
package tasks

import (
	"context"
	"fmt"
	"strings"

	"github.com/don-works/mcplexer/internal/mesh"
	"github.com/don-works/mcplexer/internal/store"
)

// MeshSender is the slice of mesh.Manager that Emitter depends on.
// Keeping it small + local lets test mocks satisfy it without pulling
// the whole mesh package surface (and lets us swap in a future
// transport without touching the events.go contract).
type MeshSender interface {
	Send(ctx context.Context, meta mesh.SessionMeta, req mesh.SendRequest) (*store.MeshMessage, error)
}

// ReplicationHook is the callback the Emitter fires, alongside the mesh
// message, on every observable task mutation in a workspace that may be
// linked to a peer. The daemon wires it to
// replication.Coordinator.OnTaskEvent; the coordinator decides whether
// the workspace is actually linked and fans out accordingly. source is
// "agent"/"worker"/"user"/"system" for local mutations and "peer" for
// peer-imported ones (so the coordinator's echo guard can drop the
// latter). Nil-safe: a nil hook disables task replication.
type ReplicationHook func(ctx context.Context, workspaceID, taskID, source string)

// Emitter publishes task_event mesh messages on every observable task
// mutation. Construct via NewEmitter; pass to Service.SetEmitter.
//
// All methods are nil-safe so callers can drop the emitter without
// branching at every call-site.
type Emitter struct {
	sender   MeshSender
	replHook ReplicationHook
}

// NewEmitter constructs an Emitter wired to a MeshSender (typically
// the daemon-singleton *mesh.Manager). Pass nil to disable emission —
// useful in tests that don't exercise mesh dispatch.
func NewEmitter(sender MeshSender) *Emitter {
	return &Emitter{sender: sender}
}

// SetReplicationHook installs the linked-workspace replication callback
// post-construction. Nil-safe — without it, the Emitter only does mesh
// emission (the pre-linked-workspaces behaviour).
func (e *Emitter) SetReplicationHook(h ReplicationHook) {
	if e == nil {
		return
	}
	e.replHook = h
}

// replicationSourceFromActor maps the Emitter's ActorKind onto the
// coordinator's source vocabulary. The only value that matters is
// "peer-import" → "peer", which trips the coordinator's echo guard so a
// task we received from a peer is never re-replicated back out. Every
// local actor collapses to its own kind (non-"peer"), which replicates.
func replicationSourceFromActor(actorKind string) string {
	if actorKind == "peer-import" {
		return "peer"
	}
	if actorKind == "" {
		return "agent"
	}
	return actorKind
}

// maybeReplicate fires the replication hook for a mutation unless it is a
// deletion (deletion sync is handled separately) or the hook is unset.
// The coordinator owns the link-or-not and echo decisions; the Emitter
// just forwards every candidate mutation with the right source tag.
func (e *Emitter) maybeReplicate(ctx context.Context, t *store.Task, evt string, ec EmitContext) {
	if e == nil || e.replHook == nil || t == nil {
		return
	}
	if evt == "deleted" {
		return
	}
	e.replHook(ctx, t.WorkspaceID, t.ID, replicationSourceFromActor(ec.ActorKind))
}

// EmitContext is the shared input to every Emit* method. Keeping it
// in one struct avoids 7-argument func signatures and matches the
// shape the service layer can produce in one place. Mirrors what the
// MCP handler / REST handler can compute up-front (session, workspace
// path, optional upstream message).
type EmitContext struct {
	// Triggering is the upstream mesh message (when present) that drove
	// the current mutation. Used to propagate chain-depth onto the new
	// emission. nil = depth 0 (fresh chain).
	Triggering *store.MeshMessage
	// ActorKind tags who fired the mutation: "agent" (default),
	// "worker" (scheduled in-process loop), "user" (REST call from
	// the dashboard), "peer-import" (came in via libp2p), "system".
	ActorKind string
	// SessionID + WorkspacePath identify the local session that
	// produced the mutation so mesh.Send can fill agent + workspace
	// metadata correctly. Empty = no active session (REST / system).
	SessionID     string
	WorkspacePath string
}

// EmitCreated fires task_event:created. Notify=false per PLAN.md.
func (e *Emitter) EmitCreated(ctx context.Context, t *store.Task, ec EmitContext) {
	if e == nil || e.sender == nil || t == nil {
		return
	}
	e.emit(ctx, t, "created", "low", false, ec)
}

// EmitUpdated fires a generic task_event:updated for non-status patches.
// Notify=false (the dashboard already subscribes via SSE).
func (e *Emitter) EmitUpdated(ctx context.Context, t *store.Task, ec EmitContext) {
	if e == nil || e.sender == nil || t == nil {
		return
	}
	e.emit(ctx, t, "updated", "low", false, ec)
}

// EmitAssigned fires task_event:assigned. notify = assigner!=assignee AND
// actor!=worker (PLAN.md notify suppression rules).
func (e *Emitter) EmitAssigned(ctx context.Context, t *store.Task, assignerSession string, ec EmitContext) {
	if e == nil || e.sender == nil || t == nil {
		return
	}
	notify := shouldNotifyAssigned(t, assignerSession, ec.ActorKind)
	e.emit(ctx, t, "assigned", "normal", notify, ec)
}

// EmitClaimed fires task_event:claimed. notify = claimant differs from
// previous-assigned (otherwise quiet — same-session continuation).
func (e *Emitter) EmitClaimed(ctx context.Context, t *store.Task, previousAssignee string, ec EmitContext) {
	if e == nil || e.sender == nil || t == nil {
		return
	}
	notify := previousAssignee != "" && previousAssignee != t.AssigneeSessionID
	e.emit(ctx, t, "claimed", "normal", notify, ec)
}

// EmitClosed fires task_event:closed. notify = closer differs from
// original assignee (PLAN.md).
func (e *Emitter) EmitClosed(ctx context.Context, t *store.Task, closerSession string, ec EmitContext) {
	if e == nil || e.sender == nil || t == nil {
		return
	}
	notify := t.AssigneeSessionID != "" && t.AssigneeSessionID != closerSession
	e.emit(ctx, t, "closed", "normal", notify, ec)
}

// EmitDeleted fires task_event:deleted. Notify=false (PLAN.md).
func (e *Emitter) EmitDeleted(ctx context.Context, t *store.Task, ec EmitContext) {
	if e == nil || e.sender == nil || t == nil {
		return
	}
	e.emit(ctx, t, "deleted", "low", false, ec)
}

// EmitStatusChanged fires task_event:status_changed. Notify hardcoded
// FALSE for Phase 2 (locked decision #5 in PLAN.md — too noisy by
// default; the per-workspace opt-in moves to Phase 5).
func (e *Emitter) EmitStatusChanged(ctx context.Context, t *store.Task, from, to string, ec EmitContext) {
	if e == nil || e.sender == nil || t == nil {
		return
	}
	e.emitStatusChanged(ctx, t, from, to, ec)
}

// EmitNote fires task_event:note_appended. Notify=false (notes are
// fan-out via SSE; the user is reading the task page).
func (e *Emitter) EmitNote(ctx context.Context, t *store.Task, note *store.TaskNote, ec EmitContext) {
	if e == nil || e.sender == nil || t == nil || note == nil {
		return
	}
	e.emit(ctx, t, "note_appended", "low", false, ec)
}

// emit is the shared rendering path. Builds the mesh.SendRequest per
// PLAN.md "Mesh event shape" and dispatches via the wired sender.
// Errors are logged-then-swallowed — task mutations succeeded; a
// notification failure must not unwind the row.
func (e *Emitter) emit(
	ctx context.Context, t *store.Task, evt, priority string, notify bool, ec EmitContext,
) {
	tags := buildEventTags(evt, t, ec)
	content := fmt.Sprintf("Task %s: %s — %s", evt, t.Title, t.Status)
	req := mesh.SendRequest{
		Kind:          "task_event",
		Content:       content,
		Priority:      priority,
		Audience:      "*",
		Tags:          tags,
		NotifyUser:    notify,
		ActorKind:     ec.ActorKind,
		WorkspacePath: ec.WorkspacePath,
	}
	meta := mesh.SessionMeta{
		SessionID:     ec.SessionID,
		WorkspaceIDs:  []string{t.WorkspaceID},
		WorkspacePath: ec.WorkspacePath,
	}
	// Best-effort: emission failure must not unwind the mutation. The
	// service already audited the write + published to the SSE bus
	// before we got here.
	_, _ = e.sender.Send(ctx, meta, req)
	e.maybeReplicate(ctx, t, evt, ec)
}

// emitStatusChanged is the specialized status-changed path so the
// content string captures the transition. Phase 2 hardcodes notify
// false (PLAN.md "Notify suppression rules" — locked decision #5).
func (e *Emitter) emitStatusChanged(
	ctx context.Context, t *store.Task, from, to string, ec EmitContext,
) {
	tags := buildEventTags("status_changed", t, ec)
	// Stamp the transition as structured tags so a WorkerMeshTrigger can
	// AND on it (StatusFromMatch/StatusToMatch) — the dispatcher parses
	// these with the same CutPrefix idiom it uses for "from:". Status
	// values are short tokens by convention; a comma would split the tag
	// list, so transition triggers don't support comma-bearing statuses
	// (use tag_match / content_regex for those rare cases).
	tags += ",status_from:" + from + ",status_to:" + to
	content := fmt.Sprintf("Task status_changed: %s — %s → %s", t.Title, from, to)
	req := mesh.SendRequest{
		Kind:          "task_event",
		Content:       content,
		Priority:      "low",
		Audience:      "*",
		Tags:          tags,
		NotifyUser:    false, // hardcoded per locked decision #5
		ActorKind:     ec.ActorKind,
		WorkspacePath: ec.WorkspacePath,
	}
	meta := mesh.SessionMeta{
		SessionID:     ec.SessionID,
		WorkspaceIDs:  []string{t.WorkspaceID},
		WorkspacePath: ec.WorkspacePath,
	}
	_, _ = e.sender.Send(ctx, meta, req)
	e.maybeReplicate(ctx, t, "status_changed", ec)
}

// buildEventTags renders the comma-separated tag list per PLAN.md
// "Mesh event shape". Includes the canonical task_event:<evt>,
// task_id:<id>, workspace:<id> trio, any task tags (read from
// TagsJSON via a lightweight unmarshal), and a chain-depth bump when
// a triggering message is present.
func buildEventTags(evt string, t *store.Task, ec EmitContext) string {
	parts := []string{
		"task_event:" + evt,
		"task_id:" + t.ID,
		"workspace:" + t.WorkspaceID,
	}
	for _, tag := range taskTagList(t) {
		// Never let a user-authored task tag inject into a structured
		// namespace the emitter+dispatcher trust: a task labelled
		// "status_to:review" could otherwise satisfy a status-transition
		// worker trigger on a non-status event, and "from:<peer>" could
		// spoof the p2p source filter. Drop any such tag.
		if tag != "" && !isReservedTag(tag) {
			parts = append(parts, tag)
		}
	}
	if depth := nextChainDepth(ec.Triggering); depth > 0 {
		parts = append(parts, mesh.ChainDepthTag(depth))
	}
	return strings.Join(parts, ",")
}

// reservedTagPrefixes are the structured tag namespaces the emitter +
// dispatcher own. A task's user-authored tags must never inject into
// them — see the comment in buildEventTags for the concrete spoof each
// guards against.
var reservedTagPrefixes = []string{
	"task_event:", "task_id:", "workspace:",
	"status_from:", "status_to:", "chain-depth:", "from:",
}

// isReservedTag reports whether a user tag would land in a reserved
// structured namespace and must therefore be dropped from the emission.
func isReservedTag(tag string) bool {
	for _, p := range reservedTagPrefixes {
		if strings.HasPrefix(tag, p) {
			return true
		}
	}
	return false
}

// nextChainDepth reads the upstream chain-depth (when present) and
// returns the next emission's depth. Missing/zero → depth 1 (fresh
// chain). This matches the worker-runner's bump-on-emit contract so
// the loop guard sees a monotonic chain.
func nextChainDepth(triggering *store.MeshMessage) int {
	if triggering == nil {
		return 1
	}
	return mesh.ChainDepthFromTags(triggering.Tags) + 1
}

// taskTagList unmarshals the task's TagsJSON into a flat []string. We
// avoid importing encoding/json here to keep the file lean — the
// TagsJSON column is the canonical "[a,b]" form, and a trivial
// substring split is enough for the mesh tag list. Any malformed
// JSON falls through to an empty slice so we never panic.
func taskTagList(t *store.Task) []string {
	raw := strings.TrimSpace(string(t.TagsJSON))
	if raw == "" || raw == "null" || raw == "[]" {
		return nil
	}
	// Trim brackets and split — TagsJSON values are short ASCII tag
	// tokens by convention, no embedded commas or quotes-with-commas.
	raw = strings.TrimPrefix(raw, "[")
	raw = strings.TrimSuffix(raw, "]")
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		p = strings.Trim(p, `"`)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// shouldNotifyAssigned implements PLAN.md "Notify suppression rules"
// for task_event:assigned — TRUE iff assigner != assignee AND
// actor_kind != "worker".
func shouldNotifyAssigned(t *store.Task, assignerSession, actorKind string) bool {
	if actorKind == "worker" {
		return false
	}
	if t.AssigneeSessionID == "" && t.AssigneePeerID == "" {
		// Workspace-broadcast claim — first claimant wins. Notify the
		// workspace so somebody picks it up.
		return true
	}
	if assignerSession != "" && assignerSession == t.AssigneeSessionID {
		return false // self-assign
	}
	return true
}
