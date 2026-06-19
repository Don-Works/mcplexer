import { useCallback, useEffect, useState } from 'react'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { useApi } from '@/hooks/use-api'
import { getP2PIdentity, getP2PPeers, getSettings } from '@/api/client'
import { p2pFetch, type ListPeersResponse } from '@/components/pairing/api'
import type { P2PPeerMode } from '@/api/types'
import { ChevronDown, ChevronRight, Network } from 'lucide-react'

// P2PDebugPanel renders a collapsed-by-default debug card showing the
// connection mode of every peer the embedded libp2p host is currently
// connected to. This is intentionally NOT user-facing config — it's a
// diagnostic tool surfaced for advanced users / support cases.
//
// Returns null when the daemon was built without -tags p2p (501 from
// the API maps to a null result), so the panel disappears entirely
// rather than showing an "unavailable" state. That's the right call for
// a debug-only feature: invisible when off, present when relevant.
export function P2PDebugPanel() {
  const idFetcher = useCallback(() => getP2PIdentity(), [])
  const { data: identity } = useApi(idFetcher)
  const settingsFetcher = useCallback(() => getSettings(), [])
  const { data: settingsData } = useApi(settingsFetcher)
  const localName = settingsData?.settings?.display_name ?? ''

  const [open, setOpen] = useState(false)
  const [peers, setPeers] = useState<P2PPeerMode[] | null>(null)
  const [names, setNames] = useState<Record<string, string>>({})
  const [loading, setLoading] = useState(false)

  useEffect(() => {
    if (!open) return
    let cancelled = false
    const refresh = async () => {
      setLoading(true)
      try {
        const [modeRes, peerListRes] = await Promise.all([
          getP2PPeers(),
          p2pFetch<ListPeersResponse>('/peers').catch(() => null),
        ])
        if (cancelled) return
        setPeers(modeRes?.peers ?? [])
        // Build a peer_id → display_name map from the paired-peers list
        // so the debug panel can replace truncated PeerIDs with friendly
        // labels. Falls back to "" so the legacy view (just peer ids) is
        // still rendered when no paired-peer row exists yet.
        const map: Record<string, string> = {}
        for (const row of peerListRes?.peers ?? []) {
          if (row.display_name) map[row.peer_id] = row.display_name
        }
        setNames(map)
      } finally {
        if (!cancelled) setLoading(false)
      }
    }
    void refresh()
    const id = setInterval(() => void refresh(), 5000)
    return () => {
      cancelled = true
      clearInterval(id)
    }
  }, [open])

  // p2p not built in OR not enabled: render nothing.
  if (!identity) return null

  return (
    <Card>
      <CardHeader
        className="cursor-pointer select-none"
        onClick={() => setOpen((v) => !v)}
      >
        <CardTitle className="flex items-center gap-2 text-sm font-medium uppercase tracking-wider text-muted-foreground">
          {open ? <ChevronDown className="h-4 w-4" /> : <ChevronRight className="h-4 w-4" />}
          <Network className="h-4 w-4" />
          P2P Debug{' '}
          <span data-testid="p2p-debug-local-name">
            ({localName || identity.peer_id.slice(0, 12) + '...'})
          </span>
        </CardTitle>
      </CardHeader>
      {open && (
        <CardContent className="space-y-3">
          <div className="text-xs text-muted-foreground">
            Listen addrs: <span className="font-mono">{identity.multiaddrs.length}</span>
            {loading && <span className="ml-2">refreshing...</span>}
          </div>
          {peers === null ? (
            <p className="text-sm text-muted-foreground">Loading...</p>
          ) : peers.length === 0 ? (
            <p className="text-sm text-muted-foreground">No active peers</p>
          ) : (
            <ul className="space-y-1">
              {peers.map((p) => (
                <PeerRow key={p.peer} peer={p} displayName={names[p.peer]} />
              ))}
            </ul>
          )}
        </CardContent>
      )}
    </Card>
  )
}

const modeStyle: Record<string, string> = {
  direct: 'bg-emerald-500/10 text-emerald-500 border-emerald-500/20',
  'hole-punched': 'bg-blue-500/10 text-blue-500 border-blue-500/20',
  'via-relay': 'bg-amber-500/10 text-amber-500 border-amber-500/20',
  none: 'bg-muted text-muted-foreground border-border',
}

const modeLabel: Record<string, string> = {
  direct: 'Direct',
  'hole-punched': 'Hole-punched',
  'via-relay': 'Via relay',
  none: 'No conn',
}

function PeerRow({
  peer,
  displayName,
}: {
  peer: P2PPeerMode
  displayName?: string
}) {
  const style = modeStyle[peer.mode] ?? modeStyle.none
  const label = modeLabel[peer.mode] ?? peer.mode
  return (
    <li
      className="flex items-center justify-between gap-2 rounded border border-border/40 bg-muted/30 px-3 py-1.5"
      data-testid={`p2p-debug-peer-${peer.peer}`}
    >
      <div className="flex flex-col min-w-0">
        {displayName && (
          <span className="text-xs font-medium" data-testid={`p2p-debug-peer-name-${peer.peer}`}>
            {displayName}
          </span>
        )}
        <span className="font-mono text-[10px] text-muted-foreground truncate">{peer.peer}</span>
      </div>
      <span
        className={`inline-flex items-center rounded-md border px-2 py-0.5 text-[10px] font-medium ${style}`}
      >
        {label}
      </span>
    </li>
  )
}
