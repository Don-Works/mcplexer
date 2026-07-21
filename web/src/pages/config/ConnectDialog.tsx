import { useCallback, useEffect, useRef, useState } from 'react'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Badge } from '@/components/ui/badge'
import { AlertCircle, CheckCircle2, ExternalLink, Loader2, RotateCcw, Zap } from 'lucide-react'
import { useApi } from '@/hooks/use-api'
import { connectDownstream, getOAuthCapabilities, getOAuthStatus, listWorkspaces } from '@/api/client'
import type { DownstreamServer, OAuthCapabilities } from '@/api/types'
import { toast } from 'sonner'
import { CopyButton } from '@/components/ui/copy-button'
import { redirectToOAuth } from '@/lib/safe-redirect'

interface ConnectDialogProps {
  open: boolean
  onClose: () => void
  server: DownstreamServer | null
  onConnected: () => void
}

export function ConnectDialog({ open, onClose, server, onConnected }: ConnectDialogProps) {
  const workspacesFetcher = useCallback(() => listWorkspaces(), [])
  const { data: workspaces } = useApi(workspacesFetcher)

  const [caps, setCaps] = useState<OAuthCapabilities | null>(null)
  const [capsLoading, setCapsLoading] = useState(false)
  const [capsError, setCapsError] = useState<string | null>(null)
  const [clientId, setClientId] = useState('')
  const [clientSecret, setClientSecret] = useState('')
  const [accountLabel, setAccountLabel] = useState('')
  const [workspaceId, setWorkspaceId] = useState('global')
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [awaitingAuth, setAwaitingAuth] = useState(false)
  const [authScopeId, setAuthScopeId] = useState<string | null>(null)
  const [authComplete, setAuthComplete] = useState(false)
  const pollRef = useRef<ReturnType<typeof setInterval> | null>(null)

  function stopPolling() {
    if (pollRef.current) {
      clearInterval(pollRef.current)
      pollRef.current = null
    }
  }

  function fetchCapabilities() {
    if (!server) return
    setCapsLoading(true)
    setCaps(null)
    setCapsError(null)
    getOAuthCapabilities(server.id)
      .then(setCaps)
      .catch(() => setCapsError('Failed to check server capabilities. The server may be offline.'))
      .finally(() => setCapsLoading(false))
  }

  useEffect(() => {
    if (open && server) fetchCapabilities()
  }, [open, server?.id])

  useEffect(() => {
    if (!open) {
      stopPolling()
      return
    }
    setClientId('')
    setClientSecret('')
    setAccountLabel('')
    setWorkspaceId('global')
    setSaving(false)
    setError(null)
    setAwaitingAuth(false)
    setAuthScopeId(null)
    setAuthComplete(false)
  }, [open, server?.id])

  useEffect(() => {
    if (!open || !workspaces || workspaces.length === 0) return
    if (!workspaces.some((workspace) => workspace.id === workspaceId)) {
      setWorkspaceId(workspaces[0].id)
    }
  }, [open, workspaceId, workspaces])

  async function handleSubmit() {
    if (!server) return
    setSaving(true)
    setError(null)
    try {
      const resp = await connectDownstream(server.id, {
        workspace_id: workspaceId,
        client_id: clientId || undefined,
        client_secret: clientSecret || undefined,
        account_label: accountLabel || undefined,
      })
      if (resp.authorize_url) {
        // Show waiting state, open browser, poll for completion.
        setAuthScopeId(resp.auth_scope?.id ?? null)
        setAwaitingAuth(true)
        setSaving(false)
        redirectToOAuth(resp.authorize_url)

        // Poll OAuth status every 2 seconds.
        if (resp.auth_scope?.id) {
          const scopeId = resp.auth_scope.id
          stopPolling()
          pollRef.current = setInterval(async () => {
            try {
              const status = await getOAuthStatus(scopeId)
              if (status.status === 'valid') {
                stopPolling()
                setAuthComplete(true)
                toast.success(`${server.name} authenticated`)
                onConnected()
              }
            } catch {
              // Ignore polling errors, keep trying.
            }
          }, 2000)
        }
      } else {
        toast.success(`${server.name} connected`)
        onConnected()
        onClose()
      }
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : 'Failed to connect')
      setSaving(false)
    }
  }

  const isAutoDiscovery = caps?.supports_auto_discovery && !caps.needs_credentials
  const template = caps?.template ?? null

  if (!server) return null

  const canSubmit =
    !saving &&
    !capsLoading &&
    !capsError &&
    !!workspaceId &&
    (caps?.needs_credentials !== true ||
      (!!clientId.trim() && (!template?.needs_secret || !!clientSecret.trim())))

  return (
    <Dialog open={open} onOpenChange={() => onClose()}>
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle>Connect {server.name}</DialogTitle>
        </DialogHeader>

        {awaitingAuth && !authComplete && (
          <div className="flex flex-col items-center gap-4 py-8">
            <Loader2 className="h-8 w-8 animate-spin text-primary" />
            <div className="text-center">
              <p className="text-sm font-medium">Waiting for authentication</p>
              <p className="mt-1 text-xs text-muted-foreground">
                Complete the login in your browser. This dialog will update automatically.
              </p>
            </div>
            <Button
              variant="outline"
              size="sm"
              onClick={() => {
                if (authScopeId) {
                  const url = `/api/v1/auth-scopes/${authScopeId}/oauth/authorize`
                  fetch(url)
                    .then((r) => r.json())
                    .then((data: { authorize_url?: string }) => {
                      if (data.authorize_url) redirectToOAuth(data.authorize_url)
                    })
                    .catch(() => {})
                }
              }}
            >
              <ExternalLink className="mr-1.5 h-3.5 w-3.5" />
              Reopen login page
            </Button>
          </div>
        )}

        {authComplete && (
          <div className="flex flex-col items-center gap-4 py-8">
            <CheckCircle2 className="h-8 w-8 text-emerald-500" />
            <div className="text-center">
              <p className="text-sm font-medium">Authentication successful</p>
              <p className="mt-1 text-xs text-muted-foreground">
                {server.name} is now connected and ready to use.
              </p>
            </div>
            <Button onClick={onClose}>Done</Button>
          </div>
        )}

        {!awaitingAuth && <>
        <div className="space-y-4">
          {capsLoading && (
            <div className="flex items-center gap-2 text-sm text-muted-foreground">
              <Loader2 className="h-4 w-4 animate-spin" />
              Checking capabilities…
            </div>
          )}

          {!capsLoading && capsError && (
            <div className="flex items-start gap-2 rounded-md border border-destructive/30 bg-destructive/5 p-3">
              <AlertCircle className="mt-0.5 h-4 w-4 shrink-0 text-destructive" />
              <div className="flex-1 text-sm text-destructive">{capsError}</div>
              <Button variant="ghost" size="sm" className="h-7 text-xs" onClick={fetchCapabilities}>
                <RotateCcw className="mr-1 h-3 w-3" />
                Retry
              </Button>
            </div>
          )}

          {!capsLoading && !capsError && (
            <div className="flex flex-wrap items-center gap-2">
              <Badge variant="outline" className="font-mono text-xs">
                {server.tool_namespace}
              </Badge>
              {isAutoDiscovery && (
                <Badge className="border-0 bg-emerald-500/15 text-emerald-600">
                  <Zap className="mr-1 h-3 w-3" />
                  Automatic setup
                </Badge>
              )}
              {!isAutoDiscovery && template && (
                <Badge variant="outline" className="text-xs">
                  {template.name} OAuth app
                </Badge>
              )}
            </div>
          )}

          {!capsLoading && !capsError && !isAutoDiscovery && template && (
            <TemplateForm
              template={template}
              clientId={clientId}
              setClientId={setClientId}
              clientSecret={clientSecret}
              setClientSecret={setClientSecret}
            />
          )}

          {!capsLoading && !capsError && !isAutoDiscovery && !template && (
            <p className="text-sm text-muted-foreground">
              This server exposes OAuth, but has no saved template. MCPlexer will try discovery or
              use credentials you provide.
            </p>
          )}

          <div className="space-y-1.5">
            <Label className="text-xs text-muted-foreground">Workspace</Label>
            <Select value={workspaceId} onValueChange={setWorkspaceId}>
              <SelectTrigger>
                <SelectValue placeholder="Select workspace..." />
              </SelectTrigger>
              <SelectContent>
                {(workspaces ?? []).map((workspace) => (
                  <SelectItem key={workspace.id} value={workspace.id}>
                    {workspace.name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>

          <div className="space-y-1.5">
            <Label className="text-xs text-muted-foreground">
              Account label <span className="text-muted-foreground/60">(optional)</span>
            </Label>
            <Input
              value={accountLabel}
              onChange={(e) => setAccountLabel(e.target.value)}
              placeholder="Personal, Work, Client X"
            />
            <p className="text-xs text-muted-foreground/70">
              Only needed for multiple accounts on the same integration.
            </p>
          </div>

          <p className="text-xs text-muted-foreground/80">
            Connect redirects you to authenticate in the browser.
          </p>
        </div>

        {error && (
          <div className="flex items-start gap-2 rounded-md border border-destructive/30 bg-destructive/5 p-3">
            <AlertCircle className="mt-0.5 h-4 w-4 shrink-0 text-destructive" />
            <div className="flex-1 text-sm text-destructive">{error}</div>
            <Button variant="ghost" size="sm" className="h-7 text-xs" onClick={handleSubmit}>
              <RotateCcw className="mr-1 h-3 w-3" />
              Retry
            </Button>
          </div>
        )}

        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            Cancel
          </Button>
          <Button onClick={handleSubmit} disabled={!canSubmit}>
            {saving ? 'Connecting...' : 'Connect'}
          </Button>
        </DialogFooter>
        </>}
        {/* end !awaitingAuth */}
      </DialogContent>
    </Dialog>
  )
}

function TemplateForm({
  template,
  clientId,
  setClientId,
  clientSecret,
  setClientSecret,
}: {
  template: NonNullable<OAuthCapabilities['template']>
  clientId: string
  setClientId: (value: string) => void
  clientSecret: string
  setClientSecret: (value: string) => void
}) {
  return (
    <div className="space-y-3 rounded-md border border-border/50 bg-muted/20 p-3">
      <p className="text-xs text-muted-foreground">{template.help_text}</p>

      {template.callback_url && (
        <div className="space-y-1">
          <Label className="text-xs text-muted-foreground">Callback URL</Label>
          <div className="flex items-center gap-2">
            <code className="flex-1 overflow-x-auto whitespace-nowrap rounded-none border border-border bg-background/60 px-2 py-1.5 font-mono text-xs">
              {template.callback_url}
            </code>
            <CopyButton value={template.callback_url} />
          </div>
        </div>
      )}

      {template.setup_url && (
        <Button
          variant="outline"
          size="sm"
          className="rounded-none"
          asChild
        >
          <a
            href={template.setup_url}
            target="_blank"
            rel="noopener noreferrer"
          >
            <ExternalLink className="mr-1.5 h-3.5 w-3.5" />
            Create {template.name} OAuth App
          </a>
        </Button>
      )}

      <div className="space-y-1">
        <Label className="text-xs text-muted-foreground">Client ID</Label>
        <Input
          value={clientId}
          onChange={(e) => setClientId(e.target.value)}
          placeholder="Paste your client ID"
        />
      </div>

      {template.needs_secret && (
        <div className="space-y-1">
          <Label className="text-xs text-muted-foreground">Client Secret</Label>
          <Input
            type="password"
            value={clientSecret}
            onChange={(e) => setClientSecret(e.target.value)}
            placeholder="Paste your client secret"
          />
        </div>
      )}

      {template.scopes.length > 0 && (
        <div className="flex flex-wrap gap-1">
          <span className="mr-1 text-xs text-muted-foreground">Scopes:</span>
          {template.scopes.map((scope) => (
            <Badge key={scope} variant="secondary" className="font-mono text-xs">
              {scope}
            </Badge>
          ))}
        </div>
      )}
    </div>
  )
}
