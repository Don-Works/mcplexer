import { useState } from 'react'
import { ExternalLink, Loader2, ShieldCheck } from 'lucide-react'
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
import {
  runAddonOAuthSetup,
  type AddonAuthSpec,
  type AddonOAuthWizardResponse,
  type OAuth2GrantType,
} from '@/api/client'

export interface OAuthStepProps {
  authSpec: AddonAuthSpec
  setAuthSpec: (next: AddonAuthSpec) => void
  authScopeName: string
  setAuthScopeName: (v: string) => void
  parentServer: string
  redirectURL: string
  oauthResult: AddonOAuthWizardResponse | null
  setOAuthResult: (r: AddonOAuthWizardResponse | null) => void
}

// OAuthStep is the wizard step that lets the user (or completes after an
// OpenAPI import set kind="oauth2_pending") fill in OAuth2 credentials and
// either kick off a human-in-the-loop authorize redirect or store
// client_credentials for service-to-service auth.
export function OAuthStep(p: OAuthStepProps) {
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<string | null>(null)
  const grantType: OAuth2GrantType = (p.authSpec.grant_type ?? 'authorization_code') as OAuth2GrantType
  const usePKCE = !!p.authSpec.use_pkce
  const scopesText = (p.authSpec.scopes ?? []).join(' ')
  const [clientSecret, setClientSecret] = useState('')

  function patch(next: Partial<AddonAuthSpec>) {
    p.setAuthSpec({ ...p.authSpec, ...next })
  }

  async function runWizard() {
    setBusy(true); setErr(null)
    try {
      const res = await runAddonOAuthSetup({
        auth_scope_name: p.authScopeName,
        parent_server: p.parentServer,
        auth_url: p.authSpec.auth_url ?? '',
        token_url: p.authSpec.token_url ?? '',
        scopes: p.authSpec.scopes ?? [],
        client_id: p.authSpec.client_id ?? '',
        client_secret: clientSecret || undefined,
        use_pkce: usePKCE,
        grant_type: grantType,
      })
      p.setOAuthResult(res)
      // Pin the kind so the spec leaves "_pending" once a wizard run succeeds.
      patch({ kind: 'oauth2' })
    } catch (e) {
      setErr(e instanceof Error ? e.message : 'oauth setup failed')
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="space-y-4">
      {p.authSpec.kind === 'oauth2_pending' && (
        <div className="rounded border border-amber-500/40 bg-amber-500/10 p-3 text-sm">
          OpenAPI import flagged this as an OAuth2 flow. Fill in the fields below and click
          <span className="mx-1 font-semibold">Test OAuth</span>
          to finish setup.
        </div>
      )}
      <div className="grid grid-cols-2 gap-3">
        <Field label="Auth scope name">
          <Input
            value={p.authScopeName}
            onChange={(e) => p.setAuthScopeName(e.target.value)}
            placeholder="weatherco"
          />
        </Field>
        <Field label="Grant type">
          <Select value={grantType} onValueChange={(v) => patch({ grant_type: v as OAuth2GrantType })}>
            <SelectTrigger><SelectValue /></SelectTrigger>
            <SelectContent>
              <SelectItem value="authorization_code">Authorization code (PKCE)</SelectItem>
              <SelectItem value="client_credentials">Client credentials</SelectItem>
            </SelectContent>
          </Select>
        </Field>
      </div>
      <Field label="Authorization URL">
        <Input
          value={p.authSpec.auth_url ?? ''}
          onChange={(e) => patch({ auth_url: e.target.value })}
          placeholder="https://auth.example.com/oauth/authorize"
          disabled={grantType === 'client_credentials'}
        />
      </Field>
      <Field label="Token URL">
        <Input
          value={p.authSpec.token_url ?? ''}
          onChange={(e) => patch({ token_url: e.target.value })}
          placeholder="https://auth.example.com/oauth/token"
        />
      </Field>
      <Field label="Scopes (space-separated)">
        <Input
          value={scopesText}
          onChange={(e) => patch({ scopes: e.target.value.split(/\s+/).filter(Boolean) })}
          placeholder="read write"
        />
      </Field>
      <div className="grid grid-cols-2 gap-3">
        <Field label="Client ID">
          <Input
            value={p.authSpec.client_id ?? ''}
            onChange={(e) => patch({ client_id: e.target.value })}
            placeholder={usePKCE ? '(public client — optional)' : 'client_xxx'}
          />
        </Field>
        <Field label={grantType === 'client_credentials' ? 'Client secret (required)' : 'Client secret (optional with PKCE)'}>
          <Input
            type="password"
            value={clientSecret}
            onChange={(e) => setClientSecret(e.target.value)}
            placeholder="••••••••"
          />
        </Field>
      </div>
      <label className="flex items-center gap-2 text-sm">
        <input
          type="checkbox"
          checked={usePKCE}
          onChange={(e) => patch({ use_pkce: e.target.checked })}
          disabled={grantType === 'client_credentials'}
        />
        Use PKCE (S256) — recommended for public clients
      </label>
      <Field label="Redirect URL (read-only)">
        <Input value={p.redirectURL} readOnly className="font-mono text-xs" />
      </Field>

      <div className="flex items-center gap-2">
        <Button onClick={runWizard} disabled={busy || !p.authScopeName || !p.authSpec.token_url}>
          {busy ? <Loader2 className="mr-1 h-4 w-4 animate-spin" /> : <ShieldCheck className="mr-1 h-4 w-4" />}
          Test OAuth
        </Button>
        {p.oauthResult?.authorize_url && (
          <a
            href={p.oauthResult.authorize_url}
            target="_blank"
            rel="noopener noreferrer"
            className="inline-flex items-center gap-1 text-sm text-primary hover:underline"
          >
            Authorize in browser <ExternalLink className="h-3 w-3" />
          </a>
        )}
      </div>
      {err && <p className="text-sm text-red-500">{err}</p>}
      {p.oauthResult && (
        <div className="rounded border bg-muted p-3 text-xs">
          <div className="font-semibold">{p.oauthResult.message}</div>
          {p.oauthResult.human_approval_required && (
            <div className="mt-1 text-muted-foreground">
              Open the authorize link in a new tab. After you grant access the auth scope is ready to use.
            </div>
          )}
        </div>
      )}
    </div>
  )
}

function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="space-y-1.5">
      <Label>{label}</Label>
      {children}
    </div>
  )
}
