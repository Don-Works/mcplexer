// Pairing API helpers. The pairing endpoints live under /api/p2p (NOT
// /api/v1) because they exist regardless of API version + are wired
// directly on the http.ServeMux.
const P2P_BASE = '/api/p2p'

export interface PairStartResponse {
  code: string
  qr_payload: string
  qr_data_url: string
  expires_at: string
}

// QRPayload is the JSON shape encoded inside `qr_payload` (and in the QR
// PNG). multiaddrs were dropped in 0.3 — the responder side resolves the
// peer's current AddrInfo via the DHT, which keeps the QR payload small
// and survives IP-rotations. See `pairCompleteRequest` in
// internal/api/p2p_pairing_handler.go for the matching server shape.
export interface QRPayload {
  code: string
  peer_id: string
  display_name?: string
  user_id?: string
}

// ReconnectState mirrors the Go-side string constants in
// internal/p2p/reconnector_p2p.go. Stable wire contract: the UI
// switch-cases on these literals to color the reconnector badge. New
// states from the server land as `string` so the UI never breaks.
export type ReconnectState =
  | 'connected'
  | 'searching_dht'
  | 'dial_failed'
  | 'not_found_in_dht'
  | 'dht_unavailable'
  | ''

export interface PeerRow {
  peer_id: string
  display_name: string
  paired_at: string
  last_seen?: string
  trust_level: number
  scopes: string[]
  revoked_at?: string
  // connection_mode is best-effort: the list endpoint may not populate it.
  // When present it drives the <PeerConnectionBadge /> in the row UI.
  connection_mode?: 'direct' | 'hole-punched' | 'relay' | ''
  // Reconnector telemetry — populated by the daemon's reconnect loop.
  // omitempty server-side; treat undefined and "" identically.
  last_dial_attempt_at?: string
  last_dial_error?: string
  reconnect_state?: ReconnectState
  // ssh_target is user@host (or ssh-config alias) used by the dashboard's
  // "Focus" button for peer-origin agents. Empty when unset.
  ssh_target?: string
}

// setPeerSSHTarget updates the SSH alias used to focus into this peer's
// tmux. Empty target clears. Returns the new target on success.
export async function setPeerSSHTarget(peerId: string, sshTarget: string): Promise<{ ok: boolean; ssh_target: string }> {
  return p2pFetch(`/peers/${encodeURIComponent(peerId)}/ssh-target`, {
    method: 'PATCH',
    body: JSON.stringify({ ssh_target: sshTarget }),
  })
}

export interface PeerStatus {
  peer_id: string
  connection_mode: 'direct' | 'hole-punched' | 'relay' | 'none' | ''
  addrs?: string[]
  last_seen?: string
  last_dial_attempt_at?: string
  last_dial_error?: string
  reconnect_state?: ReconnectState
}

export interface ListPeersResponse {
  peers: PeerRow[]
}

export async function p2pFetch<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(`${P2P_BASE}${path}`, {
    headers: { 'Content-Type': 'application/json' },
    ...init,
  })
  if (!res.ok) {
    const body = await res.text()
    throw new Error(body || `request failed: ${res.status}`)
  }
  if (res.status === 204) return undefined as T
  return res.json() as Promise<T>
}

export function formatRelative(s?: string): string {
  if (!s) return 'never'
  const d = Date.now() - new Date(s).getTime()
  const mins = Math.floor(d / 60000)
  if (mins < 1) return 'just now'
  if (mins < 60) return `${mins}m ago`
  const hours = Math.floor(mins / 60)
  if (hours < 24) return `${hours}h ago`
  return `${Math.floor(hours / 24)}d ago`
}

export function formatExpiry(iso: string): string {
  const ms = new Date(iso).getTime() - Date.now()
  if (ms <= 0) return 'expired'
  const mins = Math.floor(ms / 60000)
  const secs = Math.floor((ms % 60000) / 1000)
  return `${mins}m ${secs}s`
}
