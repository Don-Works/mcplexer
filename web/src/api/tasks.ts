// tasks API client — backs /api/v1/tasks/* and the per-workspace
// status vocabulary surface (migration 061). Shapes mirror the Go
// store.Task / TaskNote / TaskStatusHistoryEntry / TaskStatusVocab
// rows.

import { request } from './client'

export type TaskPriority = 'low' | 'normal' | 'high' | 'critical' | string
export type TaskAssigneeOrigin = 'local' | 'peer' | 'human' | string
export type TaskSourceKind = 'user' | 'agent' | 'worker' | 'peer' | string

export interface Task {
  id: string
  workspace_id: string

  title: string
  description: string
  status: string
  closed_at?: string | null

  priority: TaskPriority
  due_at?: string | null

  tags?: string[] | null
  meta?: string

  assignee_session_id?: string
  assignee_origin_kind: TaskAssigneeOrigin
  assignee_peer_id?: string
  // assignee_user_id is set when the task is owned by a human (operator,
  // paired peer user, etc). It is the M7.1 user-store user id and stays
  // stable across device handovers — the dashboard renders it as a
  // distinct "human assignee" chip.
  assignee_user_id?: string
  assigned_by_session_id?: string
  assigned_by_peer_id?: string
  assigned_at?: string | null
  // lease_expires_at is set by the backend when status flips to "doing"
  // with an assignee; the assignee bumps it via heartbeatTask. Pre-071
  // rows are null — the UI falls back to updated_at as the staleness
  // proxy for those.
  lease_expires_at?: string | null

  source_kind: TaskSourceKind
  source_session_id?: string
  source_tool_call_id?: string
  created_by_session_id?: string
  updated_by_session_id?: string
  origin_peer_id?: string

  status_history?: TaskStatusHistoryEntry[] | null

  pinned?: boolean
  deleted_at?: string | null
  created_at: string
  updated_at: string
}

export interface TaskStatusHistoryEntry {
  at: string
  by_session?: string
  by_peer?: string
  evt: string
  from?: string
  to?: string
  note?: string
}

export interface TaskNote {
  id: string
  task_id: string
  author_session_id?: string
  author_kind: string
  body: string
  created_at: string
}

export interface TaskHistoryEntry {
  id: string
  task_id: string
  workspace_id: string
  revision: number
  action: string
  actor_kind?: string
  actor_session_id?: string
  actor_peer_id?: string
  actor_user_id?: string
  source_kind?: string
  source_session_id?: string
  source_tool_call_id?: string
  workspace_path?: string
  origin_peer_id?: string
  related_revision?: number
  changed_fields?: string[] | null
  note?: string
  before?: Task | null
  after?: Task | null
  created_at: string
}

// StatusKind — the canonical bucket a freeform status word maps to.
// Drives UI working-state chips + the service-layer auto-claim path.
// See migration 070 + internal/store/models.go TaskStatusVocab.
export type StatusKind = 'open' | 'working' | 'blocked' | 'review' | 'done' | 'cancelled'

export interface TaskStatusVocab {
  workspace_id: string
  status_text: string
  is_terminal: boolean
  kind?: StatusKind | string
  display_color?: string
  display_order: number
  managed_by: 'user' | 'skill' | 'system' | string
  updated_at: string
}

export interface TaskListFilter {
  workspace_id?: string
  status?: string
  state?: 'open' | 'closed' | 'all'
  tag?: string
  assignee_session_id?: string
  assignee_origin_kind?: TaskAssigneeOrigin
  assignee_peer_id?: string
  // assignee_user_id filters to tasks assigned to a specific human.
  // The REST querystring accepts the raw user id; combined with
  // assignee_origin_kind=human it is the canonical "tasks for me" view.
  assignee_user_id?: string
  origin_peer_id?: string
  q?: string
  limit?: number
  offset?: number
  updated_after?: string
}

function qs(params: Record<string, string | number | undefined | null>): string {
  const u = new URLSearchParams()
  for (const [k, v] of Object.entries(params)) {
    if (v === undefined || v === null || v === '') continue
    u.set(k, String(v))
  }
  const s = u.toString()
  return s ? `?${s}` : ''
}

export function listTasks(f: TaskListFilter = {}): Promise<Task[]> {
  return request<Task[]>(`/tasks${qs({ ...f } as Record<string, string | number | undefined | null>)}`)
}

export function getTask(workspaceId: string, id: string): Promise<Task> {
  return request<Task>(`/tasks/${encodeURIComponent(id)}${qs({ workspace_id: workspaceId })}`)
}

export interface CreateTaskBody {
  workspace_id: string
  title: string
  description?: string
  status?: string
  priority?: TaskPriority
  due_at?: string | null
  tags?: string[]
  meta?: string
  compose_into?: string
  assignee?: { session_id?: string; peer_id?: string; user_id?: string }
}

export function createTask(body: CreateTaskBody): Promise<Task> {
  return request<Task>('/tasks', {
    method: 'POST',
    body: JSON.stringify(body),
  })
}

export interface UpdateTaskBody {
  title?: string
  description?: string
  status?: string
  priority?: TaskPriority
  due_at?: string | null
  tags?: string[]
  meta?: string
  terminal?: boolean
  pinned?: boolean
  // assignee re-assigns the task in one shot — pass user_id for human
  // owners, session_id for local agents, peer_id (+ session_id) for
  // remote agents. The server rejects ambiguous mixes.
  assignee?: { session_id?: string; peer_id?: string; user_id?: string }
  clear?: Array<'assignee' | 'due_at' | 'meta' | 'description'>
}

export function updateTask(workspaceId: string, id: string, body: UpdateTaskBody): Promise<Task> {
  return request<Task>(`/tasks/${encodeURIComponent(id)}/update${qs({ workspace_id: workspaceId })}`, {
    method: 'POST',
    body: JSON.stringify(body),
  })
}

export interface ClaimTaskBody {
  session_id: string
  status?: string
  note?: string
}

export function claimTask(workspaceId: string, id: string, body: ClaimTaskBody): Promise<Task> {
  return request<Task>(`/tasks/${encodeURIComponent(id)}/claim${qs({ workspace_id: workspaceId })}`, {
    method: 'POST',
    body: JSON.stringify(body),
  })
}

// heartbeatTask bumps the row's lease window when the caller owns the
// lease. Silent no-op for non-assignees — always resolves to the
// canonical post-call row so the UI can reconcile the lease chip.
export function heartbeatTask(workspaceId: string, id: string, sessionId: string): Promise<Task> {
  return request<Task>(`/tasks/${encodeURIComponent(id)}/heartbeat${qs({ workspace_id: workspaceId })}`, {
    method: 'POST',
    body: JSON.stringify({ session_id: sessionId }),
  })
}

export function deleteTask(workspaceId: string, id: string): Promise<void> {
  return request<void>(`/tasks/${encodeURIComponent(id)}${qs({ workspace_id: workspaceId })}`, {
    method: 'DELETE',
  })
}

export function listTaskNotes(workspaceId: string, id: string, limit = 200): Promise<TaskNote[]> {
  return request<TaskNote[]>(`/tasks/${encodeURIComponent(id)}/notes${qs({ workspace_id: workspaceId, limit })}`)
}

export function appendTaskNote(
  workspaceId: string,
  id: string,
  body: { body: string; author_session_id?: string; author_kind?: string },
): Promise<TaskNote> {
  return request<TaskNote>(`/tasks/${encodeURIComponent(id)}/notes${qs({ workspace_id: workspaceId })}`, {
    method: 'POST',
    body: JSON.stringify(body),
  })
}

export function listTaskHistory(
  workspaceId: string,
  id: string,
  limit = 100,
): Promise<{ history: TaskHistoryEntry[] }> {
  return request<{ history: TaskHistoryEntry[] }>(
    `/tasks/${encodeURIComponent(id)}/history${qs({ workspace_id: workspaceId, limit })}`,
  )
}

export function rollbackTask(
  workspaceId: string,
  id: string,
  body: { revision: number; session_id?: string; actor_kind?: string; note?: string },
): Promise<Task> {
  return request<Task>(`/tasks/${encodeURIComponent(id)}/rollback${qs({ workspace_id: workspaceId })}`, {
    method: 'POST',
    body: JSON.stringify(body),
  })
}

export function countTasksByStatus(workspaceId: string): Promise<Record<string, number>> {
  return request<Record<string, number>>(`/tasks/count${qs({ workspace_id: workspaceId })}`)
}

export interface TaskStatusCount {
  status: string
  count: number
}

export function listTaskStatuses(
  f: Pick<TaskListFilter, 'workspace_id' | 'state'> = {},
): Promise<{ statuses: TaskStatusCount[] }> {
  return request<{ statuses: TaskStatusCount[] }>(
    `/tasks/statuses${qs({ workspace_id: f.workspace_id, state: f.state })}`,
  )
}

// MilestoneBurndown mirrors the Go store.MilestoneBurndown shape. A
// "milestone" is a task with tag=milestone + due_at set. The aggregate
// view rolls up its composed children + a per-day burndown series.
export interface MilestoneBurndown {
  task: Task
  total_children: number
  closed_children: number
  days_remaining: number
  burndown_points: BurndownPoint[]
}

export interface BurndownPoint {
  date: string // YYYY-MM-DD
  children_open: number
  children_closed: number
}

export function listMilestones(workspaceId: string): Promise<MilestoneBurndown[]> {
  return request<MilestoneBurndown[]>(`/tasks/milestones${qs({ workspace_id: workspaceId })}`)
}

export function listTaskVocab(workspaceId: string): Promise<TaskStatusVocab[]> {
  return request<TaskStatusVocab[]>(`/task-status-vocabulary${qs({ workspace_id: workspaceId })}`)
}

export function upsertTaskVocab(v: Omit<TaskStatusVocab, 'updated_at'>): Promise<TaskStatusVocab> {
  return request<TaskStatusVocab>('/task-status-vocabulary', {
    method: 'POST',
    body: JSON.stringify(v),
  })
}

// Cross-peer offers — mirrors store.TaskOffer. Preview-only payload;
// full task content transfers over libp2p when the user accepts.
export interface TaskOffer {
  id: string
  task_id?: string
  remote_task_id: string
  from_peer_id: string
  to_peer_id: string
  remote_workspace_id: string
  remote_workspace_name?: string
  workspace_id?: string

  title: string
  description_preview?: string
  meta_preview?: string
  status_preview?: string
  priority_preview?: string
  tags?: string[] | null

  is_direct_assign: boolean
  envelope_nonce: string
  envelope_created_at: string
  direction: 'incoming' | 'outgoing' | string
  state:
    | 'pending'
    | 'accepted'
    | 'declined'
    | 'expired'
    | 'auto_accepted'
    | 'rejected_throttle'
    | 'rejected_unscoped'
    | string
  accepted_at?: string | null
  declined_at?: string | null
  declined_reason?: string
  created_at: string
}

export interface TaskOfferFilter {
  direction?: 'incoming' | 'outgoing'
  state?: string
  peer?: string
  since?: string
  limit?: number
}

export function listTaskOffers(f: TaskOfferFilter = {}): Promise<TaskOffer[]> {
  return request<TaskOffer[]>(
    `/tasks/offers${qs({
      direction: f.direction,
      state: f.state,
      peer: f.peer,
      since: f.since,
      limit: f.limit,
    })}`,
  )
}

export interface CreateTaskOfferBody {
  workspace_id?: string
  task_id: string
  to_peer_id: string
  message?: string
  direct_assign?: boolean
}

export function createTaskOffer(body: CreateTaskOfferBody): Promise<TaskOffer> {
  return request<TaskOffer>('/tasks/offers', {
    method: 'POST',
    body: JSON.stringify(body),
  })
}

// acceptTaskOffer pulls the full task payload over libp2p, creates the
// local row, and returns it. workspaceId is required on the first
// offer from a (peer, remote_workspace) pair — the binding is
// memoized server-side after that.
export function acceptTaskOffer(id: string, workspaceId?: string): Promise<Task> {
  return request<Task>(`/tasks/offers/${encodeURIComponent(id)}/accept`, {
    method: 'POST',
    body: JSON.stringify({ workspace_id: workspaceId ?? '' }),
  })
}

export function declineTaskOffer(id: string, reason?: string): Promise<void> {
  return request<void>(`/tasks/offers/${encodeURIComponent(id)}/decline`, {
    method: 'POST',
    body: JSON.stringify({ reason: reason ?? '' }),
  })
}

// WorkContext is the typed view of the structured frontmatter slots
// the dashboard renders as chips on TaskDetail. Mirrors the Go
// internal/tasks/work_context.go shape exactly so JSON marshalling
// round-trips without translation.
export interface WorkContext {
  worktree?: string
  branch?: string
  pr?: string
  commits?: string
  peer?: string
  session?: string
  linear?: string
  mesh_thread?: string
}

// SetWorkContextBody — POST body shape. Each field is optional;
// passing an empty string for any key clears that line. `clear` is an
// alternate channel that names keys explicitly (useful for "reset
// branch but leave PR untouched" workflows).
export interface SetWorkContextBody extends WorkContext {
  session_id?: string
  clear?: Array<keyof WorkContext>
}

// setWorkContext writes structured pointers onto a task's meta column.
// Returns the post-mutation task.
export function setWorkContext(
  workspaceId: string,
  id: string,
  body: SetWorkContextBody,
): Promise<Task> {
  return request<Task>(
    `/tasks/${encodeURIComponent(id)}/work_context${qs({ workspace_id: workspaceId })}`,
    { method: 'POST', body: JSON.stringify(body) },
  )
}

// parseWorkContext extracts the typed view from a task's meta string —
// mirrors internal/tasks/work_context.go ParseWorkContext. Used by
// WorkContextCard to render chips without a round-trip. Silently
// skips fields that fail validation client-side (server is the source
// of truth for what's accepted on write).
//
// Reads each field through readMetaList so both meta shapes (canonical
// post-072 JSON and legacy frontmatter) resolve identically — taking
// the first value matches the backend's MetaGetScalar contract. Doing
// our own frontmatter line-split here would silently return an empty
// context for the JSON rows the backend writes today.
export function parseWorkContext(meta: string | undefined): WorkContext {
  const out: WorkContext = {}
  if (!meta) return out
  const keys: (keyof WorkContext)[] = [
    'worktree',
    'branch',
    'pr',
    'commits',
    'peer',
    'session',
    'linear',
    'mesh_thread',
  ]
  for (const key of keys) {
    const value = readMetaList(meta, key)[0]
    if (value) out[key] = value
  }
  return out
}

// TaskActivityRow mirrors the Go-side projection in
// internal/api/dashboard_activity_handler.go. Decoupled from Task so
// the dashboard tile gets exactly the fields it renders + the backend
// can grow per-tile shape without breaking unrelated consumers.
export interface TaskActivityRow {
  id: string
  workspace_id: string
  workspace_name: string
  title: string
  status: string
  priority: TaskPriority
  assignee_display: string
  updated_at: string
  last_event: string
}

export interface TaskActivityResponse {
  tasks: TaskActivityRow[]
}

// listRecentTasks fetches the dashboard's cross-workspace rolling task
// activity feed (recent open + updated, newest first). Default limit
// is server-side (20); pass an explicit limit to override.
export function listRecentTasks(limit?: number): Promise<TaskActivityResponse> {
  return request<TaskActivityResponse>(
    `/dashboard/activity/tasks${qs({ limit })}`,
  )
}

// readMetaList — mirrors internal/tasks/meta.go MetaListGet. The `meta`
// column is dual-shape while migration 072's backfill rolls forward:
// canonical rows are a JSON object (`{"key": "v"}` or `{"key": ["a","b"]}`),
// untouched legacy rows are frontmatter (`key: value, value, ...` lines).
// We must read both — the JSON path is what the backend writes today, so
// parsing only frontmatter here would make composes / composed_by invisible
// and collapse the composition tree to a flat list.
export function readMetaList(meta: string | undefined, key: string): string[] {
  if (!meta) return []
  // JSON objects always start with `{` after trimming (matches the
  // backend's MetaIsLegacyFrontmatter check). Coerce string | string[]
  // values to a string list; drop empties and non-string array members.
  if (meta.trimStart().startsWith('{')) {
    try {
      const obj = JSON.parse(meta) as Record<string, unknown>
      const v = obj?.[key]
      if (typeof v === 'string') return v ? [v] : []
      if (Array.isArray(v)) return v.filter((x): x is string => typeof x === 'string' && x !== '')
      return []
    } catch {
      return []
    }
  }
  const prefix = `${key}:`
  for (const line of meta.split('\n')) {
    const trim = line.trim()
    if (!trim.startsWith(prefix)) continue
    const body = trim.slice(prefix.length).trim()
    if (!body) return []
    return body
      .split(',')
      .map((v) => v.trim())
      .filter(Boolean)
  }
  return []
}
