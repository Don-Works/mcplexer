import type { AuthScope, DownstreamServer, RouteRule, Workspace } from '@/api/types'
import type { CellState } from './ConnectionCell'

export type ConnectionFilter = 'all' | 'connected' | 'needs-auth' | 'available' | 'denied'

export interface WorkspaceConnectionSummary {
  workspace: Workspace
  connected: number
  needsAuth: number
  disabled: number
  available: number
}

export interface WorkspaceConnectionRow {
  server: DownstreamServer
  workspace: Workspace
  routes: RouteRule[]
  route: RouteRule | null
  scope: AuthScope | null
  state: CellState
  searchText: string
}

export interface ConnectionTarget {
  server: DownstreamServer
  workspace: Workspace
  state: CellState
  routes: RouteRule[]
  route: RouteRule | null
}

export function connectionKey(serverId: string, workspaceId: string): string {
  return `${serverId}::${workspaceId}`
}

export type RouteIndex = Map<string, RouteRule[]>

export function indexRoutes(routes: RouteRule[]): RouteIndex {
  const idx: RouteIndex = new Map()
  for (const route of routes) {
    if (!route.downstream_server_id) continue
    const key = connectionKey(route.downstream_server_id, route.workspace_id)
    const current = idx.get(key) ?? []
    current.push(route)
    idx.set(key, current)
  }
  for (const rules of idx.values()) rules.sort(compareRules)
  return idx
}

export function deriveConnectionCells(
  routeIndex: RouteIndex,
  scopes: AuthScope[],
): Map<string, CellState> {
  const scopeById = new Map(scopes.map((scope) => [scope.id, scope]))
  const out = new Map<string, CellState>()

  for (const [key, routes] of routeIndex) {
    const allowRules = routes.filter((route) => route.policy === 'allow')
    if (allowRules.length === 0) {
      out.set(key, { kind: 'disabled', routeId: routes[0]?.id })
      continue
    }

    for (const route of allowRules) {
      if (!route.auth_scope_id) continue
      const scope = scopeById.get(route.auth_scope_id)
      if (!scope) {
        out.set(key, { kind: 'needs-auth', hint: 'scope deleted', routeId: route.id })
        break
      }
      if (scopeNeedsSecret(scope)) {
        out.set(key, { kind: 'needs-auth', hint: 'no secrets', routeId: route.id })
        break
      }
    }
    if (!out.has(key)) out.set(key, { kind: 'connected', routeId: allowRules[0].id })
  }

  return out
}

export function buildWorkspaceSummaries(
  workspaces: Workspace[],
  servers: DownstreamServer[],
  routeIndex: RouteIndex,
  cells: Map<string, CellState>,
): WorkspaceConnectionSummary[] {
  return workspaces.map((workspace) => {
    let connected = 0
    let needsAuth = 0
    let disabled = 0
    let available = 0

    for (const server of servers) {
      const key = connectionKey(server.id, workspace.id)
      const state = cells.get(key) ?? { kind: 'add' as const }
      const hasRoute = routeIndex.has(key)

      if (!hasRoute) available += 1
      else if (state.kind === 'connected') connected += 1
      else if (state.kind === 'needs-auth') needsAuth += 1
      else if (state.kind === 'disabled') disabled += 1
    }

    return { workspace, connected, needsAuth, disabled, available }
  })
}

export function buildWorkspaceRows(
  workspace: Workspace,
  servers: DownstreamServer[],
  routeIndex: RouteIndex,
  cells: Map<string, CellState>,
  scopes: AuthScope[],
): WorkspaceConnectionRow[] {
  const scopeById = new Map(scopes.map((scope) => [scope.id, scope]))

  return servers
    .map((server) => {
      const key = connectionKey(server.id, workspace.id)
      const routes = routeIndex.get(key) ?? []
      const route = primaryRoute(routes, cells.get(key))
      const scope = route?.auth_scope_id ? scopeById.get(route.auth_scope_id) ?? null : null
      const state = cells.get(key) ?? { kind: 'add' as const }
      const routeScopes = routes
        .map((item) => (item.auth_scope_id ? scopeById.get(item.auth_scope_id) : null))
        .filter((item): item is AuthScope => Boolean(item))
      const searchText = [
        server.name,
        server.tool_namespace,
        server.transport,
        ...routes.flatMap((item) => [item.name, item.path_glob, item.tool_match.join(' ')]),
        ...routeScopes.flatMap((item) => [item.display_name, item.name, item.type]),
      ]
        .filter(Boolean)
        .join(' ')
        .toLowerCase()

      return { server, workspace, routes, route, scope, state, searchText }
    })
    .sort(compareConnectionRows)
}

export function filterWorkspaceRows(
  rows: WorkspaceConnectionRow[],
  filter: ConnectionFilter,
  query: string,
): WorkspaceConnectionRow[] {
  const q = query.trim().toLowerCase()
  return rows.filter((row) => {
    if (filter === 'connected' && row.state.kind !== 'connected') return false
    if (filter === 'needs-auth' && row.state.kind !== 'needs-auth') return false
    if (filter === 'denied' && row.state.kind !== 'disabled') return false
    if (filter === 'available' && row.route) return false
    if (!q) return true
    return row.searchText.includes(q)
  })
}

export function connectionCounts(rows: WorkspaceConnectionRow[]) {
  return rows.reduce(
    (acc, row) => {
      acc.all += 1
      if (!row.route) acc.available += 1
      else if (row.state.kind === 'connected') acc.connected += 1
      else if (row.state.kind === 'needs-auth') acc.needsAuth += 1
      else if (row.state.kind === 'disabled') acc.denied += 1
      return acc
    },
    { all: 0, connected: 0, needsAuth: 0, available: 0, denied: 0 },
  )
}

export function resolveFocusTarget(
  focusServer: string | null,
  focusWorkspace: string | null,
  servers: DownstreamServer[],
  workspaces: Workspace[],
  routeIndex: RouteIndex,
  cells: Map<string, CellState>,
): ConnectionTarget | null {
  if (!focusServer) return null

  const server = findServerForFocus(focusServer, servers)
  if (!server) return null

  const workspace =
    workspaces.find((item) => item.id === focusWorkspace || item.name === focusWorkspace) ??
    findWorkspaceForServer(server.id, workspaces, routeIndex) ??
    // A server with no route in any workspace (e.g. freshly installed, jumped
    // to from the palette) still opens the drawer in the first workspace's
    // 'add' state — connecting it is exactly the intent of the jump.
    workspaces[0]
  if (!workspace) return null

  const key = connectionKey(server.id, workspace.id)
  const routes = routeIndex.get(key) ?? []
  return {
    server,
    workspace,
    state: cells.get(key) ?? { kind: 'add' },
    routes,
    route: primaryRoute(routes, cells.get(key)),
  }
}

function primaryRoute(routes: RouteRule[], state?: CellState): RouteRule | null {
  if (routes.length === 0) return null
  if (state?.routeId) return routes.find((route) => route.id === state.routeId) ?? routes[0]
  return routes.find((route) => route.policy === 'allow') ?? routes[0]
}

function compareRules(a: RouteRule, b: RouteRule): number {
  if (a.priority !== b.priority) return b.priority - a.priority
  return a.id.localeCompare(b.id)
}

function compareConnectionRows(a: WorkspaceConnectionRow, b: WorkspaceConnectionRow): number {
  const byState = stateRank(a) - stateRank(b)
  if (byState !== 0) return byState
  return a.server.name.localeCompare(b.server.name)
}

function findServerForFocus(focusServer: string, servers: DownstreamServer[]): DownstreamServer | null {
  return (
    servers.find((item) => item.id === focusServer) ??
    servers.find((item) => item.name === focusServer) ??
    null
  )
}

function findWorkspaceForServer(
  serverId: string,
  workspaces: Workspace[],
  routeIndex: RouteIndex,
): Workspace | null {
  for (const workspace of workspaces) {
    if (routeIndex.has(connectionKey(serverId, workspace.id))) return workspace
  }
  return null
}

function stateRank(row: WorkspaceConnectionRow): number {
  if (row.state.kind === 'needs-auth') return 0
  if (row.state.kind === 'connected') return 1
  if (row.state.kind === 'disabled') return 2
  return 3
}

function scopeNeedsSecret(scope: AuthScope): boolean {
  return (
    (scope.type === 'env' ||
      scope.type === 'header' ||
      scope.type === 'hawk' ||
      scope.type === 'client_credentials') &&
    !scope.has_secrets
  )
}
