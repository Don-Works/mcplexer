// useMeshDiscovery — single-fetch helper that surfaces every value the
// mesh-trigger form needs as a dropdown source:
//
//   - peers       paired libp2p peers with their display names (so the
//                 user sees "morgan" not "12D3KooW…"), plus a synthetic
//                 "self" entry for local-origin messages
//   - agents      active mesh agents by Name (deduped — multiple
//                 sessions of the same agent collapse to one entry)
//   - tags        distinct tag tokens observed on live mesh messages
//                 in the last 2h
//   - audiences   distinct audience values observed in the last 2h,
//                 prepended with the broadcast wildcard "*"
//   - kinds       static list of the mesh kind enum so the form doesn't
//                 hard-code it inline
//
// All four lists are derived client-side from a single GET
// /api/v1/mesh/status + a parallel GET /api/p2p/peers. We refresh on a
// 30s tick so the dropdowns stay current even if the user keeps the
// editor open while peers join / new tags emerge.

import { useEffect, useState } from 'react'

import { getMeshStatus } from '@/api/client'
import { p2pFetch, type ListPeersResponse } from '@/components/pairing/api'

export interface KnownPeer {
  peerID: string
  displayName?: string
  // self=true for the synthetic "self" entry — matches local-origin
  // messages on the backend FromFilter.peer_id semantic.
  self?: boolean
}

export interface KnownAgent {
  name: string
  role?: string
  origin?: string // "local" | "peer:<peer_id>"
}

export interface MeshDiscovery {
  peers: KnownPeer[]
  agents: KnownAgent[]
  tags: string[]
  audiences: string[]
  kinds: string[]
  loading: boolean
}

const MESH_KINDS = [
  'finding',
  'task',
  'alert',
  'question',
  'result',
  'event',
  'reply',
]

export function useMeshDiscovery(): MeshDiscovery {
  const [state, setState] = useState<MeshDiscovery>({
    peers: [],
    agents: [],
    tags: [],
    audiences: [],
    kinds: MESH_KINDS,
    loading: true,
  })

  useEffect(() => {
    let cancelled = false
    async function refresh() {
      try {
        const [status, peersResp] = await Promise.all([
          getMeshStatus().catch(() => null),
          p2pFetch<ListPeersResponse>('/peers').catch(() => null),
        ])
        if (cancelled) return

        const peers: KnownPeer[] = [{ peerID: 'self', self: true }]
        for (const row of peersResp?.peers ?? []) {
          if (!row.peer_id) continue
          peers.push({ peerID: row.peer_id, displayName: row.display_name || undefined })
        }

        const agentMap = new Map<string, KnownAgent>()
        for (const a of status?.agents ?? []) {
          if (!a.name) continue
          if (!agentMap.has(a.name)) {
            agentMap.set(a.name, { name: a.name, role: a.role, origin: a.origin })
          }
        }

        const tags = new Set<string>()
        const audiences = new Set<string>(['*'])
        for (const m of status?.messages ?? []) {
          if (m.tags) {
            for (const t of m.tags.split(',')) {
              const v = t.trim()
              if (v) tags.add(v)
            }
          }
          if (m.audience && m.audience !== '*') audiences.add(m.audience)
        }

        setState({
          peers,
          agents: Array.from(agentMap.values()).sort((a, b) => a.name.localeCompare(b.name)),
          tags: Array.from(tags).sort(),
          audiences: Array.from(audiences).sort(),
          kinds: MESH_KINDS,
          loading: false,
        })
      } catch {
        if (!cancelled) setState((s) => ({ ...s, loading: false }))
      }
    }
    void refresh()
    const id = window.setInterval(() => void refresh(), 30_000)
    return () => {
      cancelled = true
      window.clearInterval(id)
    }
  }, [])

  return state
}

// labelForPeer renders a peer ID for the dropdown. Display names take
// priority; self gets a friendly label; otherwise we truncate the
// libp2p peer ID so the suggestion list stays compact.
export function labelForPeer(p: KnownPeer): string {
  if (p.self) return 'self (this machine)'
  if (p.displayName) return `${p.displayName} (${shortPeerID(p.peerID)})`
  return shortPeerID(p.peerID)
}

function shortPeerID(id: string): string {
  if (id.length <= 16) return id
  return `${id.slice(0, 8)}…${id.slice(-6)}`
}
