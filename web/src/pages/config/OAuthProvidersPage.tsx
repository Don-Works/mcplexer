import { useCallback, useState } from 'react'
import { Card, CardContent } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Table, TableBody, TableCell, TableHead, TableHeader, TableRow,
} from '@/components/ui/table'
import {
  Dialog, DialogContent, DialogFooter, DialogHeader, DialogTitle,
} from '@/components/ui/dialog'
import { Badge } from '@/components/ui/badge'
import { useApi } from '@/hooks/use-api'
import {
  createOAuthProvider, deleteOAuthProvider, discoverOIDC, listOAuthProviders, updateOAuthProvider,
} from '@/api/client'
import type { OAuthProvider } from '@/api/types'
import { Info, KeyRound, Loader2, Pencil, Plus, Search, Trash2 } from 'lucide-react'
import { Link } from 'react-router-dom'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { toast } from 'sonner'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'

interface FormData {
  name: string
  template_id: string
  authorize_url: string
  token_url: string
  client_id: string
  client_secret: string
  scopes: string[]
  use_pkce: boolean
}

const emptyForm: FormData = {
  name: '',
  template_id: '',
  authorize_url: '',
  token_url: '',
  client_id: '',
  client_secret: '',
  scopes: [],
  use_pkce: true,
}

export function OAuthProvidersPage() {
  const fetcher = useCallback(() => listOAuthProviders(), [])
  const { data, loading, error, refetch } = useApi(fetcher)

  const [dialogOpen, setDialogOpen] = useState(false)
  const [editing, setEditing] = useState<OAuthProvider | null>(null)
  const [form, setForm] = useState<FormData>(emptyForm)
  const [saving, setSaving] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)
  const [deleteTarget, setDeleteTarget] = useState<OAuthProvider | null>(null)

  function openCreate() {
    setEditing(null)
    setForm(emptyForm)
    setSaveError(null)
    setDialogOpen(true)
  }

  function openEdit(provider: OAuthProvider) {
    setEditing(provider)
    setForm({
      name: provider.name,
      template_id: provider.template_id ?? '',
      authorize_url: provider.authorize_url,
      token_url: provider.token_url,
      client_id: provider.client_id,
      client_secret: '',
      scopes: [...(provider.scopes ?? [])],
      use_pkce: provider.use_pkce,
    })
    setSaveError(null)
    setDialogOpen(true)
  }

  async function handleSave() {
    setSaving(true)
    setSaveError(null)
    try {
      if (editing) {
        const payload: Record<string, unknown> = { ...form }
        if (!form.client_secret) delete payload.client_secret
        await updateOAuthProvider(editing.id, payload)
      } else {
        await createOAuthProvider(form)
      }
      setDialogOpen(false)
      toast.success(editing ? 'Provider updated' : 'Provider created')
      refetch()
    } catch (err: unknown) {
      setSaveError(err instanceof Error ? err.message : 'Failed to save OAuth provider')
    } finally {
      setSaving(false)
    }
  }

  async function confirmDelete() {
    if (!deleteTarget) return
    try {
      await deleteOAuthProvider(deleteTarget.id)
      setDeleteTarget(null)
      toast.success('Provider deleted')
      refetch()
    } catch (err: unknown) {
      toast.error(err instanceof Error ? err.message : 'Failed to delete OAuth provider')
    }
  }

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-end">
        <Button onClick={openCreate} data-testid="oauth-provider-add">
          <Plus className="mr-2 h-4 w-4" />
          Add OAuth Provider
        </Button>
      </div>

      <div className="flex items-start gap-3 rounded-md border border-border/60 bg-muted/30 px-4 py-3">
        <Info className="mt-0.5 h-4 w-4 shrink-0 text-muted-foreground" />
        <p className="text-sm text-muted-foreground">
          OAuth providers are typically managed automatically through{' '}
          <Link to="/setup" className="text-primary hover:underline">Add a Tool Server</Link>.
          Edit these only if you need custom OAuth configurations.
        </p>
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
                  <TableHead className="hidden md:table-cell">Token URL</TableHead>
                  <TableHead className="hidden sm:table-cell">Client ID</TableHead>
                  <TableHead className="hidden sm:table-cell">PKCE</TableHead>
                  <TableHead className="w-24">Actions</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {data.length === 0 ? (
                  <TableRow>
                    <TableCell colSpan={5} className="h-32">
                      <div className="flex flex-col items-center justify-center text-muted-foreground">
                        <KeyRound className="mb-2 h-8 w-8 text-muted-foreground/50" />
                        <p className="text-sm">No OAuth providers configured</p>
                        <button onClick={openCreate} className="text-xs text-primary hover:underline">
                          Add an OAuth provider to get started
                        </button>
                      </div>
                    </TableCell>
                  </TableRow>
                ) : (
                  data.map((provider) => (
                    <TableRow key={provider.id} className="border-border/30 hover:bg-muted/30">
                      <TableCell className="font-medium">{provider.name}</TableCell>
                      <TableCell className="hidden md:table-cell">
                        <div className="max-w-[20rem] truncate font-mono text-xs text-muted-foreground" title={provider.token_url}>
                          {provider.token_url}
                        </div>
                      </TableCell>
                      <TableCell className="hidden sm:table-cell">
                        <div className="max-w-[14rem] truncate font-mono text-xs" title={provider.client_id}>
                          {provider.client_id}
                        </div>
                      </TableCell>
                      <TableCell className="hidden sm:table-cell">
                        {provider.use_pkce ? (
                          <Badge variant="outline" className="text-xs">PKCE</Badge>
                        ) : (
                          <span className="text-muted-foreground">-</span>
                        )}
                      </TableCell>
                      <TableCell>
                        <div className="flex gap-1">
                          <Tooltip>
                            <TooltipTrigger asChild>
                              <Button
                                variant="ghost"
                                size="sm"
                                className="h-8 w-8 p-0"
                                aria-label={`Edit ${provider.name}`}
                                data-testid={`oauth-provider-edit-${provider.id}`}
                                onClick={() => openEdit(provider)}
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
                                className="h-8 w-8 p-0 hover:bg-destructive/10 hover:text-destructive"
                                aria-label={`Delete ${provider.name}`}
                                data-testid={`oauth-provider-delete-${provider.id}`}
                                onClick={() => setDeleteTarget(provider)}
                              >
                                <Trash2 className="h-3.5 w-3.5" />
                              </Button>
                            </TooltipTrigger>
                            <TooltipContent>Delete</TooltipContent>
                          </Tooltip>
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

      <OAuthProviderDialog
        open={dialogOpen}
        onClose={() => setDialogOpen(false)}
        form={form}
        setForm={setForm}
        onSave={handleSave}
        saving={saving}
        editing={!!editing}
        saveError={saveError}
      />

      <ConfirmDialog
        open={!!deleteTarget}
        onOpenChange={(open) => !open && setDeleteTarget(null)}
        title="Delete OAuth provider"
        description={`Are you sure you want to delete "${deleteTarget?.name}"?`}
        confirmLabel="Delete"
        variant="destructive"
        onConfirm={confirmDelete}
      />
    </div>
  )
}

function OAuthProviderDialog({
  open,
  onClose,
  form,
  setForm,
  onSave,
  saving,
  editing,
  saveError,
}: {
  open: boolean
  onClose: () => void
  form: FormData
  setForm: React.Dispatch<React.SetStateAction<FormData>>
  onSave: () => void
  saving: boolean
  editing: boolean
  saveError: string | null
}) {
  const [scopeInput, setScopeInput] = useState('')
  const [issuerUrl, setIssuerUrl] = useState('')
  const [discovering, setDiscovering] = useState(false)
  const [discoverError, setDiscoverError] = useState<string | null>(null)

  async function handleDiscover() {
    if (!issuerUrl) return
    setDiscovering(true)
    setDiscoverError(null)
    try {
      const result = await discoverOIDC(issuerUrl)
      setForm((f) => ({
        ...f,
        authorize_url: result.authorize_url,
        token_url: result.token_url,
        scopes: result.scopes ?? [],
        use_pkce: result.use_pkce,
      }))
    } catch (err: unknown) {
      setDiscoverError(
        err instanceof Error ? err.message : 'Discovery failed',
      )
    } finally {
      setDiscovering(false)
    }
  }

  function addScope() {
    if (!scopeInput) return
    setForm((f) => ({
      ...f,
      scopes: [...f.scopes, scopeInput],
    }))
    setScopeInput('')
  }

  function removeScope(index: number) {
    setForm((f) => ({
      ...f,
      scopes: f.scopes.filter((_, i) => i !== index),
    }))
  }

  return (
    <Dialog open={open} onOpenChange={() => onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{editing ? 'Edit OAuth Provider' : 'Add OAuth Provider'}</DialogTitle>
        </DialogHeader>
        <div className="space-y-4">
          {!editing && (
            <div className="space-y-2">
              <Label className="text-xs text-muted-foreground">
                OpenID Connect Discovery
              </Label>
              <div className="flex gap-2">
                <Input
                  placeholder="https://accounts.google.com"
                  value={issuerUrl}
                  onChange={(e) => setIssuerUrl(e.target.value)}
                  onKeyDown={(e) => {
                    if (e.key === 'Enter') {
                      e.preventDefault()
                      handleDiscover()
                    }
                  }}
                />
                <Tooltip>
                  <TooltipTrigger asChild>
                    <Button
                      type="button"
                      variant="outline"
                      size="sm"
                      onClick={handleDiscover}
                      disabled={discovering || !issuerUrl}
                    >
                      {discovering ? (
                        <Loader2 className="h-4 w-4 animate-spin" />
                      ) : (
                        <Search className="h-4 w-4" />
                      )}
                    </Button>
                  </TooltipTrigger>
                  <TooltipContent>Discover endpoints</TooltipContent>
                </Tooltip>
              </div>
              {discoverError && (
                <p className="text-xs text-destructive">{discoverError}</p>
              )}
              <p className="text-xs text-muted-foreground/60">
                Enter an issuer URL to auto-fill endpoints and scopes
              </p>
            </div>
          )}
          <div className="space-y-2">
            <Label className="text-xs text-muted-foreground">Name</Label>
            <Input
              value={form.name}
              onChange={(e) => setForm((f) => ({ ...f, name: e.target.value }))}
            />
          </div>
          <div className="space-y-2">
            <Label className="text-xs text-muted-foreground">Authorize URL</Label>
            <Input
              value={form.authorize_url}
              onChange={(e) => setForm((f) => ({ ...f, authorize_url: e.target.value }))}
              placeholder="https://provider.com/oauth/authorize"
            />
          </div>
          <div className="space-y-2">
            <Label className="text-xs text-muted-foreground">Token URL</Label>
            <Input
              value={form.token_url}
              onChange={(e) => setForm((f) => ({ ...f, token_url: e.target.value }))}
              placeholder="https://provider.com/oauth/token"
            />
          </div>
          <div className="space-y-2">
            <Label className="text-xs text-muted-foreground">Client ID</Label>
            <Input
              value={form.client_id}
              onChange={(e) => setForm((f) => ({ ...f, client_id: e.target.value }))}
            />
          </div>
          <div className="space-y-2">
            <Label className="text-xs text-muted-foreground">
              Client Secret {editing && '(leave blank to keep existing)'}
            </Label>
            <Input
              type="password"
              value={form.client_secret}
              onChange={(e) => setForm((f) => ({ ...f, client_secret: e.target.value }))}
              placeholder={editing ? '••••••••' : ''}
            />
          </div>
          <div className="space-y-2">
            <Label className="text-xs text-muted-foreground">Scopes</Label>
            <div className="flex flex-wrap gap-1 mb-2">
              {form.scopes.map((scope, i) => (
                <Badge
                  key={i}
                  variant="outline"
                  className="cursor-pointer font-mono text-xs hover:bg-destructive/10 hover:text-destructive"
                  onClick={() => removeScope(i)}
                >
                  {scope} x
                </Badge>
              ))}
            </div>
            <div className="flex gap-2">
              <Input
                className="font-mono text-sm"
                placeholder="e.g. read:user"
                value={scopeInput}
                onChange={(e) => setScopeInput(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === 'Enter') {
                    e.preventDefault()
                    addScope()
                  }
                }}
              />
              <Button type="button" variant="outline" size="sm" onClick={addScope}>
                Add
              </Button>
            </div>
          </div>
          <div className="flex items-center gap-2">
            <input
              type="checkbox"
              id="use_pkce"
              checked={form.use_pkce}
              onChange={(e) => setForm((f) => ({ ...f, use_pkce: e.target.checked }))}
              className="h-4 w-4 rounded border-border"
            />
            <Label htmlFor="use_pkce" className="text-sm">
              Use PKCE (Proof Key for Code Exchange)
            </Label>
          </div>
        </div>
        {saveError && (
          <p className="text-sm text-destructive">{saveError}</p>
        )}
        <DialogFooter>
          <Button variant="outline" onClick={onClose} data-testid="oauth-provider-cancel">
            Cancel
          </Button>
          <Button onClick={onSave} disabled={saving || !form.name || !form.authorize_url || !form.token_url || !form.client_id} data-testid="oauth-provider-save">
            {saving ? 'Saving...' : 'Save'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
