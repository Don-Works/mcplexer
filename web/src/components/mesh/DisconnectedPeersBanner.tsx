import { useCallback, useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { AlertTriangle, RotateCw } from 'lucide-react'
import { getP2PPeers } from '@/api/client'
import { p2pFetch, type ListPeersResponse, type PeerRow } from '@/components/pairing/api'
import type { MeshAgent, P2PPeerMode } from '@/api/types'
import { PEER_PREFIX } from './AgentOriginBadge'

// DisconnectedPeersBanner surfaces paired libp2p peers that are not
// currently reachable, tiered to match how alarming the situation
// actually is. Earlier UX failure was the OPPOSITE: the silent kind,
// where the mesh was broken and the active-agents list looked normal.
// The earlier "always shout NO CONNECTION the moment any agent goes
// quiet" failure was the other extreme — it fired during 30-second
// reconnector cycles and trained the eye to ignore real outages.
//
// Tiers (no row is hidden — only the wording softens):
//   • offline       — libp2p down AND peer.last_seen > 5 min ago.
//                     Loud red banner: "No connection to X for Nm".
//   • reconnecting  — libp2p down but peer.last_seen < 5 min ago.
//                     Soft amber banner: "Briefly disconnected from X
//                     (last seen Nm ago). The reconnector is searching."
//   • never_seen    — paired_at > 5 min ago AND no last_seen recorded.
//                     Soft amber banner: "X paired but never connected".
//   • live          — libp2p up OR a recent peer-origin agent in the
//                     directory. No banner row.
//
// Debounce: the banner waits until a peer has been classified non-live
// for at least DEBOUNCE_MS before showing — so a single failed-poll
// frame doesn't paint the page red.

interface Props {
  agents: MeshAgent[]
}

const DEBOUNCE_MS = 60 * 1000 // suppress for the first 60s of a flap
const RECONNECTING_WINDOW_MS = 5 * 60 * 1000

export type PeerTier = 'live' | 'reconnecting' | 'offline' | 'never_seen'

interface ClassifiedPeer {
  peer_id: string
  display_name: string
  tier: Exclude<PeerTier, 'live'>
  ageMin: number // minutes since peer.last_seen (Infinity = never)
}

export function DisconnectedPeersBanner({ agents }: Props): React.ReactElement | null {
  const [classified, setClassified] = useState<ClassifiedPeer[]>([])
  // firstSeenDisconnectedAt tracks when each peer first crossed into a
  // non-live tier in this client session. We only render rows whose
  // disconnection has persisted ≥ DEBOUNCE_MS so brief flaps don't paint.
  const [firstSeen, setFirstSeen] = useState<Record<string, number>>({})

  const refresh = useCallback(async () => {
    const [modeRes, listRes] = await Promise.all([
      getP2PPeers().catch(() => null),
      p2pFetch<ListPeersResponse>('/peers').catch(() => null),
    ])
    if (modeRes === null || listRes === null) {
      setClassified([])
      return
    }
    setClassified(classifyAll(listRes.peers ?? [], modeRes.peers ?? [], agents))
  }, [agents])

  useEffect(() => {
    void refresh()
    const id = setInterval(() => void refresh(), 10000)
    return () => clearInterval(id)
  }, [refresh])

  // Maintain firstSeen entries — set on first non-live tier, clear when
  // peer returns to live. Doing this in an effect (rather than during
  // refresh) keeps the state update single-source.
  useEffect(() => {
    const now = Date.now()
    setFirstSeen((prev) => {
      const next = { ...prev }
      const seen = new Set<string>()
      for (const c of classified) {
        seen.add(c.peer_id)
        if (next[c.peer_id] === undefined) next[c.peer_id] = now
      }
      for (const id of Object.keys(next)) {
        if (!seen.has(id)) delete next[id]
      }
      return next
    })
  }, [classified])

  const now = Date.now()
  const shown = classified.filter((c) => {
    const since = firstSeen[c.peer_id]
    return since !== undefined && now - since >= DEBOUNCE_MS
  })

  if (shown.length === 0) return null

  const offline = shown.filter((c) => c.tier === 'offline')
  const soft = shown.filter((c) => c.tier !== 'offline')

  return (
    <div className="space-y-2">
      {offline.length > 0 && (
        <div
          role="alert"
          data-testid="mesh-disconnected-peers-banner"
          className="rounded-lg border border-amber-500/40 bg-amber-500/10 px-4 py-3 text-sm text-amber-700 dark:text-amber-300"
        >
          <div className="flex items-start gap-2">
            <AlertTriangle className="mt-0.5 h-4 w-4 flex-none" aria-hidden />
            <div className="flex-1 space-y-1">
              <p className="font-medium">{formatOfflineHeadline(offline)}</p>
              <p className="text-xs opacity-80">
                <Link to="/pairing?tab=devices" className="underline hover:no-underline">
                  Open Devices
                </Link>{' '}
                to inspect pairing state. The reconnector is searching.
              </p>
            </div>
          </div>
        </div>
      )}
      {soft.length > 0 && (
        <div
          role="status"
          data-testid="mesh-reconnecting-peers-banner"
          className="rounded-lg border border-muted-foreground/20 bg-muted/40 px-4 py-2 text-xs text-muted-foreground"
        >
          <div className="flex items-center gap-2">
            <RotateCw className="h-3.5 w-3.5 animate-spin-slow" aria-hidden />
            <span>{formatSoftHeadline(soft)}</span>
          </div>
        </div>
      )}
    </div>
  )
}

function formatOfflineHeadline(peers: ClassifiedPeer[]): string {
  if (peers.length === 1) {
    const p = peers[0]
    const for_ = p.ageMin === Infinity ? '' : ` for ${formatMin(p.ageMin)}`
    return `No connection to ${peerLabel(p)}${for_}.`
  }
  const list = peers.map(peerLabel).join(', ')
  return `No connection to ${peers.length} paired peers (${list}).`
}

function formatSoftHeadline(peers: ClassifiedPeer[]): string {
  const names = peers.map(peerLabel).join(', ')
  return `Reconnecting to ${names}.`
}

function formatMin(min: number): string {
  if (min < 60) return `${min}m`
  const h = Math.floor(min / 60)
  return `${h}h${min % 60}m`
}

function peerLabel(p: { peer_id: string; display_name: string }): string {
  return p.display_name || `${PEER_PREFIX}${p.peer_id.slice(-10)}`
}

// classifyAll combines paired-peer rows, live libp2p modes, and active
// mesh agents into a per-peer tier. Pure for snapshot-testability.
export function classifyAll(
  paired: PeerRow[],
  liveModes: P2PPeerMode[],
  agents: MeshAgent[],
): ClassifiedPeer[] {
  const liveByPeer = new Map<string, string>()
  for (const m of liveModes) liveByPeer.set(m.peer, m.mode)

  const seenViaMesh = new Set<string>()
  for (const a of agents) {
    if (a.origin && a.origin.startsWith(PEER_PREFIX)) {
      seenViaMesh.add(a.origin.slice(PEER_PREFIX.length))
    }
  }

  const now = Date.now()
  const out: ClassifiedPeer[] = []
  for (const row of paired) {
    if (row.revoked_at) continue
    const live = liveByPeer.get(row.peer_id) ?? ''
    const libp2pUp = live !== '' && live !== 'none'
    if (libp2pUp || seenViaMesh.has(row.peer_id)) continue
    const ageMin = row.last_seen
      ? Math.floor((now - new Date(row.last_seen).getTime()) / 60000)
      : Infinity
    let tier: Exclude<PeerTier, 'live'>
    if (!row.last_seen) tier = 'never_seen'
    else if (ageMin * 60000 < RECONNECTING_WINDOW_MS) tier = 'reconnecting'
    else tier = 'offline'
    out.push({ peer_id: row.peer_id, display_name: row.display_name, tier, ageMin })
  }
  return out
}

// peerConnectedFromModes is a small helper for AgentRow tier dimming —
// returns whether the given peer ID currently shows an active libp2p
// connection (anything other than 'none' or absent).
export function peerConnectedFromModes(peerID: string, modes: P2PPeerMode[]): boolean {
  for (const m of modes) {
    if (m.peer === peerID) return m.mode !== '' && m.mode !== 'none'
  }
  return false
}
