// Package tasks is the operational task primitive — per-workspace
// freeform-status work items that mirror patterns from memory/mesh
// (workspace scoping, peer-offer/request, FTS search, peer-origin
// provenance) but are operational, not informational.
//
// Phase 1 (this PR): local CRUD + status_history audit + workspace
// vocabulary for terminal-status discovery.
// Phase 2: mesh lifecycle events on every mutation.
// Phase 3: cross-peer offers + assignment via /mcplexer/task/1.0.0.
//
// See .planning/tasks/PLAN.md for the full design.
package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/clock"
	"github.com/don-works/mcplexer/internal/p2p"
	"github.com/don-works/mcplexer/internal/store"
)

// LeaseTTL is the per-heartbeat lease window for status=doing rows.
// Five minutes matches the UI's HEARTBEAT_TTL_MS staleness threshold —
// a row's owner has to bump within this window or the sweep clears
// the assignee + appends evt=lease_expired. Exported so tests can
// reference the same constant the service uses.
const LeaseTTL = 5 * time.Minute

// Service orchestrates task CRUD on top of the TaskStore. It owns the
// status-history append-on-write contract + the "closed_at flips when
// status enters terminal vocabulary" rule. Audit emission lives here
// too so it stays adjacent to the mutation.
type Service struct {
	store      store.TaskStore
	workspaces WorkspaceLookup
	peerScopes PeerScopeLookup
	bus        *Bus     // optional; nil disables event fan-out
	emitter    *Emitter // optional; nil disables mesh task_event emission

	// Phase 3 — cross-peer fields (see offer.go).
	taskShare   *p2p.TaskShareService // wired by SetTaskShare; nil disables cross-peer surface
	localPeerID string                // best-effort self peer id for outgoing offer rows

	// brainHook dual-writes a task to its canonical .md file on every
	// mutation. Wired by the daemon only when the brain is enabled; nil =
	// today's behaviour (no file write).
	brainHook BrainHook

	// schemaErr is the boot-probe failure (see health.go). Written once
	// in New before the service escapes the constructor; read via
	// SchemaErr by the gateway to surface an actionable degraded-mode
	// error on every task__* call instead of an opaque SQL error.
	schemaErr error
}

// BrainHook is the optional dual-write sink the MCPlexer Brain registers
// so a task mutation also serializes the task's canonical .md file. All
// methods are best-effort: an implementation MUST NOT fail the mutation,
// and the service calls them after the DB write succeeds. The brain.
// Serializer satisfies this interface.
type BrainHook interface {
	// OnTaskWrite is called with the post-mutation task row on create,
	// update, claim, compose, lease change, work-context change, and note
	// append (the note is folded into the task body).
	OnTaskWrite(ctx context.Context, t *store.Task)
	// OnTaskDelete is called when a task is soft-deleted.
	OnTaskDelete(ctx context.Context, id, workspaceID string)
}

// SetBrainHook installs the brain dual-write hook post-construction. Nil
// is safe (the default) — the service never writes files.
func (s *Service) SetBrainHook(h BrainHook) { s.brainHook = h }

// WorkspaceLookup is the slice of store.WorkspaceStore the Service
// needs for resolving workspace names on outgoing offer envelopes.
// Kept narrow so test fakes don't have to satisfy the full Workspace
// store surface.
type WorkspaceLookup interface {
	GetWorkspace(ctx context.Context, id string) (*store.Workspace, error)
}

// PeerScopeLookup is the slice of store.P2PPeerStore + scope helpers
// the Service needs to gate inbound offers by peer scope. Returns
// (false, nil) for unknown/revoked peers — callers treat both as no
// permission.
type PeerScopeLookup interface {
	HasPeerScope(ctx context.Context, peerID, scope string) (bool, error)
}

// New constructs a Service. The store must satisfy store.TaskStore;
// store.Store is the canonical caller. Runs the one-shot schema health
// probe (health.go) so a broken tasks schema degrades loudly at boot
// rather than opaquely at first use.
func New(s store.TaskStore) *Service {
	svc := &Service{store: s}
	svc.probeSchema(context.Background())
	return svc
}

// SetWorkspaceLookup wires the workspace resolver post-construction.
// Nil-safe — offers will fall back to empty workspace names.
func (s *Service) SetWorkspaceLookup(w WorkspaceLookup) {
	s.workspaces = w
}

// SetPeerScopeLookup wires the peer-scope lookup post-construction.
// Required for inbound offer scope checks; without it, all incoming
// offers are denied.
func (s *Service) SetPeerScopeLookup(p PeerScopeLookup) {
	s.peerScopes = p
}

// SetLocalPeerID stamps the local libp2p peer id onto outgoing offer
// rows so the dashboard can show "sent from <me>" stable across
// daemon restarts. Empty = unknown.
func (s *Service) SetLocalPeerID(id string) {
	s.localPeerID = id
}

// SetBus installs the event bus post-construction. Optional — a nil
// bus is safe; Publish is a no-op. Wired by the daemon so the SSE
// handler can subscribe.
func (s *Service) SetBus(b *Bus) {
	s.bus = b
}

// Bus returns the installed event bus, or nil if none was wired. The
// HTTP handler uses this to refuse the SSE endpoint defensively.
func (s *Service) Bus() *Bus {
	return s.bus
}

// SetEmitter installs the mesh task_event emitter post-construction.
// Wired by the daemon after the mesh.Manager is built so the service
// can publish kind=task_event mesh messages on every mutation. Nil
// disables emission — Emitter methods are all nil-safe.
func (s *Service) SetEmitter(e *Emitter) {
	s.emitter = e
}

// publish is the internal shorthand that tolerates a nil bus. It is the
// single funnel every observable task mutation reaches, so the brain
// dual-write hook fires here too (when wired) — keeping the store
// policy-free and covering every mutator (create/update/claim/compose/
// lease-sweep/release/work-context/note) without per-site wiring.
func (s *Service) publish(evt Event) {
	s.fireBrainHook(evt)
	if s.bus == nil {
		return
	}
	s.bus.Publish(evt)
}

// publishBusOnly fans an event to the SSE bus WITHOUT tripping the
// dual-write brain hook. Used for volatile-only mutations — currently
// the heartbeat lease bump — where the dashboard wants a live refresh
// but the canonical on-disk .md file must NOT be re-serialized: the
// lease window is ephemeral, and churning the brain files on every
// 5-minute heartbeat would spam the federation/git layer with no
// durable content change.
func (s *Service) publishBusOnly(evt Event) {
	if s.bus == nil {
		return
	}
	s.bus.Publish(evt)
}

// fireBrainHook routes a task event to the brain dual-write hook. Delete
// events remove the file; every other kind re-serializes the post-mutation
// task. A note-appended event carries the parent Task (folded into the
// body) as well as the Note.
func (s *Service) fireBrainHook(evt Event) {
	if s.brainHook == nil || evt.Task == nil {
		return
	}
	ctx := context.Background()
	if evt.Kind == EventTaskDeleted {
		s.brainHook.OnTaskDelete(ctx, evt.Task.ID, evt.Task.WorkspaceID)
		return
	}
	s.brainHook.OnTaskWrite(ctx, evt.Task)
}

// serializeBrain re-serializes a single task's canonical .md file via the
// brain hook WITHOUT touching the event bus / mesh emitter. It is the
// brain-only notification for mutators (compose / decompose) that
// deliberately stay quiet on the bus to avoid mesh noise but still change
// the on-disk task file (meta.composes / meta.composed_by). Without this,
// the .md files silently drift from the DB on every compose/decompose.
func (s *Service) serializeBrain(ctx context.Context, t *store.Task) {
	if s.brainHook == nil || t == nil {
		return
	}
	s.brainHook.OnTaskWrite(ctx, t)
}

// CreateOptions is the input to Service.Create — a partial Task plus the
// runtime context (who is creating it, in what session).
type CreateOptions struct {
	WorkspaceID string
	Title       string
	Description string
	Status      string
	Priority    string
	DueAt       *time.Time
	Tags        []string
	Meta        string
	Assignee    *Assignee
	ComposeInto string // parent task id; appended to parent's meta composes list

	SourceKind         string
	SourceSessionID    string
	SourceToolCallID   string
	CreatedBySessionID string

	// Phase-2 mesh plumbing. Triggering carries the upstream mesh
	// message (when the mutation came from one) so the emitted
	// task_event:* propagates chain-depth instead of starting fresh.
	// ActorKind tags who fired the mutation ("agent"|"worker"|"user"|...).
	// WorkspacePath is the absolute repo path; mesh.Send uses it to
	// auto-fill repo/branch metadata for cross-machine subscribers.
	Triggering    *store.MeshMessage
	ActorKind     string
	WorkspacePath string
}

// Assignee captures the polymorphic assignee identity. Local sessions
// set SessionID; remote-peer assignees set PeerID + SessionID; human assignees
// set UserID.
type Assignee struct {
	SessionID string // local session id of the assignee (empty = unassigned)
	PeerID    string // libp2p peer id; non-empty = remote
	UserID    string // user id for human assignees (non-empty = human)
}

// Create inserts a new task with the initial "created" history entry.
// Returns the canonical row read back from the store.
func (s *Service) Create(ctx context.Context, opts CreateOptions) (*store.Task, error) {
	if strings.TrimSpace(opts.Title) == "" {
		return nil, errors.New("title is required")
	}
	if strings.TrimSpace(opts.WorkspaceID) == "" {
		return nil, errors.New("workspace_id is required")
	}
	now := time.Now().UTC()
	tagsJSON, err := json.Marshal(opts.Tags)
	if err != nil {
		return nil, fmt.Errorf("encode tags: %w", err)
	}
	if opts.Status == "" {
		opts.Status = "open"
	}
	if opts.Priority == "" {
		opts.Priority = "normal"
	}
	// Normalise meta to canonical JSON shape on write. Accepts legacy
	// frontmatter input (REST/MCP callers may still send `key: value`
	// style after the 072 cut-over) and rewrites it transparently.
	normalisedMeta, err := MetaToJSON(opts.Meta)
	if err != nil {
		return nil, fmt.Errorf("encode meta: %w", err)
	}

	history := []store.TaskStatusHistoryEntry{{
		At:        now,
		BySession: opts.CreatedBySessionID,
		Evt:       "created",
		To:        opts.Status,
	}}
	historyJSON, _ := json.Marshal(history)

	t := &store.Task{
		WorkspaceID:        opts.WorkspaceID,
		Title:              opts.Title,
		Description:        opts.Description,
		Status:             opts.Status,
		Priority:           opts.Priority,
		DueAt:              opts.DueAt,
		TagsJSON:           tagsJSON,
		Meta:               normalisedMeta,
		SourceKind:         firstNonEmpty(opts.SourceKind, store.TaskSourceAgent),
		SourceSessionID:    opts.SourceSessionID,
		SourceToolCallID:   opts.SourceToolCallID,
		CreatedBySessionID: opts.CreatedBySessionID,
		StatusHistoryJSON:  historyJSON,
		// Stamp a fresh HLC so the gossip watermark picks this row up
		// immediately. Empty HlcAt would let the store fall back to a
		// fresh stamp anyway — explicit here so the contract is visible
		// in the service layer (this is where row identity is born).
		HlcAt:     clock.Now(),
		CreatedAt: now,
		UpdatedAt: now,
	}
	if opts.Assignee != nil && (opts.Assignee.SessionID != "" || opts.Assignee.PeerID != "" || opts.Assignee.UserID != "") {
		t.AssigneeSessionID = opts.Assignee.SessionID
		t.AssigneePeerID = opts.Assignee.PeerID
		t.AssigneeUserID = opts.Assignee.UserID
		if opts.Assignee.UserID != "" {
			t.AssigneeOriginKind = store.TaskAssigneeHuman
		} else if opts.Assignee.PeerID != "" {
			t.AssigneeOriginKind = store.TaskAssigneePeer
		} else {
			t.AssigneeOriginKind = store.TaskAssigneeLocal
		}
		t.AssignedBySessionID = opts.CreatedBySessionID
		t.AssignedAt = &now
		// Append assigned event to history.
		history = append(history, store.TaskStatusHistoryEntry{
			At:        now,
			BySession: opts.CreatedBySessionID,
			Evt:       "assigned",
			To:        assigneeDisplay(opts.Assignee),
		})
		t.StatusHistoryJSON, _ = json.Marshal(history)
	}

	// Stamp closed_at if creating directly in a terminal status.
	if terminal, _ := s.store.IsTerminalStatus(ctx, opts.WorkspaceID, opts.Status); terminal {
		t.ClosedAt = &now
	}

	if err := s.store.CreateTask(ctx, t); err != nil {
		return nil, err
	}

	// Composition: append child id to parent's meta composes list.
	if opts.ComposeInto != "" {
		if err := s.composeAppend(ctx, opts.WorkspaceID, opts.ComposeInto, t.ID, opts.CreatedBySessionID); err != nil {
			// Composition failure does not unwind the create — log via
			// caller's audit path. The child task exists; the parent is
			// just not updated.
			return t, fmt.Errorf("created task %s but compose_into failed: %w", t.ID, err)
		}
	}

	final, err := s.store.GetTask(ctx, t.ID)
	if err != nil {
		return nil, err
	}
	if err := s.recordTaskHistory(ctx, "create", nil, final, taskHistoryMeta{
		ActorKind:        firstNonEmpty(opts.ActorKind, opts.SourceKind, store.TaskSourceAgent),
		SessionID:        firstNonEmpty(opts.CreatedBySessionID, opts.SourceSessionID),
		SourceKind:       opts.SourceKind,
		SourceSessionID:  opts.SourceSessionID,
		SourceToolCallID: opts.SourceToolCallID,
		WorkspacePath:    opts.WorkspacePath,
	}); err != nil {
		return nil, err
	}
	s.publish(Event{Kind: EventTaskCreated, WorkspaceID: final.WorkspaceID, Task: final, At: now})
	ec := EmitContext{
		Triggering:    opts.Triggering,
		ActorKind:     opts.ActorKind,
		SessionID:     opts.CreatedBySessionID,
		WorkspacePath: opts.WorkspacePath,
	}
	s.emitter.EmitCreated(ctx, final, ec)
	// When the row was born with an assignee already set, the assignment
	// is observable + the user expects a notification (assigner != self).
	// Fire assigned AFTER created so subscribers see the lifecycle in order.
	if final.AssigneeSessionID != "" || final.AssigneePeerID != "" {
		s.emitter.EmitAssigned(ctx, final, opts.CreatedBySessionID, ec)
	}
	return final, nil
}

// Get returns one task by id, rejecting cross-workspace lookups. A
// task that exists in a different workspace is reported as
// ErrNotFound — not as a permission error — to avoid leaking
// existence across workspace boundaries.
func (s *Service) Get(ctx context.Context, workspaceID, id string) (*store.Task, error) {
	t, err := s.store.GetTask(ctx, id)
	if err != nil {
		return nil, err
	}
	if workspaceID != "" && t.WorkspaceID != workspaceID {
		return nil, store.ErrNotFound
	}
	return t, nil
}

// List is a thin pass-through.
func (s *Service) List(ctx context.Context, f store.TaskFilter) ([]store.Task, error) {
	return s.store.ListTasks(ctx, f)
}

// Search runs FTS5 search through the store.
func (s *Service) Search(ctx context.Context, f store.TaskFilter, query string) ([]store.Task, error) {
	return s.store.SearchTasks(ctx, f, query)
}

// CountByStatus returns counts grouped by status for a workspace.
func (s *Service) CountByStatus(ctx context.Context, workspaceID string) (map[string]int, error) {
	return s.store.CountTasksByStatus(ctx, workspaceID)
}

// ListHistory returns full edit/action history for one task, newest
// revision first. workspaceID gates access even for soft-deleted tasks.
func (s *Service) ListHistory(ctx context.Context, workspaceID, id string, limit int) ([]store.TaskHistoryEntry, error) {
	hs, ok := s.taskHistoryStore()
	if !ok {
		return nil, errors.New("task history is not supported by this store")
	}
	t, err := hs.GetTaskIncludingDeleted(ctx, id)
	if err != nil {
		return nil, err
	}
	if workspaceID != "" && t.WorkspaceID != workspaceID {
		return nil, store.ErrNotFound
	}
	rows, err := hs.ListTaskHistory(ctx, id, limit)
	if err != nil {
		return nil, err
	}
	if rows == nil {
		rows = []store.TaskHistoryEntry{}
	}
	return rows, nil
}

// Rollback restores a task to the after-snapshot captured at the given
// history revision. The rollback itself is recorded as a new history
// row, so it can be undone by rolling back to the revision immediately
// before it.
func (s *Service) Rollback(ctx context.Context, workspaceID, id string, opts RollbackOptions) (*store.Task, error) {
	if opts.Revision <= 0 {
		return nil, errors.New("revision is required")
	}
	hs, ok := s.taskHistoryStore()
	if !ok {
		return nil, errors.New("task history is not supported by this store")
	}
	current, err := hs.GetTaskIncludingDeleted(ctx, id)
	if err != nil {
		return nil, err
	}
	if workspaceID != "" && current.WorkspaceID != workspaceID {
		return nil, store.ErrNotFound
	}
	h, err := hs.GetTaskHistoryRevision(ctx, id, opts.Revision)
	if err != nil {
		return nil, err
	}
	if h.WorkspaceID != current.WorkspaceID {
		return nil, store.ErrNotFound
	}
	raw := h.AfterJSON
	if len(raw) == 0 {
		raw = h.BeforeJSON
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("history revision %d has no task snapshot", opts.Revision)
	}
	var target store.Task
	if err := json.Unmarshal(raw, &target); err != nil {
		return nil, fmt.Errorf("decode history snapshot: %w", err)
	}
	if target.ID != id || target.WorkspaceID != current.WorkspaceID {
		return nil, fmt.Errorf("history snapshot does not match task")
	}
	before := cloneTask(current)
	now := time.Now().UTC()
	history := readHistory(target.StatusHistoryJSON)
	history = append(history, store.TaskStatusHistoryEntry{
		At:        now,
		BySession: opts.SessionID,
		ByPeer:    opts.PeerID,
		Evt:       "rollback",
		To:        fmt.Sprintf("revision:%d", opts.Revision),
		Note:      opts.Note,
	})
	target.StatusHistoryJSON, _ = json.Marshal(history)
	target.UpdatedBySessionID = opts.SessionID
	target.HlcAt = ""
	if err := hs.RestoreTask(ctx, &target); err != nil {
		return nil, err
	}
	after, err := hs.GetTaskIncludingDeleted(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := s.recordTaskHistory(ctx, "rollback", before, after, taskHistoryMeta{
		ActorKind:       opts.ActorKind,
		SessionID:       opts.SessionID,
		PeerID:          opts.PeerID,
		UserID:          opts.UserID,
		WorkspacePath:   opts.WorkspacePath,
		Note:            opts.Note,
		RelatedRevision: opts.Revision,
	}); err != nil {
		return nil, err
	}
	kind := EventTaskUpdated
	if after.DeletedAt != nil {
		kind = EventTaskDeleted
	}
	s.publish(Event{Kind: kind, WorkspaceID: after.WorkspaceID, Task: after, At: now})
	ec := EmitContext{ActorKind: opts.ActorKind, SessionID: opts.SessionID, WorkspacePath: opts.WorkspacePath}
	if after.DeletedAt != nil {
		s.emitter.EmitDeleted(ctx, after, ec)
	} else {
		s.emitter.EmitUpdated(ctx, after, ec)
	}
	return after, nil
}

// UpdatePatch describes the patch to apply in a single Update call. Nil
// pointer fields mean "leave unchanged"; explicit string fields in the
// Clear slice mean "clear to zero value" (resolves the null-vs-omit
// ambiguity for JSON callers).
type UpdatePatch struct {
	Title       *string
	Description *string
	Status      *string
	Priority    *string
	DueAt       *time.Time
	Tags        *[]string
	Meta        *string
	Assignee    *Assignee
	Terminal    *bool // explicit terminal flip; when set, vocabulary entry is upserted too
	Pinned      *bool
	Clear       []string // field names to clear: assignee, due_at, etc.

	UpdatedBySessionID string

	// Phase-2 mesh plumbing (see CreateOptions).
	Triggering    *store.MeshMessage
	ActorKind     string
	WorkspacePath string
}

// UpdateSignals carries non-blocking post-write advisories computed
// during Update. The gateway folds them into the response envelope
// (same advisory posture as coordination_warnings); they never affect
// the mutation itself.
type UpdateSignals struct {
	// ReviewSkipped is true when this patch flipped the task from a
	// working-kind status straight to a terminal-kind status and the
	// status history never visited a review-kind status.
	ReviewSkipped bool `json:"review_skipped,omitempty"`
	// ReviewSkippedHint is the one-line nudge to surface alongside.
	ReviewSkippedHint string `json:"review_skipped_hint,omitempty"`
}

// RollbackOptions describes an explicit restore to a task history
// revision. Revision means "restore the task to the after-snapshot of
// that revision"; the rollback operation records its own history row.
type RollbackOptions struct {
	Revision int

	ActorKind     string
	SessionID     string
	PeerID        string
	UserID        string
	WorkspacePath string
	Note          string
}

// reviewSkippedHint is the one-liner attached when ReviewSkipped fires.
const reviewSkippedHint = "This task went from a working status straight to a terminal status without ever visiting a review-kind status. Verify the work end-to-end (build/tests/behavior observed) — next time pause at status:'review' before closing."

// Update applies the patch. Append-only audit lives in
// status_history_json. Returns the post-mutation row. Thin wrapper
// over UpdateWithSignals for callers that don't surface advisories.
//
// workspaceID gates the mutation: a task in a different workspace is
// rejected as ErrNotFound (no cross-workspace existence leak).
func (s *Service) Update(ctx context.Context, workspaceID, id string, p UpdatePatch) (*store.Task, error) {
	t, _, err := s.UpdateWithSignals(ctx, workspaceID, id, p)
	return t, err
}

// UpdateWithSignals is Update plus the non-blocking advisory envelope.
// The signals pointer is nil when no advisory fired.
func (s *Service) UpdateWithSignals(ctx context.Context, workspaceID, id string, p UpdatePatch) (*store.Task, *UpdateSignals, error) {
	return s.updateWithSignals(ctx, workspaceID, id, p, "")
}

func (s *Service) updateWithSignals(ctx context.Context, workspaceID, id string, p UpdatePatch, claimSession string) (*store.Task, *UpdateSignals, error) {
	t, err := s.store.GetTask(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	if workspaceID != "" && t.WorkspaceID != workspaceID {
		return nil, nil, store.ErrNotFound
	}
	// Snapshot pre-mutation state so the post-update emit decision can
	// distinguish status_changed from closed (terminal entry) and
	// detect assignee transitions. Single-event-per-call contract per
	// PLAN.md.
	before := cloneTask(t)
	preStatus := t.Status
	preAssignee := assigneeDisplay(&Assignee{
		SessionID: t.AssigneeSessionID,
		PeerID:    t.AssigneePeerID,
		UserID:    t.AssigneeUserID,
	})
	preClosed := t.ClosedAt != nil
	history := readHistory(t.StatusHistoryJSON)
	now := time.Now().UTC()
	if p.Title != nil {
		t.Title = *p.Title
	}
	if p.Description != nil {
		t.Description = *p.Description
	}
	if p.Meta != nil {
		// Normalise to JSON on every write so the dual-read state
		// machine converges row-by-row.
		nm, err := MetaToJSON(*p.Meta)
		if err != nil {
			return nil, nil, fmt.Errorf("encode meta: %w", err)
		}
		t.Meta = nm
	}
	if p.Priority != nil {
		t.Priority = *p.Priority
	}
	if p.DueAt != nil {
		due := *p.DueAt
		t.DueAt = &due
	}
	if p.Tags != nil {
		b, _ := json.Marshal(*p.Tags)
		t.TagsJSON = b
	}
	var signals *UpdateSignals
	if p.Status != nil && *p.Status != t.Status {
		from := t.Status
		// Skipped-review nudge: working-kind → terminal-kind with no
		// review-kind status anywhere in the pre-mutation history.
		// Computed BEFORE this transition is appended. Non-blocking —
		// rides the returned signals, never gates the write.
		if s.detectSkippedReview(ctx, t.WorkspaceID, from, *p.Status, history) {
			signals = &UpdateSignals{ReviewSkipped: true, ReviewSkippedHint: reviewSkippedHint}
		}
		t.Status = *p.Status
		history = append(history, store.TaskStatusHistoryEntry{
			At: now, BySession: p.UpdatedBySessionID, Evt: "status_changed",
			From: from, To: t.Status,
		})
		// Auto-claim on transition to a working status when the row has
		// no assignee and the caller didn't supply one in the patch.
		// This fixes the long-standing "tasks land in doing with no
		// owner" hole: every working-status row should have an agent
		// next to it. Generalised across the workspace's vocabulary via
		// migration 070: any vocab entry with kind="working" triggers
		// the same auto-claim path. Shared fallback classification covers
		// fresh installs where no vocab has been declared yet.
		if p.Assignee == nil && t.AssigneeSessionID == "" && t.AssigneePeerID == "" && t.AssigneeUserID == "" && p.UpdatedBySessionID != "" &&
			s.isWorkingStatus(ctx, t.WorkspaceID, t.Status) {
			t.AssigneeSessionID = p.UpdatedBySessionID
			t.AssigneeOriginKind = store.TaskAssigneeLocal
			t.AssignedBySessionID = p.UpdatedBySessionID
			t.AssignedAt = &now
			history = append(history, store.TaskStatusHistoryEntry{
				At: now, BySession: p.UpdatedBySessionID, Evt: "assigned",
				To: p.UpdatedBySessionID, Note: "auto-claim on status=doing",
			})
		}
		// Lease stamp on entry to "doing" — covers the auto-claim path
		// above AND the explicit-assignee path below (which mutates t
		// after this block runs). For the explicit case we re-stamp at
		// the end of the patch so the assignee read is correct.
	}
	if p.Assignee != nil {
		from := assigneeDisplay(&Assignee{SessionID: t.AssigneeSessionID, PeerID: t.AssigneePeerID, UserID: t.AssigneeUserID})
		to := assigneeDisplay(p.Assignee)
		assigneeChanged := from != to
		t.AssigneeSessionID = p.Assignee.SessionID
		t.AssigneePeerID = p.Assignee.PeerID
		t.AssigneeUserID = p.Assignee.UserID
		if p.Assignee.UserID != "" {
			t.AssigneeOriginKind = store.TaskAssigneeHuman
		} else if p.Assignee.PeerID != "" {
			t.AssigneeOriginKind = store.TaskAssigneePeer
		} else {
			t.AssigneeOriginKind = store.TaskAssigneeLocal
		}
		if assigneeChanged {
			t.AssignedBySessionID = p.UpdatedBySessionID
			t.AssignedAt = &now
			// A lease belongs to the previous local/peer assignee. Drop it
			// on reassignment; the working-status stamp below installs a
			// fresh lease when the new assignee can heartbeat it.
			t.LeaseExpiresAt = nil
			history = append(history, store.TaskStatusHistoryEntry{
				At: now, BySession: p.UpdatedBySessionID, Evt: "assigned",
				From: from, To: to,
			})
		}
	}
	if p.Pinned != nil {
		t.Pinned = *p.Pinned
	}
	for _, field := range p.Clear {
		switch strings.ToLower(field) {
		case "assignee":
			fromDisplay := assigneeDisplay(&Assignee{SessionID: t.AssigneeSessionID, PeerID: t.AssigneePeerID, UserID: t.AssigneeUserID})
			t.AssigneeSessionID = ""
			t.AssigneePeerID = ""
			t.AssigneeUserID = ""
			t.AssigneeOriginKind = store.TaskAssigneeLocal
			t.AssignedAt = nil
			// Lease is owned by the assignee — clear it when the row
			// has no one to bump it.
			t.LeaseExpiresAt = nil
			if fromDisplay != "" {
				history = append(history, store.TaskStatusHistoryEntry{
					At: now, BySession: p.UpdatedBySessionID, Evt: "unassigned", From: fromDisplay,
				})
			}
		case "due_at":
			t.DueAt = nil
		case "meta":
			t.Meta = ""
		case "description":
			t.Description = ""
		}
	}

	// Terminal handling: explicit flip OR status entered the workspace's
	// terminal vocabulary.
	if p.Terminal != nil {
		if *p.Terminal {
			if t.ClosedAt == nil {
				t.ClosedAt = &now
				history = append(history, store.TaskStatusHistoryEntry{
					At: now, BySession: p.UpdatedBySessionID, Evt: "closed", To: t.Status,
				})
			}
			kinds := s.workspaceStatusKinds(ctx, t.WorkspaceID)
			kind := kinds[t.Status]
			if kind == "" || kindTerminal(kind) {
				vocabKind := KindDone
				if kind != "" {
					vocabKind = kind
				}
				_ = s.store.UpsertTaskStatusVocab(ctx, &store.TaskStatusVocab{
					WorkspaceID: t.WorkspaceID, StatusText: t.Status,
					IsTerminal: true, Kind: vocabKind, ManagedBy: "user", UpdatedAt: now,
				})
			}
		} else {
			if t.ClosedAt != nil {
				t.ClosedAt = nil
				history = append(history, store.TaskStatusHistoryEntry{
					At: now, BySession: p.UpdatedBySessionID, Evt: "reopened",
				})
			}
		}
	} else if p.Status != nil {
		// No explicit terminal flag — consult vocabulary.
		isTerm, _ := s.store.IsTerminalStatus(ctx, t.WorkspaceID, t.Status)
		if isTerm && t.ClosedAt == nil {
			t.ClosedAt = &now
		} else if !isTerm && t.ClosedAt != nil {
			t.ClosedAt = nil
		}
	}

	// Lease auto-stamp — fires whenever the resulting state is a
	// WORKING status (per the workspace vocabulary, kind="working",
	// with shared fallback classification for fresh installs) with an
	// assignee AND either the status flipped INTO a working status on
	// this patch OR an explicit Assignee patch landed. The post-state
	// check covers both the auto-claim branch above (mutates
	// t.AssigneeSessionID before the explicit-assignee section runs)
	// and the explicit-assignee branch (mutates t after auto-claim
	// already exited). Skips terminal rows — closing a row should not
	// extend its lease.
	//
	// CRITICAL: this MUST use isWorkingStatus, not a literal "doing"
	// check, so it stays in lock-step with the auto-claim path (line
	// 446), Claim (any vocab working status), and the store's
	// taskWorkingStatusPredicate / ClearExpiredTaskLeases "no-lease
	// working zombie" arm. A literal "doing" check leaves a task
	// claimed into a CUSTOM working status (e.g. vocab "in_progress")
	// with an assignee but NO lease — the very next passive sweep
	// reclaims it as a zombie, and hasActiveLocalLease returns false so
	// a converging peer push stomps the in-progress work.
	statusEntered := s.isWorkingStatus(ctx, t.WorkspaceID, t.Status) &&
		p.Status != nil && !s.isWorkingStatus(ctx, t.WorkspaceID, preStatus)
	postAssignee := assigneeDisplay(&Assignee{SessionID: t.AssigneeSessionID, PeerID: t.AssigneePeerID, UserID: t.AssigneeUserID})
	assigneeJustSet := p.Assignee != nil && postAssignee != preAssignee && (t.AssigneeSessionID != "" || t.AssigneePeerID != "")
	hasAssignee := t.AssigneeSessionID != "" || t.AssigneePeerID != ""
	if t.ClosedAt == nil && s.isWorkingStatus(ctx, t.WorkspaceID, t.Status) && hasAssignee && (statusEntered || assigneeJustSet) {
		expires := now.Add(LeaseTTL)
		t.LeaseExpiresAt = &expires
	}

	t.UpdatedBySessionID = p.UpdatedBySessionID
	t.StatusHistoryJSON, _ = json.Marshal(history)
	// Stamp a fresh HLC for every local Update so the gossip layer sees
	// the row move past every paired peer's watermark. Gossip apply
	// pre-populates t.HlcAt with the REMOTE stamp before calling
	// UpdateTask via its own path; this branch never runs from there.
	t.HlcAt = clock.Now()

	var updateErr error
	if claimSession != "" {
		updateErr = s.store.ClaimTask(ctx, t, claimSession)
	} else {
		updateErr = s.store.UpdateTask(ctx, t)
	}
	if updateErr != nil {
		if errors.Is(updateErr, store.ErrTaskAlreadyClaimed) {
			return nil, nil, ErrTaskAlreadyClaimed
		}
		return nil, nil, updateErr
	}
	updated, err := s.store.GetTask(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	if err := s.recordTaskHistory(ctx, "update", before, updated, taskHistoryMeta{
		ActorKind:     p.ActorKind,
		SessionID:     p.UpdatedBySessionID,
		WorkspacePath: p.WorkspacePath,
	}); err != nil {
		return nil, nil, err
	}
	s.publish(Event{Kind: EventTaskUpdated, WorkspaceID: updated.WorkspaceID, Task: updated, At: now})
	s.emitUpdate(ctx, updated, p, preStatus, preAssignee, preClosed)
	// Epic rollup: when a child task ENTERS terminal state on this patch,
	// flip any opted-in parent to its meta.rollup_to status — but only
	// once every live sibling is also terminal. This is what lets an
	// orchestration pipeline fire a "lander" worker on the epic's
	// status_to:<target> transition without polling for completion.
	if !preClosed && updated.ClosedAt != nil {
		s.rollupParents(ctx, updated, p)
	}
	return updated, signals, nil
}

// rollupParents walks the closing child's composed_by links and flips
// each opted-in parent forward. Opt-in only: a parent with no
// meta.rollup_to behaves like classic composition (no auto-flip).
func (s *Service) rollupParents(ctx context.Context, child *store.Task, p UpdatePatch) {
	for _, parentID := range MetaListGet(child.Meta, "composed_by") {
		s.rollupParent(ctx, parentID, p)
	}
}

// rollupParent flips one parent to its meta.rollup_to status when all of
// its live composed children are terminal. Guards make it idempotent and
// loop-safe: a parent already at the target (or already closed) is left
// alone, and the target status is non-terminal in the common case so the
// flip itself doesn't re-enter rollup. Best-effort — a failure here never
// unwinds the child mutation that triggered it.
//
// Concurrency: the read-verify-flip is NOT one atomic transaction, so two
// siblings closing at the same instant can both pass the parent.Status !=
// target guard and both call Update with the same target. This is benign,
// not a double-flip: Update re-reads the row and emits status_changed only
// when *p.Status != the freshly-read preStatus (emitUpdate), so the second
// flip produces at most one redundant generic task_event:updated + an
// updated_at bump — never a duplicate status_changed and never a duplicate
// close (enteredTerminal is gated on !preClosed). A truly once-only flip
// would need a CAS UPDATE … WHERE status != target inside a txn; the
// benign-redundancy cost doesn't justify that complexity here.
func (s *Service) rollupParent(ctx context.Context, parentID string, p UpdatePatch) {
	parent, err := s.store.GetTask(ctx, parentID)
	if err != nil {
		return
	}
	target, ok := MetaGetScalar(parent.Meta, "rollup_to")
	if !ok || target == "" {
		return // not an auto-rollup parent
	}
	if parent.ClosedAt != nil || parent.Status == target {
		return // already landed or already at target — avoid re-flip / loops
	}
	childIDs := MetaListGet(parent.Meta, "composes")
	if len(childIDs) == 0 {
		return
	}
	verified := 0
	for _, cid := range childIDs {
		c, gerr := s.store.GetTask(ctx, cid)
		if gerr != nil || c.DeletedAt != nil {
			continue // deleted / unreachable child no longer gates the parent
		}
		if c.ClosedAt == nil {
			return // a live sibling is still open — not done yet
		}
		verified++
	}
	if verified == 0 {
		return // nothing actually completed
	}
	status := target
	// Recurse through Update so the flip emits a real status_changed
	// (status_to:<target>) event and writes status_history. The target is
	// non-terminal in the canonical pipeline, so this won't re-enter
	// rollupParents; a terminal target intentionally cascades to a
	// grandparent epic.
	_, _ = s.Update(ctx, parent.WorkspaceID, parentID, UpdatePatch{
		Status:             &status,
		ActorKind:          "system",
		UpdatedBySessionID: p.UpdatedBySessionID,
		Triggering:         p.Triggering, // propagate chain-depth into the emitted event
		WorkspacePath:      p.WorkspacePath,
	})
}

// Heartbeat bumps the row's lease window when the caller IS the
// current assignee. Returns nil for non-assignees (silent no-op
// semantics). Returns ErrNotFound for unknown ids or rows in a
// different workspace.
func (s *Service) Heartbeat(ctx context.Context, workspaceID, id, sessionID string) error {
	if id == "" {
		return errors.New("id is required")
	}
	if sessionID == "" {
		return errors.New("session_id is required")
	}
	// Workspace-gate via GetTask so a caller can't bump a row that
	// belongs to a different workspace. Mirrors the gate on Update.
	t, err := s.store.GetTask(ctx, id)
	if err != nil {
		return err
	}
	if workspaceID != "" && t.WorkspaceID != workspaceID {
		return store.ErrNotFound
	}
	bumped, err := s.store.HeartbeatTask(ctx, id, sessionID, LeaseTTL)
	if err != nil {
		return err
	}
	if bumped {
		// Publish a lightweight update event so the dashboard's SSE
		// stream re-renders the lease chip without waiting for a
		// status-change. The mesh emitter stays quiet on heartbeats
		// (would be too chatty for the cross-machine signal).
		fresh, gerr := s.store.GetTask(ctx, id)
		if gerr == nil {
			// Bus-only: a heartbeat bumps the volatile lease window, not
			// durable task content — it must NOT re-serialize the
			// canonical .md file via the brain hook (would churn the
			// federation/git layer every 5 minutes for no content change).
			s.publishBusOnly(Event{Kind: EventTaskUpdated, WorkspaceID: fresh.WorkspaceID, Task: fresh, At: time.Now().UTC()})
		}
	}
	return nil
}

// recentLeaseExpired returns true if the task's embedded status_history
// already contains a lease_expired entry within the last hour. This
// prevents the sweep from spamming duplicate entries on tasks whose
// lease_expires_at was already stale.
func recentLeaseExpired(t *store.Task) bool {
	history := readHistory(t.StatusHistoryJSON)
	cutoff := time.Now().UTC().Add(-1 * time.Hour)
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Evt == "lease_expired" && history[i].At.After(cutoff) {
			return true
		}
		if history[i].At.Before(cutoff) {
			break
		}
	}
	return false
}

// SweepExpiredLeases clears every row whose lease window has elapsed
// and appends evt=lease_expired to each cleared row's status_history
// so the audit trail shows why the row was abandoned. Tasks in a
// working status (per workspace vocabulary) are demoted back to
// "open" so they don't linger in "doing" without an owner. Called on
// a 1-minute tick by the daemon goroutine.
func (s *Service) SweepExpiredLeases(ctx context.Context) (int, error) {
	now := time.Now().UTC()
	ids, err := s.store.ClearExpiredTaskLeases(ctx, now)
	if err != nil {
		return 0, fmt.Errorf("sweep expired leases: %w", err)
	}
	for _, id := range ids {
		t, gerr := s.store.GetTask(ctx, id)
		if gerr != nil {
			continue
		}
		// Dedup: skip if the last history entry was also lease_expired
		// within the last hour (prevents spam from repeated sweeps on
		// tasks whose lease_expires_at was already stale).
		if recentLeaseExpired(t) {
			continue
		}
		before := cloneTask(t)
		history := readHistory(t.StatusHistoryJSON)
		preStatus := t.Status
		demoted := false
		if s.isWorkingStatus(ctx, t.WorkspaceID, t.Status) {
			t.Status = "open"
			demoted = true
			history = append(history, store.TaskStatusHistoryEntry{
				At: now, Evt: "status_changed",
				From: preStatus, To: "open", Note: "lease expired, demoted from working status",
			})
		}
		history = append(history, store.TaskStatusHistoryEntry{
			At: now, Evt: "lease_expired", From: "",
		})
		t.StatusHistoryJSON, _ = json.Marshal(history)
		if uerr := s.store.UpdateTask(ctx, t); uerr != nil {
			continue
		}
		after := t
		if fresh, ferr := s.store.GetTask(ctx, id); ferr == nil {
			after = fresh
		}
		_ = s.recordTaskHistory(ctx, "lease_expired", before, after, taskHistoryMeta{
			ActorKind: "system",
			Note:      "lease expired",
		})
		s.publish(Event{Kind: EventTaskUpdated, WorkspaceID: t.WorkspaceID, Task: t, At: now})
		if demoted {
			s.emitter.EmitStatusChanged(ctx, t, preStatus, "open", EmitContext{ActorKind: "system"})
		} else {
			s.emitter.EmitUpdated(ctx, t, EmitContext{ActorKind: "system"})
		}
	}
	return len(ids), nil
}

// ReleaseSessionTasks immediately expires all task leases held by
// the given session. Called on agent disconnect so tasks don't linger
// in "doing" without an owner until the passive sweep catches them.
// Working-status tasks are demoted to "open" (same logic as the sweep).
func (s *Service) ReleaseSessionTasks(ctx context.Context, sessionID string) (int, error) {
	return s.ReleaseSessionTasksWithReason(ctx, sessionID, "")
}

// ReleaseSessionTasksWithReason is ReleaseSessionTasks with an explicit
// status-history reason. Empty reason preserves the legacy disconnect notes.
func (s *Service) ReleaseSessionTasksWithReason(ctx context.Context, sessionID, reason string) (int, error) {
	if sessionID == "" {
		return 0, nil
	}
	demoteNote := "agent disconnected, demoted from working status"
	releaseNote := "released on disconnect"
	if reason != "" {
		demoteNote = reason + ": demoted from working status"
		releaseNote = reason
	}
	now := time.Now().UTC()
	ids, err := s.store.ClearSessionTaskLeases(ctx, sessionID)
	if err != nil {
		return 0, fmt.Errorf("release session tasks: %w", err)
	}
	for _, id := range ids {
		t, gerr := s.store.GetTask(ctx, id)
		if gerr != nil {
			continue
		}
		before := cloneTask(t)
		history := readHistory(t.StatusHistoryJSON)
		preStatus := t.Status
		demoted := false
		if s.isWorkingStatus(ctx, t.WorkspaceID, t.Status) {
			t.Status = "open"
			demoted = true
			history = append(history, store.TaskStatusHistoryEntry{
				At: now, BySession: sessionID, Evt: "status_changed",
				From: preStatus, To: "open", Note: demoteNote,
			})
		}
		history = append(history, store.TaskStatusHistoryEntry{
			At: now, Evt: "lease_expired", From: "",
			Note: releaseNote,
		})
		t.StatusHistoryJSON, _ = json.Marshal(history)
		if uerr := s.store.UpdateTask(ctx, t); uerr != nil {
			continue
		}
		after := t
		if fresh, ferr := s.store.GetTask(ctx, id); ferr == nil {
			after = fresh
		}
		_ = s.recordTaskHistory(ctx, "release", before, after, taskHistoryMeta{
			ActorKind: "system",
			SessionID: sessionID,
			Note:      releaseNote,
		})
		s.publish(Event{Kind: EventTaskUpdated, WorkspaceID: t.WorkspaceID, Task: t, At: now})
		ec := EmitContext{ActorKind: "system", SessionID: sessionID}
		if demoted {
			s.emitter.EmitStatusChanged(ctx, t, preStatus, t.Status, ec)
		} else {
			s.emitter.EmitUpdated(ctx, t, ec)
		}
	}
	return len(ids), nil
}

// emitUpdate is the post-Update mesh-event router. Picks the single
// most-specific event per PLAN.md: closed wins over status_changed
// when the new status entered terminal vocabulary; assigned fires when
// the assignee changed; otherwise a generic updated.
func (s *Service) emitUpdate(
	ctx context.Context, updated *store.Task, p UpdatePatch,
	preStatus, preAssignee string, preClosed bool,
) {
	ec := EmitContext{
		Triggering:    p.Triggering,
		ActorKind:     p.ActorKind,
		SessionID:     p.UpdatedBySessionID,
		WorkspacePath: p.WorkspacePath,
	}
	statusChanged := p.Status != nil && *p.Status != preStatus
	enteredTerminal := !preClosed && updated.ClosedAt != nil
	assigneeChanged := p.Assignee != nil && assigneeDisplay(&Assignee{
		SessionID: updated.AssigneeSessionID,
		PeerID:    updated.AssigneePeerID,
		UserID:    updated.AssigneeUserID,
	}) != preAssignee
	switch {
	case enteredTerminal:
		s.emitter.EmitClosed(ctx, updated, p.UpdatedBySessionID, ec)
	case statusChanged:
		s.emitter.EmitStatusChanged(ctx, updated, preStatus, updated.Status, ec)
	case assigneeChanged:
		s.emitter.EmitAssigned(ctx, updated, p.UpdatedBySessionID, ec)
	default:
		s.emitter.EmitUpdated(ctx, updated, ec)
	}
}

// Delete soft-deletes a task. Cross-workspace deletes are reported as
// ErrNotFound. The optional MutationContext propagates chain-depth +
// actor_kind onto the emitted task_event:deleted.
func (s *Service) Delete(ctx context.Context, workspaceID, id string, mc ...MutationContext) error {
	t, err := s.store.GetTask(ctx, id)
	if err != nil {
		return err
	}
	if workspaceID != "" && t.WorkspaceID != workspaceID {
		return store.ErrNotFound
	}
	before := cloneTask(t)
	if err := s.store.SoftDeleteTask(ctx, id); err != nil {
		return err
	}
	now := time.Now().UTC()
	t.DeletedAt = &now
	after := cloneTask(t)
	if hs, ok := s.taskHistoryStore(); ok {
		if fresh, err := hs.GetTaskIncludingDeleted(ctx, id); err == nil {
			after = fresh
		}
	}
	ec := mutationEmitContext(mc, "")
	if err := s.recordTaskHistory(ctx, "delete", before, after, taskHistoryMeta{
		ActorKind:     ec.ActorKind,
		SessionID:     ec.SessionID,
		WorkspacePath: ec.WorkspacePath,
	}); err != nil {
		return err
	}
	s.publish(Event{Kind: EventTaskDeleted, WorkspaceID: t.WorkspaceID, Task: t, At: now})
	s.emitter.EmitDeleted(ctx, t, ec)
	return nil
}

// MutationContext bundles the optional mesh-plumbing args (chain-depth
// source, actor kind, workspace path, acting session) for mutations
// whose existing signature is too tight to absorb new fields without
// a breaking change. Delete + AppendNote take this as a variadic
// trailing arg so callers stay terse — the gateway/REST/test paths
// pass MutationContext{} or omit it entirely.
type MutationContext struct {
	Triggering    *store.MeshMessage
	ActorKind     string
	SessionID     string
	PeerID        string
	UserID        string
	WorkspacePath string
}

// mutationEmitContext folds a variadic MutationContext + a fallback
// session id into the EmitContext shape the emitter expects. Empty mc
// = bare-bones (no actor, no triggering). When the caller passed an
// explicit SessionID it wins; otherwise the fallback (used by
// AppendNote which can derive it from the author field) fills in.
func mutationEmitContext(mc []MutationContext, fallbackSession string) EmitContext {
	if len(mc) == 0 {
		return EmitContext{SessionID: fallbackSession}
	}
	c := mc[0]
	if c.SessionID == "" {
		c.SessionID = fallbackSession
	}
	return EmitContext{
		Triggering:    c.Triggering,
		ActorKind:     c.ActorKind,
		SessionID:     c.SessionID,
		WorkspacePath: c.WorkspacePath,
	}
}

// Claim atomically assigns the task to the calling session AND moves
// status to the first non-open vocabulary entry (or `"doing"` as the
// sensible default when status_text is empty). This is the
// "workspace-broadcast → first claimant wins" happy path from the
// locked design — a single primitive instead of assign + update.
//
// The claim is atomic at the store level: the UPDATE includes a CAS
// guard (WHERE assignee_session_id IS NULL/empty/self) so only one
// claimant wins when two sessions race for the same unassigned row.
// The loser receives ErrTaskAlreadyClaimed.
func (s *Service) Claim(ctx context.Context, workspaceID, id, statusText, claimantSession, note string, mc ...MutationContext) (*store.Task, error) {
	if claimantSession == "" {
		return nil, errors.New("claim requires an active session")
	}
	current, err := s.store.GetTask(ctx, id)
	if err != nil {
		return nil, err
	}
	if workspaceID != "" && current.WorkspaceID != workspaceID {
		return nil, store.ErrNotFound
	}
	before := cloneTask(current)
	mctx := mutationContext(mc, claimantSession)
	if statusText == "" {
		statusText = "doing"
	}
	now := time.Now().UTC()
	preStatus := current.Status
	preClosed := current.ClosedAt != nil
	history := readHistory(current.StatusHistoryJSON)

	// Status change
	if statusText != current.Status {
		history = append(history, store.TaskStatusHistoryEntry{
			At: now, BySession: claimantSession, Evt: "status_changed",
			From: current.Status, To: statusText,
		})
	}
	current.Status = statusText

	// Assignee
	previousAssigneeSession := current.AssigneeSessionID
	fromAssignee := assigneeDisplay(&Assignee{
		SessionID: current.AssigneeSessionID,
		PeerID:    current.AssigneePeerID,
		UserID:    current.AssigneeUserID,
	})
	current.AssigneeSessionID = claimantSession
	current.AssigneeOriginKind = store.TaskAssigneeLocal
	current.AssigneePeerID = ""
	current.AssigneeUserID = ""
	current.AssignedBySessionID = claimantSession
	current.AssignedAt = &now
	history = append(history, store.TaskStatusHistoryEntry{
		At: now, BySession: claimantSession, Evt: "assigned",
		From: fromAssignee, To: claimantSession,
	})

	// Terminal handling
	isTerm, _ := s.store.IsTerminalStatus(ctx, current.WorkspaceID, statusText)
	if isTerm && current.ClosedAt == nil {
		current.ClosedAt = &now
		history = append(history, store.TaskStatusHistoryEntry{
			At: now, BySession: claimantSession, Evt: "closed", To: statusText,
		})
	}

	// Lease stamp — mirrors the logic in UpdateWithSignals for
	// working-status + assignee transitions.
	if current.ClosedAt == nil && s.isWorkingStatus(ctx, current.WorkspaceID, current.Status) {
		expires := now.Add(LeaseTTL)
		current.LeaseExpiresAt = &expires
	}

	current.UpdatedBySessionID = claimantSession
	current.StatusHistoryJSON, _ = json.Marshal(history)
	current.HlcAt = clock.Now()

	// ATOMIC CAS UPDATE — only one claimant wins.
	if err := s.store.ClaimTask(ctx, current, claimantSession); err != nil {
		if errors.Is(err, store.ErrTaskAlreadyClaimed) {
			return nil, ErrTaskAlreadyClaimed
		}
		return nil, err
	}

	// Read back the post-mutation row for events + return value.
	t, err := s.store.GetTask(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := s.recordTaskHistory(ctx, "claim", before, t, taskHistoryMeta{
		ActorKind:     mctx.ActorKind,
		SessionID:     mctx.SessionID,
		PeerID:        mctx.PeerID,
		UserID:        mctx.UserID,
		WorkspacePath: mctx.WorkspacePath,
		Note:          note,
	}); err != nil {
		return nil, err
	}
	s.publish(Event{Kind: EventTaskUpdated, WorkspaceID: t.WorkspaceID, Task: t, At: now})
	s.emitUpdate(ctx, t, UpdatePatch{
		Status:             &statusText,
		Assignee:           &Assignee{SessionID: claimantSession},
		UpdatedBySessionID: claimantSession,
		ActorKind:          mctx.ActorKind,
		WorkspacePath:      mctx.WorkspacePath,
	}, preStatus, "", preClosed)

	// Emit task_claimed AFTER the underlying update event so consumers
	// can distinguish "claimed" from a generic edit.
	s.publish(Event{Kind: EventTaskClaimed, WorkspaceID: t.WorkspaceID, Task: t, At: now})
	ec := mutationEmitContext(mc, claimantSession)
	s.emitter.EmitClaimed(ctx, t, previousAssigneeSession, ec)

	// Epic rollup: if the claim moved the task into terminal state,
	// flip any opted-in parent.
	if t.ClosedAt != nil {
		s.rollupParents(ctx, t, UpdatePatch{
			Status:             &statusText,
			UpdatedBySessionID: claimantSession,
		})
	}

	if note != "" {
		_, _ = s.AppendNote(ctx, workspaceID, id, note, claimantSession, store.TaskSourceAgent)
	}
	return t, nil
}

// ErrTaskAlreadyClaimed signals a workspace-broadcast direct-assign was
// raced — another session claimed first. The handler maps this to a
// friendly text result.
var ErrTaskAlreadyClaimed = errors.New("task is already claimed by another session")

// BulkUpdate applies the same patch to every id, scoped to workspaceID.
// Cross-workspace ids surface as per-row ErrNotFound failures.
func (s *Service) BulkUpdate(ctx context.Context, workspaceID string, ids []string, patch UpdatePatch) (ok []*store.Task, failed []BulkFailure) {
	for _, id := range ids {
		t, err := s.Update(ctx, workspaceID, id, patch)
		if err != nil {
			failed = append(failed, BulkFailure{ID: id, Error: err.Error()})
			continue
		}
		ok = append(ok, t)
	}
	return
}

// BulkFailure is one row's error from BulkUpdate.
type BulkFailure struct {
	ID    string `json:"id"`
	Error string `json:"error"`
}

// SetWorkContext merges the work-context patch (worktree / branch /
// pr / commits / peer / session / linear / mesh_thread) into the
// task's meta column, preserving all non-work-context lines verbatim.
// The clears slice names keys whose existing line should be removed
// (empty string in the patch alone is NOT a clear signal — the API
// surface uses an explicit clears channel to keep struct semantics
// unambiguous). Returns the post-mutation task. Appends a
// `evt=work_context_updated` row to status_history so the audit trail
// captures who set the pointer + when.
//
// Workspace gate behaves like the other mutators — cross-workspace
// ids surface as ErrNotFound.
func (s *Service) SetWorkContext(
	ctx context.Context, workspaceID, id string,
	patch WorkContext, clears []string, updatedBy string, mc ...MutationContext,
) (*store.Task, error) {
	current, err := s.store.GetTask(ctx, id)
	if err != nil {
		return nil, err
	}
	if workspaceID != "" && current.WorkspaceID != workspaceID {
		return nil, store.ErrNotFound
	}
	before := cloneTask(current)
	mctx := mutationContext(mc, updatedBy)
	newMeta, err := mergeWithClears(current.Meta, patch, clears)
	if err != nil {
		return nil, fmt.Errorf("merge work context: %w", err)
	}
	now := time.Now().UTC()
	history := readHistory(current.StatusHistoryJSON)
	history = append(history, store.TaskStatusHistoryEntry{
		At: now, BySession: updatedBy, Evt: "work_context_updated",
		Note: workContextNote(patch, clears),
	})
	current.Meta = newMeta
	current.StatusHistoryJSON, _ = json.Marshal(history)
	current.UpdatedBySessionID = updatedBy
	if err := s.store.UpdateTask(ctx, current); err != nil {
		return nil, err
	}
	updated, err := s.store.GetTask(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := s.recordTaskHistory(ctx, "work_context", before, updated, taskHistoryMeta{
		ActorKind:     mctx.ActorKind,
		SessionID:     mctx.SessionID,
		PeerID:        mctx.PeerID,
		UserID:        mctx.UserID,
		WorkspacePath: mctx.WorkspacePath,
		Note:          workContextNote(patch, clears),
	}); err != nil {
		return nil, err
	}
	s.publish(Event{Kind: EventTaskUpdated, WorkspaceID: updated.WorkspaceID, Task: updated, At: now})
	s.emitter.EmitUpdated(ctx, updated, mutationEmitContext(mc, updatedBy))
	return updated, nil
}

// mergeWithClears applies the patch then strips any keys in clears
// from the resulting meta. The two-pass approach keeps Merge's
// preserve-other-keys guarantee intact even when the same key
// appears in both patch and clears (clears wins).
//
// Both passes emit JSON-shaped meta via the helpers in meta.go.
func mergeWithClears(meta string, patch WorkContext, clears []string) (string, error) {
	merged, err := MergeWorkContext(meta, patch)
	if err != nil {
		return meta, err
	}
	if len(clears) == 0 {
		return merged, nil
	}
	out := merged
	for _, k := range clears {
		k = strings.TrimSpace(k)
		if k == "" || !isWorkContextKey(k) {
			continue
		}
		next, err := MetaClearKey(out, k)
		if err != nil {
			return meta, err
		}
		out = next
	}
	return out, nil
}

// workContextNote builds a compact summary for the status_history
// entry — lists the keys touched so audit consumers can answer
// "which pointers changed at <timestamp>" without diffing meta.
func workContextNote(patch WorkContext, clears []string) string {
	parts := []string{}
	if patch.Worktree != "" {
		parts = append(parts, "worktree")
	}
	if patch.Branch != "" {
		parts = append(parts, "branch")
	}
	if patch.PR != "" {
		parts = append(parts, "pr")
	}
	if patch.Commits != "" {
		parts = append(parts, "commits")
	}
	if patch.Peer != "" {
		parts = append(parts, "peer")
	}
	if patch.Session != "" {
		parts = append(parts, "session")
	}
	if patch.Linear != "" {
		parts = append(parts, "linear")
	}
	if patch.MeshThread != "" {
		parts = append(parts, "mesh_thread")
	}
	if len(clears) > 0 {
		parts = append(parts, "cleared="+strings.Join(clears, ","))
	}
	if len(parts) == 0 {
		return "no-op"
	}
	return strings.Join(parts, ",")
}

// AppendNote adds an append-only note. workspaceID gates the parent
// task so notes can't be appended across workspace boundaries.
func (s *Service) AppendNote(ctx context.Context, workspaceID, taskID, body, authorSession, authorKind string, mc ...MutationContext) (*store.TaskNote, error) {
	if strings.TrimSpace(body) == "" {
		return nil, errors.New("body is required")
	}
	// Always resolve the parent so we know the canonical workspace_id
	// for the published Event, even when workspaceID was passed empty
	// (the cross-package MCP callers).
	parent, err := s.store.GetTask(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if workspaceID != "" && parent.WorkspaceID != workspaceID {
		return nil, store.ErrNotFound
	}
	before := cloneTask(parent)
	mctx := mutationContext(mc, authorSession)
	n := &store.TaskNote{
		TaskID:          taskID,
		AuthorSessionID: authorSession,
		AuthorKind:      firstNonEmpty(authorKind, store.TaskSourceAgent),
		Body:            body,
	}
	if err := s.store.AppendTaskNote(ctx, n); err != nil {
		return nil, err
	}
	if err := s.recordTaskHistory(ctx, "note", before, parent, taskHistoryMeta{
		ActorKind:     firstNonEmpty(mctx.ActorKind, authorKind),
		SessionID:     mctx.SessionID,
		PeerID:        mctx.PeerID,
		UserID:        mctx.UserID,
		WorkspacePath: mctx.WorkspacePath,
		Note:          truncateForHistory(body, 240),
	}); err != nil {
		return nil, err
	}
	// Include the parent Task so the brain hook re-serializes the task
	// body with the appended note folded in (Appendix B #4 — notes inline).
	s.publish(Event{Kind: EventTaskNoteAppended, WorkspaceID: parent.WorkspaceID, Task: parent, Note: n, At: time.Now().UTC()})
	// Mesh task_event:note_appended — quiet by default (notes fan
	// out via SSE to the page the user is already on).
	ec := EmitContext{
		SessionID:     mctx.SessionID,
		ActorKind:     firstNonEmpty(mctx.ActorKind, authorKind, store.TaskSourceAgent),
		WorkspacePath: mctx.WorkspacePath,
	}
	s.emitter.EmitNote(ctx, parent, n, ec)
	return n, nil
}

// ListNotes returns notes for a task (newest first). workspaceID gates
// the parent task lookup.
func (s *Service) ListNotes(ctx context.Context, workspaceID, taskID string, limit int) ([]store.TaskNote, error) {
	if workspaceID != "" {
		parent, err := s.store.GetTask(ctx, taskID)
		if err != nil {
			return nil, err
		}
		if parent.WorkspaceID != workspaceID {
			return nil, store.ErrNotFound
		}
	}
	return s.store.ListTaskNotes(ctx, taskID, limit)
}

// isWorkingStatus reports whether a freeform status counts as
// "actively being worked on" — gates the auto-claim service path.
// Consults the workspace vocabulary (kind="working") first, then
// falls back to shared default classification so a fresh install with no
// vocab declarations still works. Errors from the store are swallowed: a
// vocab-lookup failure must not block the auto-claim transition.
func (s *Service) isWorkingStatus(ctx context.Context, workspaceID, status string) bool {
	if status == "" {
		return false
	}
	return s.workspaceStatusKinds(ctx, workspaceID)[status] == KindWorking
}

// KnownStatuses returns the per-workspace vocabulary entries + the
// fallback set of common statuses. The handler returns this in the
// task__list response so agents self-correct toward established
// workspace vocabulary.
func (s *Service) KnownStatuses(ctx context.Context, workspaceID string) ([]string, error) {
	vocab, err := s.store.ListTaskStatusVocab(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	out := []string{}
	seen := map[string]bool{}
	for _, v := range vocab {
		if !seen[v.StatusText] {
			out = append(out, v.StatusText)
			seen[v.StatusText] = true
		}
	}
	for _, common := range []string{"open", "doing", "blocked", "review", "done", "cancelled"} {
		if !seen[common] {
			out = append(out, common)
			seen[common] = true
		}
	}
	return out, nil
}

// Compose is the exported post-hoc form of the create-time
// `compose_into` hook — links an existing parent and child task
// bidirectionally (parent.meta.composes += childID, child.meta.composed_by
// += parentID). Idempotent: calling twice is a no-op past the first.
// Refuses cross-workspace links (returns ErrNotFound — same posture as
// every other workspace-scoped mutation, so caller can't probe).
//
// Status_history on the parent records the `composed` event when a
// real append happens. The child's meta is updated quietly (the
// caller already knows it's becoming a child by virtue of the call).
func (s *Service) Compose(ctx context.Context, workspaceID, parentID, childID, bySession string) error {
	return s.composeAppend(ctx, workspaceID, parentID, childID, bySession)
}

// BulkCompose links many children to a single parent in one call.
// Per-id failures land in `failed` so the caller can see which links
// landed and which didn't — mirrors the BulkUpdate {ok, failed}
// pattern. Returns the ids that succeeded (idempotent re-applies count
// as success — the link IS present after the call) plus the per-id
// failures.
func (s *Service) BulkCompose(ctx context.Context, workspaceID, parentID string, childIDs []string, bySession string) (ok []string, failed []BulkFailure) {
	for _, childID := range childIDs {
		if err := s.composeAppend(ctx, workspaceID, parentID, childID, bySession); err != nil {
			failed = append(failed, BulkFailure{ID: childID, Error: err.Error()})
			continue
		}
		ok = append(ok, childID)
	}
	return
}

// Decompose is the mirror of Compose — drops childID from
// parent.meta.composes AND drops parentID from child.meta.composed_by.
// No-op (and no error) when the link doesn't exist: calling decompose
// on already-disconnected tasks is safe. Workspace-gated (cross-workspace
// refused as ErrNotFound). Stamps `decomposed` on the parent's
// status_history when a real removal happened.
func (s *Service) Decompose(ctx context.Context, workspaceID, parentID, childID, bySession string) error {
	parent, err := s.store.GetTask(ctx, parentID)
	if err != nil {
		return fmt.Errorf("decompose parent: %w", err)
	}
	if workspaceID != "" && parent.WorkspaceID != workspaceID {
		return store.ErrNotFound
	}
	parentBefore := cloneTask(parent)
	newMeta := removeMetaListLine(parent.Meta, "composes", childID)
	if newMeta != parent.Meta {
		parent.Meta = newMeta
		history := readHistory(parent.StatusHistoryJSON)
		history = append(history, store.TaskStatusHistoryEntry{
			At: time.Now().UTC(), BySession: bySession,
			Evt: "decomposed", From: childID,
		})
		parent.StatusHistoryJSON, _ = json.Marshal(history)
		parent.UpdatedBySessionID = bySession
		if err := s.store.UpdateTask(ctx, parent); err != nil {
			return err
		}
		parentAfter, gerr := s.store.GetTask(ctx, parent.ID)
		if gerr != nil {
			parentAfter = parent
		}
		if err := s.recordTaskHistory(ctx, "decompose", parentBefore, parentAfter, taskHistoryMeta{
			ActorKind: "agent",
			SessionID: bySession,
			Note:      "child:" + childID,
		}); err != nil {
			return err
		}
		s.serializeBrain(ctx, parent)
	}
	// Reverse edge: drop parentID from child.meta.composed_by. Child may
	// live in the same workspace (the only legal case for a valid link);
	// if it disappears between calls we silently skip the reverse so the
	// parent-side cleanup isn't lost.
	child, err := s.store.GetTask(ctx, childID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		return fmt.Errorf("decompose child: %w", err)
	}
	newChildMeta := removeMetaListLine(child.Meta, "composed_by", parentID)
	if newChildMeta == child.Meta {
		return nil
	}
	childBefore := cloneTask(child)
	child.Meta = newChildMeta
	child.UpdatedBySessionID = bySession
	if err := s.store.UpdateTask(ctx, child); err != nil {
		return err
	}
	childAfter, gerr := s.store.GetTask(ctx, child.ID)
	if gerr != nil {
		childAfter = child
	}
	if err := s.recordTaskHistory(ctx, "decompose", childBefore, childAfter, taskHistoryMeta{
		ActorKind: "agent",
		SessionID: bySession,
		Note:      "parent:" + parentID,
	}); err != nil {
		return err
	}
	s.serializeBrain(ctx, child)
	return nil
}

// composeAppend appends childID to parentID's meta.composes list AND
// stamps the reverse-link `composed_by: parentID` on the child's
// meta. The meta column is freeform text but follows a frontmatter
// convention ("key: value, value" lines) that AI consumers parse on
// read. Bidirectional links cost two meta mutations on create and
// zero on read — without the reverse-link the child opened in
// isolation can't tell it's part of an epic.
func (s *Service) composeAppend(ctx context.Context, workspaceID, parentID, childID, bySession string) error {
	// Resolve both ends FIRST — validating before any mutation ensures
	// the bulk-form per-id failures don't pollute the parent with
	// references to non-existent children. The forward edge wouldn't
	// know to roll back the parent's composes line if the child lookup
	// failed afterwards.
	parent, err := s.store.GetTask(ctx, parentID)
	if err != nil {
		return fmt.Errorf("compose parent: %w", err)
	}
	// Composition is a single-workspace relationship — refuse to link a
	// task into a parent in a different workspace. Surfaces as ErrNotFound
	// so callers can't probe for parent ids across workspaces.
	if workspaceID != "" && parent.WorkspaceID != workspaceID {
		return store.ErrNotFound
	}
	child, err := s.store.GetTask(ctx, childID)
	if err != nil {
		return fmt.Errorf("compose child: %w", err)
	}
	// Cross-workspace child also refused — preserves the same
	// no-existence-leak posture as the parent gate.
	if workspaceID != "" && child.WorkspaceID != workspaceID {
		return store.ErrNotFound
	}

	// Forward edge: parent.meta.composes += childID
	parentBefore := cloneTask(parent)
	newMeta := appendMetaListLine(parent.Meta, "composes", childID)
	if newMeta != parent.Meta {
		parent.Meta = newMeta
		history := readHistory(parent.StatusHistoryJSON)
		history = append(history, store.TaskStatusHistoryEntry{
			At: time.Now().UTC(), BySession: bySession,
			Evt: "composed", To: childID,
		})
		parent.StatusHistoryJSON, _ = json.Marshal(history)
		parent.UpdatedBySessionID = bySession
		// Clear the loaded HLC so the store stamps a fresh one (every
		// local mutation must advance the gossip watermark).
		parent.HlcAt = ""
		if err := s.store.UpdateTask(ctx, parent); err != nil {
			return err
		}
		parentAfter, gerr := s.store.GetTask(ctx, parent.ID)
		if gerr != nil {
			parentAfter = parent
		}
		if err := s.recordTaskHistory(ctx, "compose", parentBefore, parentAfter, taskHistoryMeta{
			ActorKind: "agent",
			SessionID: bySession,
			Note:      "child:" + childID,
		}); err != nil {
			return err
		}
		s.serializeBrain(ctx, parent)
	}
	// Reverse edge: child.meta.composed_by += parentID
	newChildMeta := appendMetaListLine(child.Meta, "composed_by", parentID)
	if newChildMeta == child.Meta {
		return nil
	}
	childBefore := cloneTask(child)
	child.Meta = newChildMeta
	child.UpdatedBySessionID = bySession
	child.HlcAt = ""
	if err := s.store.UpdateTask(ctx, child); err != nil {
		return err
	}
	childAfter, gerr := s.store.GetTask(ctx, child.ID)
	if gerr != nil {
		childAfter = child
	}
	if err := s.recordTaskHistory(ctx, "compose", childBefore, childAfter, taskHistoryMeta{
		ActorKind: "agent",
		SessionID: bySession,
		Note:      "parent:" + parentID,
	}); err != nil {
		return err
	}
	s.serializeBrain(ctx, child)
	return nil
}

type taskHistoryMeta struct {
	ActorKind        string
	SessionID        string
	PeerID           string
	UserID           string
	SourceKind       string
	SourceSessionID  string
	SourceToolCallID string
	WorkspacePath    string
	OriginPeerID     string
	RelatedRevision  int
	Note             string
}

func (s *Service) taskHistoryStore() (store.TaskHistoryStore, bool) {
	hs, ok := s.store.(store.TaskHistoryStore)
	return hs, ok
}

func (s *Service) recordTaskHistory(ctx context.Context, action string, before, after *store.Task, meta taskHistoryMeta) error {
	hs, ok := s.taskHistoryStore()
	if !ok {
		return nil
	}
	t := after
	if t == nil {
		t = before
	}
	if t == nil {
		return nil
	}
	beforeJSON, err := marshalTaskSnapshot(before)
	if err != nil {
		return fmt.Errorf("task history before snapshot: %w", err)
	}
	afterJSON, err := marshalTaskSnapshot(after)
	if err != nil {
		return fmt.Errorf("task history after snapshot: %w", err)
	}
	changedJSON, err := changedTaskFieldsJSON(before, after)
	if err != nil {
		return fmt.Errorf("task history changed fields: %w", err)
	}
	if meta.ActorKind == "" {
		meta.ActorKind = firstNonEmpty(t.SourceKind, store.TaskSourceAgent)
	}
	if meta.SessionID == "" {
		meta.SessionID = firstNonEmpty(t.UpdatedBySessionID, t.CreatedBySessionID, t.SourceSessionID)
	}
	if meta.SourceKind == "" {
		meta.SourceKind = t.SourceKind
	}
	if meta.SourceSessionID == "" {
		meta.SourceSessionID = t.SourceSessionID
	}
	if meta.SourceToolCallID == "" {
		meta.SourceToolCallID = t.SourceToolCallID
	}
	if meta.OriginPeerID == "" {
		meta.OriginPeerID = t.OriginPeerID
	}
	return hs.AppendTaskHistory(ctx, &store.TaskHistoryEntry{
		TaskID:            t.ID,
		WorkspaceID:       t.WorkspaceID,
		Action:            action,
		ActorKind:         meta.ActorKind,
		ActorSessionID:    meta.SessionID,
		ActorPeerID:       meta.PeerID,
		ActorUserID:       meta.UserID,
		SourceKind:        meta.SourceKind,
		SourceSessionID:   meta.SourceSessionID,
		SourceToolCallID:  meta.SourceToolCallID,
		WorkspacePath:     meta.WorkspacePath,
		OriginPeerID:      meta.OriginPeerID,
		RelatedRevision:   meta.RelatedRevision,
		ChangedFieldsJSON: changedJSON,
		Note:              meta.Note,
		BeforeJSON:        beforeJSON,
		AfterJSON:         afterJSON,
	})
}

func marshalTaskSnapshot(t *store.Task) (json.RawMessage, error) {
	if t == nil {
		return nil, nil
	}
	b, err := json.Marshal(t)
	if err != nil {
		return nil, err
	}
	return json.RawMessage(b), nil
}

func changedTaskFieldsJSON(before, after *store.Task) (json.RawMessage, error) {
	var beforeMap, afterMap map[string]json.RawMessage
	if before != nil {
		b, err := json.Marshal(before)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(b, &beforeMap); err != nil {
			return nil, err
		}
	}
	if after != nil {
		b, err := json.Marshal(after)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(b, &afterMap); err != nil {
			return nil, err
		}
	}
	fields := []string{}
	seen := map[string]bool{}
	for k := range beforeMap {
		seen[k] = true
	}
	for k := range afterMap {
		seen[k] = true
	}
	for k := range seen {
		if string(beforeMap[k]) != string(afterMap[k]) {
			fields = append(fields, k)
		}
	}
	sort.Strings(fields)
	b, err := json.Marshal(fields)
	if err != nil {
		return nil, err
	}
	return b, nil
}

func cloneTask(t *store.Task) *store.Task {
	if t == nil {
		return nil
	}
	out := *t
	out.TagsJSON = append(json.RawMessage(nil), t.TagsJSON...)
	out.StatusHistoryJSON = append(json.RawMessage(nil), t.StatusHistoryJSON...)
	if t.ClosedAt != nil {
		v := *t.ClosedAt
		out.ClosedAt = &v
	}
	if t.DueAt != nil {
		v := *t.DueAt
		out.DueAt = &v
	}
	if t.AssignedAt != nil {
		v := *t.AssignedAt
		out.AssignedAt = &v
	}
	if t.LeaseExpiresAt != nil {
		v := *t.LeaseExpiresAt
		out.LeaseExpiresAt = &v
	}
	if t.DeletedAt != nil {
		v := *t.DeletedAt
		out.DeletedAt = &v
	}
	return &out
}

func mutationContext(mc []MutationContext, fallbackSession string) MutationContext {
	if len(mc) == 0 {
		return MutationContext{SessionID: fallbackSession}
	}
	c := mc[0]
	if c.SessionID == "" {
		c.SessionID = fallbackSession
	}
	return c
}

func truncateForHistory(s string, limit int) string {
	s = strings.TrimSpace(s)
	if limit <= 0 || len(s) <= limit {
		return s
	}
	// rune-safe truncation: find the last full rune within the cap.
	for i := limit; i > 0; i-- {
		if i < len(s) && (s[i]&0xc0) != 0x80 {
			return s[:i] + "..."
		}
	}
	return s[:limit] + "..."
}

func readHistory(raw json.RawMessage) []store.TaskStatusHistoryEntry {
	if len(raw) == 0 {
		return nil
	}
	var out []store.TaskStatusHistoryEntry
	_ = json.Unmarshal(raw, &out)
	return out
}

func assigneeDisplay(a *Assignee) string {
	if a == nil || (a.SessionID == "" && a.PeerID == "" && a.UserID == "") {
		return ""
	}
	if a.UserID != "" {
		return "user:" + a.UserID
	}
	if a.PeerID != "" {
		if a.SessionID == "" {
			return "peer:" + a.PeerID
		}
		return "peer:" + a.PeerID + "/" + a.SessionID
	}
	return a.SessionID
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
