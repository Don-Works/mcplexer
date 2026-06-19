// use-connection-health — shared hook that derives two glanceable
// health counts from the connections data already fetched by
// ConnectionsPage / DashboardPage.
//
// serversNeedingCreds:
//   Any non-disabled server that has at least one route whose auth
//   scope exists but has no stored secrets (env / header / client_creds
//   types only — OAuth is optimistic per the ConnectionsPage comment).
//
// workspacesWithoutRoutes:
//   Any workspace that has zero routes pointing at it.
//
// Both are derived from listDownstreams + listWorkspaces + listRoutes +
// listAuthScopes — the same four fetches ConnectionsPage uses.
// The hook owns its own React Query-style fetches via useApi so it can
// be dropped into DashboardPage without threading props down.

import { useCallback, useMemo } from 'react'
import { listAuthScopes, listDownstreams, listRoutes, listWorkspaces } from '@/api/client'
import type { DownstreamServer, Workspace } from '@/api/types'
import { useApi } from '@/hooks/use-api'

export interface ConnectionHealth {
  serversNeedingCreds: DownstreamServer[]
  workspacesWithoutRoutes: Workspace[]
  loading: boolean
}

export function useConnectionHealth(): ConnectionHealth {
  const serversFetcher = useCallback(() => listDownstreams(), [])
  const workspacesFetcher = useCallback(() => listWorkspaces(), [])
  const routesFetcher = useCallback(() => listRoutes(), [])
  const scopesFetcher = useCallback(() => listAuthScopes(), [])

  const { data: servers, loading: serversLoading } = useApi(serversFetcher)
  const { data: workspaces, loading: wsLoading } = useApi(workspacesFetcher)
  const { data: routes, loading: routesLoading } = useApi(routesFetcher)
  const { data: scopes, loading: scopesLoading } = useApi(scopesFetcher)

  const loading = serversLoading || wsLoading || routesLoading || scopesLoading

  const serversNeedingCreds = useMemo<DownstreamServer[]>(() => {
    if (!servers || !routes || !scopes) return []

    // Build a map of scope id → scope for O(1) lookup.
    const scopeMap = new Map(scopes.map((s) => [s.id, s]))

    // Collect server ids that have at least one route needing credentials.
    const needsCreds = new Set<string>()
    for (const r of routes) {
      if (!r.auth_scope_id) continue
      const scope = scopeMap.get(r.auth_scope_id)
      if (!scope) {
        // Auth scope referenced by the route is missing — treat as needing creds.
        needsCreds.add(r.downstream_server_id)
        continue
      }
      if (
        (scope.type === 'env' ||
          scope.type === 'header' ||
          scope.type === 'hawk' ||
          scope.type === 'client_credentials') &&
        !scope.has_secrets
      ) {
        needsCreds.add(r.downstream_server_id)
      }
    }

    return servers.filter((s) => !s.disabled && needsCreds.has(s.id))
  }, [servers, routes, scopes])

  const workspacesWithoutRoutes = useMemo<Workspace[]>(() => {
    if (!workspaces || !routes) return []

    const workspaceIdsWithRoutes = new Set(routes.map((r) => r.workspace_id))
    return workspaces.filter((w) => !workspaceIdsWithRoutes.has(w.id))
  }, [workspaces, routes])

  return { serversNeedingCreds, workspacesWithoutRoutes, loading }
}
