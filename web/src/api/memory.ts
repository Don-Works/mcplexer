// Memory API client — thin typed wrappers over /api/v1/memory/*.
//
// The backend handler (internal/api/memory_handler.go) is the source of truth
// for shapes; these types mirror internal/store/models.go MemoryEntry,
// MemoryHit, and MemoryOffer.

import { request } from './client'

// ---------- types ----------

export type MemoryKind = 'fact' | 'note' | string

export type MemorySourceKind =
  | 'agent'
  | 'human'
  | 'imported'
  | 'worker'
  | 'peer'
  | string

export interface MemoryEntry {
  id: string
  name: string
  kind: MemoryKind
  content: string
  tags?: string[] | null
  metadata?: Record<string, unknown> | null
  workspace_id?: string | null
  user_id?: string
  worker_id?: string
  run_id?: string
  source_kind: MemorySourceKind
  source_session_id?: string
  source_peer_id?: string
  source_tool_call_id?: string
  origin_peer_id?: string
  embed_model?: string
  embed_version?: number
  t_valid_start: string
  t_valid_end?: string | null
  invalidated_by?: string
  pinned?: boolean
  created_at: string
  updated_at: string
  deleted_at?: string | null
}

export interface MemoryHit {
  entry: MemoryEntry
  score: number
  source: 'fts' | 'vec' | 'rrf' | string
}

export interface MemoryOffer {
  id: string
  peer_id: string
  peer_name?: string
  remote_id: string
  name: string
  kind: MemoryKind
  description?: string
  preview?: string
  tags?: string[] | null
  metadata?: Record<string, unknown> | null
  embed_model?: string
  received_at: string
  accepted_at?: string | null
  declined_at?: string | null
  accepted_as_id?: string
}

export interface MemoryCount {
  facts: number
  notes: number
}

// MemoryStats mirrors internal/store/models.go MemoryStats — the
// aggregate "shape of the brain" payload powering the memory landing
// header. Backend handler lives at internal/api/memory_stats_handler.go.
//
// Note: `recall_rate_7d` is intentionally absent — there is no recall
// tracking yet on the backend; DecayPressure uses an updated_at heuristic
// instead.
export interface MemoryRecencyBuckets {
  fresh: number
  warm: number
  cold: number
  dormant: number
}

export interface MemoryDailyCount {
  date: string // YYYY-MM-DD (UTC)
  count: number
}

export interface MemoryNetworkReach {
  shared_memory_count: number
  peer_count: number
}

export interface MemoryTagCount {
  tag: string
  count: number
}

export interface MemoryStats {
  brain_age_days: number
  brain_age_born_at?: string | null
  total_memories: number
  total_bytes: number
  pages_equivalent: number
  type_mix: Record<string, number>
  recency_buckets: MemoryRecencyBuckets
  writes_per_day_30d: MemoryDailyCount[]
  network_reach: MemoryNetworkReach
  top_tags: MemoryTagCount[]
  decay_pressure: number
}

// EntityRef is the wire shape for a memory entity link (migration 076).
// `role` defaults to 'subject' on the write path; on filters, an empty
// role matches any role on the row.
export interface EntityRef {
  kind: string
  id: string
  role?: 'subject' | 'mentioned' | 'derived_from' | string
}

// MemoryEntityRow is one row of the memory_entities join table —
// returned by listMemoryEntities(memoryID).
export interface MemoryEntityRow {
  id: string
  memory_id: string
  entity_kind: string
  entity_id: string
  role: string
  created_at: string
  created_by?: string
}

// EntitySummary is one distinct entity surfaced by listEntities — used
// for the entity picker autocomplete + "Top entities" tile.
export interface EntitySummary {
  kind: string
  id: string
  memory_count: number
  last_linked_at: string
}

export interface MemoryListParams {
  kind?: MemoryKind
  tags?: string[]
  limit?: number
  offset?: number
  include_invalid?: boolean
  workspace_id?: string
}

export interface MemorySearchParams {
  query: string
  kind?: MemoryKind
  tags?: string[]
  limit?: number
  include_invalid?: boolean
  workspace_id?: string
  // AND across links — every entity must be linked to the memory.
  entities?: EntityRef[]
  // OR — at least one entity must be linked.
  entities_any?: EntityRef[]
}

export interface ListEntitiesParams {
  kind?: string
  limit?: number
  offset?: number
  workspace_id?: string
}

export interface MemoryOffersParams {
  pending_only?: boolean
  peer_id?: string
  limit?: number
}

// ---------- requests ----------

function buildQuery(params: Record<string, string | number | boolean | undefined>): string {
  const q = new URLSearchParams()
  for (const [k, v] of Object.entries(params)) {
    if (v === undefined || v === '' || v === null) continue
    q.set(k, String(v))
  }
  const s = q.toString()
  return s ? `?${s}` : ''
}

export function listMemories(p: MemoryListParams = {}): Promise<MemoryEntry[]> {
  const qs = buildQuery({
    kind: p.kind,
    tags: p.tags && p.tags.length > 0 ? p.tags.join(',') : undefined,
    limit: p.limit,
    offset: p.offset,
    include_invalid: p.include_invalid ? 'true' : undefined,
    workspace_id: p.workspace_id,
  })
  return request<MemoryEntry[]>(`/memory${qs}`)
}

export function countMemories(): Promise<MemoryCount> {
  return request<MemoryCount>('/memory/count')
}

// memoryStats fetches the aggregate brain-shape snapshot. Pass a
// workspace_id to narrow scope; omit to see everything (the default for
// the landing page).
export function memoryStats(workspaceID?: string): Promise<MemoryStats> {
  const qs = buildQuery({ workspace_id: workspaceID })
  return request<MemoryStats>(`/memory/stats${qs}`)
}

export function getMemory(id: string): Promise<MemoryEntry> {
  return request<MemoryEntry>(`/memory/${encodeURIComponent(id)}`)
}

export function searchMemories(p: MemorySearchParams): Promise<MemoryHit[]> {
  return request<MemoryHit[]>('/memory/search', {
    method: 'POST',
    body: JSON.stringify(p),
  })
}

export function createMemory(body: {
  name: string
  content: string
  kind?: MemoryKind
  tags?: string[]
  metadata?: Record<string, unknown>
  workspace_id?: string
  pinned?: boolean
  entities?: EntityRef[]
}): Promise<MemoryEntry> {
  return request<MemoryEntry>('/memory', {
    method: 'POST',
    body: JSON.stringify(body),
  })
}

export function setMemoryPinned(id: string, pinned: boolean): Promise<void> {
  return request<void>(
    `/memory/${encodeURIComponent(id)}/${pinned ? 'pin' : 'unpin'}`,
    { method: 'POST' },
  )
}

// ---------- entity links (migration 076) ----------

// listEntities returns distinct entities ranked by memory_count DESC.
// Powers the entity picker autocomplete + the "Top entities" landing tile.
export function listEntities(p: ListEntitiesParams = {}): Promise<EntitySummary[]> {
  const qs = buildQuery({
    kind: p.kind,
    limit: p.limit,
    offset: p.offset,
    workspace_id: p.workspace_id,
  })
  return request<EntitySummary[]>(`/memory/entities${qs}`)
}

// listMemoryEntities returns the entity links for one memory.
export function listMemoryEntities(memoryID: string): Promise<MemoryEntityRow[]> {
  return request<MemoryEntityRow[]>(
    `/memory/${encodeURIComponent(memoryID)}/entities`,
  )
}

// linkMemoryEntity adds an "about X" link. Idempotent on (kind, id, role).
export function linkMemoryEntity(memoryID: string, ref: EntityRef): Promise<void> {
  return request<void>(`/memory/${encodeURIComponent(memoryID)}/entities`, {
    method: 'POST',
    body: JSON.stringify(ref),
  })
}

// unlinkMemoryEntity removes an "about X" link. Empty role removes
// every role flavour for the (kind, id) pair.
export function unlinkMemoryEntity(memoryID: string, ref: EntityRef): Promise<void> {
  return request<void>(`/memory/${encodeURIComponent(memoryID)}/entities`, {
    method: 'DELETE',
    body: JSON.stringify(ref),
  })
}

// ---------- associative recall (AR1, AR2, AR3) ----------

// EntityCoLink is one entity returned by relatedEntities() or
// spreadingActivation(). For AR2 the shared_count field carries an
// integer score-proxy (sum of (1/(1+vec_dist)) × 1000 across neighbours),
// not a literal count.
export interface EntityCoLink {
  kind: string
  id: string
  shared_count: number
  last_seen_at: string
}

// EntityEdge is one weighted co-link edge in the entity graph (AR3).
// Source/Target are formatted "kind:id".
export interface EntityEdge {
  source: string
  target: string
  weight: number
}

export interface EntityGraph {
  nodes: EntitySummary[]
  edges: EntityEdge[]
  node_cap: number
  truncated: boolean
}

// relatedEntities — AR1. Entities that co-link with the named entity in
// at least one memory, ranked by shared_count DESC. Powers the "Related"
// section on MemoryAboutPage.
export function relatedEntities(
  kind: string,
  id: string,
  limit?: number,
): Promise<EntityCoLink[]> {
  const qs = buildQuery({ limit })
  return request<EntityCoLink[]>(
    `/memory/entities/${encodeURIComponent(kind)}/${encodeURIComponent(id)}/related${qs}`,
  )
}

// spreadingActivation — AR2. Entities adjacent to the named entity via
// vec-neighbours of the memories about it. Empty when no embedding
// provider is configured.
export function spreadingActivation(
  kind: string,
  id: string,
  limit?: number,
): Promise<EntityCoLink[]> {
  const qs = buildQuery({ limit })
  return request<EntityCoLink[]>(
    `/memory/entities/${encodeURIComponent(kind)}/${encodeURIComponent(id)}/spreading${qs}`,
  )
}

// entityGraph — AR3. Returns the entity-to-entity graph in scope.
export function entityGraph(p: {
  node_cap?: number
  min_weight?: number
  workspace_id?: string
} = {}): Promise<EntityGraph> {
  const qs = buildQuery({
    node_cap: p.node_cap,
    min_weight: p.min_weight,
    workspace_id: p.workspace_id,
  })
  return request<EntityGraph>(`/memory/entities/graph${qs}`)
}

// CoRecalledMemory — AR4. One memory that frequently co-surfaces with
// the query memory in the recall log.
export interface CoRecalledMemory {
  memory_id: string
  name: string
  co_occurrences: number
  score: number
  last_seen_at: string
}

// MemorySuggestion — AR5. Unified "you might also remember" entry,
// merging co-recall + related-entity + semantic axes.
export interface MemorySuggestion {
  memory_id: string
  name: string
  score: number
  source: 'co_recall' | 'related_entity' | 'semantic' | string
  reason: string
}

// coRecalledMemories — AR4. Empty when MCPLEXER_RECALL_TRACKING is off
// or no co-recall signal has accumulated for this memory yet.
export function coRecalledMemories(
  memoryID: string,
  limit?: number,
): Promise<CoRecalledMemory[]> {
  const qs = buildQuery({ limit })
  return request<CoRecalledMemory[]>(
    `/memory/${encodeURIComponent(memoryID)}/co-recalled${qs}`,
  )
}

// memorySuggestions — AR5. Composes co-recall + related-entity +
// semantic neighbour signals into one ranked stream.
export function memorySuggestions(
  memoryID: string,
  limit?: number,
): Promise<MemorySuggestion[]> {
  const qs = buildQuery({ limit })
  return request<MemorySuggestion[]>(
    `/memory/${encodeURIComponent(memoryID)}/suggestions${qs}`,
  )
}

export function invalidateMemory(id: string, supersededByID?: string): Promise<void> {
  return request<void>(`/memory/${encodeURIComponent(id)}/invalidate`, {
    method: 'POST',
    body: JSON.stringify(
      supersededByID ? { superseded_by_id: supersededByID } : {},
    ),
  })
}

export function deleteMemory(id: string): Promise<void> {
  return request<void>(`/memory/${encodeURIComponent(id)}`, {
    method: 'DELETE',
  })
}

export function forgetMemoriesBySource(
  sourceSessionID: string,
): Promise<{ count: number }> {
  return request<{ count: number }>('/memory/forget-by-source', {
    method: 'POST',
    body: JSON.stringify({ source_session_id: sourceSessionID }),
  })
}

export function listMemoryOffers(p: MemoryOffersParams = {}): Promise<MemoryOffer[]> {
  const qs = buildQuery({
    pending_only: p.pending_only ? 'true' : undefined,
    peer_id: p.peer_id,
    limit: p.limit,
  })
  return request<MemoryOffer[]>(`/memory/offers${qs}`)
}

export function acceptMemoryOffer(id: string, localMemoryID: string): Promise<void> {
  return request<void>(`/memory/offers/${encodeURIComponent(id)}/accept`, {
    method: 'POST',
    body: JSON.stringify({ local_memory_id: localMemoryID }),
  })
}

export function declineMemoryOffer(id: string): Promise<void> {
  return request<void>(`/memory/offers/${encodeURIComponent(id)}/decline`, {
    method: 'POST',
  })
}

// ---------- dashboard activity ----------

// MemoryActivityRow mirrors the Go-side projection in
// internal/api/dashboard_activity_handler.go. Tailored for the
// dashboard's "what was just learned" tile — summary is the first
// sentence of the body, body is the full content for the expand
// affordance, agent_display is who wrote it.
export interface MemoryActivityRow {
  id: string
  kind: MemoryKind
  name: string
  summary: string
  body: string
  agent_display: string
  workspace_id?: string
  workspace_name?: string
  scope_label: string
  source_kind: MemorySourceKind
  created_at: string
  pinned?: boolean
}

export interface MemoryActivityResponse {
  memories: MemoryActivityRow[]
}

// listRecentMemoryActivity fetches the dashboard's "what was just
// learned" feed. Default server-side limit is 8; pass a higher value
// to override (hard cap 30).
export function listRecentMemoryActivity(
  limit?: number,
): Promise<MemoryActivityResponse> {
  const qs = buildQuery({ limit })
  return request<MemoryActivityResponse>(`/dashboard/activity/memories${qs}`)
}

// ---------- graph view ----------

export interface MemoryGraphNode {
  id: string
  title: string
  kind: MemoryKind
  tags?: string[]
  created_at: string
  size: number
  pinned?: boolean
}

export interface MemoryGraphEdge {
  source: string
  target: string
  weight: number
  reason: 'co_tag' | 'wikilink' | string
}

export interface MemoryGraph {
  nodes: MemoryGraphNode[]
  edges: MemoryGraphEdge[]
  truncated: boolean
  node_cap: number
}

export interface MemoryGraphParams {
  workspace_id?: string
  include_invalid?: boolean
}

export function getMemoryGraph(p: MemoryGraphParams = {}): Promise<MemoryGraph> {
  const qs = buildQuery({
    workspace_id: p.workspace_id,
    include_invalid: p.include_invalid ? 'true' : undefined,
  })
  return request<MemoryGraph>(`/memory/graph${qs}`)
}

// ---------- consolidator (sleep-time worker) ----------

export interface ConsolidatorStatus {
  enabled: boolean
  installed: boolean
  worker_id?: string
  workspace_id?: string
  schedule_spec?: string
  last_run_status?: string
  last_run_at?: string
  last_run_id?: string
  recent_runs: number
  needs_secret_hint?: string
}

export interface ConsolidatorEnableParams {
  workspace_id: string
  secret_scope_id?: string
  schedule_spec?: string
}

export function getConsolidatorStatus(workspaceID: string): Promise<ConsolidatorStatus> {
  return request<ConsolidatorStatus>(
    `/memory/consolidate/status?workspace_id=${encodeURIComponent(workspaceID)}`,
  )
}

export function enableConsolidator(p: ConsolidatorEnableParams): Promise<unknown> {
  return request<unknown>('/memory/consolidate/enable', {
    method: 'POST',
    body: JSON.stringify(p),
  })
}

export function disableConsolidator(workspaceID: string): Promise<unknown> {
  return request<unknown>('/memory/consolidate/disable', {
    method: 'POST',
    body: JSON.stringify({ workspace_id: workspaceID }),
  })
}

export function runConsolidatorNow(workspaceID: string): Promise<{ run_id: string; status: string }> {
  return request<{ run_id: string; status: string }>('/memory/consolidate/run', {
    method: 'POST',
    body: JSON.stringify({ workspace_id: workspaceID }),
  })
}
