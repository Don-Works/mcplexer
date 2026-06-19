package store

import (
	"context"
	"encoding/json"
	"time"
)

// Store is the composite interface for all data access.
type Store interface {
	WorkspaceStore
	AuthScopeStore
	ModelProfileStore
	OAuthProviderStore
	DownstreamServerStore
	RouteRuleStore
	SessionStore
	AuditStore
	ToolApprovalStore
	ToolDescriptionStore
	SettingsStore
	MeshStore
	TelegramStore
	GoogleChatStore
	SkillInvocationStore
	TrustedSignerStore
	P2PPeerStore
	UserStore
	InstalledSkillStore
	SkillRegistryStore
	WorkerTemplateStore
	MemoryStore
	PersonStore
	SecretPromptStore
	ScheduledJobStore
	SanitizerMetaStore
	InstalledClientStore
	HarnessInitStore
	ApprovalRuleStore
	WorkerStore
	TaskStore
	SkillRunStore
	SkillRefinementStore
	BrainIndexStore
	RecipeStore
	DataWorkbenchStore
	Tx(ctx context.Context, fn func(Store) error) error
	Ping(ctx context.Context) error
	Close() error
}

// TaskStore manages the tasks table (migration 061) + its companion
// tables: task_notes, task_status_vocabulary, task_offers,
// workspace_peer_bindings, task_assign_throttles. The operational
// task primitive — see internal/store/sqlite/task.go for the
// implementation and .planning/tasks/PLAN.md for the design.
type TaskStore interface {
	// CreateTask inserts a new task. ID, CreatedAt, UpdatedAt default to
	// new ULID + now when empty. StatusHistoryJSON should be initialised
	// with the "created" event by the service layer before this call.
	CreateTask(ctx context.Context, t *Task) error

	// GetTask returns one task by ID. ErrNotFound when missing or
	// soft-deleted.
	GetTask(ctx context.Context, id string) (*Task, error)

	// ListTasks returns rows matching the filter, ordered by
	// updated_at DESC.
	ListTasks(ctx context.Context, f TaskFilter) ([]Task, error)

	// ListTaskIDsByPrefix returns task IDs in the given workspace whose
	// ID starts with the (case-insensitive) prefix. Excludes soft-deleted
	// rows. Capped at `limit` (use a small value — the caller is
	// disambiguating, not paging). Backs the compose_into short-prefix
	// resolver in the task handler.
	ListTaskIDsByPrefix(ctx context.Context, workspaceID, prefix string, limit int) ([]string, error)

	// UpdateTask replaces the row with the given Task. Caller is
	// responsible for appending to StatusHistoryJSON before this call.
	// Touches updated_at. Returns ErrNotFound if missing or soft-deleted.
	UpdateTask(ctx context.Context, t *Task) error

	// SoftDeleteTask stamps deleted_at. Idempotent. ErrNotFound when
	// already soft-deleted.
	SoftDeleteTask(ctx context.Context, id string) error

	// SearchTasks runs an FTS5 query over title + description + meta +
	// tags + status, intersected with the filter. query is a porter-
	// stemmed FTS5 MATCH expression.
	SearchTasks(ctx context.Context, f TaskFilter, query string) ([]Task, error)

	// CountTasksByStatus returns counts grouped by status for a single
	// workspace. Excludes deleted rows. Backs the dashboard + the
	// task_status_vocabulary discovery surface.
	CountTasksByStatus(ctx context.Context, workspaceID string) (map[string]int, error)

	// ListTasksSinceHLC streams tasks in the workspace whose hlc_at is
	// strictly greater than sinceHLC, in ascending HLC order. Backs the
	// /mcplexer/task-sync/1.0.0 gossip stream — the receiver advances
	// its watermark per batch and re-issues the query until the page
	// returns < limit rows. limit <= 0 falls back to a 1000-row cap.
	// Pass sinceHLC="" to receive every row.
	ListTasksSinceHLC(ctx context.Context, workspaceID, sinceHLC string, limit int) ([]Task, error)

	// MaxHLCForWorkspace returns the workspace's high-water hlc_at
	// across non-deleted tasks — the local watermark a task-sync client
	// sends in its Hello so the serving peer only replays newer rows.
	// Returns "" (no error) when the workspace holds no tasks yet.
	MaxHLCForWorkspace(ctx context.Context, workspaceID string) (string, error)

	// AppendTaskNote inserts an append-only note. ID + CreatedAt default
	// to ULID + now when empty.
	AppendTaskNote(ctx context.Context, n *TaskNote) error

	// ListTaskNotes returns notes for one task ordered by created_at DESC.
	ListTaskNotes(ctx context.Context, taskID string, limit int) ([]TaskNote, error)

	// InsertTaskAttachment inserts an attachment index row. ID + CreatedAt
	// default to ULID + now when empty. The on-disk file is the caller's
	// responsibility; this method only writes the index. Sha256,
	// StoragePath, SizeBytes and WorkspaceID are required.
	InsertTaskAttachment(ctx context.Context, a *TaskAttachment) error

	// GetTaskAttachment returns one attachment row by ID. ErrNotFound when
	// missing or soft-deleted.
	GetTaskAttachment(ctx context.Context, id string) (*TaskAttachment, error)

	// ListTaskAttachments returns the non-deleted attachment rows for a
	// task ordered by created_at DESC. Backs task__list_attachments.
	ListTaskAttachments(ctx context.Context, taskID string) ([]TaskAttachment, error)

	// SoftDeleteTaskAttachment stamps deleted_at. Idempotent: returns
	// ErrNotFound when already soft-deleted or missing. The on-disk blob
	// is NOT removed — a row deleted here may share its sha256 with
	// another live row in the same task. Background GC is a future
	// concern (C2.3+).
	SoftDeleteTaskAttachment(ctx context.Context, id string) error

	// UpsertTaskStatusVocab inserts-or-replaces the (workspace_id,
	// status_text) row. ManagedBy defaults to "user" when empty; Kind
	// defaults to "open" when empty (the conservative bucket — won't
	// trigger working-state UI affordances or auto-claim).
	UpsertTaskStatusVocab(ctx context.Context, v *TaskStatusVocab) error

	// ListTaskStatusVocab returns every vocab entry for a workspace.
	ListTaskStatusVocab(ctx context.Context, workspaceID string) ([]TaskStatusVocab, error)

	// DeleteTaskStatusVocab removes one entry.
	DeleteTaskStatusVocab(ctx context.Context, workspaceID, statusText string) error

	// IsTerminalStatus reports whether the given (workspace, status) is
	// flagged terminal in the vocabulary. Falls back to false (non-
	// terminal) when no vocab entry exists yet — operational by default.
	IsTerminalStatus(ctx context.Context, workspaceID, status string) (bool, error)

	// UpsertWorkspacePeerBinding records the workspace identity binding
	// between a peer's remote workspace and a local workspace.
	UpsertWorkspacePeerBinding(ctx context.Context, b *WorkspacePeerBinding) error

	// GetWorkspacePeerBinding looks up the local workspace for a peer's
	// remote workspace. Returns ErrNotFound when no binding exists yet.
	GetWorkspacePeerBinding(ctx context.Context, peerID, remoteWorkspaceID string) (*WorkspacePeerBinding, error)

	// ListLocalWorkspaceIDsForPeer returns the distinct local workspace IDs
	// a peer is bound to. This is the authoritative authorization set for
	// workspace-scoped pairing: a peer may only see local data whose
	// workspace_id is in this set. An empty slice means default-deny
	// (the peer sees nothing).
	ListLocalWorkspaceIDsForPeer(ctx context.Context, peerID string) ([]string, error)

	// ListWorkspacePeerBindingsForPeer returns every binding row for a peer
	// (one per remote workspace it is paired with). Used to map outbound
	// data and confirm pairing scope.
	ListWorkspacePeerBindingsForPeer(ctx context.Context, peerID string) ([]WorkspacePeerBinding, error)

	// SetWorkspaceLink promotes (or creates) a binding into an explicit
	// linked workspace — sets linked=1, link_established_by, and
	// link_established_at. Upserts the row so a link can be declared
	// before any offer has established a plain binding. establishedBy is
	// "local" (operator declared here) or "peer" (mirror from the peer's
	// first linked task). See migration 088.
	SetWorkspaceLink(ctx context.Context, b *WorkspacePeerBinding, establishedBy string) error

	// ClearWorkspaceLink demotes a binding back to a plain offer-routing
	// memo (linked=0); the routing row itself is preserved so in-flight
	// offers still resolve. No-op when no row exists.
	ClearWorkspaceLink(ctx context.Context, peerID, remoteWorkspaceID string) error

	// ListWorkspaceLinks returns every linked binding (linked=1) across all
	// peers — the dashboard's "linked workspaces" view.
	ListWorkspaceLinks(ctx context.Context) ([]WorkspacePeerBinding, error)

	// ListLinkedPeersForWorkspace returns the peer IDs a local workspace is
	// linked to — the send-side gate for task replication. Empty slice
	// means "this workspace is not linked anywhere" (replicate to nobody).
	ListLinkedPeersForWorkspace(ctx context.Context, localWorkspaceID string) ([]string, error)

	// CreateTaskOffer inserts a new offer row. Dedupe via
	// (direction, from_peer_id, to_peer_id, remote_task_id, envelope_nonce);
	// a duplicate is a no-op (returns nil with no error so libp2p replay
	// is idempotent).
	CreateTaskOffer(ctx context.Context, o *TaskOffer) error

	// GetTaskOffer returns one offer by ID. ErrNotFound when missing.
	GetTaskOffer(ctx context.Context, id string) (*TaskOffer, error)

	// ListTaskOffers returns offers matching the filter, ordered by
	// created_at DESC.
	ListTaskOffers(ctx context.Context, f TaskOfferFilter) ([]TaskOffer, error)

	// UpdateTaskOfferState transitions an offer to a new state +
	// optional accepted/declined timestamps + declined_reason.
	UpdateTaskOfferState(ctx context.Context, id, state string, acceptedAt, declinedAt *time.Time, declinedReason string, taskID, workspaceID string) error

	// FindLocalTaskForRemoteOffer returns the local task id a previously-
	// accepted offer from this (peer, remote_task_id) produced — the
	// mapping that lets a re-pushed task (linked-workspace replication)
	// CONVERGE onto the existing local row instead of creating a
	// duplicate. Returns "" + ErrNotFound when no accepted offer maps to a
	// live local task yet.
	FindLocalTaskForRemoteOffer(ctx context.Context, fromPeerID, remoteTaskID string) (string, error)

	// UpsertTaskAssignThrottle records the throttle window for a
	// (peer, workspace) pair. The window cadence is decided by the
	// service layer; the store is pure write-through.
	UpsertTaskAssignThrottle(ctx context.Context, t *TaskAssignThrottle) error

	// GetTaskAssignThrottle returns the current throttle row.
	// ErrNotFound when there's no recorded assignment yet.
	GetTaskAssignThrottle(ctx context.Context, peerID, workspaceID string) (*TaskAssignThrottle, error)

	// SelectDistinctTaskStatuses returns each status value currently
	// in use on non-deleted tasks within a workspace plus the number of
	// tasks carrying it. Backs the Phase 5 task__consolidate_statuses
	// admin tool — operators see real frequencies before agreeing to a
	// merge plan. The map is keyed by status_text → count.
	SelectDistinctTaskStatuses(ctx context.Context, workspaceID string) (map[string]int, error)

	// RebindPeerInTasks rewrites every reference to oldPeerID across
	// task-related tables (tasks.assignee_peer_id, tasks.origin_peer_id,
	// tasks.assigned_by_peer_id, task_offers.from_peer_id,
	// task_offers.to_peer_id, workspace_peer_bindings.peer_id) to
	// newPeerID, inside a single transaction. Returns the per-table
	// row counts so the admin tool can report what moved. Used after a
	// re-pair / device-key rotation when a peer's identity changes but
	// the workspace's task graph should follow.
	RebindPeerInTasks(ctx context.Context, oldPeerID, newPeerID string) (map[string]int, error)

	// ListMilestonesWithBurndown returns one MilestoneBurndown per
	// milestone-tagged epic in the workspace (tag includes "milestone"
	// + due_at IS NOT NULL + not soft-deleted), with their children rollup
	// and a per-day burndown series computed against the children's
	// closed_at timestamps. Children are discovered via the parent's
	// `meta.composes: id, id, ...` frontmatter line. Ordered by due_at ASC
	// so the soonest milestone is first.
	ListMilestonesWithBurndown(ctx context.Context, workspaceID string) ([]MilestoneBurndown, error)

	// HeartbeatTask bumps lease_expires_at to now+ttl ONLY when the
	// caller matches the current assignee. Returns (true, nil) if a
	// row matched + was bumped, (false, nil) when the caller is not
	// the current assignee (silent no-op semantics — peers can't
	// extend each other's leases). Excludes soft-deleted rows.
	HeartbeatTask(ctx context.Context, id, sessionID string, ttl time.Duration) (bool, error)

	// ClearExpiredTaskLeases finds every row whose lease_expires_at is
	// before `now`, nulls its assignee + lease columns, and returns
	// the cleared row ids so the caller can append
	// evt=lease_expired to each row's status_history. Excludes
	// soft-deleted rows.
	ClearExpiredTaskLeases(ctx context.Context, now time.Time) ([]string, error)

	// ClearSessionTaskLeases nulls assignee + lease columns for every
	// non-deleted task owned by the given session. Returns cleared row
	// ids so the caller can append history + publish events. Used for
	// immediate release on agent disconnect (vs. the passive sweep).
	ClearSessionTaskLeases(ctx context.Context, sessionID string) ([]string, error)

	// ClaimTask atomically claims an unassigned task for claimantSession.
	// The caller provides a fully-prepared Task (status, assignee, lease,
	// status_history_json, etc. already set). The UPDATE adds a CAS guard
	// that only succeeds when the row's current assignee_session_id is
	// NULL, empty, or already matches claimantSession, and no peer/user
	// assignee owns the row. Returns ErrTaskAlreadyClaimed when the CAS
	// fails (another assignee won the race). Returns ErrNotFound when the
	// row is missing or soft-deleted.
	ClaimTask(ctx context.Context, t *Task, claimantSession string) error
}

// InstalledSkillStore manages the registry of installed .mcskill bundles
// (M2.2). The on-disk skill directory is the source of truth for content;
// this table is the index used by `mcplexer skill list/show/remove` and
// surfaces capability metadata to the runtime without re-parsing TOML.
type InstalledSkillStore interface {
	UpsertInstalledSkill(ctx context.Context, s *InstalledSkill) error
	GetInstalledSkill(ctx context.Context, name string) (*InstalledSkill, error)
	ListInstalledSkills(ctx context.Context) ([]InstalledSkill, error)
	DeleteInstalledSkill(ctx context.Context, name string) error
}

// SkillRegistryFilter narrows skill registry queries.
type SkillRegistryFilter struct {
	Name           string // exact name match; empty = all
	IncludeDeleted bool   // include soft-deleted rows
	Limit          int    // 0 = no limit
}

// SkillScope identifies which slice of the registry a query addresses.
// WorkspaceIDs is the set of workspace IDs to include alongside global
// rows (nil-workspace). An empty slice means "global only". Pass
// IncludeAll=true to bypass scoping (admin operations + import wizards).
type SkillScope struct {
	WorkspaceIDs []string
	IncludeAll   bool
}

// SkillRegistryStore manages versioned skill rows for the agent-facing
// registry exposed via mcpx__skill_* tools. See SkillRegistryEntry for
// the model. Operations are racy-safe: PublishSkillRegistryEntry runs
// inside its own transaction so concurrent publishes for the same
// (workspace_id, name) pair receive contiguous version numbers.
//
// Scope semantics: every read takes a SkillScope describing which
// workspace_ids the caller can see. Skills pinned to a workspace are
// invisible from any other workspace, while global rows (workspace_id
// IS NULL) are visible everywhere. Pass SkillScope{IncludeAll: true}
// to bypass — used by admin tools and the per-name shadowing logic.
type SkillRegistryStore interface {
	// PublishSkillRegistryEntry inserts a new version for entry.Name +
	// entry.WorkspaceID (nil = global). If entry.ContentHash matches
	// the most recent active row for that (workspace, name), the
	// existing row is returned and dedup=true. Otherwise a new row is
	// inserted at MAX(version)+1 (or 1 if first) and dedup=false.
	// entry.Version is set to the chosen version on return.
	PublishSkillRegistryEntry(ctx context.Context, entry *SkillRegistryEntry) (dedup bool, err error)

	// GetSkillRegistryEntry returns a specific (workspace, name, version)
	// row. ErrNotFound when missing or soft-deleted. workspaceID nil = global.
	// Does NOT load the bundle BLOB — use GetSkillRegistryBundle for that.
	GetSkillRegistryEntry(ctx context.Context, workspaceID *string, name string, version int) (*SkillRegistryEntry, error)

	// GetSkillRegistryBundle fetches the raw tar.gz bytes for one entry.
	// Returns (nil, "", nil) when the row exists but no bundle is attached,
	// and ErrNotFound when the row is missing or soft-deleted. Kept out of
	// the default reads so list / head queries don't pay the bundle-size
	// cost on every row.
	GetSkillRegistryBundle(ctx context.Context, workspaceID *string, name string, version int) ([]byte, string, error)

	// GetSkillRegistryHead returns the latest active version for name
	// in the given scope. Workspace rows shadow global rows of the same
	// name when both are visible: when scope.WorkspaceIDs contains a
	// workspace and a row exists with that workspace_id, the global row
	// (if any) is hidden. Returns ErrNotFound when no active row matches.
	GetSkillRegistryHead(ctx context.Context, scope SkillScope, name string) (*SkillRegistryEntry, error)

	// ListSkillRegistryHeads returns one row per (effective) skill name
	// in scope, ordered by name. Workspace rows shadow global rows of
	// the same name.
	ListSkillRegistryHeads(ctx context.Context, scope SkillScope, limit int) ([]SkillRegistryEntry, error)

	// ListSkillRegistryVersions returns every version for name in scope,
	// in descending version order. When scope.WorkspaceIDs is non-empty,
	// versions from those workspaces AND globals are returned interleaved
	// (caller decides how to render them).
	ListSkillRegistryVersions(ctx context.Context, scope SkillScope, name string, includeDeleted bool) ([]SkillRegistryEntry, error)

	// SoftDeleteSkillRegistryEntry sets deleted_at on the (workspace,
	// name, version) row. When version=0 every active row for that
	// (workspace, name) is deleted. workspaceID nil = global.
	SoftDeleteSkillRegistryEntry(ctx context.Context, workspaceID *string, name string, version int) error

	// SetSkillRegistryTag points (name, tag) at a version of a specific
	// workspace scope. Idempotent. `@latest` is rejected.
	SetSkillRegistryTag(ctx context.Context, t *SkillRegistryTag) error

	// GetSkillRegistryTag resolves a (name, tag) pair to a version.
	// Returns ErrNotFound when the tag is missing.
	GetSkillRegistryTag(ctx context.Context, name, tag string) (*SkillRegistryTag, error)

	// DeleteSkillRegistryTag removes a (name, tag) row.
	DeleteSkillRegistryTag(ctx context.Context, name, tag string) error
}

// WorkerTemplateStore manages versioned worker_templates rows (migration
// 057). Same shape and scoping rules as SkillRegistryStore: linear
// monotonic version per (workspace_id, name), content-hash dedup,
// SkillScope-based visibility, soft delete via deleted_at. Workspace
// rows shadow global rows of the same name.
type WorkerTemplateStore interface {
	// PublishWorkerTemplate inserts a new version for entry.Name +
	// entry.WorkspaceID (nil = global). Dedup on ContentHash within the
	// same scope returns the existing row (dedup=true). Otherwise a new
	// row is inserted at MAX(version)+1. entry.Version + entry.ID are
	// set on return.
	PublishWorkerTemplate(ctx context.Context, entry *WorkerTemplateEntry) (dedup bool, err error)

	// GetWorkerTemplate returns a specific (workspace, name, version) row.
	// ErrNotFound when missing or soft-deleted.
	GetWorkerTemplate(ctx context.Context, workspaceID *string, name string, version int) (*WorkerTemplateEntry, error)

	// GetWorkerTemplateHead returns the latest active version for name in
	// scope. Workspace rows shadow global rows of the same name.
	GetWorkerTemplateHead(ctx context.Context, scope SkillScope, name string) (*WorkerTemplateEntry, error)

	// ListWorkerTemplateHeads returns one row per (effective) name in
	// scope, ordered by name. Workspace rows shadow global rows.
	ListWorkerTemplateHeads(ctx context.Context, scope SkillScope, limit int) ([]WorkerTemplateEntry, error)

	// ListWorkerTemplateVersions returns every version for name in scope,
	// descending.
	ListWorkerTemplateVersions(ctx context.Context, scope SkillScope, name string, includeDeleted bool) ([]WorkerTemplateEntry, error)

	// SoftDeleteWorkerTemplate sets deleted_at on the (workspace, name,
	// version) row. version=0 deletes every active row for that
	// (workspace, name). workspaceID nil = global.
	SoftDeleteWorkerTemplate(ctx context.Context, workspaceID *string, name string, version int) error
}

// MemoryStore manages the memories table (migration 058) — the cross-
// harness memory layer. Two record kinds share one table: fact and
// note (see store.MemoryKind* constants).
//
// Scoping mirrors SkillScope. WriteMemory is the unified entry point:
// for kind=fact it enforces "one active per (workspace, worker, name)"
// by invalidating the existing active row (stamping t_valid_end) and
// inserting a new active row. For kind=note it always inserts.
//
// Embedding: WriteMemory does NOT compute or store vectors. Call
// UpsertMemoryEmbedding after a successful WriteMemory once the
// embedding provider has produced the vector. This keeps the write
// path latency-free even when an embed provider is configured.
//
// Sync (libp2p /mcplexer/memory/1.0.0): incoming offers are recorded
// via UpsertMemoryOffer; the local user (or admin tool) decides accept/
// decline via AcceptMemoryOffer / DeclineMemoryOffer. The actual content
// is fetched out-of-band and inserted via WriteMemory with
// SourceKind=peer + OriginPeerID populated.
type MemoryStore interface {
	// WriteMemory inserts a row. For Kind=fact, if an active row exists
	// in the same (workspace, worker, name) bucket, that row is
	// invalidated (t_valid_end stamped, invalidated_by pointed at the
	// new row) before insert — atomic in a single transaction. ID is
	// generated when empty. CreatedAt/UpdatedAt/TValidStart default to
	// now when zero. Returns the inserted row's ID.
	WriteMemory(ctx context.Context, e *MemoryEntry) error

	// GetMemory returns one row by ID. ErrNotFound when missing or
	// soft-deleted.
	GetMemory(ctx context.Context, id string) (*MemoryEntry, error)

	// GetMemoryForPeer is the scope-aware variant used by the cross-peer
	// /mcplexer/memory/1.0.0 request handler. Returns the row ONLY when
	// its workspace_id falls within the requesting peer's grant set
	// (allowedWorkspaceIDs, optionally OR'd with workspace_id IS NULL
	// when allowGlobal=true). On non-match returns the SAME ErrNotFound
	// sentinel as a genuinely missing id — the cross-peer caller uses
	// this to keep the constant-shape deny envelope identical for
	// "memory missing" vs "memory exists but you're not scoped".
	//
	// The scope filter runs in the WHERE clause so un-granted rows
	// never load into Go memory — closes the side-channel where a
	// fetched-then-rejected row might leak via log lines, count fields,
	// or partial response construction. JTAC65 fix.
	GetMemoryForPeer(
		ctx context.Context, id string,
		allowedWorkspaceIDs []string, allowGlobal bool,
	) (*MemoryEntry, error)

	// ListMemories returns rows matching the filter, ordered by
	// updated_at DESC. Honors scope, kind, tags, source filters, and
	// the IncludeInvalid / IncludeDeleted flags.
	ListMemories(ctx context.Context, f MemoryFilter) ([]MemoryEntry, error)

	// SearchMemories runs an FTS5 query against name + content + tags
	// in the given filter scope. query is a porter-stemmed FTS5 MATCH
	// expression; callers should NOT raw-concatenate user input (use
	// FTS5 quoting). Results are scored by BM25 (lower = better);
	// MemoryHit.Source = "fts".
	SearchMemories(ctx context.Context, f MemoryFilter, query string) ([]MemoryHit, error)

	// VectorSearchMemories runs a vec0 KNN query and intersects with
	// the filter scope. embedModel must match the model the embeddings
	// were written with — mismatches return ErrNotFound rather than
	// returning garbage. vector is the query embedding (1536 dims).
	// k caps results. MemoryHit.Source = "vec".
	VectorSearchMemories(ctx context.Context, f MemoryFilter, embedModel string, vector []float32, k int) ([]MemoryHit, error)

	// UpsertMemoryEmbedding writes (or replaces) the vector for one
	// memory ID. EmbedModel + EmbedVersion are also stamped on the
	// memories row so callers can detect stale-vector situations.
	UpsertMemoryEmbedding(ctx context.Context, id, embedModel string, embedVersion int, vector []float32) error

	// GetMemoryEmbedding returns the stored vector for one memory ID
	// together with the embed_model it was written under (read from the
	// memories row). Used by consolidation / re-ranking paths that need
	// the persisted vector without re-embedding. Returns ErrNotFound when
	// the memory has no vector row in memories_vec.
	GetMemoryEmbedding(ctx context.Context, id string) (model string, vector []float32, err error)

	// InvalidateMemory stamps t_valid_end + invalidated_by on the row.
	// The row stays visible to history queries (IncludeInvalid=true)
	// but is excluded from default lists. Useful when a fact is
	// superseded without a new row replacing it.
	InvalidateMemory(ctx context.Context, id, supersededByID string) error

	// UpdateMemory rewrites the human-editable fields of an existing row
	// in place (the FTS update trigger fires). Used by the brain indexer
	// when a memory .md file is edited: the file is canonical, so the row
	// is reconciled to match. Does NOT touch the bi-temporal invalidation
	// chain or generate a new id. ErrNotFound when the row is missing or
	// soft-deleted.
	UpdateMemory(ctx context.Context, e *MemoryEntry) error

	// SoftDeleteMemory stamps deleted_at on the row. The vector row in
	// memories_vec is also removed so KNN no longer surfaces the entry.
	// Idempotent.
	SoftDeleteMemory(ctx context.Context, id string) error

	// SetMemoryPinned flips the pinned flag on the row. Pinned rows are
	// excluded from the consolidator's auto-prune and shown with a star
	// in the UI. Idempotent. ErrNotFound when the row is missing or
	// soft-deleted.
	SetMemoryPinned(ctx context.Context, id string, pinned bool) error

	// ForgetMemoryBySource hard-deletes every row whose source_session_id
	// matches inside scope. Returns the count. This is the "purge a poisoned
	// session" surface — irreversible, and ALSO drops the matching vector rows.
	// Use SkillScope{IncludeAll:true} only for explicit admin/redaction flows.
	ForgetMemoryBySource(ctx context.Context, sourceSessionID string, scope SkillScope) (int, error)

	// CountMemories returns counts by kind+scope for the dashboard.
	// Honors scope but not kind/source filters.
	CountMemories(ctx context.Context, scope SkillScope) (totalFacts, totalNotes int, err error)

	// GetMemoryStats returns the aggregate "shape of the brain" used by
	// the memory landing header (brain age, totals, type mix, recency
	// histogram, 30-day write series, network reach, top tags, decay
	// pressure). Pure-SQL aggregations — never loads the full memory
	// set. Honors scope. See store.MemoryStats for field semantics.
	GetMemoryStats(ctx context.Context, scope SkillScope) (MemoryStats, error)

	// UpsertMemoryOffer records an incoming offer. The unique constraint
	// is (peer_id, remote_id) — a duplicate offer (same peer + same
	// remote memory id) is a no-op (returns nil).
	UpsertMemoryOffer(ctx context.Context, o *MemoryOffer) error

	// GetMemoryOffer returns one offer by ID. ErrNotFound when missing.
	GetMemoryOffer(ctx context.Context, id string) (*MemoryOffer, error)

	// ListMemoryOffers returns offers matching the filter, ordered by
	// received_at DESC.
	ListMemoryOffers(ctx context.Context, f MemoryOfferFilter) ([]MemoryOffer, error)

	// AcceptMemoryOffer stamps accepted_at + accepted_as_id on the row.
	AcceptMemoryOffer(ctx context.Context, id, localMemoryID string) error

	// DeclineMemoryOffer stamps declined_at on the row.
	DeclineMemoryOffer(ctx context.Context, id string) error

	// LinkMemoryEntity records that memoryID is about the given entity
	// (migration 076). Idempotent on (memory_id, entity_kind, entity_id,
	// role) — re-linking the same edge is a no-op. Empty Role defaults to
	// "subject". createdBy carries the writer's source_session_id for
	// audit; pass "" when unknown.
	LinkMemoryEntity(ctx context.Context, memoryID string, e EntityRef, createdBy string) error

	// UnlinkMemoryEntity removes the matching link. When Role is empty,
	// removes every role for that (memory, kind, id). Idempotent.
	UnlinkMemoryEntity(ctx context.Context, memoryID string, e EntityRef) error

	// ListMemoryEntities returns every entity link for one memory.
	ListMemoryEntities(ctx context.Context, memoryID string) ([]MemoryEntityRow, error)

	// ListEntities surfaces distinct entities aggregated from memory_entities
	// across the filter's scope, sorted by MemoryCount DESC then
	// LastLinkedAt DESC. Powers the "Top entities" tile + entity-picker
	// autocomplete. Honors EntityFilter.Kind to scope to one entity kind.
	ListEntities(ctx context.Context, f EntityFilter) ([]EntitySummary, error)

	// RelatedEntities returns entities that CO-LINK with the given entity
	// in at least one memory (associative-recall AR1). Self is excluded.
	// Ranked by SharedCount DESC then LastSeenAt DESC. Powers
	// "tell me what else this task is related to".
	RelatedEntities(ctx context.Context, x EntityRef, scope SkillScope, limit int) ([]EntityCoLink, error)

	// BuildEntityGraph returns the entity-to-entity graph in scope (AR3):
	// nodes are distinct entities (capped at nodeCap by MemoryCount DESC),
	// edges are co-link pairs with Weight = memory count linking BOTH
	// endpoints. Edges are undirected — emitted once with Source < Target
	// lexically. minWeight drops edges below that count (0 = all).
	BuildEntityGraph(ctx context.Context, scope SkillScope, nodeCap, minWeight int) (EntityGraph, error)

	// LogMemoryRecallEvents persists a batch of recall events (AR4).
	// Best-effort: errors are returned but callers typically use a
	// background goroutine + drop on error to keep recall latency bounded.
	// Idempotency is on (id) — duplicate IDs no-op.
	LogMemoryRecallEvents(ctx context.Context, events []MemoryRecallEvent) error

	// CoRecalledMemories returns memories that frequently co-surface with
	// memoryID in the recall log (AR4). Excludes self. Ranked by score
	// DESC (co-occurrence count weighted by rank proximity).
	CoRecalledMemories(ctx context.Context, memoryID string, scope SkillScope, limit int) ([]CoRecalledMemory, error)

	// GetMemoryRecallStats returns the per-memory recall aggregate (recent
	// surfacing count + last surfaced time) for the given ids, keyed by
	// memory id. Computed in ONE grouped query over memory_recall_events
	// within a recency window — never N round-trips. ids not present in the
	// log are simply absent from the map (the caller treats a missing entry
	// as zero recall, which degrades the ranking nudge to a no-op). An empty
	// ids slice returns an empty map with no query. Powers the bounded
	// recall-driven ranking term (AR4).
	GetMemoryRecallStats(ctx context.Context, ids []string) (map[string]MemoryRecallStat, error)

	// ForgetRecallEventsBySource hard-purges every recall event whose
	// session_id matches inside scope. Mirrors ForgetMemoryBySource — a
	// poisoned session's recall trail can be excised forensically.
	ForgetRecallEventsBySource(ctx context.Context, sessionID string, scope SkillScope) (int, error)

	// InsertChatTurnSignal appends one signal row (B1 of the self-improving
	// chat epic). ID is auto-generated when blank, CreatedAt defaults to
	// now. Idempotent on (id) — duplicate IDs no-op.
	InsertChatTurnSignal(ctx context.Context, s *ChatTurnSignal) error

	// ListChatTurnSignals returns rows matching the filter, newest first.
	// Used by both the friction-extractor worker (NotPromoted=true,
	// Labels=[correction,frustration]) and the A/B telemetry aggregator
	// (PromptVersion+WorkerID grouping).
	ListChatTurnSignals(ctx context.Context, f ChatTurnSignalFilter) ([]ChatTurnSignal, error)

	// MarkChatTurnSignalPromoted stamps promoted_to_refinement_id on a
	// signal once the friction extractor has fed it into a refinement
	// proposal. Idempotent on (id, refinementID) — re-stamping with the
	// same refinement is a no-op.
	MarkChatTurnSignalPromoted(ctx context.Context, signalID, refinementID string) error

	// ForgetChatTurnSignalsBySource hard-purges signals written by the
	// named session id. Mirrors ForgetMemoryBySource for forensic
	// redaction symmetry.
	ForgetChatTurnSignalsBySource(ctx context.Context, sessionID string) (int, error)
}

// RecipeStore provides the structured store for harvested tool-call recipes
// (migration 102). Recipes are mined patterns (per tool_name) with scores,
// param shapes, and FTS search. Used by gateway recipe search tools and the
// harvester.
type RecipeStore interface {
	UpsertRecipe(ctx context.Context, r *Recipe) error
	GetRecipe(ctx context.Context, id string) (*Recipe, error)
	GetRecipeByToolName(ctx context.Context, toolName string) (*Recipe, error)
	ListRecipes(ctx context.Context, f RecipeFilter) ([]Recipe, error)
	SearchRecipes(ctx context.Context, f RecipeFilter) ([]Recipe, error)
	DeleteRecipe(ctx context.Context, id string) error
}

// PersonStore manages the crm_person table + its person_entities companion
// (migration 094). A Person is a workspace-scoped CRM contact record, the
// first brain entity kind beyond task/memory/workspace. The markdown brain is
// canonical; these methods are the derived-index read/write surface the
// indexer + editor drive. See internal/store/sqlite/person.go.
type PersonStore interface {
	// WritePerson inserts a row. ID defaults to a new ULID when empty;
	// CreatedAt/UpdatedAt default to now when zero; SourceKind floors to
	// "agent"; WorkspaceID defaults to the CRM workspace. Name is required
	// and unique within the workspace — a duplicate returns
	// ErrAlreadyExists. The crm_person_ai FTS trigger fires.
	WritePerson(ctx context.Context, p *PersonEntry) error

	// GetPerson returns one row by ID. ErrNotFound when missing or
	// soft-deleted.
	GetPerson(ctx context.Context, id string) (*PersonEntry, error)

	// ListPeople returns rows matching the filter, ordered by updated_at
	// DESC. Honors workspace/name/company/tags/entities filters +
	// IncludeDeleted.
	ListPeople(ctx context.Context, f PersonFilter) ([]PersonEntry, error)

	// SearchPeople runs an FTS5 MATCH query over every text field in the
	// filter scope. query is sanitised internally; pass an empty string to
	// fall back to ListPeople. Results are scored by BM25 (lower=better).
	SearchPeople(ctx context.Context, f PersonFilter, query string) ([]PersonHit, error)

	// UpdatePerson rewrites the human-editable fields of an existing row in
	// place (name, email, phone, company, role, tags, notes, pinned,
	// updated_at). The crm_person_au FTS trigger fires. ErrNotFound when the
	// row is missing or soft-deleted.
	UpdatePerson(ctx context.Context, p *PersonEntry) error

	// SoftDeletePerson stamps deleted_at on the row. Idempotent.
	SoftDeletePerson(ctx context.Context, id string) error

	// CountPeople returns the count of live (non-deleted) people.
	CountPeople(ctx context.Context) (int, error)

	// LinkPersonEntity records that personID is linked to the given entity.
	// Idempotent on (person_id, entity_kind, entity_id, role). Empty Role
	// defaults to "subject". createdBy carries the writer's session id; "".
	LinkPersonEntity(ctx context.Context, personID string, e EntityRef, createdBy string) error

	// UnlinkPersonEntity removes the matching link. Empty Role removes every
	// role for that (person, kind, id). Idempotent.
	UnlinkPersonEntity(ctx context.Context, personID string, e EntityRef) error

	// ListPersonEntities returns every entity link for one person.
	ListPersonEntities(ctx context.Context, personID string) ([]PersonEntityRow, error)
}

// SecretPromptStore manages human-in-the-loop secret prompt records. The
// row never holds the secret value itself — only metadata + the path to the
// 0600 file the daemon wrote on Submit. The path is treated as sensitive
// internal data and MUST NOT be exposed via SSE / audit / agent-visible APIs.
type SecretPromptStore interface {
	CreateSecretPrompt(ctx context.Context, p *SecretPrompt) error
	GetSecretPrompt(ctx context.Context, id string) (*SecretPrompt, error)
	ListPendingSecretPrompts(ctx context.Context) ([]SecretPrompt, error)
	CompleteSecretPrompt(ctx context.Context, id, status, filePath string, completedAt time.Time) error
	ListExpiredSecretPrompts(ctx context.Context, before time.Time) ([]SecretPrompt, error)
}

// SkillInvocationFilter narrows skill invocation queries.
type SkillInvocationFilter struct {
	SkillName *string
	Allowed   *bool
	Limit     int
	Offset    int
}

// SkillInvocationStore records and queries per-skill tool call attempts.
type SkillInvocationStore interface {
	InsertSkillInvocation(ctx context.Context, inv *SkillInvocation) error
	ListSkillInvocations(ctx context.Context, f SkillInvocationFilter) ([]SkillInvocation, error)
}

// TelegramStore manages chat bridge records (Telegram, Google Chat, ...).
type TelegramStore interface {
	UpsertTelegramChat(ctx context.Context, c *TelegramChat) error
	GetTelegramChat(ctx context.Context, id string) (*TelegramChat, error)
	GetTelegramChatByNative(ctx context.Context, platform, nativeChatID string) (*TelegramChat, error)
	ListTelegramChats(ctx context.Context) ([]TelegramChat, error)
	ListActiveTelegramChatsByWorkspace(ctx context.Context, workspaceID string) ([]TelegramChat, error)
	UpdateTelegramChatMinPriority(ctx context.Context, id, minPriority string) error
	DeactivateTelegramChat(ctx context.Context, id string) error
	TouchTelegramChat(ctx context.Context, id string) error

	CreateTelegramPairing(ctx context.Context, p *TelegramPairing) error
	GetTelegramPairing(ctx context.Context, code string) (*TelegramPairing, error)
	DeleteTelegramPairing(ctx context.Context, code string) error
	SweepExpiredTelegramPairings(ctx context.Context, now time.Time) (int, error)

	InsertTelegramSentMessage(ctx context.Context, m *TelegramSentMessage) error
	GetTelegramSentMessage(ctx context.Context, platform, nativeChatID, nativeMessageID string) (*TelegramSentMessage, error)
}

// GoogleChatStore manages the Google Chat bridge records (migration 067).
// Sibling to TelegramStore — same primitives (space row, pairing code,
// sent-message map) shaped for Google Chat's native identifiers (space_name
// instead of native_chat_id, thread_name for threading).
type GoogleChatStore interface {
	UpsertGoogleChatSpace(ctx context.Context, s *GoogleChatSpace) error
	GetGoogleChatSpace(ctx context.Context, id string) (*GoogleChatSpace, error)
	GetGoogleChatSpaceByName(ctx context.Context, spaceName string) (*GoogleChatSpace, error)
	ListGoogleChatSpaces(ctx context.Context) ([]GoogleChatSpace, error)
	ListActiveGoogleChatSpacesByWorkspace(ctx context.Context, workspaceID string) ([]GoogleChatSpace, error)
	UpdateGoogleChatSpaceMinPriority(ctx context.Context, id, minPriority string) error
	UpdateGoogleChatSpaceListenMode(ctx context.Context, id, listenMode string) error
	DeactivateGoogleChatSpace(ctx context.Context, id string) error
	TouchGoogleChatSpace(ctx context.Context, id string) error

	CreateGoogleChatPairing(ctx context.Context, p *GoogleChatPairing) error
	GetGoogleChatPairing(ctx context.Context, code string) (*GoogleChatPairing, error)
	DeleteGoogleChatPairing(ctx context.Context, code string) error
	SweepExpiredGoogleChatPairings(ctx context.Context, now time.Time) (int, error)

	InsertGoogleChatSentMessage(ctx context.Context, m *GoogleChatSentMessage) error
	GetGoogleChatSentMessage(ctx context.Context, spaceName, nativeMessageID string) (*GoogleChatSentMessage, error)
}

// UserStore manages per-HUMAN identity rows + the peer_users join table
// (M7.1). Exactly one User row has IsSelf=true: the local human. Remote
// users are inserted when pairing completes; multiple peers may map to a
// single user when one human pairs multiple machines.
type UserStore interface {
	CreateUser(ctx context.Context, u *User) error
	GetUser(ctx context.Context, userID string) (*User, error)
	GetSelfUser(ctx context.Context) (*User, error)
	ListUsers(ctx context.Context) ([]User, error)
	UpdateUserDisplayName(ctx context.Context, userID, displayName string) error

	// UpsertUser creates a user (is_self=0) or updates the display_name on
	// conflict. Used by the pairing flow when persisting a remote user
	// either for the first time or on re-pair with an updated name.
	UpsertUser(ctx context.Context, userID, displayName string) error

	// LinkPeerToUser inserts a (peer_id, user_id) row. Idempotent: a duplicate
	// (peer_id, user_id) returns nil so re-pair flows just no-op.
	LinkPeerToUser(ctx context.Context, peerID, userID string) error
	GetUserForPeer(ctx context.Context, peerID string) (*User, error)
	ListPeersForUser(ctx context.Context, userID string) ([]P2PPeer, error)
}

// P2PPeerStore manages paired libp2p peers and the short-lived pending-pair
// records that survive a daemon restart mid-handshake. The 6-digit pairing
// code itself is the primary key for pending pairs; consumed codes are
// deleted, never reused.
type P2PPeerStore interface {
	AddPeer(ctx context.Context, p *P2PPeer) error
	GetPeer(ctx context.Context, peerID string) (*P2PPeer, error)
	ListPeers(ctx context.Context) ([]P2PPeer, error)
	RevokePeer(ctx context.Context, peerID string) error
	// UnrevokePeer clears revoked_at so a previously-revoked peer becomes
	// active again. Used by the pair handler when a re-pair handshake
	// completes for a peer that was revoked. No-op (no error) if the row
	// is already active or absent — the caller has already verified the
	// row exists via AddPeer's ErrAlreadyExists.
	UnrevokePeer(ctx context.Context, peerID string) error
	// GrantPeerScope adds a scope to the peer's authorized scope set.
	// Idempotent. Used by the mesh__grant_peer_scope MCP tool to
	// authorize a paired peer for actions like skill-share
	// (mesh.skill_request scope). Returns ErrNotFound if peer is
	// missing or revoked.
	GrantPeerScope(ctx context.Context, peerID, scope string) error
	// RevokePeerScope strips a previously-granted scope. Idempotent.
	RevokePeerScope(ctx context.Context, peerID, scope string) error
	UpdateLastSeen(ctx context.Context, peerID string, t time.Time) error
	// UpdateDisplayName renames a paired peer. Wired by the pairing handler
	// (re-pair) and the display_name_changed mesh event handler.
	UpdateDisplayName(ctx context.Context, peerID, newName string) error
	// SetPeerSSHTarget records the SSH user@host alias the dashboard uses
	// when the user clicks "Focus" on a peer-origin agent. Pass empty to
	// clear. Silently no-ops if the peer is revoked or absent.
	SetPeerSSHTarget(ctx context.Context, peerID, target string) error
	// UpdateSecretTransferRecipient persists the peer's age X25519
	// recipient learned via a peer_identity mesh broadcast. Empty clears.
	// Silently no-ops on revoked peers.
	UpdateSecretTransferRecipient(ctx context.Context, peerID, recipient string) error
	// RememberPeerAddrs persists the most recent direct (non-relay)
	// multiaddrs observed for a peer. Used by the discovery service to
	// hot-start the libp2p peerstore on the next daemon restart so the
	// reconnector doesn't have to wait for the DHT to converge.
	RememberPeerAddrs(ctx context.Context, peerID string, addrs []string) error
	// LoadPeerAddrs returns the JSON-encoded addrs persisted by the most
	// recent RememberPeerAddrs call for peerID. Returns an empty slice (not
	// an error) for unknown / never-persisted peers.
	LoadPeerAddrs(ctx context.Context, peerID string) ([]string, error)

	CreatePendingPair(ctx context.Context, p *P2PPendingPair) error
	GetPendingPair(ctx context.Context, code string) (*P2PPendingPair, error)
	DeletePendingPair(ctx context.Context, code string) error
	SweepExpiredPendingPairs(ctx context.Context, now time.Time) (int, error)
}

// TrustedSignerStore manages the local skill-signer trust list (ADR 0002).
type TrustedSignerStore interface {
	AddTrustedSigner(ctx context.Context, s *TrustedSigner) error
	RemoveTrustedSigner(ctx context.Context, pubkeyID string) error
	IsTrusted(ctx context.Context, pubkeyID string) (bool, error)
	ListTrustedSigners(ctx context.Context) ([]TrustedSigner, error)
}

// SettingsStore manages the singleton settings record.
type SettingsStore interface {
	GetSettings(ctx context.Context) (json.RawMessage, error)
	UpdateSettings(ctx context.Context, data json.RawMessage) error
}

// WorkspaceStore manages workspace records.
type WorkspaceStore interface {
	CreateWorkspace(ctx context.Context, w *Workspace) error
	GetWorkspace(ctx context.Context, id string) (*Workspace, error)
	GetWorkspaceByName(ctx context.Context, name string) (*Workspace, error)
	ListWorkspaces(ctx context.Context) ([]Workspace, error)
	UpdateWorkspace(ctx context.Context, w *Workspace) error
	DeleteWorkspace(ctx context.Context, id string) error
}

// AuthScopeStore manages auth scope records.
type AuthScopeStore interface {
	CreateAuthScope(ctx context.Context, a *AuthScope) error
	GetAuthScope(ctx context.Context, id string) (*AuthScope, error)
	GetAuthScopeByName(ctx context.Context, name string) (*AuthScope, error)
	ListAuthScopes(ctx context.Context) ([]AuthScope, error)
	UpdateAuthScope(ctx context.Context, a *AuthScope) error
	DeleteAuthScope(ctx context.Context, id string) error
	UpdateAuthScopeTokenData(ctx context.Context, id string, data []byte) error
	UpdateAuthScopeEncryptedData(ctx context.Context, id string, data []byte) error
}

// OAuthProviderStore manages OAuth provider records.
type OAuthProviderStore interface {
	CreateOAuthProvider(ctx context.Context, p *OAuthProvider) error
	GetOAuthProvider(ctx context.Context, id string) (*OAuthProvider, error)
	GetOAuthProviderByName(ctx context.Context, name string) (*OAuthProvider, error)
	ListOAuthProviders(ctx context.Context) ([]OAuthProvider, error)
	UpdateOAuthProvider(ctx context.Context, p *OAuthProvider) error
	DeleteOAuthProvider(ctx context.Context, id string) error
}

// DownstreamServerStore manages downstream server records.
type DownstreamServerStore interface {
	CreateDownstreamServer(ctx context.Context, d *DownstreamServer) error
	GetDownstreamServer(ctx context.Context, id string) (*DownstreamServer, error)
	GetDownstreamServerByName(ctx context.Context, name string) (*DownstreamServer, error)
	ListDownstreamServers(ctx context.Context) ([]DownstreamServer, error)
	UpdateDownstreamServer(ctx context.Context, d *DownstreamServer) error
	DeleteDownstreamServer(ctx context.Context, id string) error
	UpdateCapabilitiesCache(ctx context.Context, id string, cache json.RawMessage) error
}

// RouteRuleStore manages route rule records.
type RouteRuleStore interface {
	CreateRouteRule(ctx context.Context, r *RouteRule) error
	GetRouteRule(ctx context.Context, id string) (*RouteRule, error)
	ListRouteRules(ctx context.Context, workspaceID string) ([]RouteRule, error)
	UpdateRouteRule(ctx context.Context, r *RouteRule) error
	DeleteRouteRule(ctx context.Context, id string) error
}

// SessionStore manages session records.
type SessionStore interface {
	CreateSession(ctx context.Context, s *Session) error
	GetSession(ctx context.Context, id string) (*Session, error)
	DisconnectSession(ctx context.Context, id string) error
	DisconnectAllSessions(ctx context.Context) (int, error)
	ListActiveSessions(ctx context.Context) ([]Session, error)
	CleanupStaleSessions(ctx context.Context, before time.Time) (int, error)
}

// AuditStore manages audit log records.
type AuditStore interface {
	InsertAuditRecord(ctx context.Context, r *AuditRecord) error
	QueryAuditRecords(ctx context.Context, f AuditFilter) ([]AuditRecord, int, error)
	GetAuditStats(ctx context.Context, workspaceID string, after, before time.Time) (*AuditStats, error)
	GetDashboardTimeSeries(ctx context.Context, after, before time.Time) ([]TimeSeriesPoint, error)
	GetDashboardTimeSeriesBucketed(ctx context.Context, after, before time.Time, bucketSec int) ([]TimeSeriesPoint, error)
	GetToolLeaderboard(ctx context.Context, after, before time.Time, limit int) ([]ToolLeaderboardEntry, error)
	GetServerHealth(ctx context.Context, after, before time.Time) ([]ServerHealthEntry, error)
	GetErrorBreakdown(ctx context.Context, after, before time.Time, limit int) ([]ErrorBreakdownEntry, error)
	GetRouteHitMap(ctx context.Context, after, before time.Time) ([]RouteHitEntry, error)
	GetAuditCacheStats(ctx context.Context, after, before time.Time) (*AuditCacheStats, error)
	// PruneAuditRecords deletes audit_records whose created_at is older
	// than `before`. Returns the number of rows deleted. Idempotent —
	// calling with a `before` in the past simply returns 0. Wired by the
	// nightly retention job.
	PruneAuditRecords(ctx context.Context, before time.Time) (int64, error)

	// CountChildCLIToolCalls counts audit_records produced by a CLI-child
	// MCP session within the given (workspace_id, time-window). Backs the
	// WorkerRun.tool_calls_count derive-at-read-time fix for the
	// claude_cli / opencode_cli / grok_cli / mimo_cli adapter families — those adapters spawn a
	// child CLI that opens its own stdio MCP connection back to the
	// gateway, so the real tool calls land in audit_records (with
	// client_type identifying the child) rather than the model response.
	//
	// Filter (all conjunctive):
	//   workspace_id = workspaceID
	//   timestamp >= start AND timestamp <= end
	//   actor_kind != 'worker'                  // exclude the runner's own audit emissions
	//   client_type IN clientTypes              // narrow to CLI children
	//   status = 'success'                      // a tool dispatch is what we want to count, not a denial row
	//
	// clientTypes must be non-empty. Returns 0 when no rows match.
	// Callers normalise the list (claude_cli, opencode, opencode_cli,
	// grok, grok_cli, xai, xai_cli, mimo, mimo_cli, mimocode, claude_code, claude-code).
	CountChildCLIToolCalls(ctx context.Context, workspaceID string, start, end time.Time, clientTypes []string) (int, error)
}

// MeshStore manages mesh messages and agent records.
type MeshStore interface {
	InsertMeshMessage(ctx context.Context, m *MeshMessage) error
	QueryMeshMessages(ctx context.Context, f MeshMessageFilter) ([]MeshMessage, error)
	GetMeshMessage(ctx context.Context, id string) (*MeshMessage, error)
	IncrementReplyCount(ctx context.Context, messageID string) error
	ExtendMessageExpiry(ctx context.Context, messageID string, expiresAt time.Time) error
	ArchiveExpiredMessages(ctx context.Context, now time.Time) (int, error)
	DeleteArchivedMessages(ctx context.Context, before time.Time) (int, error)
	CountLiveMessages(ctx context.Context, workspaceID string) (int, error)
	ArchiveLowestPriority(ctx context.Context, workspaceID string, count int) (int, error)
	// ArchiveMessagesBySenderAndKinds archives (status='archived') all live
	// messages matching any of the given sender session IDs AND any of the
	// given kinds. Used by delegation review to auto-resolve worker findings
	// and delegation_reply messages so they stop counting as pending.
	ArchiveMessagesBySenderAndKinds(ctx context.Context, senderSessionIDs []string, kinds []string) (int, error)
	// ArchiveOldWorkerFindings archives all live messages with actor_kind
	// "worker" and kind IN ("finding","reply") older than the given cutoff.
	// Used by the mesh reaper as a 24h safety net so unreviewed delegation
	// worker messages don't pile up indefinitely.
	ArchiveOldWorkerFindings(ctx context.Context, olderThan time.Time) (int, error)

	UpsertMeshAgent(ctx context.Context, a *MeshAgent) error
	GetMeshAgent(ctx context.Context, sessionID string) (*MeshAgent, error)
	ListActiveMeshAgents(ctx context.Context, workspaceID string, since time.Time) ([]MeshAgent, error)
	// ListActiveMeshAgentsInWorkspaces returns active local-origin agents
	// in the given workspace set — the outbound filter for workspace-scoped
	// peer gossip. Empty set => no rows (default-deny).
	ListActiveMeshAgentsInWorkspaces(ctx context.Context, wsIDs []string, since time.Time) ([]MeshAgent, error)
	UpdateAgentCursor(ctx context.Context, sessionID, cursor string) error
	TouchMeshAgent(ctx context.Context, sessionID string) error
	// SetMeshAgentStatus updates ONLY the status field for an existing
	// session, bumping last_seen_at. Used by mesh__set_agent_status.
	SetMeshAgentStatus(ctx context.Context, sessionID, status string, now time.Time) error
	// SetMeshAgentTerminalLocator updates ONLY the tmux_session/window/pane
	// fields for an existing session. Used by the mesh__receive entry point
	// when the agent passes tmux_* fields and by the cross-peer agent
	// gossip path. Silently no-ops if the row doesn't exist.
	SetMeshAgentTerminalLocator(ctx context.Context, sessionID, tmuxSession, tmuxWindow, tmuxPane string, now time.Time) error
	// FindRecentLocalAgentByClient looks up the most-recent prior local
	// agent row in the same (workspace_id, client_type) bucket, excluding
	// excludeSessionID. Returns nil (not ErrNotFound) when no match. Used
	// by mesh.ensureAgent to inherit name/role/status/locator across
	// process restarts — session_id is per-process so a fresh row would
	// otherwise drop the agent's identity.
	FindRecentLocalAgentByClient(ctx context.Context, workspaceID, clientType, excludeSessionID string) (*MeshAgent, error)
	DeleteMeshAgent(ctx context.Context, sessionID string) error

	// DeleteMeshAgentsByOrigin removes every mesh_agents row whose origin
	// column matches origin exactly. Used by the agent-directory gossip
	// receiver to implement snapshot-replace semantics for a peer
	// ("origin = peer:<sender>") and the bye-frame "drop everything from
	// this peer" path. Returns the number of rows deleted.
	DeleteMeshAgentsByOrigin(ctx context.Context, origin string) (int, error)

	// GetWorkspacePeerBinding looks up the local workspace_id for a
	// peer's remote workspace_id. ErrNotFound when no binding exists.
	// Used by the p2p bridge to resolve inbound envelopes' WorkspaceID
	// into the receiver's local workspace before insert (G1).
	GetWorkspacePeerBinding(ctx context.Context, peerID, remoteWorkspaceID string) (*WorkspacePeerBinding, error)

	// MeshOutboundQueueStore — offline-delivery queue for the cross-machine
	// p2p mesh. See internal/mesh/outbound_queue.go for the lifecycle.

	// EnqueueMeshOutbound writes one row into mesh_outbound_queue. The
	// message_id column is UNIQUE — a second call with the same ID is a
	// no-op (returns nil, no row created). Used by the mesh dispatch path
	// when a libp2p SendToPeer fails because the peer is offline.
	EnqueueMeshOutbound(ctx context.Context, o *MeshOutbound) error
	// ListDueMeshOutbound returns rows whose target peer matches and
	// next_attempt_at <= now AND expires_at > now AND delivered_at IS NULL.
	// Ordered by enqueued_at ASC (oldest first). limit caps the batch.
	ListDueMeshOutbound(ctx context.Context, peerID string, now time.Time, limit int) ([]MeshOutbound, error)
	// MarkMeshOutboundDelivered stamps delivered_at = now for the row.
	// Idempotent — a second call is a harmless UPDATE.
	MarkMeshOutboundDelivered(ctx context.Context, messageID string, now time.Time) error
	// BumpMeshOutboundAttempt records a failed retry: attempts++,
	// last_error set, next_attempt_at pushed out.
	BumpMeshOutboundAttempt(ctx context.Context, messageID string, lastErr string, nextAttemptAt time.Time) error
	// ListPendingMeshOutbound returns every undelivered, unexpired row
	// across all peers — backs the mesh__list_queue admin tool and the
	// 30s sweeper. limit <= 0 means "no limit" (caps applied in the
	// implementation to keep results bounded).
	ListPendingMeshOutbound(ctx context.Context, now time.Time, limit int) ([]MeshOutbound, error)
	// ListExpiredMeshOutbound returns rows whose expires_at < now AND
	// delivered_at IS NULL. Surfaces undelivered messages that aged out
	// before the peer came back online — logged as warn + then pruned.
	ListExpiredMeshOutbound(ctx context.Context, now time.Time, limit int) ([]MeshOutbound, error)
	// PruneMeshOutbound deletes (a) delivered rows older than
	// deliveredBefore and (b) expired-undelivered rows older than
	// expiredBefore. Returns the row count. Called by the daily prune.
	PruneMeshOutbound(ctx context.Context, deliveredBefore, expiredBefore time.Time) (int, error)
}

// ToolDescriptionStore manages tool description version records.
type ToolDescriptionStore interface {
	CreateToolDescriptionVersion(ctx context.Context, v *ToolDescriptionVersion) error
	GetToolDescriptionVersion(ctx context.Context, id string) (*ToolDescriptionVersion, error)
	ListToolDescriptionVersions(ctx context.Context, f ToolDescriptionFilter) ([]ToolDescriptionVersion, int, error)
	GetActiveDescriptions(ctx context.Context) (map[string]string, error)
	ActivateVersion(ctx context.Context, id, reviewedBy, reviewNote string) error
	RejectVersion(ctx context.Context, id, reviewedBy, reviewNote string) error
	HasPendingForToolBySession(ctx context.Context, toolName, sessionID string) (bool, error)
}

// ToolApprovalStore manages tool call approval records.
type ToolApprovalStore interface {
	CreateToolApproval(ctx context.Context, a *ToolApproval) error
	GetToolApproval(ctx context.Context, id string) (*ToolApproval, error)
	ListPendingApprovals(ctx context.Context) ([]ToolApproval, error)
	// ListResolvedApprovals returns terminal (non-pending) approvals
	// newest-first by resolved_at, capped at limit. limit <= 0 applies an
	// internal default cap. This backs the dashboard's approval history.
	ListResolvedApprovals(ctx context.Context, limit int) ([]ToolApproval, error)
	ResolveToolApproval(ctx context.Context, id, status, approverSessionID, approverType, resolution string) error
	ExpirePendingApprovals(ctx context.Context, before time.Time) (int, error)
	GetApprovalMetrics(ctx context.Context, after, before time.Time) (*ApprovalMetrics, error)
}

// ScheduledJobStore manages Schedule Guard job rows (M0-A). The scheduler
// tick selects due jobs via DueScheduledJobs; CRUD is exposed for the
// admin tooling that wires jobs up.
type ScheduledJobStore interface {
	CreateScheduledJob(ctx context.Context, j *ScheduledJob) error
	GetScheduledJob(ctx context.Context, id string) (*ScheduledJob, error)
	ListScheduledJobs(ctx context.Context) ([]ScheduledJob, error)
	UpdateScheduledJob(ctx context.Context, j *ScheduledJob) error
	DeleteScheduledJob(ctx context.Context, id string) error
	// DueScheduledJobs returns enabled jobs whose next_run_at <= now,
	// ordered oldest-first, capped at limit. limit <= 0 means "no caller
	// limit" (the implementation may still apply an internal cap).
	DueScheduledJobs(ctx context.Context, now time.Time, limit int) ([]ScheduledJob, error)
}

// SanitizerMetaStore manages per-scope sanitizer policy + counters
// (M0-A). UpsertSanitizerMeta is the primary write path so policy
// updates and first-time inserts collapse into one call.
type SanitizerMetaStore interface {
	GetSanitizerMeta(ctx context.Context, scope, scopeID string) (*SanitizerMeta, error)
	UpsertSanitizerMeta(ctx context.Context, m *SanitizerMeta) error
	ListSanitizerMeta(ctx context.Context) ([]SanitizerMeta, error)
	// IncrementSanitizerCounter atomically bumps one of detected_count,
	// redacted_count, or blocked_count for the (scope, scopeID) row and
	// updates last_event_at. counter must be one of those three names;
	// any other value returns an error.
	IncrementSanitizerCounter(ctx context.Context, scope, scopeID, counter string) error
}

// InstalledClientStore manages the registry of MCP clients mcplexer has
// hooked into on this machine plus the receipt ledger of reversible OS
// mutations (M0-A). Used by `mcplexer setup` and `mcplexer uninstall`.
type InstalledClientStore interface {
	UpsertInstalledClient(ctx context.Context, c *InstalledClient) error
	GetInstalledClient(ctx context.Context, id string) (*InstalledClient, error)
	ListInstalledClients(ctx context.Context) ([]InstalledClient, error)
	CreateInstallReceipt(ctx context.Context, r *InstallReceipt) error
	// ListInstallReceipts returns receipts filtered by clientID. Empty
	// clientID returns every receipt. When includeReversed is false,
	// rows with a non-NULL reversed_at are excluded.
	ListInstallReceipts(ctx context.Context, clientID string, includeReversed bool) ([]InstallReceipt, error)
	// MarkReceiptReversed stamps reversed_at = now and records the
	// (possibly empty) reverseError. Idempotent.
	MarkReceiptReversed(ctx context.Context, id string, reverseError string) error
}

// HarnessInitStore tracks MCP initialize.clientInfo per harness key
// (for last_initialize_at + client_info) and the content-hash bootstrap
// receipts + drift flags for harness-sync artifacts. Added in migration 104.
type HarnessInitStore interface {
	RecordHarnessInitialize(ctx context.Context, key, clientInfo string) error
	GetHarnessInitialization(ctx context.Context, key string) (*HarnessInitialization, error)
	ListHarnessInitializations(ctx context.Context) ([]HarnessInitialization, error)
	UpsertHarnessBootstrap(ctx context.Context, h *HarnessInitialization) error
}

// ApprovalRuleStore manages the 3-axis allowlist consulted by the
// various guards before prompting (M0-A). ListApprovalRules takes a
// surface so the matcher can scope to just the rules it cares about;
// pass an empty surface to list every rule.
type ApprovalRuleStore interface {
	CreateApprovalRule(ctx context.Context, r *ApprovalRule) error
	GetApprovalRule(ctx context.Context, id string) (*ApprovalRule, error)
	ListApprovalRules(ctx context.Context, surface string) ([]ApprovalRule, error)
	UpdateApprovalRule(ctx context.Context, r *ApprovalRule) error
	DeleteApprovalRule(ctx context.Context, id string) error
	// IncrementHitCount bumps hit_count and sets last_hit_at = hitAt for
	// the rule. Used by the matcher after a successful match.
	IncrementHitCount(ctx context.Context, id string, hitAt time.Time) error
}

// WorkerStore manages Worker configurations and the WorkerRun ledger
// (M0.1). Workers are scheduled in-process AI agents — the scheduler
// fires them on cron, the runner executes, output goes to the mesh.
//
// Sentinel errors: GetWorker / GetWorkerByName / UpdateWorker /
// DeleteWorker return ErrWorkerNotFound when the row is missing.
// GetWorkerRun / UpdateWorkerRunStatus return ErrWorkerRunNotFound.
// CreateWorker returns ErrAlreadyExists on the (workspace_id, name)
// unique constraint so the admin tool surfaces a clean "duplicate name"
// to the agent.
type WorkerStore interface {
	// ListWorkers returns every Worker in the workspace, ordered by
	// created_at DESC. When enabledOnly=true, only rows with Enabled=true
	// are returned — used by the scheduler at boot.
	ListWorkers(ctx context.Context, workspaceID string, enabledOnly bool) ([]*Worker, error)
	GetWorker(ctx context.Context, id string) (*Worker, error)
	GetWorkerByName(ctx context.Context, workspaceID, name string) (*Worker, error)
	CreateWorker(ctx context.Context, w *Worker) error
	UpdateWorker(ctx context.Context, w *Worker) error
	ListWorkerWorkspaceAccess(ctx context.Context, workerID string) ([]WorkerWorkspaceAccess, error)
	ReplaceWorkerWorkspaceAccess(ctx context.Context, workerID string, grants []WorkerWorkspaceAccess) error
	// DeleteWorker hard-deletes the row. M0 has no soft-delete — orphaned
	// WorkerRun rows are kept on purpose so the audit ledger survives a
	// Worker rename or recreate.
	DeleteWorker(ctx context.Context, id string) error

	CreateWorkerRun(ctx context.Context, r *WorkerRun) error
	// UpdateWorkerRunStatus commits the terminal-state snapshot for a
	// run. fin.FinishedAt is required; DurationMS is derived from the
	// run's StartedAt when set to zero. Returns ErrWorkerRunNotFound when
	// the run is missing.
	UpdateWorkerRunStatus(ctx context.Context, runID string, fin WorkerRunFinalize) error

	// ReapOrphanedRunningRuns marks every row still in status="running"
	// or status="dispatched" (excluding delegation workers) as
	// status="interrupted" with error="interrupted by daemon restart",
	// but ONLY rows whose started_at is before startedBefore.
	// Post-boot rows (started_at >= startedBefore) are left untouched
	// — those belong to the current daemon process. Called at daemon
	// startup.
	//
	// Safe semantics: a run that finalises cleanly stamps its terminal
	// state BEFORE the process exits, so a true survivor has already
	// transitioned out of "running"/"dispatched" by the time the next
	// process boots.
	ReapOrphanedRunningRuns(ctx context.Context, startedBefore, reasonNow time.Time) (int, error)
	// ListOrphanedDelegationRuns returns every status="running" row whose
	// parent worker is a delegation worker (name LIKE 'delegate-%'). These
	// are the rows the reaper SKIPS so ResumeOrphanedDelegations can
	// re-dispatch them after a daemon restart.
	ListOrphanedDelegationRuns(ctx context.Context) ([]*WorkerRun, error)
	GetWorkerRun(ctx context.Context, id string) (*WorkerRun, error)
	// ListWorkerRuns returns runs for workerID ordered by started_at DESC.
	// limit <= 0 falls back to an internal cap (matches the
	// DueScheduledJobs convention).
	ListWorkerRuns(ctx context.Context, workerID string, limit int) ([]*WorkerRun, error)
	// ListRecentWorkerRunsByWorkerIDs returns up to perWorker most-recent
	// runs (started_at DESC within each worker) for EVERY id in
	// workerIDs, in a single query. Backs ListDelegations' run hydration
	// without the historical List-then-Get-per-row N+1. Workers with no
	// runs are simply absent from the returned map. perWorker <= 0 falls
	// back to an internal cap.
	ListRecentWorkerRunsByWorkerIDs(ctx context.Context, workerIDs []string, perWorker int) (map[string][]*WorkerRun, error)
	// CountRunningWorkerRuns returns the number of rows for workerID with
	// Status="running". Backs the concurrency_policy check before the
	// scheduler starts a new run.
	CountRunningWorkerRuns(ctx context.Context, workerID string) (int, error)

	// SumCostThisMonth returns the sum of cost_usd over WorkerRun rows
	// for workerID with started_at >= the first of the current calendar
	// month in UTC. Backs the monthly-budget auto-pause check.
	SumCostThisMonth(ctx context.Context, workerID string, now time.Time) (float64, error)

	// LastFailureStatuses returns the Status of the last N WorkerRun
	// rows for workerID ordered by started_at DESC. Backs the
	// consecutive-failure auto-pause check (the caller scans for
	// all-"failure"). Excludes runs whose Status is still "running".
	LastFailureStatuses(ctx context.Context, workerID string, n int) ([]string, error)

	// CreateWorkerApproval inserts a new pending row.
	CreateWorkerApproval(ctx context.Context, a *WorkerApproval) error
	// GetWorkerApproval returns the row by id or ErrWorkerApprovalNotFound.
	GetWorkerApproval(ctx context.Context, id string) (*WorkerApproval, error)
	// ListWorkerApprovals returns approvals filtered by status. Empty
	// status returns every row ordered created_at DESC, capped at the
	// implementation's internal limit.
	ListWorkerApprovals(ctx context.Context, status string, limit int) ([]*WorkerApproval, error)
	// DecideWorkerApproval transitions a row from pending to one of
	// approved|rejected, recording the decider. resumedRunID may be
	// empty when no resume run was fired (rejection path). Returns
	// ErrWorkerApprovalNotFound when the row is missing or already
	// decided.
	DecideWorkerApproval(ctx context.Context, id, decision, decidedBy, resumedRunID string, decidedAt time.Time) error
	// CountPendingWorkerApprovals is the cheap dashboard-badge query.
	CountPendingWorkerApprovals(ctx context.Context) (int, error)

	// WorkerCostAggregate returns per-worker cost rollups for the cost
	// dashboard (M2). For each Worker in workspaceID (or every workspace
	// when workspaceID is empty), it returns:
	//   - DailyCosts: one row per UTC day from (now - days + 1) to now,
	//     inclusive. Days with no runs are 0.
	//   - MonthToDateUSD: SUM(cost_usd) for runs started on or after the
	//     first of the current calendar month in UTC.
	//   - RunCount30D: number of WorkerRun rows started in the last
	//     `days` days (regardless of status).
	// Workers with zero runs in the last `days` days are still included
	// so the UI can list them with $0 spend.
	WorkerCostAggregate(ctx context.Context, workspaceID string, days int, now time.Time) ([]WorkerCostAggregate, error)

	// PruneWorkerRuns deletes worker_runs older than `beforeCutoff`,
	// but always keeps the most recent `keepPerWorker` rows for each
	// worker so a low-volume worker doesn't lose its history just
	// because it hasn't fired in a while. Returns the number of rows
	// deleted. A keepPerWorker <= 0 disables the per-worker floor and
	// the cutoff acts as a hard age cap.
	PruneWorkerRuns(ctx context.Context, keepPerWorker int, beforeCutoff time.Time) (int64, error)

	// ReconcileOrphanedRuns flips rows stuck in status='running' past
	// `olderThan` into status='failure' with a uniform error message,
	// so a daemon restart or panicked goroutine doesn't leave the
	// ledger lying. finishedAt + duration_ms are derived from the row's
	// started_at relative to `now`. Returns the number of rows
	// reconciled. Idempotent: a follow-up call with the same args
	// returns 0 because the rows are no longer 'running'.
	ReconcileOrphanedRuns(ctx context.Context, olderThan, now time.Time, reason string) (int64, error)

	// CancelRun flips a single status='running' row to status='failure'
	// with the supplied error text. Returns ErrWorkerRunNotFound when
	// the row is missing AND ErrRunNotCancellable when the row is in a
	// terminal status (so callers can distinguish "already finished"
	// from "doesn't exist"). finishedAt + duration_ms are derived from
	// the row's started_at relative to `now`.
	CancelRun(ctx context.Context, runID string, now time.Time, reason string) error

	// WorkerMeshTrigger CRUD (M4). Triggers are CASCADE-deleted with
	// their worker by foreign key, so DeleteWorker handles the cleanup.

	// ListWorkerMeshTriggers returns every trigger for workerID ordered
	// by created_at ASC (stable for UI). Includes disabled rows so the
	// admin surface can render them with a toggle.
	ListWorkerMeshTriggers(ctx context.Context, workerID string) ([]*WorkerMeshTrigger, error)
	// ListAllEnabledMeshTriggers returns every enabled trigger row across
	// all workers. Backs the dispatcher's cache hydration at daemon boot
	// + after every CRUD invalidation.
	ListAllEnabledMeshTriggers(ctx context.Context) ([]*WorkerMeshTrigger, error)
	GetWorkerMeshTrigger(ctx context.Context, id string) (*WorkerMeshTrigger, error)
	CreateWorkerMeshTrigger(ctx context.Context, t *WorkerMeshTrigger) error
	UpdateWorkerMeshTrigger(ctx context.Context, t *WorkerMeshTrigger) error
	DeleteWorkerMeshTrigger(ctx context.Context, id string) error

	// HasPeerScope returns true when peerID has scope granted in its
	// p2p_peers.scopes JSON array. Returns false (no error) for unknown /
	// revoked peers — callers treat both as "no permission". Backs the
	// mesh-trigger cross-peer permission gate.
	HasPeerScope(ctx context.Context, peerID, scope string) (bool, error)

	// Secret offers (v0.13.0): peer→peer age-encrypted secret transfer.
	// Plaintext is NEVER stored here; only the age ciphertext blob.

	// InsertSecretOffer persists an in-flight offer row. Direction is
	// "inbound" (we received) or "outbound" (we sent).
	InsertSecretOffer(ctx context.Context, o *SecretOffer) error
	// GetSecretOffer returns one offer by ID. ErrNotFound if absent.
	GetSecretOffer(ctx context.Context, offerID string) (*SecretOffer, error)
	// ListPendingSecretOffers returns rows in pending status for the
	// given direction, newest first.
	ListPendingSecretOffers(ctx context.Context, direction string) ([]*SecretOffer, error)
	// DecideSecretOffer transitions an offer to a terminal status
	// ("accepted"|"rejected"|"expired"|"delivered"). savedAs is only
	// meaningful for inbound accept. ErrNotFound if missing or already decided.
	DecideSecretOffer(ctx context.Context, offerID, status string, decidedAt time.Time, savedAs string) error
	// ExpireOldSecretOffers transitions expired pending rows to "expired"
	// status. Returns the count of rows updated. Idempotent.
	ExpireOldSecretOffers(ctx context.Context, now time.Time) (int64, error)

	// Skill offers (mesh__push_skill): peer→peer registry-skill push.
	// Metadata only — the body + bundle are pulled on accept, never stored
	// in the offer row.

	// InsertSkillOffer persists an in-flight skill offer row. Direction is
	// "inbound" (we received) or "outbound" (we pushed).
	InsertSkillOffer(ctx context.Context, o *SkillOffer) error
	// GetSkillOffer returns one offer by ID. ErrNotFound if absent.
	GetSkillOffer(ctx context.Context, offerID string) (*SkillOffer, error)
	// ListPendingSkillOffers returns rows in pending status for the given
	// direction, newest first.
	ListPendingSkillOffers(ctx context.Context, direction string) ([]*SkillOffer, error)
	// DecideSkillOffer transitions an offer to a terminal status
	// ("accepted"|"rejected"|"expired"). publishedVersion is the local
	// registry version produced by an accept (0 otherwise). ErrNotFound if
	// missing or already decided.
	DecideSkillOffer(ctx context.Context, offerID, status string, decidedAt time.Time, publishedVersion int) error
	// ExpireOldSkillOffers transitions expired pending rows to "expired".
	// Returns the count of rows updated. Idempotent.
	ExpireOldSkillOffers(ctx context.Context, now time.Time) (int64, error)
}

// === Skill telemetry (W2) ===
//
// SkillRunStore records every invocation of a registry skill so the
// dashboard, refinement loop (W3), and composition graph (W6) all
// share one append-only signal of which skills are doing what. The
// runtime substrate is the new skill_runs table (migration 074);
// `task_epic_id` optionally cross-links to a task__create epic so a
// run's progress is also visible (and resumable) in the task UI.
//
// Append-only by intent: phase events accumulate in PhasesJSON rather
// than mutating in place so a restart/retry surfaces as friction data
// for the refinement loop rather than silent overwrites.
type SkillRunStore interface {
	// RecordSkillRun inserts a new row. ID defaults to a fresh ULID
	// when empty; StartedAt defaults to now(UTC) when zero; Outcome
	// defaults to "running" when empty. The struct is the canonical
	// shape so callers don't have to think about JSON envelope.
	RecordSkillRun(ctx context.Context, r *SkillRun) error

	// UpdateSkillRun applies a partial patch. Nil fields leave the
	// stored column unchanged; non-nil fields overwrite. CompletedAt
	// is the one timestamp the store stamps for the caller (when the
	// patch carries an Outcome that is terminal AND CompletedAt is
	// nil, the store stamps now(UTC) so a "set outcome=success" call
	// finishes the row in one round-trip).
	UpdateSkillRun(ctx context.Context, id string, patch SkillRunPatch) error

	// GetSkillRun returns one row by ID. ErrNotFound when missing.
	GetSkillRun(ctx context.Context, id string) (*SkillRun, error)

	// ListSkillRuns returns rows matching the filter, ordered by
	// started_at DESC. Default limit is 50 when filter.Limit is zero.
	ListSkillRuns(ctx context.Context, f SkillRunFilter) ([]SkillRun, error)
}

// === Skill refinement (W3) ===
//
// SkillRefinementStore is the persistence surface for refinement
// proposals — agent-authored suggestions that a particular skill
// version could be improved. The dashboard inbox + mesh-quorum
// aggregator are both layered on top of this. See migration 075 +
// internal/skillregistry/refinement_quorum.go for the canonical
// shape. Append-only by intent: a rejected proposal stays in the
// table for audit, and the quorum aggregator counts EVERY status —
// a once-rejected friction that resurfaces is still meaningful
// signal.
type SkillRefinementStore interface {
	// RecordRefinementProposal inserts a new row. ID defaults to a
	// fresh ULID when empty; CreatedAt defaults to now(UTC) when
	// zero; Status defaults to "pending"; MetadataJSON defaults to
	// "{}". Returns ErrAlreadyExists if a proposal with this ID
	// already exists (callers shouldn't be passing IDs — let the
	// store mint one).
	RecordRefinementProposal(ctx context.Context, p *SkillRefinementProposal) error

	// UpdateRefinementProposal applies a partial patch. Nil pointers
	// leave the column unchanged; non-nil overwrites. When Status
	// flips to a terminal value (promoted/rejected) AND ResolvedAt is
	// nil, the store stamps now(UTC) — keeps "approve this proposal"
	// to one round-trip.
	UpdateRefinementProposal(ctx context.Context, id string, patch RefinementProposalPatch) error

	// GetRefinementProposal returns one row by ID. ErrNotFound when
	// missing.
	GetRefinementProposal(ctx context.Context, id string) (*SkillRefinementProposal, error)

	// ListRefinementProposals returns rows matching the filter,
	// ordered by created_at DESC. Default limit 100, max 500.
	ListRefinementProposals(ctx context.Context, f RefinementFilter) ([]SkillRefinementProposal, error)

	// CountSimilarProposals returns the count of rows with the same
	// skill_name whose `friction` column contains `frictionSubstring`
	// (case-sensitive substring match — the quorum aggregator does
	// the lowercase+trim normalisation before calling). Powers the
	// mesh-quorum gate: when the count crosses the threshold, the
	// freshest matching proposal transitions to `candidate`. Counts
	// proposals in ANY status — a previously-rejected friction that
	// resurfaces is still signal worth surfacing.
	CountSimilarProposals(ctx context.Context, skillName, frictionSubstring string) (int, error)
}
