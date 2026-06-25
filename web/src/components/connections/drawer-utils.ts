import type { AuthScope, DownstreamServer } from '@/api/types'
import type { CellState } from './ConnectionCell'

// filterCompatibleScopes restricts the credential dropdown to scopes
// that can actually be attached to the given server. env-style scopes
// inject into a subprocess environment, so they only make sense for
// stdio transports. header / oauth2 / client_credentials work for
// either stdio or http. Returning all scopes when no server is set
// avoids flicker while the parent's data is loading.
export function filterCompatibleScopes(
  scopes: AuthScope[],
  server: DownstreamServer | null,
): AuthScope[] {
  if (!server) return scopes
  if (server.transport === 'http') return scopes.filter((s) => s.type !== 'env')
  return scopes
}

export function describeState(state: CellState | null): string {
  if (!state) return 'Configure server access for this workspace.'
  switch (state.kind) {
    case 'connected':
      return 'Access is live. Edit matching, approval, credential, policy, or remove it below.'
    case 'needs-auth':
      return 'Access is configured, but the selected credential still needs setup.'
    case 'add':
      return 'No access rule yet. Configure it, then connect this server to the workspace.'
    case 'disabled':
      return 'Access is set to deny. Review the rule, switch policy, or remove it.'
  }
}
