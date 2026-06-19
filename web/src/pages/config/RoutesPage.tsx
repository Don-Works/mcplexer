import { useCallback, useEffect, useMemo, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { Button } from '@/components/ui/button'
import { useApi } from '@/hooks/use-api'
import {
  deleteRoute,
  flushCache,
  listAuthScopes,
  listDownstreams,
  listRoutes,
  listWorkspaces,
  createRoute,
  updateRoute,
} from '@/api/client'
import type { RouteRule, Workspace } from '@/api/types'
import { Plus, RotateCw } from 'lucide-react'
import { toast } from 'sonner'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import { RouteDialog, emptyForm } from './RouteDialog'
import type { RouteFormData } from './RouteDialog'
import { BulkEnableDialog } from './BulkEnableDialog'
import { RouteWorkspaceGroup } from './RouteWorkspaceGroup'

interface WorkspaceGroup {
  workspace: Workspace
  rules: RouteRule[]
  enabledDownstreamIds: Set<string>
}

export function RoutesPage() {
  const fetcher = useCallback(() => listRoutes(), [])
  const { data: routes, loading, error, refetch } = useApi(fetcher)

  const workspacesFetcher = useCallback(() => listWorkspaces(), [])
  const { data: workspaces } = useApi(workspacesFetcher)

  const downstreamsFetcher = useCallback(() => listDownstreams(), [])
  const { data: downstreams } = useApi(downstreamsFetcher)

  const authScopesFetcher = useCallback(() => listAuthScopes(), [])
  const { data: authScopes } = useApi(authScopesFetcher)

  // Dialog state
  const [dialogOpen, setDialogOpen] = useState(false)
  const [editing, setEditing] = useState<RouteRule | null>(null)
  const [form, setForm] = useState<RouteFormData>(emptyForm)
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)
  const [deleteTarget, setDeleteTarget] = useState<RouteRule | null>(null)

  // Bulk enable state
  const [bulkWorkspace, setBulkWorkspace] = useState<Workspace | null>(null)

  // Expanded workspaces
  const [expandedWs, setExpandedWs] = useState<Set<string>>(new Set())

  // Deep links from command palette / workspace settings expand and pulse the
  // target workspace or route before clearing the URL param.
  const [searchParams, setSearchParams] = useSearchParams()
  const [highlightRouteId, setHighlightRouteId] = useState<string | null>(null)
  const [highlightWorkspaceId, setHighlightWorkspaceId] = useState<string | null>(null)
  useEffect(() => {
    const routeTarget = searchParams.get('route')
    const workspaceTarget = searchParams.get('workspace')
    if (!routeTarget && !workspaceTarget) return

    let targetRouteId: string | null = null
    let targetWorkspaceId: string | null = null

    if (routeTarget) {
      if (!routes) return
      const found = routes.find((r) => r.id === routeTarget)
      if (!found) return
      targetRouteId = routeTarget
      targetWorkspaceId = found.workspace_id
    } else if (workspaceTarget) {
      if (!workspaces) return
      if (!workspaces.some((workspace) => workspace.id === workspaceTarget)) return
      targetWorkspaceId = workspaceTarget
    }

    if (!targetWorkspaceId) return
    setExpandedWs((prev) => {
      if (prev.has(targetWorkspaceId)) return prev
      const next = new Set(prev)
      next.add(targetWorkspaceId)
      return next
    })
    setHighlightRouteId(targetRouteId)
    setHighlightWorkspaceId(targetWorkspaceId)
    const scrollTimer = setTimeout(() => {
      const selector = targetRouteId
        ? `[data-route-id="${targetRouteId}"]`
        : `[data-route-workspace-id="${targetWorkspaceId}"]`
      const el = document.querySelector<HTMLElement>(selector)
      el?.scrollIntoView({ behavior: 'smooth', block: 'center' })
    }, 50)
    const clearParamTimer = setTimeout(() => {
      setSearchParams(
        (prev) => {
          const next = new URLSearchParams(prev)
          next.delete('route')
          next.delete('workspace')
          return next
        },
        { replace: true },
      )
    }, 200)
    const clearHighlightTimer = setTimeout(() => {
      setHighlightRouteId(null)
      setHighlightWorkspaceId(null)
    }, 3500)
    return () => {
      clearTimeout(scrollTimer)
      clearTimeout(clearParamTimer)
      clearTimeout(clearHighlightTimer)
    }
  }, [searchParams, setSearchParams, routes, workspaces])

  const [reloading, setReloading] = useState(false)
  async function handleReload() {
    setReloading(true)
    try {
      await flushCache('all')
      toast.success('Routes reloaded — changes are live for new tool calls')
      refetch()
    } catch (err: unknown) {
      toast.error(err instanceof Error ? err.message : 'Failed to reload routes')
    } finally {
      setReloading(false)
    }
  }

  // Group routes by workspace
  const groups = useMemo((): WorkspaceGroup[] => {
    if (!workspaces || !routes) return []

    const rulesByWs = new Map<string, RouteRule[]>()
    for (const r of routes) {
      const existing = rulesByWs.get(r.workspace_id) ?? []
      existing.push(r)
      rulesByWs.set(r.workspace_id, existing)
    }

    const result: WorkspaceGroup[] = workspaces.map((ws) => {
      const wsRules = rulesByWs.get(ws.id) ?? []
      const enabledIds = new Set(wsRules.map((r) => r.downstream_server_id).filter(Boolean))
      return { workspace: ws, rules: wsRules, enabledDownstreamIds: enabledIds }
    })

    // Sort: workspaces with rules first, then alphabetical
    result.sort((a, b) => {
      if (a.rules.length > 0 && b.rules.length === 0) return -1
      if (a.rules.length === 0 && b.rules.length > 0) return 1
      return a.workspace.name.localeCompare(b.workspace.name)
    })

    return result
  }, [workspaces, routes])

  function toggleExpand(wsId: string) {
    setExpandedWs((prev) => {
      const next = new Set(prev)
      if (next.has(wsId)) next.delete(wsId)
      else next.add(wsId)
      return next
    })
  }

  function openCreate(prefillWorkspaceId?: string) {
    setEditing(null)
    setForm({ ...emptyForm, workspace_id: prefillWorkspaceId ?? '' })
    setSaveError(null)
    setDialogOpen(true)
  }

  function openEdit(r: RouteRule) {
    setEditing(r)
    const tm = Array.isArray(r.tool_match) ? (r.tool_match as string[]) : []
    const normalizedToolMatch = tm.length === 1 && tm[0] === '*' ? [] : tm
    setForm({
      name: r.name || '',
      priority: r.priority,
      workspace_id: r.workspace_id,
      path_glob: r.path_glob || '**',
      tool_match: normalizedToolMatch,
      scope_policy: r.scope_policy ?? {},
      downstream_server_id: r.downstream_server_id,
      auth_scope_id: r.auth_scope_id,
      policy: r.policy,
      log_level: r.log_level,
      approval_mode: r.approval_mode ?? 'none',
      approval_timeout: r.approval_timeout ?? 300,
    })
    setSaveError(null)
    setDialogOpen(true)
  }

  async function handleSave() {
    setSaving(true)
    setSaveError(null)
    try {
      if (editing) {
        await updateRoute(editing.id, form)
      } else {
        await createRoute(form)
      }
      setDialogOpen(false)
      toast.success(editing ? 'Route updated' : 'Route created')
      refetch()
    } catch (err: unknown) {
      setSaveError(err instanceof Error ? err.message : 'Failed to save route rule')
    } finally {
      setSaving(false)
    }
  }

  async function confirmDelete() {
    if (!deleteTarget) return
    try {
      await deleteRoute(deleteTarget.id)
      setDeleteTarget(null)
      toast.success('Route deleted')
      refetch()
    } catch (err: unknown) {
      toast.error(err instanceof Error ? err.message : 'Failed to delete route rule')
    }
  }

  return (
    <div className="space-y-6">
      <div className="space-y-1">
        <h2 className="text-lg font-semibold">Route Rules</h2>
        <p className="max-w-2xl text-sm text-muted-foreground">
          Each rule says: for this workspace, when a tool matches these patterns,
          use this server and credential. Rules are evaluated by priority; first
          match wins.
        </p>
      </div>

      <div className="flex items-center justify-end">
        <Button
          variant="outline"
          onClick={handleReload}
          disabled={reloading}
          data-testid="route-reload"
          title="Apply route changes immediately (normally cached until restart)"
        >
          <RotateCw className={`mr-2 h-4 w-4 ${reloading ? 'animate-spin' : ''}`} />
          {reloading ? 'Reloading…' : 'Reload'}
        </Button>
      </div>

      {loading && !routes && (
        <div className="flex items-center gap-2 text-muted-foreground">
          <div className="h-2 w-2 rounded-full bg-primary/60" />
          Loading...
        </div>
      )}
      {error && <p className="text-destructive">Error: {error}</p>}

      {groups.length > 0 && (
        <div className="space-y-3">
          {groups.map((g) => (
            <RouteWorkspaceGroup
              key={g.workspace.id}
              workspace={g.workspace}
              rules={g.rules}
              expanded={expandedWs.has(g.workspace.id)}
              onToggle={() => toggleExpand(g.workspace.id)}
              onEnableServers={() => setBulkWorkspace(g.workspace)}
              onAddRule={() => openCreate(g.workspace.id)}
              onEditRule={openEdit}
              onDeleteRule={setDeleteTarget}
              downstreams={downstreams ?? []}
              authScopes={authScopes ?? []}
              highlightRouteId={highlightRouteId}
              highlightWorkspace={highlightWorkspaceId === g.workspace.id}
            />
          ))}
        </div>
      )}

      {workspaces && routes && groups.length === 0 && (
        <div className="text-center py-12 text-muted-foreground">
          <p className="text-sm">No route rules configured yet.</p>
          <p className="text-xs mt-1">Create a workspace, then add a route to connect a server to it.</p>
        </div>
      )}

      <div className="flex justify-end">
        <Button onClick={() => openCreate()} data-testid="route-add">
          <Plus className="mr-2 h-4 w-4" />
          Add Route
        </Button>
      </div>

      <RouteDialog
        open={dialogOpen}
        onClose={() => setDialogOpen(false)}
        form={form}
        setForm={setForm}
        onSave={handleSave}
        saving={saving}
        editing={!!editing}
        workspaces={workspaces ?? []}
        downstreams={downstreams ?? []}
        authScopes={authScopes ?? []}
        saveError={saveError}
      />

      {bulkWorkspace && (
        <BulkEnableDialog
          open={!!bulkWorkspace}
          onClose={() => setBulkWorkspace(null)}
          workspace={bulkWorkspace}
          downstreams={downstreams ?? []}
          enabledDownstreamIds={
            groups.find((g) => g.workspace.id === bulkWorkspace.id)?.enabledDownstreamIds ?? new Set()
          }
          onSuccess={refetch}
        />
      )}

      <ConfirmDialog
        open={!!deleteTarget}
        onOpenChange={(open) => !open && setDeleteTarget(null)}
        title="Delete route rule"
        description="Are you sure you want to delete this route rule?"
        confirmLabel="Delete"
        variant="destructive"
        onConfirm={confirmDelete}
      />
    </div>
  )
}
