// ConnectionsPage — workspace-first route management.
//
// Reads: list_downstreams, list_workspaces, list_routes, list_auth_scopes
// All four already exist in the REST surface — this page composes them into
// "what is this workspace connected to?" instead of a server × workspace grid.

import { useCallback, useEffect, useMemo, useState } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import {
  listAuthScopes,
  listDownstreams,
  listRoutes,
  listWorkspaces,
} from '@/api/client'
import type { DownstreamServer, RouteRule, Workspace } from '@/api/types'
import { useApi } from '@/hooks/use-api'
import type { CellState } from '@/components/connections/ConnectionCell'
import { ConnectionDrawer } from '@/components/connections/ConnectionDrawer'
import { WorkspaceConnectionsView } from '@/components/connections/WorkspaceConnectionsView'
import { useWorkspaceOperations } from '@/components/connections/use-workspace-operations'
import {
  buildWorkspaceRows,
  buildWorkspaceSummaries,
  connectionCounts,
  deriveConnectionCells,
  filterWorkspaceRows,
  indexRoutes,
  resolveFocusTarget,
  type ConnectionFilter,
  type WorkspaceConnectionRow,
} from '@/components/connections/connection-model'
import { Button } from '@/components/ui/button'
import { Plus, Route, Settings2 } from 'lucide-react'

interface DrawerTarget {
  server: DownstreamServer
  workspace: Workspace
  state: CellState
  route: RouteRule | null
}

export function ConnectionsPage() {
  const serversFetcher = useCallback((signal: AbortSignal) => listDownstreams({ signal }), [])
  const workspacesFetcher = useCallback((signal: AbortSignal) => listWorkspaces({ signal }), [])
  const routesFetcher = useCallback((signal: AbortSignal) => listRoutes({ signal }), [])
  const scopesFetcher = useCallback((signal: AbortSignal) => listAuthScopes({ signal }), [])

  const {
    data: servers,
    loading: serversLoading,
    error: serversError,
    refetch: refetchServers,
  } = useApi(serversFetcher)
  const {
    data: workspaces,
    loading: workspacesLoading,
    error: workspacesError,
    refetch: refetchWorkspaces,
  } = useApi(workspacesFetcher)
  const {
    data: routes,
    loading: routesLoading,
    error: routesError,
    refetch: refetchRoutes,
  } = useApi(routesFetcher)
  const {
    data: scopes,
    loading: scopesLoading,
    error: scopesError,
    refetch: refetchScopes,
  } = useApi(scopesFetcher)

  const [searchParams, setSearchParams] = useSearchParams()
  const [drawer, setDrawer] = useState<DrawerTarget | null>(null)
  const [selectedWorkspaceId, setSelectedWorkspaceId] = useState<string | null>(
    () => searchParams.get('focus_workspace'),
  )
  const [query, setQuery] = useState('')
  const [filter, setFilter] = useState<ConnectionFilter>('all')

  const enabledServers = useMemo(
    () => (servers ?? []).filter((s) => !s.disabled),
    [servers],
  )

  // Index routes by (server, workspace) so cell-state computation is O(N×M).
  const routeIndex = useMemo(() => {
    return indexRoutes(routes ?? [])
  }, [routes])

  const cells = useMemo(
    () => deriveConnectionCells(routeIndex, scopes ?? []),
    [routeIndex, scopes],
  )

  const summaries = useMemo(
    () => buildWorkspaceSummaries(workspaces ?? [], enabledServers, routeIndex, cells),
    [workspaces, enabledServers, routeIndex, cells],
  )

  const selectedWorkspace = useMemo(() => {
    if (!workspaces?.length) return null
    return workspaces.find((workspace) => workspace.id === selectedWorkspaceId) ?? workspaces[0]
  }, [workspaces, selectedWorkspaceId])
  const selectedWorkspaceID = selectedWorkspace?.id ?? ''
  const operations = useWorkspaceOperations(selectedWorkspaceID, routes ?? [])

  const rows = useMemo<WorkspaceConnectionRow[]>(() => {
    if (!selectedWorkspace) return []
    return buildWorkspaceRows(
      selectedWorkspace,
      enabledServers,
      routeIndex,
      cells,
      scopes ?? [],
    )
  }, [selectedWorkspace, enabledServers, routeIndex, cells, scopes])

  const visibleRows = useMemo(
    () => filterWorkspaceRows(rows, filter, query),
    [rows, filter, query],
  )

  const counts = useMemo(() => connectionCounts(rows), [rows])
  const loading = serversLoading || workspacesLoading || routesLoading || scopesLoading
  const error = [serversError, workspacesError, routesError, scopesError]
    .filter(Boolean)
    .join('\n')

  useEffect(() => {
    if (!workspaces?.length) {
      setSelectedWorkspaceId(null)
      return
    }
    if (!selectedWorkspaceId || !workspaces.some((workspace) => workspace.id === selectedWorkspaceId)) {
      setSelectedWorkspaceId(workspaces[0].id)
    }
  }, [workspaces, selectedWorkspaceId])

  // Deep-link support:
  // - ?focus_workspace=<id> selects a workspace.
  // - ?focus_server=<id>&focus_workspace=<id> opens that pair.
  // - ?focus_server=<id> opens the first existing route for that server,
  //   useful for dashboard "fix auth" links that know only the server.
  const focusServer = searchParams.get('focus_server') ?? searchParams.get('server')
  const focusWorkspace = searchParams.get('focus_workspace') ?? searchParams.get('workspace')
  useEffect(() => {
    if (!focusServer && !focusWorkspace) return
    if (!servers || !workspaces || !routes || !scopes) return

    if (focusWorkspace && workspaces.some((workspace) => workspace.id === focusWorkspace)) {
      setSelectedWorkspaceId(focusWorkspace)
    }

    const target = resolveFocusTarget(focusServer, focusWorkspace, enabledServers, workspaces, routeIndex, cells)
    if (target && focusServer) {
      setSelectedWorkspaceId(target.workspace.id)
      setDrawer(target)
    }

    setSearchParams(
      (prev) => {
        const next = new URLSearchParams(prev)
        next.delete('focus_server')
        next.delete('focus_workspace')
        next.delete('server')
        next.delete('workspace')
        next.delete('action')
        return next
      },
      { replace: true },
    )
  }, [
    focusServer,
    focusWorkspace,
    servers,
    workspaces,
    routes,
    scopes,
    enabledServers,
    routeIndex,
    cells,
    setSearchParams,
  ])

  const handleOpenConnection = useCallback(
    (row: WorkspaceConnectionRow) => {
      const { server, workspace, state, route } = row
      setDrawer({ server, workspace, state, route })
    },
    [],
  )

  const handleDrawerClose = useCallback(() => setDrawer(null), [])

  const handleDrawerChanged = useCallback(() => {
    // Refetch the slices that could have changed. Cheap; the API
    // returns small JSON.
    void refetchRoutes()
    void refetchScopes()
    // Server + workspace lists rarely change as a side-effect, but
    // refetching is safe and keeps the drawer's empty-state honest.
    void refetchServers()
    void refetchWorkspaces()
  }, [refetchRoutes, refetchScopes, refetchServers, refetchWorkspaces])

  const handleRetry = useCallback(() => {
    refetchRoutes()
    refetchScopes()
    refetchServers()
    refetchWorkspaces()
  }, [refetchRoutes, refetchScopes, refetchServers, refetchWorkspaces])

  return (
    <div className="space-y-5">
      <header className="flex flex-col gap-3 md:flex-row md:items-start md:justify-between">
        <div className="space-y-1">
          <h1 className="text-xl font-semibold">Workspace command center</h1>
          <p className="max-w-2xl text-sm text-muted-foreground">
            Start from a workspace to see access, routing, pending actions,
            automation, memory, tasks, and recent tool activity in one place.
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Button variant="outline" asChild>
            <Link to="/workspaces/routes">
              <Route className="mr-1.5 h-4 w-4" />
              Server access
            </Link>
          </Button>
          <Button variant="outline" asChild>
            <Link to="/workspaces/manage">
              <Settings2 className="mr-1.5 h-4 w-4" />
              Manage workspaces
            </Link>
          </Button>
          <Button asChild>
            <Link to="/setup">
              <Plus className="mr-1.5 h-4 w-4" />
              Add server
            </Link>
          </Button>
        </div>
      </header>

      <WorkspaceConnectionsView
        workspaces={workspaces ?? []}
        serverCount={enabledServers.length}
        summaries={summaries}
        selectedWorkspace={selectedWorkspace}
        rows={rows}
        visibleRows={visibleRows}
        counts={counts}
        operations={operations}
        filter={filter}
        query={query}
        loading={loading}
        error={error || null}
        onRetry={handleRetry}
        onSelectWorkspace={setSelectedWorkspaceId}
        onFilterChange={setFilter}
        onQueryChange={setQuery}
        onOpenConnection={handleOpenConnection}
      />

      <ConnectionDrawer
        open={drawer !== null}
        server={drawer?.server ?? null}
        workspace={drawer?.workspace ?? null}
        state={drawer?.state ?? null}
        route={drawer?.route ?? null}
        scopes={scopes ?? []}
        workspaces={workspaces ?? []}
        downstreams={servers ?? []}
        onClose={handleDrawerClose}
        onChanged={handleDrawerChanged}
      />
    </div>
  )
}
