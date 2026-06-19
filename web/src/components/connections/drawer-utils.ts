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
  if (!state) return 'Configure the route between this server and workspace.'
  switch (state.kind) {
    case 'connected':
      return 'Route is live. Pick a different credential or remove it below.'
    case 'needs-auth':
      return 'Route exists but the credential needs setup.'
    case 'add':
      return 'No route yet. Pick a credential (or none) and click Connect.'
    case 'disabled':
      return 'Route is set to deny. Save to switch to allow, or remove it.'
  }
}
