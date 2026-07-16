import { ExternalLink } from 'lucide-react'
import { toast } from 'sonner'
import { getOAuthAuthorizeURL } from '@/api/client'
import type { AuthScope, DownstreamServer, OAuthProvider } from '@/api/types'
import { Button } from '@/components/ui/button'
import { redirectToOAuth } from '@/lib/safe-redirect'
import { InlineCredentialCreate } from './InlineCredentialCreate'
import { InlineSecretEditor } from './InlineSecretEditor'

export function ConnectionCredentialExtras({
  scope,
  server,
  providers,
  onCreated,
  onChanged,
}: {
  scope: AuthScope | null
  server: DownstreamServer | null
  providers: OAuthProvider[]
  onCreated: (scope: AuthScope) => void
  onChanged: () => void
}) {
  async function authenticate() {
    if (!scope || scope.type !== 'oauth2') return
    try {
      const { authorize_url } = await getOAuthAuthorizeURL(scope.id)
      redirectToOAuth(authorize_url)
    } catch (error) {
      toast.error(error instanceof Error ? error.message : 'Failed to start authentication')
    }
  }

  const needsSecret = scope && (
    scope.type === 'env' ||
    scope.type === 'header' ||
    scope.type === 'hawk' ||
    scope.type === 'client_credentials'
  ) && !scope.has_secrets

  return (
    <div className="space-y-3">
      <InlineCredentialCreate
        server={server}
        providers={providers}
        onCreated={onCreated}
        onCancel={() => {}}
      />

      {scope?.type === 'oauth2' && (
        <div className="border border-border/40 bg-muted/20 px-3 py-2.5 text-sm">
          <div className="flex items-center justify-between">
            <span className="text-muted-foreground">OAuth session</span>
            <Button type="button" variant="outline" size="sm" onClick={() => void authenticate()} data-testid="connection-drawer-authenticate">
              <ExternalLink className="mr-1.5 h-3 w-3" /> Authenticate
            </Button>
          </div>
        </div>
      )}

      {needsSecret && (
        <div className="border border-amber-500/30 bg-amber-500/5 px-3 py-2.5 text-sm">
          <div className="space-y-2">
            <p className="text-amber-700">No secrets stored yet</p>
            <InlineSecretEditor scope={scope} onSaved={onChanged} />
          </div>
        </div>
      )}
    </div>
  )
}
