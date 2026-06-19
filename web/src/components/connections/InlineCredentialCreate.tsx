// InlineCredentialCreate — a self-contained inline form for creating a new
// credential (auth scope) and immediately seeding its secret material, all
// without leaving the surface that mounted it.
//
// Design:
//   1. The credential type is INFERRED from the server's transport:
//      - stdio → "env"   (environment variables injected into the process)
//      - http  → "header" (Authorization / API-key header)
//      - When server is null, we default to "env".
//   2. For env / header types the form stays expanded; the user pastes values
//      and hits "Create credential". NOTHING is persisted until all fields
//      have a value (Save button disabled guard).
//   3. For oauth2 we short-circuit to the OAuthProvider picker + popup flow.
//      The caller receives `onCreated` once the scope exists (OAuth auth
//      happens separately — the drawer handles the "Authenticate" button).
//
// Usage:
//   <InlineCredentialCreate
//     server={selectedServer}
//     providers={oauthProviders}
//     onCreated={(scope) => setScopeId(scope.id)}
//     onCancel={() => setShowCreate(false)}
//   />

import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Badge } from '@/components/ui/badge'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Eye, EyeOff, Loader2 } from 'lucide-react'
import { toast } from 'sonner'
import { createAuthScope, putSecret } from '@/api/client'
import type { AuthScope, DownstreamServer, OAuthProvider } from '@/api/types'

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

type CredentialType = 'env' | 'header' | 'oauth2'

export interface InlineCredentialCreateProps {
  server: DownstreamServer | null
  /** Available OAuth providers (needed for the oauth2 path). */
  providers: OAuthProvider[]
  onCreated: (scope: AuthScope) => void
  onCancel: () => void
  /** Optional label to display for the expand trigger. */
  triggerLabel?: string
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** Infer the preferred credential type from a server's transport. */
function inferCredentialType(server: DownstreamServer | null): CredentialType {
  if (!server) return 'env'
  return server.transport === 'http' ? 'header' : 'env'
}

/** Derive a stable slug from a server name + type. */
function deriveSlug(serverName: string, type: CredentialType): string {
  const base = serverName
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '_')
    .replace(/^_|_$/g, '')
  return `${base}_${type}`
}

/** Build the header secret key/value pair for the bearer pattern. */
function buildBearerSecret(token: string): { key: string; value: string } {
  return { key: 'Authorization', value: token.trim() ? `Bearer ${token.trim()}` : '' }
}

// ---------------------------------------------------------------------------
// Sub-component: FreeEnvForm (no pre-defined fields, single key/value pair)
// ---------------------------------------------------------------------------

function FreeEnvForm({
  envKey,
  setEnvKey,
  envValue,
  setEnvValue,
  showValue,
  setShowValue,
}: {
  envKey: string
  setEnvKey: (v: string) => void
  envValue: string
  setEnvValue: (v: string) => void
  showValue: boolean
  setShowValue: (v: boolean) => void
}) {
  return (
    <div className="grid gap-3 sm:grid-cols-2">
      <div className="space-y-1">
        <Label className="text-xs text-muted-foreground">Variable name</Label>
        <Input
          className="font-mono text-sm"
          value={envKey}
          onChange={(e) => setEnvKey(e.target.value.toUpperCase().replace(/[^A-Z0-9_]/g, ''))}
          placeholder="API_KEY"
          autoComplete="off"
        />
      </div>
      <div className="space-y-1">
        <Label className="text-xs text-muted-foreground">Value</Label>
        <div className="relative">
          <Input
            className="pr-8 font-mono text-sm"
            type={showValue ? 'text' : 'password'}
            value={envValue}
            onChange={(e) => setEnvValue(e.target.value)}
            placeholder="sk-..."
            autoComplete="off"
          />
          <button
            type="button"
            className="absolute right-2 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground"
            aria-label={showValue ? 'Hide secret value' : 'Show secret value'}
            onClick={() => setShowValue(!showValue)}
          >
            {showValue ? <EyeOff className="h-3.5 w-3.5" /> : <Eye className="h-3.5 w-3.5" />}
          </button>
        </div>
      </div>
    </div>
  )
}

// ---------------------------------------------------------------------------
// Sub-component: HeaderForm
// ---------------------------------------------------------------------------

function HeaderForm({
  token,
  setToken,
  showToken,
  setShowToken,
}: {
  token: string
  setToken: (v: string) => void
  showToken: boolean
  setShowToken: (v: boolean) => void
}) {
  return (
    <div className="space-y-1">
      <Label className="text-xs text-muted-foreground">Bearer token / API key</Label>
      <div className="relative">
        <Input
          className="pr-8 font-mono text-sm"
          type={showToken ? 'text' : 'password'}
          value={token}
          onChange={(e) => setToken(e.target.value)}
          placeholder="sk-..."
          autoComplete="off"
        />
        <button
          type="button"
          className="absolute right-2 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground"
          onClick={() => setShowToken(!showToken)}
        >
          {showToken ? <EyeOff className="h-3.5 w-3.5" /> : <Eye className="h-3.5 w-3.5" />}
        </button>
      </div>
      <p className="text-[11px] text-muted-foreground">
        Stored as{' '}
        <code className="rounded bg-muted px-1 py-0.5 font-mono">Authorization: Bearer &lt;token&gt;</code>
      </p>
    </div>
  )
}

// ---------------------------------------------------------------------------
// Main component
// ---------------------------------------------------------------------------

export function InlineCredentialCreate({
  server,
  providers,
  onCreated,
  onCancel,
  triggerLabel = '+ Create new credential',
}: InlineCredentialCreateProps) {
  const [expanded, setExpanded] = useState(false)
  const [saving, setSaving] = useState(false)

  const credType = useMemo(() => inferCredentialType(server), [server])
  const defaultSlug = useMemo(
    () => (server ? deriveSlug(server.name, credType) : `new_${credType}_credential`),
    [server, credType],
  )
  const [slug, setSlug] = useState(defaultSlug)

  // Update slug when server changes.
  const prevServerIdRef = useRef<string | null>(null)
  useEffect(() => {
    const newId = server?.id ?? null
    if (newId !== prevServerIdRef.current) {
      prevServerIdRef.current = newId
      setSlug(server ? deriveSlug(server.name, credType) : `new_${credType}_credential`)
    }
  }, [server, credType])

  // ENV path — pre-defined fields (from server env_field hints if available)
  // For now there are no per-server env_fields at the server level — those
  // live on the AuthScope. We expose a free key/value form here and allow
  // the user to add the primary variable. Additional ones go through the full
  // AuthScopeDialog after creation.
  const [envKey, setEnvKey] = useState('')
  const [envValue, setEnvValue] = useState('')
  const [showEnvValue, setShowEnvValue] = useState(false)

  // HEADER path
  const [headerToken, setHeaderToken] = useState('')
  const [showHeaderToken, setShowHeaderToken] = useState(false)

  // OAUTH2 path
  const [oauthProviderId, setOauthProviderId] = useState('')

  // Derived: whether the Save button should be enabled.
  const canSave = useMemo(() => {
    if (!slug.trim()) return false
    if (credType === 'env') return !!envKey.trim() && !!envValue.trim()
    if (credType === 'header') return !!headerToken.trim()
    if (credType === 'oauth2') return !!oauthProviderId.trim()
    return false
  }, [credType, slug, envKey, envValue, headerToken, oauthProviderId])

  // Reset form fields when expanding / collapsing.
  function handleToggle() {
    if (expanded) {
      onCancel()
      setExpanded(false)
    } else {
      setEnvKey('')
      setEnvValue('')
      setShowEnvValue(false)
      setHeaderToken('')
      setShowHeaderToken(false)
      setOauthProviderId('')
      setSlug(defaultSlug)
      setExpanded(true)
    }
  }

  const handleSave = useCallback(async () => {
    if (!canSave || saving) return
    setSaving(true)
    try {
      // Step 1: Create the auth scope definition.
      const scope = await createAuthScope({
        name: slug.trim(),
        display_name: '',
        type: credType,
        oauth_provider_id: credType === 'oauth2' ? oauthProviderId : '',
        redaction_hints: [],
      })

      // Step 2: Immediately seed the secret material (env / header only).
      // OAuth scopes don't need seeding here — auth comes via the OAuth flow.
      if (credType === 'env') {
        await putSecret(scope.id, envKey.trim(), envValue.trim())
      } else if (credType === 'header') {
        const { key, value } = buildBearerSecret(headerToken)
        await putSecret(scope.id, key, value)
      }

      toast.success(`Credential "${scope.name}" created`)
      setExpanded(false)
      onCreated(scope)
    } catch (err: unknown) {
      toast.error(err instanceof Error ? err.message : 'Failed to create credential')
    } finally {
      setSaving(false)
    }
  }, [canSave, saving, slug, credType, oauthProviderId, envKey, envValue, headerToken, onCreated])

  if (!expanded) {
    return (
      <button
        type="button"
        onClick={handleToggle}
        className="inline-flex items-center gap-1 text-xs text-primary hover:underline"
        data-testid="inline-credential-create-trigger"
      >
        {triggerLabel}
      </button>
    )
  }

  return (
    <div
      className="mt-2 space-y-4 rounded-md border border-border/60 bg-muted/10 px-4 py-4"
      data-testid="inline-credential-create-form"
    >
      {/* Header row */}
      <div className="flex items-center justify-between gap-2">
        <div className="flex items-center gap-2">
          <span className="text-sm font-medium">New credential</span>
          <Badge variant="outline" className="font-mono text-[10px]">
            {credType}
          </Badge>
        </div>
        <button
          type="button"
          onClick={handleToggle}
          className="text-xs text-muted-foreground hover:text-foreground"
        >
          Cancel
        </button>
      </div>

      {/* Slug field */}
      <div className="space-y-1">
        <Label className="text-xs text-muted-foreground">Slug</Label>
        <Input
          className="font-mono text-sm"
          value={slug}
          onChange={(e) => setSlug(e.target.value)}
          placeholder="e.g. github_headers"
          autoComplete="off"
        />
        <p className="text-[11px] text-muted-foreground">
          Stable identifier used in route wiring.
        </p>
      </div>

      {/* Type-specific secret input */}
      {credType === 'env' && (
        <FreeEnvForm
          envKey={envKey}
          setEnvKey={setEnvKey}
          envValue={envValue}
          setEnvValue={setEnvValue}
          showValue={showEnvValue}
          setShowValue={setShowEnvValue}
        />
      )}

      {credType === 'header' && (
        <HeaderForm
          token={headerToken}
          setToken={setHeaderToken}
          showToken={showHeaderToken}
          setShowToken={setShowHeaderToken}
        />
      )}

      {credType === 'oauth2' && (
        <div className="space-y-1">
          <Label className="text-xs text-muted-foreground">OAuth provider</Label>
          {providers.length === 0 ? (
            <p className="text-xs text-muted-foreground">
              No OAuth providers configured. Set one up via Quick Setup first.
            </p>
          ) : (
            <Select value={oauthProviderId} onValueChange={setOauthProviderId}>
              <SelectTrigger data-testid="inline-credential-oauth-provider">
                <SelectValue placeholder="Select a provider..." />
              </SelectTrigger>
              <SelectContent>
                {providers.map((p) => (
                  <SelectItem key={p.id} value={p.id}>
                    {p.name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          )}
          <p className="text-[11px] text-muted-foreground">
            After creating, use the Authenticate button to complete the OAuth flow.
          </p>
        </div>
      )}

      {/* Actions */}
      <div className="flex justify-end gap-2">
        <Button variant="ghost" size="sm" onClick={handleToggle} disabled={saving}>
          Cancel
        </Button>
        <Button
          size="sm"
          onClick={handleSave}
          disabled={!canSave || saving}
          data-testid="inline-credential-create-save"
        >
          {saving ? (
            <>
              <Loader2 className="mr-1.5 h-3.5 w-3.5 animate-spin" />
              Creating...
            </>
          ) : (
            'Create credential'
          )}
        </Button>
      </div>
    </div>
  )
}
