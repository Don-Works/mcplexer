package gateway

import "encoding/json"

// taskToolDefinitions returns the universal task__* tools (migration
// 061). Surface shape follows the eight pre-decisions in
// .planning/tasks/REVIEW_NOTES.md.
//
// Synonyms in descriptions are intentional — agents searching
// `mcpx__search_tools` for "todo" / "issue" / "ticket" / "story" /
// "epic" / "followup" / "work item" / "action item" / "inbox" /
// "backlog" should find these on the first try.
func taskToolDefinitions() []Tool {
	return []Tool{
		{
			Name:        "task__create",
			Description: "Create a task — a todo / issue / ticket / story / epic / followup / work item / checklist item / action item — in the current workspace. Returns a compact post-write envelope `{ok: true, id, task: {id, title, status, workspace_id, updated_at, closed_at?, terminal}}` (≤120 tokens). Opt-in to the historical full body (status_history, notes, composed_by, composes, full description + meta) with `full: true`. `assignee` is a single string: `\"me\"` (you), `\"<agent_name>\"` (a local agent in this workspace), or `\"<peer_short>:<agent_name>\"` (a remote agent on a paired peer). Status is freeform — prefer `open`, `doing`, `blocked`, `review`, `done`, `cancelled` unless this workspace has established others (see `task__list` response's `known_statuses`). `meta` is your frontmatter: store structured info (reviewer, links, custom fields) AI consumers will see when reading this task. Declare `meta: {touches_files: [\"path/a.go\", ...]}` before starting work — when another in-progress task in this workspace has declared an overlapping path, working-status updates return non-blocking `coordination_warnings` so agents spot collisions before editing. `compose_into: <parent_id>` makes this task a child in an epic — the link is recorded BIDIRECTIONALLY (parent gets `composes:` line, this child gets `composed_by:` line). `compose_into` accepts: full 26-char ULID, an 8+ char unique prefix in this workspace (errors when ambiguous, listing candidates), or the literal `\"last\"` to resolve to the most-recent task this session created in this workspace.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"title": {"type": "string", "description": "Short imperative title."},
					"description": {"type": "string", "description": "Body / context. Markdown supported."},
					"status": {"type": "string", "description": "Freeform status; default 'open'."},
					"priority": {"type": "string", "description": "Freeform; suggested low|normal|high|critical."},
					"due_at": {"type": "string", "description": "RFC3339 timestamp."},
					"tags": {"type": "array", "items": {"type": "string"}},
					"meta": {"type": "string", "description": "Frontmatter-style structured info. Include touches_files: [paths] to opt in to coordination warnings against other in-progress tasks."},
					"assignee": {"type": "string", "description": "\"me\" | \"<agent_name>\" | \"<peer>:<agent_name>\" | \"user:<id>\" | \"user:self\""},
					"compose_into": {"type": "string", "description": "Parent task id; bidirectional link recorded. Accepts full ULID, an 8+ char unique prefix, or \"last\" (most-recent task this session created in this workspace)."},
					"full": {"type": "boolean", "description": "Return the historical full envelope (notes, composed_by, composes, status_history) instead of the compact post-write shape. Default: false."},
					"workspace_id": {"type": "string", "description": "Override session-bound workspace; useful for concierge-style routing across workspaces. Omit to use the session's CWD-bound workspace."}
				},
				"required": ["title"]
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "Create Task",
				ReadOnlyHint:    boolPtr(false),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(false),
				OpenWorldHint:   boolPtr(false),
			}),
		},
		{
			Name:        "task__list",
			Description: "List tasks (todos / open items / backlog / my work / what's on my plate / inbox / queue) in the current workspace, newest first. The response always carries `count` (0 = genuinely no matches, not a failure) and `task_view` (`\"preview\"` or `\"full\"`) alongside `tasks`. Preview rows by default: ids, title/status/tags/assignee/timestamps, bounded description/meta previews, and counts for hidden heavy fields. Use `task__get({id})` for one full task, or `full: true` to restore historical full list rows. Filter by `state` (default `\"open\"` — also accepts `\"closed\"` or `\"any\"`), `status` (exact match — use `known_statuses` in response to pick valid values), `tag`, `assignee` (single string — `\"me\"` or `\"<agent_name>\"` or `\"<peer>:<agent>\"`), `assigned_by` (same shape), `origin_peer_id`, `updated_after`, or pass `q` for FTS5 search across title + description + meta + tags + status. Pass `semantic:true` with `q` to TF-IDF-rank already scoped and filtered candidates (cap 500) instead of running FTS5. Structured `meta_*` filters (since migration 072): `meta_match: {key: value, ...}` — every (key, value) must match (AND); `meta_has_key: \"key\" | [\"key1\", \"key2\"]` — meta object contains the key regardless of value; `meta_in: {key: [v1, v2]}` — value at key is one of the listed. The response carries discovery metadata (`known_statuses`, `known_assignees`, `known_tags`, `known_meta_keys`) trimmed to what's relevant to the returned rows + active sessions, so you can self-correct toward established workspace vocabulary without extra round-trips.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"state": {"type": "string", "enum": ["open", "closed", "any"], "description": "Default 'open'."},
					"status": {"type": "string", "description": "Exact match; freeform vocabulary."},
					"tag": {"type": "string"},
					"assignee": {"type": "string", "description": "\"me\" | \"<agent_name>\" | \"<peer>:<agent_name>\" | \"user:<id>\" | \"user:self\""},
					"assigned_by": {"type": "string"},
					"q": {"type": "string", "description": "Search query. Defaults to FTS5; pass semantic:true for scoped TF-IDF ranking."},
					"semantic": {"type": "boolean", "description": "With q, rank already scoped/filter-matched candidates using cheap TF-IDF embedding search instead of FTS5. Default false."},
					"updated_after": {"type": "string", "description": "RFC3339 timestamp."},
					"origin_peer_id": {"type": "string"},
					"meta_match": {"type": "object", "description": "Map of meta-key to expected value; every entry must match (AND). Array-valued meta entries match by containment."},
					"meta_has_key": {"description": "Single key or array of keys; meta object must contain each one (regardless of value)."},
					"meta_in": {"type": "object", "description": "Map of meta-key to list of allowed values; value at each key must be one of the listed."},
					"workspace_id": {"type": "string", "description": "Override session-bound workspace; useful for concierge-style routing across workspaces. Omit to use the session's CWD-bound workspace."},
					"limit": {"type": "integer"},
					"offset": {"type": "integer"},
					"full": {"type": "boolean", "description": "Return historical full task rows with complete description/meta/status_history. Default: false; prefer task__get({id}) for one full row."}
				}
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "List Tasks",
				ReadOnlyHint:    boolPtr(true),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(true),
				OpenWorldHint:   boolPtr(false),
			}),
		},
		{
			Name:        "task__get",
			Description: "Fetch one task by id. Response carries the task, its notes, derived `composed_by` (parent epics) + `composes` (child tasks), plus a slim `known_assignees` directory (the row already carries its own status + tags, so the workspace status/tag vocab is omitted on single-row reads to save tokens — call `task__list` when you need it).",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"id": {"type": "string"},
					"workspace_id": {"type": "string", "description": "Override session-bound workspace; useful for concierge-style routing across workspaces. Omit to use the session's CWD-bound workspace."}
				},
				"required": ["id"]
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "Get Task",
				ReadOnlyHint:    boolPtr(true),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(true),
				OpenWorldHint:   boolPtr(false),
			}),
		},
		{
			Name:        "task__update",
			Description: "Update one task (`id: <string>`) or many tasks atomically per-row (`ids: [<string>, ...]` — the bulk form returns `{ok: [...], failed: [{id, error}]}`). Returns a compact post-write envelope `{ok: true, id, task: {id, title, status, workspace_id, updated_at, closed_at?, terminal}, coordination_warnings?}` (≤120 tokens). `coordination_warnings` fires when this task declares `meta.touches_files` and another in-progress task in the workspace declared an overlapping path — non-blocking signal; coordinate via mesh__send before editing. Opt-in to the historical full body (status_history, notes, composed_by, composes) with `full: true`. Omitted fields stay unchanged; explicit `null` for `assignee`, `due_at`, `meta`, or `description` clears the field — no separate `clear` parameter needed. Set `terminal: true` to close the task (stamps closed_at, learns the status into the workspace's terminal vocabulary). Status changes append to the task's row-local history (durable independent of mesh retention).",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"id": {"type": "string", "description": "Single-task form."},
					"ids": {"type": "array", "items": {"type": "string"}, "description": "Bulk form (returns ok+failed arrays)."},
					"title": {"type": "string"},
					"description": {"description": "string or null to clear."},
					"status": {"type": "string"},
					"priority": {"type": "string"},
					"due_at": {"description": "RFC3339 string or null to clear."},
					"tags": {"type": "array", "items": {"type": "string"}},
					"meta": {"description": "string or null to clear."},
					"assignee": {"description": "\"me\" | \"<agent>\" | \"<peer>:<agent>\" | null to unassign"},
					"terminal": {"type": "boolean", "description": "true closes + learns terminal vocab; false reopens."},
					"pinned": {"type": "boolean"},
					"full": {"type": "boolean", "description": "Return the historical full envelope instead of the compact post-write shape. Default: false."},
					"workspace_id": {"type": "string", "description": "Override session-bound workspace; useful for concierge-style routing across workspaces. Omit to use the session's CWD-bound workspace."}
				}
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "Update Task",
				ReadOnlyHint:    boolPtr(false),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(false),
				OpenWorldHint:   boolPtr(false),
			}),
		},
		{
			Name:        "task__assign",
			Description: "Assign a task to an agent (the common shortcut — equivalent to task__update with just the assignee patch). `assignee` is a single string: `\"me\"` | `\"<agent_name>\"` | `\"<peer>:<agent>\"` | null to unassign. Returns the compact post-write shape; pass `full: true` for the historical envelope.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"id": {"type": "string"},
					"assignee": {"description": "\"me\" | \"<agent>\" | \"<peer>:<agent>\" | null"},
					"full": {"type": "boolean", "description": "Return the historical full envelope. Default: false."}
				},
				"required": ["id", "assignee"]
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "Assign Task",
				ReadOnlyHint:    boolPtr(false),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(false),
				OpenWorldHint:   boolPtr(false),
			}),
		},
		{
			Name:        "task__claim",
			Description: "Pick up / take on / start a task — atomic assign-to-me + status flip in one call. The happy path when a workspace-broadcast task lands and the first session to claim wins. `status` defaults to `\"doing\"`; pass `note` to record context for teammates. Returns the compact post-claim shape `{ok: true, id, task: {...}}` — pass `full: true` for the historical envelope. If another session already claimed it, fails with a clear message — no silent override.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"id": {"type": "string"},
					"status": {"type": "string", "description": "New status; default 'doing'."},
					"note": {"type": "string", "description": "Optional context appended as a note."},
					"full": {"type": "boolean", "description": "Return the historical full envelope. Default: false."}
				},
				"required": ["id"]
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "Claim Task",
				ReadOnlyHint:    boolPtr(false),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(false),
				OpenWorldHint:   boolPtr(false),
			}),
		},
		{
			Name:        "task__heartbeat",
			Description: "Heartbeat / liveness ping / keep-alive for a task — bumps the lease window on a task you currently own (status=doing, assigned to your session). Silent no-op when you're not the current assignee, so it's safe to call defensively (e.g. every 60s while you're working a row). Without periodic heartbeats, a status=doing row whose owner has been silent for 5 minutes is treated as abandoned: the background sweep clears the assignee + appends an evt=lease_expired entry to the row's status_history. Returns the compact post-bump shape; pass `full: true` for the historical envelope.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"id": {"type": "string"},
					"full": {"type": "boolean", "description": "Return the historical full envelope. Default: false."}
				},
				"required": ["id"]
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "Heartbeat Task",
				ReadOnlyHint:    boolPtr(false),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(true),
				OpenWorldHint:   boolPtr(false),
			}),
		},
		{
			Name:        "task__delete",
			Description: "Soft-delete a task. The row stays in audit history; default queries exclude it. Use this for genuine deletion, NOT for closing — closing means task__update(id, terminal: true).",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {"id": {"type": "string"}},
				"required": ["id"]
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "Delete Task",
				ReadOnlyHint:    boolPtr(false),
				DestructiveHint: boolPtr(true),
				IdempotentHint:  boolPtr(true),
				OpenWorldHint:   boolPtr(false),
			}),
		},
		{
			Name:        "task__list_milestones",
			Description: "List milestones (deadlines / target dates / release goals / sprint goals) in the current workspace with burndown rollups, ordered by due date. A milestone is convention-only: any task tagged `milestone` with `due_at` set — typically an epic with children composed in. Each row: the milestone task, total/closed children counts, signed days_remaining (negative = overdue), and a per-day burndown series from created_at to due_at. Returns `{milestones: [...], count}` — empty array means no milestone-tagged tasks, not a failure. Create one with task__create({tags:['milestone'], due_at:..., title:...}) and compose work in with compose_into.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"workspace_id": {"type": "string", "description": "Override session-bound workspace. Omit to use the session's CWD-bound workspace."}
				}
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "List Milestones",
				ReadOnlyHint:    boolPtr(true),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(true),
				OpenWorldHint:   boolPtr(false),
			}),
		},
		{
			Name:        "task__append_note",
			Description: "Append a comment / log entry / update / journal entry to a task — race-free, so multiple agents can comment on the same task without overwriting each other (each note is its own row). Use for inline updates, questions, status comments. The task's description is for the canonical statement; notes are the conversation.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"id": {"type": "string", "description": "Task id."},
					"body": {"type": "string", "description": "Note text. ('note' is accepted as an alias.)"},
					"workspace_id": {"type": "string", "description": "Override session-bound workspace; useful for concierge-style routing across workspaces. Omit to use the session's CWD-bound workspace."}
				},
				"required": ["id", "body"]
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "Append Task Note",
				ReadOnlyHint:    boolPtr(false),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(false),
				OpenWorldHint:   boolPtr(false),
			}),
		},
		{
			Name:        "task__history",
			Description: "List the append-only edit/action history for a task. Default response is compact: revision, action, actor/session, workspace_path, changed_fields, note, and created_at. Pass `full:true` to include before/after task snapshots for forensic inspection. Use the `revision` value with task__rollback to restore a previous state.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"id": {"type": "string", "description": "Task id."},
					"limit": {"type": "integer", "description": "Default 100, max 500."},
					"full": {"type": "boolean", "description": "Include before/after snapshots. Default false."},
					"workspace_id": {"type": "string", "description": "Override session-bound workspace; useful for concierge-style routing across workspaces. Omit to use the session's CWD-bound workspace."}
				},
				"required": ["id"]
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "Task History",
				ReadOnlyHint:    boolPtr(true),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(true),
				OpenWorldHint:   boolPtr(false),
			}),
		},
		{
			Name:        "task__rollback",
			Description: "Restore a task to the after-snapshot of a specific history revision. This intentionally overwrites the current task row and then records a new rollback history entry, so the rollback itself can be undone by restoring the prior revision. Run task__history first and pass the chosen `revision`.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"id": {"type": "string", "description": "Task id."},
					"revision": {"type": "integer", "description": "History revision to restore to."},
					"note": {"type": "string", "description": "Optional reason recorded on the rollback history entry."},
					"full": {"type": "boolean", "description": "Return the historical full envelope. Default false."},
					"workspace_id": {"type": "string", "description": "Override session-bound workspace; useful for concierge-style routing across workspaces. Omit to use the session's CWD-bound workspace."}
				},
				"required": ["id", "revision"]
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "Rollback Task",
				ReadOnlyHint:    boolPtr(false),
				DestructiveHint: boolPtr(true),
				IdempotentHint:  boolPtr(false),
				OpenWorldHint:   boolPtr(false),
			}),
		},
		{
			Name:        "task__set_work_context",
			Description: "Annotate a task with structured work-context pointers — git worktree, branch, PR url, commit range, peer id, session, linear ticket, mesh thread root. Stored as frontmatter on the task's `meta` column so non-work-context lines (composes, composed_by, custom keys) are preserved. Each field is optional; an empty string clears that key. Use this so agents coordinating on parallel branches can find each other's work without grepping mesh chatter.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"id": {"type": "string", "description": "Task id."},
					"worktree": {"type": "string", "description": "Absolute git worktree path (empty to clear)."},
					"branch": {"type": "string", "description": "Git branch name (empty to clear)."},
					"pr": {"type": "string", "description": "Pull request URL — must be http(s) (empty to clear)."},
					"commits": {"type": "string", "description": "Commit range sha..sha (at least 7 hex chars each; empty to clear)."},
					"peer": {"type": "string", "description": "libp2p peer id (46-52 chars) (empty to clear)."},
					"session": {"type": "string", "description": "Free-form session id (empty to clear)."},
					"linear": {"type": "string", "description": "Linear ticket id e.g. ENG-123 (empty to clear)."},
					"mesh_thread": {"type": "string", "description": "Mesh thread root id (empty to clear)."}
				},
				"required": ["id"]
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "Set Task Work Context",
				ReadOnlyHint:    boolPtr(false),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(true),
				OpenWorldHint:   boolPtr(false),
			}),
		},
		{
			Name:        "task__offer",
			Description: "Share / suggest / delegate / hand off / send-to-peer a task to a paired libp2p peer. Sends a thin preview (title + ≤256-char description/meta previews + tags); the receiving daemon stores it as a pending offer their user/agent can accept or decline. Requires the receiving peer has granted you `task_offer:<workspace>` scope (or wildcard). Use `to` to address the peer by device name (\"alice-laptop\") or full peer id. Optional `message` is a short note shown alongside the offer.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"to": {"type": "string", "description": "Peer device name or libp2p peer id."},
					"task_id": {"type": "string", "description": "Local task id to offer."},
					"message": {"type": "string", "description": "Optional cover-note shown with the offer."}
				},
				"required": ["to", "task_id"]
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "Offer Task to Peer",
				ReadOnlyHint:    boolPtr(false),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(false),
				OpenWorldHint:   boolPtr(true),
			}),
		},
		{
			Name:        "task__assign_remote",
			Description: "Directly assign / push / fast-path a task to a paired peer's workspace, skipping the accept-review step. Higher-trust variant of `task__offer` — requires the receiving peer has granted you `task_assign:<workspace>` scope, throttled at 60 envelopes / 60s per (peer, workspace). The task lands on the peer with `notify_user=true` so the receiver knows. Use only for explicit hand-offs to teammates / contractors / scheduled-agents you both trust.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"to": {"type": "string", "description": "Peer device name or libp2p peer id."},
					"task_id": {"type": "string", "description": "Local task id to direct-assign."},
					"message": {"type": "string", "description": "Optional cover-note."}
				},
				"required": ["to", "task_id"]
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "Assign Task to Peer",
				ReadOnlyHint:    boolPtr(false),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(false),
				OpenWorldHint:   boolPtr(true),
			}),
		},
		{
			Name:        "task__accept_offer",
			Description: "Accept an incoming task offer / suggested task / delegated work from a paired peer. Pulls the full task body, creates a local task with `origin_peer_id` populated, and memoizes the workspace binding so future offers from the same peer/workspace land in the same local workspace automatically. The first offer from a new peer/workspace pair may require `workspace` (a local workspace name or id) — subsequent accepts can omit it.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"offer_id": {"type": "string", "description": "task_offers.id (incoming, state=pending)."},
					"workspace": {"type": "string", "description": "Local workspace name or id; required only on first offer from this peer/workspace."}
				},
				"required": ["offer_id"]
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "Accept Task Offer",
				ReadOnlyHint:    boolPtr(false),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(false),
				OpenWorldHint:   boolPtr(true),
			}),
		},
		{
			Name:        "task__decline_offer",
			Description: "Decline / reject an incoming task offer. The offer row stays in audit history with state=declined and the optional reason; the offering peer is NOT notified (declines are private to the receiving daemon).",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"offer_id": {"type": "string"},
					"reason": {"type": "string", "description": "Optional reason recorded in audit."}
				},
				"required": ["offer_id"]
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "Decline Task Offer",
				ReadOnlyHint:    boolPtr(false),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(true),
				OpenWorldHint:   boolPtr(false),
			}),
		},
		{
			Name:        "task_status_vocabulary__upsert",
			Description: "Declare / classify / register a freeform status word in the current workspace's vocabulary. Status is freeform per workspace — `doing`, `coding`, `triaging`, `paused`, `awaiting_review`, `shipped`, etc. — but the UI affordances (working-status timers, abandoned-lease banner) and the auto-claim service logic need a small canonical bucket vocabulary. `kind` is one of `open | working | blocked | review | done | cancelled`. Set `working` on any status that means \"an agent is actively driving this\"; `review` for awaiting verification / signoff (not working — no lease — and not terminal); `blocked` for waiting / paused; `done` and `cancelled` are terminal (auto-stamps closed_at on tasks moving to that status). `is_terminal` defaults to true for `done` / `cancelled` kinds when omitted. Idempotent — re-upserting the same row updates the existing entry.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"status_text": {"type": "string", "description": "The freeform status word the agents are using on tasks (e.g. \"triaging\", \"coding\", \"awaiting_review\")."},
					"kind": {"type": "string", "enum": ["open", "working", "blocked", "review", "done", "cancelled"], "description": "Bucket. Drives UI working-status chips + auto-claim. Default 'open'."},
					"is_terminal": {"type": "boolean", "description": "If true, tasks transitioning to this status get closed_at stamped automatically. Default true for kind=done|cancelled."},
					"display_color": {"type": "string", "description": "Optional tailwind / hex token used by the dashboard chip."},
					"display_order": {"type": "integer", "description": "Sort key in the workspace status picker."}
				},
				"required": ["status_text"]
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "Declare Task Status",
				ReadOnlyHint:    boolPtr(false),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(true),
				OpenWorldHint:   boolPtr(false),
			}),
		},
		{
			Name:        "task__compose",
			Description: "Compose / link / nest / group / organise tasks into an epic AFTER they've been created. Use when you discover that two existing tasks belong in a parent/child relationship — you've already filed them flat and now want them under an epic without re-creating. Bidirectional: parent gets `composes:` updated AND child gets `composed_by:` updated in one call. Pass `child_id` for a single link or `child_ids: [...]` for bulk (returns `{ok, failed}` shape — mirrors task__update). Idempotent: calling twice is safe; the second call is a no-op. Cross-workspace links are refused. Stamps `composed` on the parent's status_history when a real link is added.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"parent_id": {"type": "string", "description": "Parent task id (the epic)."},
					"child_id": {"type": "string", "description": "Single child task id."},
					"child_ids": {"type": "array", "items": {"type": "string"}, "description": "Bulk form — returns {ok, failed} shape."}
				},
				"required": ["parent_id"]
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "Compose Tasks",
				ReadOnlyHint:    boolPtr(false),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(true),
				OpenWorldHint:   boolPtr(false),
			}),
		},
		{
			Name:        "task__decompose",
			Description: "Decompose / unlink / unnest / unparent / disconnect a child task from its epic. Mirror of task__compose — removes `child_id` from parent.meta.composes AND removes `parent_id` from child.meta.composed_by in one call. Idempotent: decomposing what was never composed is a safe no-op, no error. Cross-workspace refused. Stamps `decomposed` on the parent's status_history when a real removal happened. Neither task is deleted — only the link between them.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"parent_id": {"type": "string", "description": "Parent task id."},
					"child_id": {"type": "string", "description": "Child task id to detach from the parent."}
				},
				"required": ["parent_id", "child_id"]
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "Decompose Tasks",
				ReadOnlyHint:    boolPtr(false),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(true),
				OpenWorldHint:   boolPtr(false),
			}),
		},
		{
			Name:        "task__list_offers",
			Description: "List task offers (incoming / outgoing / pending / accepted / declined). Defaults to `state: \"pending\"` (the actionable set), most-recent 100 across both directions — pass `state: \"any\"` for the full history. Filter by `direction` (\"incoming\"|\"outgoing\"), `state` (\"pending\"|\"accepted\"|\"declined\"|\"auto_accepted\"|\"rejected_throttle\"|\"rejected_unscoped\"|\"expired\"|\"any\"), `peer` (libp2p peer id or device name — matches sender OR recipient), `since` (RFC3339 — only offers created at-or-after this), `limit`. The response carries `expired_count` — pending offers are auto-expired by a TTL sweep (outgoing > 7d, incoming > 24h).",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"direction": {"type": "string", "enum": ["incoming", "outgoing"]},
					"state": {"type": "string"},
					"peer": {"type": "string", "description": "Peer device name or libp2p peer id."},
					"since": {"type": "string", "description": "RFC3339 timestamp."},
					"limit": {"type": "integer"}
				}
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "List Task Offers",
				ReadOnlyHint:    boolPtr(true),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(true),
				OpenWorldHint:   boolPtr(false),
			}),
		},
		{
			Name:        "task__attach",
			Description: "Attach a file / blob / artifact / screenshot / log / document to a task. The bytes are content-addressed by sha256 under the daemon's data directory; duplicate uploads within the same task dedupe automatically. Pass `content_base64` (preferred — base64-encoded bytes) or `bytes_inline` (raw UTF-8 string for small text payloads). The 5 MiB inline cap protects the JSON envelope; for anything larger, REST streaming endpoints land in a follow-up. Returns the new attachment row (id, sha256, size_bytes, mime_type, filename). Set `mime_type` explicitly when uploading non-text — otherwise it defaults to `application/octet-stream`. Use `filename` for the human-readable label shown in the UI.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"task_id": {"type": "string", "description": "Task id this attachment belongs to."},
					"filename": {"type": "string", "description": "Display name; sanitized (no path separators)."},
					"mime_type": {"type": "string", "description": "MIME type; default 'application/octet-stream'."},
					"content_base64": {"type": "string", "description": "Base64-encoded bytes — preferred for binary."},
					"bytes_inline": {"type": "string", "description": "Raw string body — convenience for plain text. Mutually exclusive with content_base64."},
					"workspace_id": {"type": "string", "description": "Override session-bound workspace. Omit to use the session's CWD-bound workspace."}
				},
				"required": ["task_id"]
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "Attach File to Task",
				ReadOnlyHint:    boolPtr(false),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(false),
				OpenWorldHint:   boolPtr(false),
			}),
		},
		{
			Name:        "task__list_attachments",
			Description: "List attachments / files / artifacts / blobs / uploads on a task — slim index only (id, filename, mime_type, size_bytes, sha256, created_at, uploader_session_id, uploader_kind). Does NOT return file bodies; call `task__get_attachment` with the id to fetch the content. Newest first.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"task_id": {"type": "string"},
					"workspace_id": {"type": "string", "description": "Override session-bound workspace. Omit to use the session's CWD-bound workspace."}
				},
				"required": ["task_id"]
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "List Task Attachments",
				ReadOnlyHint:    boolPtr(true),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(true),
				OpenWorldHint:   boolPtr(false),
			}),
		},
		{
			Name:        "task__get_attachment",
			Description: "Fetch one task attachment by id — returns the full row plus `content_base64` (the file bytes, base64-encoded). Capped at 5 MiB inline; larger blobs fail with a hint pointing at the future REST streaming endpoint (C2.3). Use `task__list_attachments` to discover ids without paying the body-transfer cost.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"id": {"type": "string", "description": "Attachment id."},
					"workspace_id": {"type": "string", "description": "Override session-bound workspace. Omit to use the session's CWD-bound workspace."}
				},
				"required": ["id"]
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "Get Task Attachment",
				ReadOnlyHint:    boolPtr(true),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(true),
				OpenWorldHint:   boolPtr(false),
			}),
		},
		{
			Name:        "task__recent_activity",
			Description: "Per-workspace 'what just happened here' feed — chronological status transitions across all tasks in the workspace, newest first. Quieter than `mesh__receive` (which fires every TASK_EVENT across every paired peer and every workspace); use this to catch up on your own workspace without the firehose. Each full entry: `{at, task_id, task_title, status, evt, from, to, by_session, by_peer, note}`. Pass `dedupe:true` for bounded lexical clusters plus representative task ids instead of full rows; hydrate with `task__recent_activity({dedupe:false,...})` or `task__get({id})`. Events: status_changed | assigned | created | composed | closed | lease_expired. Pass `workspace_id` to inspect another workspace.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"since": {"type": "string", "description": "RFC3339 timestamp; default = now-1h."},
					"limit": {"type": "integer", "description": "Default 50, max 500."},
					"dedupe": {"type": "boolean", "description": "Return bounded lexical clusters and representative task ids instead of full activity rows. Default false."},
					"workspace_id": {"type": "string", "description": "Override session-bound workspace. Omit to use the session's CWD-bound workspace."}
				}
			}`),
			Extras: withAnnotations(ToolAnnotations{
				Title:           "Recent Workspace Activity",
				ReadOnlyHint:    boolPtr(true),
				DestructiveHint: boolPtr(false),
				IdempotentHint:  boolPtr(true),
				OpenWorldHint:   boolPtr(false),
			}),
		},
	}
}
