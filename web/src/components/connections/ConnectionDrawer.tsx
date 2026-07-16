// ConnectionDrawer — right-side Sheet that opens when the user clicks
// a cell in the connections matrix. Pre-bound to (server, workspace);
// renders one form that creates / edits a single route_rule.
//
// Why a Sheet instead of a modal: the user often wants to glance at
// several cells in sequence. The Sheet lets them dismiss + retap a
// neighbour without losing matrix state.
//
// Inline credential creation is handled directly in the drawer via
// the <InlineCredentialCreate> component — no navigation required.

import { useCallback, useEffect, useMemo, useState } from 'react'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from '@/components/ui/sheet'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import {
  createRoute,
  deleteRoute,
  listOAuthProviders,
  updateRoute,
} from '@/api/client'
import type { AuthScope, DownstreamServer, OAuthProvider, RouteRule, Workspace } from '@/api/types'
import { Trash2 } from 'lucide-react'
import { toast } from 'sonner'
import type { CellState } from './ConnectionCell'
import { describeState } from './drawer-utils'
import { ConnectionCredentialExtras } from './ConnectionCredentialExtras'
import { RouteRuleFormFields } from '@/components/routes/RouteRuleFormFields'
import {
  routeFormForConnection,
  type RouteFormData,
} from '@/components/routes/route-form-model'

export function ConnectionDrawer({
  open,
  server,
  workspace,
  state,
  route,
  routes,
  scopes,
  workspaces,
  downstreams,
  onClose,
  onChanged,
}: {
  open: boolean
  server: DownstreamServer | null
  workspace: Workspace | null
  state: CellState | null
  // The existing route for (server, workspace), if any. Used to
  // pre-populate the form when editing.
  route: RouteRule | null
  // Every rule for this server/workspace pair. The compact workspace view
  // must never hide additional path/tool-specific rules.
  routes: RouteRule[]
  scopes: AuthScope[]
  workspaces: Workspace[]
  downstreams: DownstreamServer[]
  onClose: () => void
  // Fires after a successful create / update / delete so the parent
  // can refetch the matrix slice.
  onChanged: () => void
}) {
  const [form, setForm] = useState<RouteFormData>(() =>
    routeFormForConnection({ route: null, server: null, workspace: null }),
  )
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)
  const [deleting, setDeleting] = useState(false)
  const [providers, setProviders] = useState<OAuthProvider[]>([])
  const [activeRoute, setActiveRoute] = useState<RouteRule | null>(route)

  // Fetch OAuth providers so the inline create form can offer the oauth2 path.
  useEffect(() => {
    if (!open) return
    listOAuthProviders()
      .then(setProviders)
      .catch(() => setProviders([]))
  }, [open])

  useEffect(() => {
    if (!open) return
    setActiveRoute(route ?? routes[0] ?? null)
  }, [open, route, routes])

  // Reset form whenever the selected rule or (server, workspace) pair changes.
  useEffect(() => {
    if (!open) return
    setSaveError(null)
    setForm(routeFormForConnection({ route: activeRoute, server, workspace }))
  }, [open, activeRoute, server, workspace])

  const selectedScope = useMemo(
    () => scopes.find((s) => s.id === form.auth_scope_id) ?? null,
    [scopes, form.auth_scope_id],
  )
  const selectedDownstream = useMemo(
    () => downstreams.find((item) => item.id === form.downstream_server_id) ?? server,
    [downstreams, form.downstream_server_id, server],
  )
  const selectedWorkspace = useMemo(
    () => workspaces.find((item) => item.id === form.workspace_id) ?? workspace,
    [workspaces, form.workspace_id, workspace],
  )

  const handleSave = useCallback(async () => {
    if (!server || !workspace) return
    setSaving(true)
    setSaveError(null)
    try {
      if (activeRoute) {
        await updateRoute(activeRoute.id, form)
        toast.success('Route updated')
      } else {
        await createRoute(form)
        toast.success(
          `Connected ${selectedDownstream?.name ?? server.name} → ${selectedWorkspace?.name ?? workspace.name}`,
        )
      }
      onChanged()
      onClose()
    } catch (err) {
      const message = err instanceof Error ? err.message : 'Failed to save route'
      setSaveError(message)
      toast.error(message)
    } finally {
      setSaving(false)
    }
  }, [server, workspace, activeRoute, form, selectedDownstream, selectedWorkspace, onChanged, onClose])

  const handleDelete = useCallback(async () => {
    if (!activeRoute) return
    setDeleting(true)
    try {
      await deleteRoute(activeRoute.id)
      toast.success('Route removed')
      onChanged()
      onClose()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to remove route')
    } finally {
      setDeleting(false)
    }
  }, [activeRoute, onChanged, onClose])

  const handleInlineCreated = useCallback(
    (scope: AuthScope) => {
      // The parent's onChanged will refetch scopes; optimistically select
      // the newly-created scope so the user doesn't have to pick it.
      setForm((current) => ({ ...current, auth_scope_id: scope.id }))
      onChanged()
    },
    [onChanged],
  )

  const title =
    selectedDownstream && selectedWorkspace
      ? `${selectedDownstream.name} → ${selectedWorkspace.name}`
      : 'Connection'
  const stateLine = describeState(state)

  return (
    <Sheet open={open} onOpenChange={(o) => !o && onClose()}>
      <SheetContent className="w-full max-w-full sm:max-w-2xl" data-testid="connection-drawer">
        <SheetHeader>
          <SheetTitle>{activeRoute ? 'Edit access rule' : 'Connect server'}</SheetTitle>
          <SheetDescription>{stateLine}</SheetDescription>
        </SheetHeader>

        <div className="mt-6 space-y-5 px-4">
          <div className="flex flex-wrap items-center gap-2">
            <Badge variant="outline" className="font-mono text-xs">
              {title}
            </Badge>
            {activeRoute && (
              <Badge variant="secondary" className="font-mono text-[10px]">
                {activeRoute.id}
              </Badge>
            )}
          </div>

          {routes.length > 0 && (
            <div className="space-y-2 border-b border-border/50 pb-4">
              <div className="flex items-center justify-between gap-3">
                <Label className="text-xs text-muted-foreground">
                  Access rule {routes.length > 1 ? `(${routes.length} for this server)` : ''}
                </Label>
                <button
                  type="button"
                  className="text-xs text-primary hover:underline"
                  onClick={() => setActiveRoute(null)}
                >
                  Add another rule
                </button>
              </div>
              <Select
                value={activeRoute?.id ?? '__new__'}
                onValueChange={(id) => {
                  setActiveRoute(id === '__new__' ? null : routes.find((item) => item.id === id) ?? null)
                }}
              >
                <SelectTrigger data-testid="connection-drawer-rule-select">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {routes.map((item) => (
                    <SelectItem key={item.id} value={item.id}>
                      {item.name || item.path_glob || item.id} · {item.policy}
                    </SelectItem>
                  ))}
                  <SelectItem value="__new__">New access rule</SelectItem>
                </SelectContent>
              </Select>
            </div>
          )}

          <RouteRuleFormFields
            form={form}
            setForm={setForm}
            visible={open}
            resetKey={activeRoute?.id ?? `${server?.id ?? 'server'}:${workspace?.id ?? 'workspace'}:new`}
            workspaces={workspaces}
            downstreams={downstreams}
            authScopes={scopes}
            saveError={saveError}
            authScopeExtras={
              form.policy === 'allow' ? (
                <ConnectionCredentialExtras
                  scope={selectedScope}
                  server={selectedDownstream}
                  providers={providers}
                  onCreated={handleInlineCreated}
                  onChanged={onChanged}
                />
              ) : null
            }
          />

          <div className="flex flex-wrap items-center justify-between gap-2 pt-2">
            <div>
              {activeRoute && (
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  className="text-destructive hover:bg-destructive/10"
                  onClick={handleDelete}
                  disabled={deleting}
                  data-testid="connection-drawer-delete"
                >
                  <Trash2 className="mr-1.5 h-3 w-3" />
                  {deleting ? 'Removing...' : 'Remove route'}
                </Button>
              )}
            </div>
            <div className="flex gap-2">
              <Button type="button" variant="ghost" onClick={onClose} disabled={saving}>
                Cancel
              </Button>
              <Button
                type="button"
                onClick={handleSave}
                disabled={
                  saving ||
                  !server ||
                  !workspace ||
                  !form.workspace_id ||
                  (form.policy === 'allow' && !form.downstream_server_id)
                }
                data-testid="connection-drawer-save"
              >
                {saving ? 'Saving...' : activeRoute ? 'Save changes' : routes.length > 0 ? 'Add rule' : 'Connect'}
              </Button>
            </div>
          </div>
        </div>
      </SheetContent>
    </Sheet>
  )
}
