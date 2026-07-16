// ConnectionsPage is the canonical workspace configuration console.
// Access, activity, metadata, server installation, and setup all stay in one
// workspace-first surface; legacy URLs redirect here with query-backed views.

import { useCallback, useEffect, useMemo, useState } from 'react'
import { Layers, Plus, Server } from 'lucide-react'
import { useSearchParams } from 'react-router-dom'
import { listAuthScopes, listDownstreams, listRoutes, listWorkspaces } from '@/api/client'
import type { AuthScope, DownstreamServer, RouteRule, Workspace } from '@/api/types'
import { useApi } from '@/hooks/use-api'
import type { CellState } from '@/components/connections/ConnectionCell'
import { ConnectionDrawer } from '@/components/connections/ConnectionDrawer'
import { EmptySetupState, ErrorBlock } from '@/components/connections/ConnectionEmptyStates'
import { WorkspaceCommandCenter } from '@/components/connections/WorkspaceCommandCenter'
import { WorkspaceConnectionsView } from '@/components/connections/WorkspaceConnectionsView'
import { WorkspaceContextHeader, type WorkspaceSection } from '@/components/connections/WorkspaceContextHeader'
import { WorkspaceGlobalView } from '@/components/connections/WorkspaceGlobalView'
import { WorkspaceRail } from '@/components/connections/WorkspaceRail'
import { WorkspaceRulesPanel } from '@/components/connections/WorkspaceRulesPanel'
import { WorkspaceSettingsPanel } from '@/components/connections/WorkspaceSettingsPanel'
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

type ConsoleView = WorkspaceSection | 'add-server' | 'servers' | 'new-workspace'

const EMPTY_SERVERS: DownstreamServer[] = []
const EMPTY_WORKSPACES: Workspace[] = []
const EMPTY_ROUTES: RouteRule[] = []
const EMPTY_SCOPES: AuthScope[] = []

interface DrawerTarget {
  server: DownstreamServer
  workspace: Workspace
  state: CellState
  routes: RouteRule[]
  route: RouteRule | null
}

function normalizeView(raw: string | null): ConsoleView {
  if (raw === 'activity' || raw === 'settings' || raw === 'add-server' || raw === 'servers' || raw === 'new-workspace') return raw
  return 'access'
}

export function ConnectionsPage() {
  const serversApi = useApi(useCallback((signal: AbortSignal) => listDownstreams({ signal }), []))
  const workspacesApi = useApi(useCallback((signal: AbortSignal) => listWorkspaces({ signal }), []))
  const routesApi = useApi(useCallback((signal: AbortSignal) => listRoutes({ signal }), []))
  const scopesApi = useApi(useCallback((signal: AbortSignal) => listAuthScopes({ signal }), []))
  const [searchParams, setSearchParams] = useSearchParams()
  const [drawer, setDrawer] = useState<DrawerTarget | null>(null)
  const [query, setQuery] = useState('')
  const [filter, setFilter] = useState<ConnectionFilter>('all')

  const servers = serversApi.data ?? EMPTY_SERVERS
  const workspaces = workspacesApi.data ?? EMPTY_WORKSPACES
  const routes = routesApi.data ?? EMPTY_ROUTES
  const scopes = scopesApi.data ?? EMPTY_SCOPES
  const enabledServers = useMemo(() => servers.filter((server) => !server.disabled), [servers])
  const routeIndex = useMemo(() => indexRoutes(routes), [routes])
  const cells = useMemo(() => deriveConnectionCells(routeIndex, scopes), [routeIndex, scopes])
  const summaries = useMemo(
    () => buildWorkspaceSummaries(workspaces, enabledServers, routeIndex, cells),
    [workspaces, enabledServers, routeIndex, cells],
  )

  const requestedWorkspaceId = searchParams.get('workspace')
  const selectedWorkspace = useMemo(
    () => workspaces.find((workspace) => workspace.id === requestedWorkspaceId) ?? workspaces[0] ?? null,
    [requestedWorkspaceId, workspaces],
  )
  const selectedWorkspaceId = selectedWorkspace?.id ?? ''
  const view = normalizeView(searchParams.get('view'))
  const section: WorkspaceSection = view === 'activity' || view === 'settings' ? view : 'access'
  const advancedOpen = searchParams.get('advanced') === '1'
  const operations = useWorkspaceOperations(selectedWorkspaceId, routes)

  const rows = useMemo<WorkspaceConnectionRow[]>(
    () => selectedWorkspace
      ? buildWorkspaceRows(selectedWorkspace, enabledServers, routeIndex, cells, scopes)
      : [],
    [selectedWorkspace, enabledServers, routeIndex, cells, scopes],
  )
  const visibleRows = useMemo(() => filterWorkspaceRows(rows, filter, query), [rows, filter, query])
  const counts = useMemo(() => connectionCounts(rows), [rows])
  const selectedSummary = summaries.find((summary) => summary.workspace.id === selectedWorkspaceId)
  const selectedRules = useMemo(
    () => routes.filter((route) => route.workspace_id === selectedWorkspaceId),
    [routes, selectedWorkspaceId],
  )

  const updateView = useCallback((nextView: ConsoleView, extra?: Record<string, string | null>) => {
    setSearchParams((previous) => {
      const next = new URLSearchParams(previous)
      if (nextView === 'access') next.delete('view')
      else next.set('view', nextView)
      if (nextView !== 'access') next.delete('advanced')
      if (nextView !== 'add-server') next.delete('setup_server')
      for (const [key, value] of Object.entries(extra ?? {})) {
        if (value === null) next.delete(key)
        else next.set(key, value)
      }
      return next
    })
  }, [setSearchParams])

  const refetchConfiguration = useCallback(() => {
    void routesApi.refetch()
    void scopesApi.refetch()
    void serversApi.refetch()
    void workspacesApi.refetch()
  }, [routesApi, scopesApi, serversApi, workspacesApi])

  const selectWorkspace = useCallback((workspaceId: string) => {
    updateView('access', { workspace: workspaceId })
    setFilter('all')
    setQuery('')
  }, [updateView])

  const focusServer = searchParams.get('focus_server') ?? searchParams.get('server')
  const transientWorkspace = searchParams.get('focus_workspace')
  useEffect(() => {
    if (!focusServer && !transientWorkspace) return
    if (!serversApi.data || !workspacesApi.data || !routesApi.data || !scopesApi.data) return
    const target = resolveFocusTarget(
      focusServer,
      transientWorkspace ?? requestedWorkspaceId,
      enabledServers,
      workspaces,
      routeIndex,
      cells,
    )
    if (target && focusServer) setDrawer(target)
    setSearchParams((previous) => {
      const next = new URLSearchParams(previous)
      const workspaceId = target?.workspace.id ?? transientWorkspace
      if (workspaceId) next.set('workspace', workspaceId)
      next.delete('focus_server')
      next.delete('focus_workspace')
      next.delete('server')
      next.delete('action')
      return next
    }, { replace: true })
  }, [cells, enabledServers, focusServer, requestedWorkspaceId, routeIndex, routesApi.data, scopesApi.data, searchParams, serversApi.data, setSearchParams, transientWorkspace, workspaces, workspacesApi.data])

  const errors = [serversApi.error, workspacesApi.error, routesApi.error, scopesApi.error].filter(Boolean).join('\n')
  const loading = serversApi.loading || workspacesApi.loading || routesApi.loading || scopesApi.loading

  return (
    <div className="space-y-4 sm:space-y-5">
      <header className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <h1 className="text-xl font-semibold">Workspaces</h1>
          <p className="mt-1 max-w-2xl text-sm text-muted-foreground">
            Choose a workspace, then decide exactly which servers and credentials its agents can use.
          </p>
        </div>
        <Button variant="outline" className="shrink-0" onClick={() => updateView('new-workspace')}>
          <Plus className="mr-1.5 h-4 w-4" />
          <span className="sm:hidden">New</span>
          <span className="hidden sm:inline">New workspace</span>
        </Button>
      </header>

      {errors && <ErrorBlock message={errors} onRetry={refetchConfiguration} />}
      {loading && workspaces.length === 0 && view !== 'new-workspace' ? (
        <div className="h-72 animate-pulse bg-muted" />
      ) : (
        <div className="grid items-start gap-3 sm:gap-4 lg:grid-cols-[15.5rem_minmax(0,1fr)]">
          <WorkspaceRail
            summaries={summaries}
            selectedWorkspaceId={selectedWorkspaceId}
            libraryActive={view === 'servers'}
            onSelect={selectWorkspace}
            onOpenLibrary={() => updateView('servers', { server_tab: searchParams.get('server_tab') ?? 'installed' })}
          />

          <main className="min-w-0 space-y-4">
            {view === 'new-workspace' && (
              <WorkspaceSettingsPanel
                workspace={null}
                onSaved={(workspace) => { void workspacesApi.refetch(); updateView('settings', { workspace: workspace.id }) }}
                onDeleted={() => {}}
                onCancel={() => updateView('access')}
              />
            )}

            {(view === 'servers' || view === 'add-server') && (
              <WorkspaceGlobalView
                view={view}
                workspace={selectedWorkspace}
                workspaceId={selectedWorkspaceId}
                serverTab={searchParams.get('server_tab')}
                setupServerId={searchParams.get('setup_server')}
                onConfigurationChanged={refetchConfiguration}
                onServerReady={(serverId) => updateView('add-server', { workspace: selectedWorkspaceId, setup_server: serverId })}
                onManageAccess={(serverId) => updateView('access', { workspace: selectedWorkspaceId, focus_server: serverId })}
              />
            )}

            {view !== 'new-workspace' && view !== 'servers' && view !== 'add-server' && !selectedWorkspace && (
              <EmptySetupState
                icon={<Layers className="h-7 w-7" />}
                title="Create your first workspace"
                body="A workspace gives each project a clear filesystem and server-access boundary."
                action={<Button onClick={() => updateView('new-workspace')}><Plus className="mr-1.5 h-4 w-4" /> Create workspace</Button>}
              />
            )}

            {view !== 'new-workspace' && view !== 'servers' && view !== 'add-server' && selectedWorkspace && (
              <>
                <WorkspaceContextHeader
                  workspace={selectedWorkspace}
                  summary={selectedSummary}
                  section={section}
                  onSectionChange={(next) => updateView(next, { workspace: selectedWorkspace.id })}
                  onAddServer={() => updateView('add-server', { workspace: selectedWorkspace.id })}
                />

                {section === 'access' && (
                  <>
                    {enabledServers.length === 0 ? (
                      <EmptySetupState
                        icon={<Server className="h-7 w-7" />}
                        title="No globally enabled servers"
                        body="Install or enable a server, then grant it only to this workspace."
                        action={<Button onClick={() => updateView('servers', { server_tab: 'available' })}>Open server library</Button>}
                      />
                    ) : (
                      <WorkspaceConnectionsView
                        rows={rows}
                        visibleRows={visibleRows}
                        counts={counts}
                        filter={filter}
                        query={query}
                        onFilterChange={setFilter}
                        onQueryChange={setQuery}
                        onOpenConnection={(row) => setDrawer(row)}
                      />
                    )}
                    <WorkspaceRulesPanel
                      workspace={selectedWorkspace}
                      rules={selectedRules}
                      downstreams={servers}
                      authScopes={scopes}
                      open={advancedOpen}
                      onOpenChange={(open) => updateView('access', { advanced: open ? '1' : null, workspace: selectedWorkspace.id })}
                      onChanged={refetchConfiguration}
                    />
                  </>
                )}

                {section === 'activity' && (
                  <WorkspaceCommandCenter
                    workspace={selectedWorkspace}
                    summary={selectedSummary}
                    rows={rows}
                    operations={operations}
                    onOpenConnection={(row) => setDrawer(row)}
                  />
                )}

                {section === 'settings' && (
                  <WorkspaceSettingsPanel
                    workspace={selectedWorkspace}
                    onSaved={() => void workspacesApi.refetch()}
                    onDeleted={() => { void workspacesApi.refetch(); updateView('access', { workspace: null }) }}
                    onCancel={() => updateView('access', { workspace: selectedWorkspace.id })}
                  />
                )}
              </>
            )}
          </main>
        </div>
      )}

      <ConnectionDrawer
        open={drawer !== null}
        server={drawer?.server ?? null}
        workspace={drawer?.workspace ?? null}
        state={drawer?.state ?? null}
        route={drawer?.route ?? null}
        routes={drawer?.routes ?? []}
        scopes={scopes}
        workspaces={workspaces}
        downstreams={servers}
        onClose={() => setDrawer(null)}
        onChanged={refetchConfiguration}
      />
    </div>
  )
}
