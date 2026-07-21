import { useCallback, useEffect, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { formatRelativeTime } from '@/lib/utils'
import { Card, CardContent } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { Badge } from '@/components/ui/badge'
import { useApi } from '@/hooks/use-api'
import {
  createAuthScope,
  deleteAuthScope,
  getOAuthAuthorizeURL,
  getOAuthStatus,
  listAuthScopes,
  listOAuthProviders,
  revokeOAuthToken,
  updateAuthScope,
} from '@/api/client'
import type { AuthScope, OAuthStatus } from '@/api/types'
import { Copy, ExternalLink, Key, Lock, Pencil, Plus, Trash2, Unplug } from 'lucide-react'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { toast } from 'sonner'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import { AuthScopeDialog, emptyAuthScopeForm } from './AuthScopeDialog'
import type { AuthScopeFormData } from './AuthScopeDialog'
import { redirectToOAuth } from '@/lib/safe-redirect'
import { scopeLabel } from '@/lib/scope-label'

export function AuthScopesPage() {
  const fetcher = useCallback(() => listAuthScopes(), [])
  const { data, loading, error, refetch } = useApi(fetcher)

  const providersFetcher = useCallback(() => listOAuthProviders(), [])
  const { data: providers } = useApi(providersFetcher)

  const [dialogOpen, setDialogOpen] = useState(false)
  const [editing, setEditing] = useState<AuthScope | null>(null)
  const [form, setForm] = useState<AuthScopeFormData>(emptyAuthScopeForm)
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)
  const [deleteTarget, setDeleteTarget] = useState<AuthScope | null>(null)

  // Deep link: /config?tab=credentials&credential=<id> from the command
  // palette. Scrolls the matching row into view + pulses a highlight so
  // the user lands on the right credential. UI-6 fix.
  const [searchParams, setSearchParams] = useSearchParams()
  const [highlightScopeId, setHighlightScopeId] = useState<string | null>(null)
  useEffect(() => {
    const target = searchParams.get('credential')
    if (!target) return
    setHighlightScopeId(target)
    const scrollTimer = setTimeout(() => {
      const el = document.querySelector<HTMLElement>(`[data-credential-id="${target}"]`)
      el?.scrollIntoView({ behavior: 'smooth', block: 'center' })
    }, 50)
    const clearParamTimer = setTimeout(() => {
      setSearchParams(
        (prev) => {
          const next = new URLSearchParams(prev)
          next.delete('credential')
          return next
        },
        { replace: true },
      )
    }, 200)
    const clearHighlightTimer = setTimeout(() => setHighlightScopeId(null), 3500)
    return () => {
      clearTimeout(scrollTimer)
      clearTimeout(clearParamTimer)
      clearTimeout(clearHighlightTimer)
    }
  }, [searchParams, setSearchParams])

  function openCreate() {
    setEditing(null)
    setForm(emptyAuthScopeForm)
    setSaveError(null)
    setDialogOpen(true)
  }

  function openEdit(scope: AuthScope) {
    setEditing(scope)
    setForm({
      name: scope.name,
      display_name: scope.display_name ?? '',
      type: scope.type,
      oauth_provider_id: scope.oauth_provider_id ?? '',
      redaction_hints: [...(scope.redaction_hints ?? [])],
    })
    setSaveError(null)
    setDialogOpen(true)
  }

  async function handleSave() {
    setSaving(true)
    setSaveError(null)
    try {
      if (editing) {
        await updateAuthScope(editing.id, form)
        setDialogOpen(false)
        toast.success('Credential updated')
      } else {
        const created = await createAuthScope(form)
        if (created.type === 'env' || created.type === 'header' || created.type === 'hawk' || created.type === 'client_credentials') {
          // Transition to edit mode so the user can immediately add secrets in
          // the full dialog (the AuthScopeDialog shows the secrets section when
          // editing is set). This is the happy path for users who open the
          // credentials page directly; the Connection drawer uses
          // InlineCredentialCreate which seeds secrets before closing.
          setEditing(created)
          toast.success('Credential created — add your secrets below')
        } else {
          setDialogOpen(false)
          toast.success('Credential created')
        }
      }
      refetch()
    } catch (err: unknown) {
      setSaveError(err instanceof Error ? err.message : 'Failed to save credential')
    } finally {
      setSaving(false)
    }
  }

  async function confirmDelete() {
    if (!deleteTarget) return
    try {
      await deleteAuthScope(deleteTarget.id)
      setDeleteTarget(null)
      toast.success('Credential deleted')
      refetch()
    } catch (err: unknown) {
      toast.error(err instanceof Error ? err.message : 'Failed to delete credential')
    }
  }

  function handleDuplicate(scope: AuthScope) {
    setEditing(null)
    setForm({
      name: `${scopeLabel(scope)}_copy`,
      display_name: scope.display_name ? `${scope.display_name} (copy)` : '',
      type: scope.type,
      oauth_provider_id: scope.oauth_provider_id ?? '',
      redaction_hints: [...(scope.redaction_hints ?? [])],
    })
    setDialogOpen(true)
  }

  async function handleAuthenticate(scopeId: string) {
    try {
      const { authorize_url } = await getOAuthAuthorizeURL(scopeId)
      redirectToOAuth(authorize_url)
    } catch (err: unknown) {
      toast.error(err instanceof Error ? err.message : 'Failed to start authentication')
    }
  }

  async function handleRevoke(scopeId: string) {
    try {
      await revokeOAuthToken(scopeId)
      toast.success('Token revoked')
      refetch()
    } catch (err: unknown) {
      toast.error(err instanceof Error ? err.message : 'Failed to revoke token')
    }
  }

  return (
    <div className="space-y-6">
      <div className="flex flex-col gap-3 sm:flex-row sm:items-end sm:justify-between">
        <p className="max-w-2xl text-sm text-muted-foreground">
          Reusable auth material for downstream servers: environment variables for stdio
          servers, HTTP headers for remote servers, or OAuth sessions for supported
          integrations.
        </p>
        <Button onClick={openCreate} data-testid="auth-scope-add">
          <Plus className="mr-2 h-4 w-4" />
          Add Credential
        </Button>
      </div>

      <div
        className="flex flex-wrap items-center gap-3 text-[11px] text-muted-foreground"
        data-testid="credentials-legend"
      >
        <span className="font-semibold uppercase tracking-wider">Status</span>
        <span className="inline-flex items-center gap-1.5">
          <span className="h-1.5 w-1.5 rounded-full bg-emerald-500" />
          Connected
          <span className="text-muted-foreground/50">— OAuth session valid</span>
        </span>
        <span className="inline-flex items-center gap-1.5">
          <span className="h-1.5 w-1.5 rounded-full bg-sky-500" />
          Configured
          <span className="text-muted-foreground/50">— env / header set</span>
        </span>
        <span className="inline-flex items-center gap-1.5">
          <span className="h-1.5 w-1.5 rounded-full bg-amber-500" />
          Needs Setup
          <span className="text-muted-foreground/50">— action required</span>
        </span>
        <span className="inline-flex items-center gap-1.5">
          <span className="h-1.5 w-1.5 rounded-full bg-muted-foreground/50" />
          Not Connected
          <span className="text-muted-foreground/50">— OAuth not authorized</span>
        </span>
      </div>

      <Card>
        <CardContent className="pt-6">
          {loading && !data && (
            <div className="flex items-center gap-2 text-muted-foreground">
              <div className="h-2 w-2 rounded-full bg-primary/60" />
              Loading...
            </div>
          )}
          {error && <p className="text-destructive">Error: {error}</p>}
          {data && (
            <Table>
              <TableHeader>
                <TableRow className="border-border/50 hover:bg-transparent">
                  <TableHead>Name</TableHead>
                  <TableHead>Type</TableHead>
                  <TableHead className="hidden sm:table-cell">Status</TableHead>
                  <TableHead className="hidden md:table-cell">Redaction Hints</TableHead>
                  <TableHead>Actions</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {data.length === 0 ? (
                  <TableRow>
                    <TableCell colSpan={5} className="h-32">
                      <div className="flex flex-col items-center justify-center text-muted-foreground">
                        <Lock className="mb-2 h-8 w-8 text-muted-foreground/50" />
                        <p className="text-sm">No credentials configured</p>
                        <button onClick={openCreate} className="text-xs text-primary hover:underline">
                          Add a credential to get started
                        </button>
                      </div>
                    </TableCell>
                  </TableRow>
                ) : (
                  data.map((scope) => (
                    <TableRow
                      key={scope.id}
                      data-credential-id={scope.id}
                      className={
                        'border-border/30 hover:bg-muted/30 ' +
                        (highlightScopeId === scope.id
                          ? 'ring-2 ring-amber-400/80 ring-offset-2 ring-offset-background'
                          : '')
                      }
                    >
                      <TableCell className="font-medium">
                        <div className="flex flex-col gap-0.5">
                          <span data-testid={`auth-scope-name-${scope.id}`}>
                            {scopeLabel(scope)}
                          </span>
                          {scopeLabel(scope) !== scope.name && (
                            <code
                              className="font-mono text-[10px] text-muted-foreground/70"
                              data-testid={`auth-scope-slug-${scope.id}`}
                            >
                              {scope.name}
                            </code>
                          )}
                        </div>
                      </TableCell>
                      <TableCell>
                        <Badge variant="outline" className="font-mono text-xs">
                          {scope.type}
                        </Badge>
                      </TableCell>
                      <TableCell className="hidden sm:table-cell">
                        {scope.type === 'oauth2' ? (
                          <OAuthStatusBadge scopeId={scope.id} />
                        ) : scope.type === 'env' || scope.type === 'header' || scope.type === 'hawk' || scope.type === 'client_credentials' ? (
                          <Badge
                            variant="outline"
                            className={`text-xs ${scope.has_secrets ? 'bg-emerald-500/10 text-emerald-600 border-emerald-500/20' : 'bg-amber-500/10 text-amber-600 border-amber-500/20'}`}
                          >
                            {scope.has_secrets ? 'Configured' : 'Needs Setup'}
                          </Badge>
                        ) : (
                          <span className="text-muted-foreground">-</span>
                        )}
                      </TableCell>
                      <TableCell className="hidden md:table-cell">
                        {(scope.redaction_hints ?? []).length > 0 ? (
                          <div className="flex max-w-[12rem] flex-wrap gap-1 overflow-hidden max-h-12">
                            {(scope.redaction_hints ?? []).map((hint) => (
                              <Badge
                                key={hint}
                                variant="secondary"
                                className="font-mono text-xs"
                              >
                                {hint}
                              </Badge>
                            ))}
                          </div>
                        ) : (
                          <span className="text-muted-foreground">-</span>
                        )}
                      </TableCell>
                      <TableCell>
                        <div className="flex gap-0.5">
                          <Tooltip>
                            <TooltipTrigger asChild>
                              <Button
                                variant="ghost"
                                size="sm"
                                className="h-7 w-7 p-0"
                                aria-label={`Duplicate ${scopeLabel(scope)}`}
                                data-testid={`auth-scope-duplicate-${scope.id}`}
                                onClick={() => handleDuplicate(scope)}
                              >
                                <Copy className="h-3.5 w-3.5" />
                              </Button>
                            </TooltipTrigger>
                            <TooltipContent>Duplicate</TooltipContent>
                          </Tooltip>
                          <Tooltip>
                            <TooltipTrigger asChild>
                              <Button
                                variant="ghost"
                                size="sm"
                                className="h-7 w-7 p-0"
                                aria-label={`Edit ${scopeLabel(scope)}`}
                                data-testid={`auth-scope-edit-${scope.id}`}
                                onClick={() => openEdit(scope)}
                              >
                                <Pencil className="h-3.5 w-3.5" />
                              </Button>
                            </TooltipTrigger>
                            <TooltipContent>Edit</TooltipContent>
                          </Tooltip>
                          <Tooltip>
                            <TooltipTrigger asChild>
                              <Button
                                variant="ghost"
                                size="sm"
                                className="h-7 w-7 p-0 hover:bg-destructive/10 hover:text-destructive"
                                aria-label={`Delete ${scopeLabel(scope)}`}
                                data-testid={`auth-scope-delete-${scope.id}`}
                                onClick={() => setDeleteTarget(scope)}
                              >
                                <Trash2 className="h-3.5 w-3.5" />
                              </Button>
                            </TooltipTrigger>
                            <TooltipContent>Delete</TooltipContent>
                          </Tooltip>
                          {(scope.type === 'env' || scope.type === 'header' || scope.type === 'hawk' || scope.type === 'client_credentials') && (
                            <Tooltip>
                              <TooltipTrigger asChild>
                                <Button
                                  variant="ghost"
                                  size="sm"
                                  className={`h-7 w-7 p-0 ${scope.has_secrets ? 'text-emerald-600 hover:bg-emerald-500/10' : 'text-amber-600 hover:bg-amber-500/10'}`}
                                  aria-label={scope.has_secrets ? `Manage secrets for ${scopeLabel(scope)}` : `Add secrets for ${scopeLabel(scope)}`}
                                  data-testid={`auth-scope-key-${scope.id}`}
                                  onClick={() => openEdit(scope)}
                                >
                                  <Key className="h-3.5 w-3.5" />
                                </Button>
                              </TooltipTrigger>
                              <TooltipContent>
                                {scope.has_secrets ? 'Manage Secret Material' : 'Add Secret Material'}
                              </TooltipContent>
                            </Tooltip>
                          )}
                          {scope.type === 'oauth2' && (
                            <>
                              <Tooltip>
                                <TooltipTrigger asChild>
                                  <Button
                                    variant="ghost"
                                    size="sm"
                                    className="h-7 w-7 p-0 text-primary hover:bg-primary/10"
                                    aria-label={`Authenticate ${scopeLabel(scope)}`}
                                    data-testid={`auth-scope-authenticate-${scope.id}`}
                                    onClick={() => handleAuthenticate(scope.id)}
                                  >
                                    <ExternalLink className="h-3.5 w-3.5" />
                                  </Button>
                                </TooltipTrigger>
                                <TooltipContent>Authenticate</TooltipContent>
                              </Tooltip>
                              <Tooltip>
                                <TooltipTrigger asChild>
                                  <Button
                                    variant="ghost"
                                    size="sm"
                                    className="h-7 w-7 p-0 hover:bg-amber-500/10 hover:text-amber-600"
                                    aria-label={`Revoke token for ${scopeLabel(scope)}`}
                                    data-testid={`auth-scope-revoke-${scope.id}`}
                                    onClick={() => handleRevoke(scope.id)}
                                  >
                                    <Unplug className="h-3.5 w-3.5" />
                                  </Button>
                                </TooltipTrigger>
                                <TooltipContent>Revoke Token</TooltipContent>
                              </Tooltip>
                            </>
                          )}
                        </div>
                      </TableCell>
                    </TableRow>
                  ))
                )}
              </TableBody>
            </Table>
          )}
        </CardContent>
      </Card>

      <AuthScopeDialog
        open={dialogOpen}
        onClose={() => setDialogOpen(false)}
        form={form}
        setForm={setForm}
        onSave={handleSave}
        saving={saving}
        editing={!!editing}
        editingId={editing?.id}
        envFields={editing?.env_fields}
        providers={providers ?? []}
        saveError={saveError}
      />

      <ConfirmDialog
        open={!!deleteTarget}
        onOpenChange={(open) => !open && setDeleteTarget(null)}
        title="Delete credential"
        description={`Are you sure you want to delete "${deleteTarget?.name}"?`}
        confirmLabel="Delete"
        variant="destructive"
        onConfirm={confirmDelete}
      />
    </div>
  )
}


function OAuthStatusBadge({ scopeId }: { scopeId: string }) {
  const fetcher = useCallback(() => getOAuthStatus(scopeId), [scopeId])
  const { data: status } = useApi<OAuthStatus>(fetcher)

  if (!status) return <span className="text-muted-foreground">...</span>

  const colors: Record<string, string> = {
    valid: 'bg-emerald-500/10 text-emerald-600 border-emerald-500/20',
    expired: 'bg-yellow-500/10 text-yellow-600 border-yellow-500/20',
    refresh_needed: 'bg-yellow-500/10 text-yellow-600 border-yellow-500/20',
    not_configured: 'bg-muted text-muted-foreground border-border',
  }

  let label = ''
  switch (status.status) {
    case 'valid':
      label = status.expires_at
        ? `Connected \u2014 ${formatRelativeTime(status.expires_at)} left`
        : 'Connected'
      break
    case 'expired':
      label = 'Expired'
      break
    case 'refresh_needed':
      label = 'Needs Refresh'
      break
    default:
      label = 'Not Connected'
  }

  return (
    <Badge variant="outline" className={`text-xs ${colors[status.status] ?? colors.not_configured}`}>
      {label}
    </Badge>
  )
}
