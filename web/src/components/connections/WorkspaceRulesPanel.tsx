import { useEffect, useMemo, useState } from 'react'
import { ChevronDown, ChevronRight, RotateCw } from 'lucide-react'
import { useSearchParams } from 'react-router-dom'
import { toast } from 'sonner'
import { createRoute, deleteRoute, flushCache, updateRoute } from '@/api/client'
import type { AuthScope, DownstreamServer, RouteRule, Workspace } from '@/api/types'
import { Button } from '@/components/ui/button'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import { newRouteFormForWorkspace, routeToForm, type RouteFormData } from '@/components/routes/route-form-model'
import { BulkEnableDialog } from '@/pages/config/BulkEnableDialog'
import { RouteDialog } from '@/pages/config/RouteDialog'
import { RouteWorkspaceGroup } from '@/pages/config/RouteWorkspaceGroup'

export function WorkspaceRulesPanel({
  workspace,
  rules,
  downstreams,
  authScopes,
  open,
  onOpenChange,
  onChanged,
}: {
  workspace: Workspace
  rules: RouteRule[]
  downstreams: DownstreamServer[]
  authScopes: AuthScope[]
  open: boolean
  onOpenChange: (open: boolean) => void
  onChanged: () => void
}) {
  const [dialogOpen, setDialogOpen] = useState(false)
  const [editing, setEditing] = useState<RouteRule | null>(null)
  const [form, setForm] = useState<RouteFormData>(() => newRouteFormForWorkspace(workspace.id))
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)
  const [deleteTarget, setDeleteTarget] = useState<RouteRule | null>(null)
  const [bulkOpen, setBulkOpen] = useState(false)
  const [reloading, setReloading] = useState(false)
  const [searchParams, setSearchParams] = useSearchParams()

  const enabledDownstreamIds = useMemo(
    () => new Set(rules.map((rule) => rule.downstream_server_id).filter(Boolean)),
    [rules],
  )

  useEffect(() => {
    const routeId = searchParams.get('route')
    if (!routeId) return
    const route = rules.find((item) => item.id === routeId)
    if (!route) return
    onOpenChange(true)
    setEditing(route)
    setForm(routeToForm(route))
    setSaveError(null)
    setDialogOpen(true)
    setSearchParams((previous) => {
      const next = new URLSearchParams(previous)
      next.delete('route')
      return next
    }, { replace: true })
  }, [onOpenChange, rules, searchParams, setSearchParams])

  function openCreate() {
    setEditing(null)
    setForm(newRouteFormForWorkspace(workspace.id))
    setSaveError(null)
    setDialogOpen(true)
  }

  function openEdit(route: RouteRule) {
    setEditing(route)
    setForm(routeToForm(route))
    setSaveError(null)
    setDialogOpen(true)
  }

  async function save() {
    setSaving(true)
    setSaveError(null)
    try {
      if (editing) await updateRoute(editing.id, form)
      else await createRoute(form)
      toast.success(editing ? 'Access rule updated' : 'Access rule created')
      setDialogOpen(false)
      onChanged()
    } catch (error) {
      setSaveError(error instanceof Error ? error.message : 'Failed to save access rule')
    } finally {
      setSaving(false)
    }
  }

  async function remove() {
    if (!deleteTarget) return
    try {
      await deleteRoute(deleteTarget.id)
      toast.success('Access rule deleted')
      setDeleteTarget(null)
      onChanged()
    } catch (error) {
      toast.error(error instanceof Error ? error.message : 'Failed to delete access rule')
    }
  }

  async function reload() {
    setReloading(true)
    try {
      await flushCache('all')
      toast.success('Access rules reloaded')
      onChanged()
    } catch (error) {
      toast.error(error instanceof Error ? error.message : 'Failed to reload access rules')
    } finally {
      setReloading(false)
    }
  }

  return (
    <section className="border border-border/60 bg-card/20" data-testid="workspace-advanced-rules">
      <div className="flex items-center gap-2">
        <button
          type="button"
          className="flex min-w-0 flex-1 items-center gap-2 px-4 py-3 text-left hover:bg-muted/30"
          onClick={() => onOpenChange(!open)}
          aria-expanded={open}
        >
          {open ? <ChevronDown className="h-4 w-4" /> : <ChevronRight className="h-4 w-4" />}
          <span className="text-sm font-semibold">Advanced rules</span>
          <span className="text-xs text-muted-foreground">{rules.length} total</span>
        </button>
        <Button variant="ghost" size="sm" className="mr-2" onClick={() => void reload()} disabled={reloading}>
          <RotateCw className={`mr-1.5 h-3.5 w-3.5 ${reloading ? 'animate-spin' : ''}`} />
          Reload
        </Button>
      </div>
      {open && (
        <div className="border-t border-border/60 p-3">
          <p className="mb-3 text-xs text-muted-foreground">
            Power-user view. Every allow or deny rule is shown, including path, tool, credential, approval, and scope restrictions.
          </p>
          <RouteWorkspaceGroup
            workspace={workspace}
            rules={rules}
            expanded
            onToggle={() => {}}
            onEnableServers={() => setBulkOpen(true)}
            onAddRule={openCreate}
            onEditRule={openEdit}
            onDeleteRule={setDeleteTarget}
            downstreams={downstreams}
            authScopes={authScopes}
          />
        </div>
      )}

      <RouteDialog
        open={dialogOpen}
        onClose={() => setDialogOpen(false)}
        form={form}
        setForm={setForm}
        onSave={() => void save()}
        onDelete={editing ? () => { setDialogOpen(false); setDeleteTarget(editing) } : undefined}
        saving={saving}
        editing={Boolean(editing)}
        workspaces={[workspace]}
        downstreams={downstreams}
        authScopes={authScopes}
        saveError={saveError}
      />

      {bulkOpen && (
        <BulkEnableDialog
          open
          onClose={() => setBulkOpen(false)}
          workspace={workspace}
          downstreams={downstreams}
          enabledDownstreamIds={enabledDownstreamIds}
          onSuccess={onChanged}
        />
      )}

      <ConfirmDialog
        open={Boolean(deleteTarget)}
        onOpenChange={(next) => !next && setDeleteTarget(null)}
        title="Delete access rule"
        description="Delete this access rule? New tool calls will stop matching it."
        confirmLabel="Delete"
        variant="destructive"
        onConfirm={() => void remove()}
      />
    </section>
  )
}
