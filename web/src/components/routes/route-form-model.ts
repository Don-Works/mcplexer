import type { DownstreamServer, RouteRule, Workspace } from '@/api/types'

export interface RouteFormData {
  name: string
  priority: number
  workspace_id: string
  path_glob: string
  tool_match: string[]
  scope_policy: Record<string, string[]>
  downstream_server_id: string
  auth_scope_id: string
  policy: 'allow' | 'deny'
  log_level: string
  approval_mode: 'none' | 'write' | 'all'
  approval_timeout: number
}

export const emptyRouteForm: RouteFormData = {
  name: '',
  priority: 100,
  workspace_id: '',
  path_glob: '**',
  tool_match: [],
  scope_policy: {},
  downstream_server_id: '',
  auth_scope_id: '',
  policy: 'allow',
  log_level: 'info',
  approval_mode: 'none',
  approval_timeout: 300,
}

export function routeToForm(route: RouteRule): RouteFormData {
  const tm = Array.isArray(route.tool_match) ? route.tool_match : []
  return {
    name: route.name || '',
    priority: route.priority,
    workspace_id: route.workspace_id,
    path_glob: route.path_glob || '**',
    tool_match: tm.length === 1 && tm[0] === '*' ? [] : tm,
    scope_policy: route.scope_policy ?? {},
    downstream_server_id: route.downstream_server_id,
    auth_scope_id: route.auth_scope_id,
    policy: route.policy,
    log_level: route.log_level,
    approval_mode: route.approval_mode ?? 'none',
    approval_timeout: route.approval_timeout ?? 300,
  }
}

export function newRouteFormForWorkspace(workspaceId?: string): RouteFormData {
  return { ...emptyRouteForm, workspace_id: workspaceId ?? '' }
}

export function routeFormForConnection({
  route,
  server,
  workspace,
}: {
  route: RouteRule | null
  server: DownstreamServer | null
  workspace: Workspace | null
}): RouteFormData {
  if (route) return routeToForm(route)
  return {
    ...emptyRouteForm,
    name: workspace && server ? `${workspace.name} → ${server.name}` : '',
    workspace_id: workspace?.id ?? '',
    downstream_server_id: server?.id ?? '',
    tool_match: server?.tool_namespace ? [`${server.tool_namespace}__*`] : [],
    approval_mode: 'write',
    approval_timeout: 300,
  }
}
