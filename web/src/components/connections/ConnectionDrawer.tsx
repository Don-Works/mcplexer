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
import {
  createRoute,
  deleteRoute,
  getOAuthAuthorizeURL,
  listOAuthProviders,
  updateRoute,
} from '@/api/client'
import type { AuthScope, DownstreamServer, OAuthProvider, RouteRule, Workspace } from '@/api/types'
import { ExternalLink, Trash2 } from 'lucide-react'
import { toast } from 'sonner'
import { redirectToOAuth } from '@/lib/safe-redirect'
import type { CellState } from './ConnectionCell'
import { describeState } from './drawer-utils'
import { InlineCredentialCreate } from './InlineCredentialCreate'
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

  // Fetch OAuth providers so the inline create form can offer the oauth2 path.
  useEffect(() => {
    if (!open) return
    listOAuthProviders()
      .then(setProviders)
      .catch(() => setProviders([]))
  }, [open])

  // Reset form whenever the selected route or (server, workspace) pair changes.
  useEffect(() => {
    if (!open) return
    setSaveError(null)
    setForm(routeFormForConnection({ route, server, workspace }))
  }, [open, route, server, workspace])

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
      if (route) {
        await updateRoute(route.id, form)
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
  }, [server, workspace, route, form, selectedDownstream, selectedWorkspace, onChanged, onClose])

  const handleDelete = useCallback(async () => {
    if (!route) return
    setDeleting(true)
    try {
      await deleteRoute(route.id)
      toast.success('Route removed')
      onChanged()
      onClose()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to remove route')
    } finally {
      setDeleting(false)
    }
  }, [route, onChanged, onClose])

  const handleAuthenticate = useCallback(async () => {
    if (!selectedScope || selectedScope.type !== 'oauth2') return
    try {
      const { authorize_url } = await getOAuthAuthorizeURL(selectedScope.id)
      redirectToOAuth(authorize_url)
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to start auth')
    }
  }, [selectedScope])

  const handleAddSecrets = useCallback(() => {
    if (!selectedScope) return
    // Scroll to the credential in the config page (deep-link fallback for
    // scopes that already exist — the inline create covers the new-cred path).
    window.location.href = `/advanced/credentials?credential=${selectedScope.id}`
  }, [selectedScope])

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
          <SheetTitle>{route ? 'Edit route' : 'Connect server'}</SheetTitle>
          <SheetDescription>{stateLine}</SheetDescription>
        </SheetHeader>

        <div className="mt-6 space-y-5 px-4">
          <div className="flex flex-wrap items-center gap-2">
            <Badge variant="outline" className="font-mono text-xs">
              {title}
            </Badge>
            {route && (
              <Badge variant="secondary" className="font-mono text-[10px]">
                {route.id}
              </Badge>
            )}
          </div>

          <RouteRuleFormFields
            form={form}
            setForm={setForm}
            visible={open}
            resetKey={route?.id ?? `${server?.id ?? 'server'}:${workspace?.id ?? 'workspace'}:new`}
            workspaces={workspaces}
            downstreams={downstreams}
            authScopes={scopes}
            saveError={saveError}
            authScopeExtras={
              form.policy === 'allow' ? (
                <div className="space-y-3">
                  <InlineCredentialCreate
                    server={selectedDownstream}
                    providers={providers}
                    onCreated={handleInlineCreated}
                    onCancel={() => {}}
                  />

                  {selectedScope && selectedScope.type === 'oauth2' && (
                    <div className="rounded-md border border-border/40 bg-muted/20 px-3 py-2.5 text-sm">
                      <div className="flex items-center justify-between">
                        <span className="text-muted-foreground">OAuth session</span>
                        <Button
                          variant="outline"
                          size="sm"
                          onClick={handleAuthenticate}
                          data-testid="connection-drawer-authenticate"
                        >
                          <ExternalLink className="mr-1.5 h-3 w-3" />
                          Authenticate
                        </Button>
                      </div>
                    </div>
                  )}

                  {selectedScope &&
                    (selectedScope.type === 'env' ||
                      selectedScope.type === 'header' ||
                      selectedScope.type === 'hawk' ||
                      selectedScope.type === 'client_credentials') &&
                    !selectedScope.has_secrets && (
                      <div className="rounded-md border border-amber-500/30 bg-amber-500/5 px-3 py-2.5 text-sm">
                        <div className="flex items-center justify-between gap-3">
                          <span className="text-amber-700">No secrets stored yet</span>
                          <Button
                            variant="outline"
                            size="sm"
                            onClick={handleAddSecrets}
                            data-testid="connection-drawer-add-secrets"
                          >
                            Add secrets
                          </Button>
                        </div>
                      </div>
                    )}
                </div>
              ) : null
            }
          />

          <div className="flex flex-wrap items-center justify-between gap-2 pt-2">
            <div>
              {route && (
                <Button
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
              <Button variant="ghost" onClick={onClose} disabled={saving}>
                Cancel
              </Button>
              <Button
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
                {saving ? 'Saving...' : route ? 'Save changes' : 'Connect'}
              </Button>
            </div>
          </div>
        </div>
      </SheetContent>
    </Sheet>
  )
}
