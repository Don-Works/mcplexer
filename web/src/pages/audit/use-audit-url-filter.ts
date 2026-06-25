import { useEffect, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import type { AuditFilter, AuditSort } from '@/api/types'

// URL params synced both ways. Adding a key here wires it into the deep-link
// round-trip (read on mount, written on change). `sort` and the Mission Control
// facets sit alongside the legacy execution_id/session_id/status/workspace_id/
// tool_name so old deep links keep working.
const URL_KEYS = [
  'execution_id',
  'session_id',
  'status',
  'workspace_id',
  'tool_name',
  'actor_kind',
  'actor_id',
  'downstream_server_id',
  'route_rule_id',
  'client_type',
  'error_code',
  'tier',
  'cache_hit',
  'min_latency_ms',
  'after',
  'before',
  'q',
  'sort',
] as const

function filterFromParams(p: URLSearchParams, pageSize: number): AuditFilter {
  const get = (k: string) => p.get(k) || undefined
  const status = p.get('status')
  const cache = p.get('cache_hit')
  const minLat = p.get('min_latency_ms')
  return {
    limit: pageSize,
    execution_id: get('execution_id'),
    session_id: get('session_id'),
    status:
      status === 'success' || status === 'error' || status === 'blocked'
        ? status
        : undefined,
    workspace_id: get('workspace_id'),
    tool_name: get('tool_name'),
    actor_kind: get('actor_kind'),
    actor_id: get('actor_id'),
    downstream_server_id: get('downstream_server_id'),
    route_rule_id: get('route_rule_id'),
    client_type: get('client_type'),
    error_code: get('error_code'),
    tier: get('tier'),
    after: get('after'),
    before: get('before'),
    q: get('q'),
    sort: (get('sort') as AuditSort) ?? undefined,
    cache_hit: cache === 'true' ? true : cache === 'false' ? false : undefined,
    min_latency_ms: minLat ? Number(minLat) : undefined,
  }
}

interface UseAuditUrlFilter {
  filter: AuditFilter
  setFilter: React.Dispatch<React.SetStateAction<AuditFilter>>
  // The current URLSearchParams + setter, exposed so the page can also drive the
  // `?selected=<id>` param off the same instance.
  searchParams: URLSearchParams
  setSearchParams: ReturnType<typeof useSearchParams>[1]
}

/**
 * useAuditUrlFilter — the single source of truth for the audit filter, kept in
 * lockstep with the URL query string. Reads the initial filter from the URL on
 * mount; writes every filter dimension + sort back (replace, so back/forward is
 * driven by real navigations, not keystrokes). Deep links and refreshes
 * round-trip cleanly.
 */
export function useAuditUrlFilter(pageSize: number): UseAuditUrlFilter {
  const [searchParams, setSearchParams] = useSearchParams()
  const [filter, setFilter] = useState<AuditFilter>(() =>
    filterFromParams(searchParams, pageSize),
  )

  useEffect(() => {
    setSearchParams(
      (prev) => {
        const next = new URLSearchParams(prev)
        const set = (k: string, v: string | undefined) =>
          v ? next.set(k, v) : next.delete(k)
        for (const key of URL_KEYS) {
          const v = (filter as Record<string, unknown>)[key]
          set(key, v === undefined || v === null ? undefined : String(v))
        }
        return next
      },
      { replace: true },
    )
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [
    filter.execution_id, filter.session_id, filter.status, filter.workspace_id,
    filter.tool_name, filter.actor_kind, filter.actor_id,
    filter.downstream_server_id, filter.route_rule_id, filter.client_type,
    filter.error_code, filter.tier, filter.cache_hit,
    filter.min_latency_ms, filter.after, filter.before, filter.q, filter.sort,
  ])

  return { filter, setFilter, searchParams, setSearchParams }
}
