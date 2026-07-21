import { request, apiURL } from './client'

// Mirrors brain.TreeNode (Go): one workspace in the browser rail with its
// parent (client/org tier) and live record counts.
export interface BrainTreeNode {
  workspace: string
  parent_id?: string
  display_name: string
  task_count: number
  memory_count: number
}

// Mirrors brain.ClientNode (Go): the client/org tier (a parent workspace).
export interface BrainClientNode {
  id: string
  display_name: string
  workspace_count: number
}

// Mirrors brain.WorkspaceNode (Go): a child workspace under a client, with
// its index source + ancestor chain for the scope picker.
export interface BrainWorkspaceNode {
  id: string
  display_name: string
  parent_id?: string
  source: string // central | repo
  chain: string[]
  task_count: number
  memory_count: number
}

// Mirrors brain.SourceRecord / AssigneeRecord (Go) — read-only provenance.
export interface BrainSource {
  kind?: string
  session_id?: string
}
export interface BrainAssignee {
  origin_kind?: string
  session_id?: string
  peer_id?: string
}

// Mirrors brain.TaskRecord (Go): the GUI-facing editable projection of a
// task — structured frontmatter fields + the prose description body — plus
// the read-side flags (live_lease, validation_*) and CAS token (on_disk_hash).
export interface BrainTaskRecord {
  id: string
  workspace: string
  title: string
  status: string
  priority?: string
  tags: string[]
  due_at?: string
  pinned: boolean
  description: string
  assignee?: BrainAssignee
  composes?: string[]
  source?: BrainSource
  path?: string
  index_source?: string
  created_at?: string
  updated_at?: string
  live_lease?: boolean
  validation_error?: string
  validation_field?: string
  on_disk_hash?: string
  raw?: string
  // if_hash is the CAS token submitted on a PUT (set to on_disk_hash at load).
  if_hash?: string
}

// Mirrors brain.EntityLinkFM (Go).
export interface BrainEntityLink {
  kind: string
  id: string
  role?: string
}

// Mirrors brain.MemoryRecord (Go).
export interface BrainMemoryRecord {
  id: string
  kind: string // note | fact
  name: string
  workspace?: string
  tags: string[]
  pinned: boolean
  content: string
  entities?: BrainEntityLink[]
  t_valid_start?: string
  source?: BrainSource
  path?: string
  index_source?: string
  created_at?: string
  updated_at?: string
  validation_error?: string
  validation_field?: string
  on_disk_hash?: string
  raw?: string
  if_hash?: string
}

export type BrainRecordKind = 'task' | 'memory'

// Mirrors brain.SearchHit / SearchResult (Go): the shared intellisense shape
// behind cmd+K and every in-field typeahead.
export interface BrainSearchHit {
  kind: string
  id: string
  title: string
  status?: string
  workspace?: string
  tags?: string[]
  score: number
  tier: number // 0 exact-prefix, 1 token, 2 fuzzy
}
export interface BrainSearchResult {
  hits: BrainSearchHit[]
  fuzzy_off: boolean
  create_label: string
}

// Mirrors brain.ConflictDetail (Go): the 409 reconciler payload — the fresh
// on-disk record + the named writer + the raw .md escape hatch.
export interface BrainConflictDetail {
  on_disk_task?: BrainTaskRecord
  on_disk_memory?: BrainMemoryRecord
  writer: string
  path: string
  on_disk_hash: string
}

export function getBrainTree(): Promise<BrainTreeNode[]> {
  return request('/brain/tree')
}

export function getBrainClients(): Promise<BrainClientNode[]> {
  return request('/brain/clients')
}

export function getBrainWorkspaces(client?: string): Promise<BrainWorkspaceNode[]> {
  const q = client ? `?client=${encodeURIComponent(client)}` : ''
  return request(`/brain/workspaces${q}`)
}

export function getBrainScope(workspace: string): Promise<{ scope: string }> {
  return request(`/brain/scope?workspace=${encodeURIComponent(workspace)}`)
}

export function listBrainTasks(ws: string): Promise<BrainTaskRecord[]> {
  return request(`/brain/workspaces/${encodeURIComponent(ws)}/tasks`)
}

export function listBrainMemories(ws: string): Promise<BrainMemoryRecord[]> {
  return request(`/brain/workspaces/${encodeURIComponent(ws)}/memory`)
}

// listBrainRecords hits the enriched /brain/records endpoint — the typed list
// the Ledger Console renders. Unlike listBrainTasks/Memories it carries the
// status + source filters and returns rows enriched with live_lease (shimmer)
// and validation_error (pulse-slow) so the list can mark alive / flagged rows
// without a second call. kind is task|memory (note maps to memory).
export function listBrainTaskRecords(
  ws: string,
  opts?: { status?: string; source?: string },
): Promise<BrainTaskRecord[]> {
  const p = new URLSearchParams({ workspace: ws, kind: 'task' })
  if (opts?.status) p.set('status', opts.status)
  if (opts?.source) p.set('source', opts.source)
  return request(`/brain/records?${p.toString()}`)
}

export function listBrainMemoryRecords(
  ws: string,
  opts?: { source?: string; memoryKind?: 'note' | 'fact' },
): Promise<BrainMemoryRecord[]> {
  const p = new URLSearchParams({ workspace: ws, kind: 'memory' })
  if (opts?.source) p.set('source', opts.source)
  if (opts?.memoryKind) p.set('memory_kind', opts.memoryKind)
  return request(`/brain/records?${p.toString()}`)
}

export function getBrainTask(id: string): Promise<BrainTaskRecord> {
  return request(`/brain/record/task/${encodeURIComponent(id)}`)
}

export function getBrainMemory(id: string): Promise<BrainMemoryRecord> {
  return request(`/brain/record/memory/${encodeURIComponent(id)}`)
}

// searchBrain powers cmd+K + every in-field typeahead: one frecency-ranked,
// three-tier search over the FTS5 index with the scale-cliff fuzzy fallback.
export function searchBrain(
  q: string,
  opts?: { kind?: string; workspace?: string; limit?: number },
): Promise<BrainSearchResult> {
  const p = new URLSearchParams({ q })
  if (opts?.kind) p.set('kind', opts.kind)
  if (opts?.workspace) p.set('workspace', opts.workspace)
  if (opts?.limit) p.set('limit', String(opts.limit))
  return request(`/brain/search?${p.toString()}`)
}

// saveBrainTask creates (no id) or updates (with id) a task record. The
// write funnels through the same serializer + hash-CAS the agent/VSCode
// paths use. A 409 (CAS conflict) or 422 (validation) is surfaced to the
// caller as an ApiClientError whose body is the JSON detail.
export function saveBrainTask(rec: BrainTaskRecord): Promise<BrainTaskRecord> {
  if (rec.id) {
    return request(`/brain/record/task/${encodeURIComponent(rec.id)}`, {
      method: 'PUT',
      body: JSON.stringify(rec),
    })
  }
  return request('/brain/record/task', {
    method: 'POST',
    body: JSON.stringify(rec),
  })
}

export function saveBrainMemory(rec: BrainMemoryRecord): Promise<BrainMemoryRecord> {
  if (rec.id) {
    return request(`/brain/record/memory/${encodeURIComponent(rec.id)}`, {
      method: 'PUT',
      body: JSON.stringify(rec),
    })
  }
  return request('/brain/record/memory', {
    method: 'POST',
    body: JSON.stringify(rec),
  })
}

// createBrainStub creates a real stub record .md (with a generated ULID) for
// the create-on-miss typeahead path (DESIGN §4.1): referencing a not-yet-
// existing record never breaks the writing flow. The caller inserts the
// returned id as a [[ref]] immediately and can flesh the stub out later. A
// task stub carries the typed text as its title; a memory stub uses it as the
// unique name. Funnels through the same serializer + index as any other write.
export function createBrainStub(
  kind: BrainRecordKind,
  text: string,
  workspace: string,
): Promise<BrainTaskRecord | BrainMemoryRecord> {
  if (kind === 'task') {
    return saveBrainTask({
      id: '',
      workspace,
      title: text,
      status: 'open',
      tags: [],
      pinned: false,
      description: '',
    })
  }
  return saveBrainMemory({
    id: '',
    kind: 'note',
    name: text,
    workspace,
    tags: [],
    pinned: false,
    content: '',
  })
}

// suppressBrainCandidate records the sticky "never suggest this candidate
// again" decision for a record (blank hash = suppress all).
export function suppressBrainCandidate(id: string, contentHash?: string): Promise<{ suppressed: boolean }> {
  return request(`/brain/record/${encodeURIComponent(id)}/suppress-candidate`, {
    method: 'POST',
    body: JSON.stringify({ content_hash: contentHash ?? '' }),
  })
}

// Mirrors store.TaskStatusVocab (Go).
export interface TaskStatusVocab {
  workspace_id: string
  status_text: string
  is_terminal: boolean
  kind: string
  display_order: number
}

// getTaskStatusVocab loads a workspace's status vocabulary so the editor's
// Status ToggleGroup is populated from the live vocab (off-vocab is then
// unselectable, not a post-save 422). An empty array means no vocab is
// configured and the editor falls back to a free-text status input.
export function getTaskStatusVocab(workspaceID: string): Promise<TaskStatusVocab[]> {
  return request(`/task-status-vocabulary?workspace_id=${encodeURIComponent(workspaceID)}`)
}

// ── AI assist (M8) ──────────────────────────────────────────────────────
// The lean augmentation path: ghost-text completion (SSE) + proactive memory
// candidates. Both construct a models.Adapter directly server-side (never the
// worker runner) and degrade silently (204) when no model profile is wired.

// Mirrors assist.CompleteRequest (Go). Context is the text BEFORE the caret;
// Cursor is the (optional) text AFTER it for fill-in-the-middle awareness.
export interface AssistCompleteRequest {
  context: string
  cursor?: string
  field?: string
  workspace?: string
  model_profile?: string
}

// streamAssistComplete opens the ghost-text SSE stream and invokes onToken
// for each `event: token` frame, then resolves with the resolving model
// profile (from the terminal `event: done`). A 204 (no model profile) resolves
// to { profile: null, degraded: true } so the caller shows nothing — the
// silent-degrade law (DESIGN §3.4). The returned promise rejects only on a
// transport / non-204 error. Pass an AbortSignal to cancel a stale request.
export async function streamAssistComplete(
  req: AssistCompleteRequest,
  onToken: (chunk: string) => void,
  signal?: AbortSignal,
): Promise<{ profile: string | null; degraded: boolean }> {
  const res = await fetch(apiURL('/assist/complete'), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(req),
    signal,
  })
  if (res.status === 204) return { profile: null, degraded: true }
  if (!res.ok || !res.body) {
    throw new Error(`assist complete failed: ${res.status}`)
  }
  const reader = res.body.getReader()
  const decoder = new TextDecoder()
  let buffer = ''
  let profile: string | null = null
  for (;;) {
    const { value, done } = await reader.read()
    if (done) break
    buffer += decoder.decode(value, { stream: true })
    let sep
    // SSE frames are separated by a blank line.
    while ((sep = buffer.indexOf('\n\n')) >= 0) {
      const frame = buffer.slice(0, sep)
      buffer = buffer.slice(sep + 2)
      const parsed = parseSseFrame(frame)
      if (!parsed) continue
      if (parsed.event === 'token') {
        onToken(decodeSseToken(parsed.data))
      } else if (parsed.event === 'done') {
        try {
          profile = (JSON.parse(parsed.data) as { profile?: string }).profile ?? null
        } catch {
          profile = null
        }
      }
    }
  }
  return { profile, degraded: false }
}

// parseSseFrame pulls the event name + data line out of one SSE frame.
function parseSseFrame(frame: string): { event: string; data: string } | null {
  let event = 'message'
  let data = ''
  for (const line of frame.split('\n')) {
    if (line.startsWith('event:')) event = line.slice(6).trim()
    else if (line.startsWith('data:')) data += line.slice(5).replace(/^ /, '')
  }
  if (data === '') return null
  return { event, data }
}

// decodeSseToken reverses the server's newline escaping (sseData) so a token
// chunk that spanned a line break is reassembled verbatim.
function decodeSseToken(data: string): string {
  return data.replace(/\\n/g, '\n')
}

// Mirrors assist.Candidate (Go): one proactive-memory suggestion.
export interface AssistMemoryCandidate {
  text: string
  kind: string // note | fact
  tags?: string[]
  refs?: string[]
  signal: string // decision-with-rationale
  content_hash: string
}

export interface AssistMemoryCandidateRequest {
  record_id?: string
  title?: string
  body: string
  workspace?: string
  model_profile?: string
}

// fetchMemoryCandidates asks the gateway for 0..N proactive-memory candidates
// for the record being edited. Returns an empty list on a 204 (no model
// profile) so the right rail stays empty silently.
export async function fetchMemoryCandidates(
  req: AssistMemoryCandidateRequest,
  signal?: AbortSignal,
): Promise<{ candidates: AssistMemoryCandidate[]; profile: string | null }> {
  const res = await fetch(apiURL('/assist/memory-candidates'), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(req),
    signal,
  })
  if (res.status === 204) return { candidates: [], profile: null }
  if (!res.ok) throw new Error(`memory candidates failed: ${res.status}`)
  const body = (await res.json()) as { candidates?: AssistMemoryCandidate[]; profile?: string }
  return { candidates: body.candidates ?? [], profile: body.profile ?? null }
}

// Mirrors assist.NudgeApply (Go): the one-click change a nudge proposes.
// Exactly one field is set per nudge.
export interface AssistNudgeApply {
  add_tag?: string
  insert_ref?: string
  append_body?: string
}

// Mirrors assist.Nudge (Go): one inline guidance suggestion (DESIGN §4.4).
export interface AssistGuidanceNudge {
  kind: string // missing-acceptance-criteria | link-related-memory | auto-tag | entity-extraction
  message: string
  apply: AssistNudgeApply
}

export interface AssistGuidanceRequest {
  record_id?: string
  title?: string
  body: string
  status?: string
  tags?: string[]
  workspace?: string
  model_profile?: string
}

// fetchGuidance asks the gateway for 0..N inline guidance nudges. Unlike the
// other assist calls it NEVER 204s: the deterministic nudges (missing-criteria,
// auto-tag, entity-extraction) work with no model wired, so an empty list means
// "nothing to suggest", not "no model". Pass an AbortSignal to cancel a stale
// request when the body changes again.
export async function fetchGuidance(
  req: AssistGuidanceRequest,
  signal?: AbortSignal,
): Promise<{ nudges: AssistGuidanceNudge[]; profile: string | null }> {
  const res = await fetch(apiURL('/assist/guidance'), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(req),
    signal,
  })
  if (res.status === 204) return { nudges: [], profile: null }
  if (!res.ok) throw new Error(`guidance failed: ${res.status}`)
  const body = (await res.json()) as { nudges?: AssistGuidanceNudge[]; profile?: string }
  return { nudges: body.nudges ?? [], profile: body.profile ?? null }
}

export function reindexBrain(): Promise<{ reindexed: boolean }> {
  return request('/brain/reindex', { method: 'POST' })
}

export function syncBrain(): Promise<{ synced: boolean; conflict: boolean; detail?: string; note?: string }> {
  return request('/brain/browser-sync', { method: 'POST' })
}
