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
import { Label } from '@/components/ui/label'
import { Badge } from '@/components/ui/badge'
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
  getOAuthAuthorizeURL,
  listOAuthProviders,
  updateRoute,
} from '@/api/client'
import type { AuthScope, DownstreamServer, OAuthProvider, RouteRule, Workspace } from '@/api/types'
import { ExternalLink, Trash2 } from 'lucide-react'
import { toast } from 'sonner'
import { scopeLabel } from '@/lib/scope-label'
import { redirectToOAuth } from '@/lib/safe-redirect'
import type { CellState } from './ConnectionCell'
import { describeState, filterCompatibleScopes } from './drawer-utils'
import { InlineCredentialCreate } from './InlineCredentialCreate'

const NO_SCOPE = '__none__'

export function ConnectionDrawer({
  open,
  server,
  workspace,
  state,
  route,
  scopes,
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
  onClose: () => void
  // Fires after a successful create / update / delete so the parent
  // can refetch the matrix slice.
  onChanged: () => void
}) {
  const [scopeId, setScopeId] = useState<string>(NO_SCOPE)
  const [saving, setSaving] = useState(false)
  const [deleting, setDeleting] = useState(false)
  const [providers, setProviders] = useState<OAuthProvider[]>([])

  // Fetch OAuth providers so the inline create form can offer the oauth2 path.
  useEffect(() => {
    if (!open) return
    listOAuthProviders()
      .then(setProviders)
      .catch(() => setProviders([]))
  }, [open])

  // Reset form whenever the (server, workspace) pair changes.
  useEffect(() => {
    if (!open) return
    setScopeId(route?.auth_scope_id ? route.auth_scope_id : NO_SCOPE)
  }, [open, route?.auth_scope_id, server?.id, workspace?.id])

  const compatibleScopes = useMemo(
    () => filterCompatibleScopes(scopes, server),
    [scopes, server],
  )

  const selectedScope = useMemo(
    () => scopes.find((s) => s.id === scopeId) ?? null,
    [scopes, scopeId],
  )

  const handleSave = useCallback(async () => {
    if (!server || !workspace) return
    setSaving(true)
    try {
      if (route) {
        await updateRoute(route.id, {
          auth_scope_id: scopeId === NO_SCOPE ? '' : scopeId,
        })
        toast.success('Route updated')
      } else {
        await createRoute({
          name: `${workspace.name} → ${server.name}`,
          priority: 100,
          workspace_id: workspace.id,
          path_glob: '**',
          tool_match: [`${server.tool_namespace}__*`],
          scope_policy: {},
          downstream_server_id: server.id,
          auth_scope_id: scopeId === NO_SCOPE ? '' : scopeId,
          policy: 'allow',
          log_level: 'info',
          approval_mode: 'write',
          approval_timeout: 0,
        })
        toast.success(`Connected ${server.name} → ${workspace.name}`)
      }
      onChanged()
      onClose()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Failed to save route')
    } finally {
      setSaving(false)
    }
  }, [server, workspace, route, scopeId, onChanged, onClose])

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
      setScopeId(scope.id)
      onChanged()
    },
    [onChanged],
  )

  const title =
    server && workspace ? `${server.name} → ${workspace.name}` : 'Connection'
  const stateLine = describeState(state)

  return (
    <Sheet open={open} onOpenChange={(o) => !o && onClose()}>
      <SheetContent className="w-full max-w-md sm:max-w-lg">
        <SheetHeader>
          <SheetTitle>{title}</SheetTitle>
          <SheetDescription>{stateLine}</SheetDescription>
        </SheetHeader>

        <div className="mt-6 space-y-5 px-4">
          <div className="space-y-2">
            <Label className="text-xs text-muted-foreground">Server</Label>
            <Badge variant="outline" className="font-mono text-xs">
              {server?.name ?? '—'}
            </Badge>
          </div>
          <div className="space-y-2">
            <Label className="text-xs text-muted-foreground">Workspace</Label>
            <Badge variant="outline" className="font-mono text-xs">
              {workspace?.name ?? '—'}
            </Badge>
          </div>

          <div className="space-y-2">
            <Label className="text-xs text-muted-foreground">Credential</Label>
            <Select value={scopeId} onValueChange={setScopeId}>
              <SelectTrigger data-testid="connection-drawer-scope">
                <SelectValue placeholder="Pick credentials..." />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={NO_SCOPE}>None (no auth required)</SelectItem>
                {compatibleScopes.map((s) => (
                  <SelectItem key={s.id} value={s.id}>
                    {scopeLabel(s)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>

            {/* Inline credential creation — no navigation required */}
            <InlineCredentialCreate
              server={server}
              providers={providers}
              onCreated={handleInlineCreated}
              onCancel={() => {}}
            />
          </div>

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
                disabled={saving || !server || !workspace}
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
