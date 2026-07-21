import type {
  AuditAlertsResponse,
  AuditCapabilities,
  AuditFilter,
  AuditPage,
  AuditSearchResponse,
  SavedSearch,
  SavedSearchCreate,
  SavedSearchPatch,
} from './types'
import { request } from './transport'

// Audit

// auditFilterParams serializes an AuditFilter into query params. Shared by the
// list endpoint and search (search overrides `q`), so any new facet is wired
// in exactly one place. Booleans serialize as "true"/"false" (omitted when
// undefined); numbers as strings; empty strings are dropped.
function auditFilterParams(filter: AuditFilter): URLSearchParams {
  const params = new URLSearchParams()
  if (filter.id) params.set('id', filter.id)
  if (filter.workspace_id) params.set('workspace_id', filter.workspace_id)
  if (filter.tool_name) params.set('tool_name', filter.tool_name)
  if (filter.status) params.set('status', filter.status)
  if (filter.execution_id) params.set('execution_id', filter.execution_id)
  if (filter.session_id) params.set('session_id', filter.session_id)
  if (filter.actor_kind) params.set('actor_kind', filter.actor_kind)
  if (filter.actor_id) params.set('actor_id', filter.actor_id)
  if (filter.downstream_server_id)
    params.set('downstream_server_id', filter.downstream_server_id)
  if (filter.route_rule_id) params.set('route_rule_id', filter.route_rule_id)
  if (filter.client_type) params.set('client_type', filter.client_type)
  if (filter.error_code) params.set('error_code', filter.error_code)
  if (filter.tier) params.set('tier', filter.tier)
  if (filter.cache_hit !== undefined)
    params.set('cache_hit', String(filter.cache_hit))
  if (filter.min_latency_ms !== undefined)
    params.set('min_latency_ms', String(filter.min_latency_ms))
  if (filter.q) params.set('q', filter.q)
  if (filter.after) params.set('after', filter.after)
  if (filter.before) params.set('before', filter.before)
  if (filter.sort) params.set('sort', filter.sort)
  if (filter.cursor) params.set('cursor', filter.cursor)
  if (filter.limit) params.set('limit', String(filter.limit))
  if (filter.offset) params.set('offset', String(filter.offset))
  return params
}

export function queryAuditLogs(filter: AuditFilter): Promise<AuditPage> {
  return request(`/audit?${auditFilterParams(filter).toString()}`)
}

// searchAuditLogs — free-text / semantic search over audit rows. The optional
// filter narrows the search space (same facets as the list); `q` here wins
// over any q on the filter.
export function searchAuditLogs(
  q: string,
  filter?: AuditFilter,
): Promise<AuditSearchResponse> {
  const params = filter ? auditFilterParams(filter) : new URLSearchParams()
  params.set('q', q)
  return request(`/audit/search?${params.toString()}`)
}

// getAuditCapabilities — what the audit subsystem supports on this install
// (which search rankers, alerts, saved searches). Used to gate UI features.
export function getAuditCapabilities(): Promise<AuditCapabilities> {
  return request('/audit/capabilities')
}

// listAuditAlerts — current anomaly + security alerts. window_sec scopes the
// observation window; workspace_id narrows to one workspace.
export function listAuditAlerts(opts?: {
  workspace_id?: string
  window_sec?: number
}): Promise<AuditAlertsResponse> {
  const params = new URLSearchParams()
  if (opts?.workspace_id) params.set('workspace_id', opts.workspace_id)
  if (opts?.window_sec !== undefined)
    params.set('window_sec', String(opts.window_sec))
  const qs = params.toString()
  return request(`/audit/alerts${qs ? `?${qs}` : ''}`)
}

// Saved searches — persisted query + facet sets, optionally alerting.
// The REST surface wraps the payload in {data: ...}; unwrap here so callers
// get the bare value the TS signature promises.
export function listSavedSearches(): Promise<SavedSearch[]> {
  return request<{ data: SavedSearch[] }>('/audit/saved-searches').then(
    (r) => r.data ?? [],
  )
}

export function createSavedSearch(
  body: SavedSearchCreate,
): Promise<SavedSearch> {
  return request<{ data: SavedSearch }>('/audit/saved-searches', {
    method: 'POST',
    body: JSON.stringify(body),
  }).then((r) => r.data)
}

export function updateSavedSearch(
  id: string,
  patch: SavedSearchPatch,
): Promise<SavedSearch> {
  return request<{ data: SavedSearch }>(
    `/audit/saved-searches/${encodeURIComponent(id)}`,
    {
      method: 'PATCH',
      body: JSON.stringify(patch),
    },
  ).then((r) => r.data)
}

export function deleteSavedSearch(id: string): Promise<void> {
  return request(`/audit/saved-searches/${encodeURIComponent(id)}`, {
    method: 'DELETE',
  })
}
