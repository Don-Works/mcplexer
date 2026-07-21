import type { P2PIdentityResponse, P2PPeersResponse } from './types'
import { ApiClientError, DEFAULT_TIMEOUT_MS } from './transport'

// P2P (debug-only — endpoints live at /api/p2p/* not /api/v1/p2p/*).
// Returns 501 if the daemon was built without the `p2p` build tag; the UI
// should treat that as "feature off, hide the panel".
async function p2pRequest<T>(path: string): Promise<T | null> {
  const base = import.meta.env.VITE_API_BASE_URL ? import.meta.env.VITE_API_BASE_URL.replace(/\/v1$/, '') : '/api'
  const res = await fetch(`${base}${path}`, { signal: AbortSignal.timeout(DEFAULT_TIMEOUT_MS) })
  if (res.status === 501 || res.status === 404) return null
  if (!res.ok) {
    throw new ApiClientError(res.status, await res.text())
  }
  return res.json() as Promise<T>
}

export function getP2PIdentity(): Promise<P2PIdentityResponse | null> {
  return p2pRequest('/p2p/identity')
}

export function getP2PPeers(): Promise<P2PPeersResponse | null> {
  return p2pRequest('/p2p/peers')
}

// P2P (M1.3) — peer connection-mode status.
export interface P2PPeerStatus {
  peer_id: string
  connection_mode?: 'direct' | 'hole-punched' | 'relay' | ''
  last_seen?: string
  addrs: string[]
}

export async function getP2PPeerStatus(peerID: string): Promise<P2PPeerStatus> {
  const res = await fetch(`/api/p2p/peers/${encodeURIComponent(peerID)}/status`, {
    headers: { 'Content-Type': 'application/json' },
    signal: AbortSignal.timeout(DEFAULT_TIMEOUT_MS),
  })
  if (!res.ok) {
    const body = await res.text()
    throw new ApiClientError(res.status, body)
  }
  return res.json() as Promise<P2PPeerStatus>
}
