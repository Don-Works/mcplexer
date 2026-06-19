import { useCallback, useEffect, useMemo, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Badge } from '@/components/ui/badge'
import { CopyButton } from '@/components/ui/copy-button'
import { Loader2, ShieldOff, Trash2, UserRound, X } from 'lucide-react'
import { toast } from 'sonner'
import { getUser, listUsers } from '@/api/client'
import type { UserWithPeers } from '@/api/types'
import {
  formatRelative,
  p2pFetch,
  type ListPeersResponse,
  type PeerRow,
  type PeerStatus,
} from '@/components/pairing/api'
import { ShowCodeModal } from '@/components/pairing/ShowCodeModal'
import { EnterCodeModal } from '@/components/pairing/EnterCodeModal'
import { PeerConnectionBadge, type ConnectionMode } from '@/components/p2p/PeerConnectionBadge'
import { ReconnectBadge } from '@/components/p2p/ReconnectBadge'

const GETTING_STARTED_DISMISSED_KEY = 'mcplexer:pairing:getting-started:dismissed'

// shortPeerSuffix is the same 8-char tail we use server-side for the
// fallback "peer-…" label. Keeping this in sync is load-bearing — UI logic
// for collision disambiguation depends on it lining up with the server.
function shortPeerSuffix(peerID: string): string {
  return peerID.length > 8 ? peerID.slice(-8) : peerID
}

function GettingStartedCard({ onDismiss }: { onDismiss: () => void }) {
  const [selfPeerID, setSelfPeerID] = useState<string | null>(null)
  const [selfLoading, setSelfLoading] = useState(true)
  const [selfError, setSelfError] = useState<string | null>(null)
  const [remotePeerID, setRemotePeerID] = useState('')
  const [pairing, setPairing] = useState(false)
  const [pairResult, setPairResult] = useState<{ ok: boolean; msg: string } | null>(null)

  useEffect(() => {
    let active = true
    p2pFetch<{ peer_id: string }>('/identity')
      .then((res) => { if (active) setSelfPeerID(res.peer_id) })
      .catch((e) => { if (active) setSelfError(e instanceof Error ? e.message : String(e)) })
      .finally(() => { if (active) setSelfLoading(false) })
    return () => { active = false }
  }, [])

  const handlePair = useCallback(async () => {
    const id = remotePeerID.trim()
    if (!id) {
      toast.error('Paste the remote peer ID first')
      return
    }
    setPairing(true)
    setPairResult(null)
    try {
      const res = await p2pFetch<{ code: string; qr_payload: string }>('/pair/start', { method: 'POST' })
      await p2pFetch<void>('/pair/complete', {
        method: 'POST',
        body: JSON.stringify({ code: res.code, peer_id: id }),
      })
      setPairResult({ ok: true, msg: 'Pairing initiated. Check the remote machine to confirm.' })
      toast.success('Pairing request sent')
    } catch (e) {
      const msg = e instanceof Error ? e.message : String(e)
      setPairResult({ ok: false, msg })
      toast.error(`Pair failed: ${msg}`)
    } finally {
      setPairing(false)
    }
  }, [remotePeerID])

  return (
    <Card className="border-dashed">
      <CardHeader className="flex flex-row items-start justify-between space-y-0">
        <div>
          <CardTitle className="text-base">Getting started</CardTitle>
          <p className="mt-1 text-sm text-muted-foreground">
            Connect another machine to this MCPlexer instance.
          </p>
        </div>
        <Button
          variant="ghost"
          size="icon-xs"
          onClick={onDismiss}
          aria-label="Dismiss getting started"
          className="text-muted-foreground hover:text-foreground shrink-0"
        >
          <X className="h-4 w-4" />
        </Button>
      </CardHeader>
      <CardContent className="space-y-6">
        <div className="space-y-2">
          <div className="text-xs font-medium text-muted-foreground uppercase tracking-wider">
            This machine
          </div>
          {selfLoading && (
            <div className="flex items-center gap-2 py-3 text-sm text-muted-foreground">
              <Loader2 className="h-4 w-4 animate-spin" />
              Loading peer ID...
            </div>
          )}
          {selfError && (
            <div className="flex items-center gap-2 py-3 text-sm text-destructive">
              <ShieldOff className="h-4 w-4" />
              {selfError}
            </div>
          )}
          {selfPeerID && (
            <div className="flex items-center gap-3 rounded border bg-muted/40 px-4 py-3">
              <div className="min-w-0 flex-1">
                <div className="text-[10px] text-muted-foreground mb-1">Peer ID</div>
                <div
                  className="font-mono text-lg font-semibold tracking-wide break-all select-all"
                  data-testid="self-peer-id"
                >
                  {selfPeerID}
                </div>
              </div>
              <CopyButton value={selfPeerID} className="shrink-0" />
            </div>
          )}
        </div>

        <div className="space-y-4">
          <div className="text-xs font-medium text-muted-foreground uppercase tracking-wider">
            Connect another machine
          </div>

          <div className="space-y-3">
            <div className="flex gap-3">
              <Badge variant="outline" tone="mono" className="shrink-0 mt-0.5">1</Badge>
              <div className="space-y-1.5 min-w-0">
                <div className="text-sm font-medium">Install mcplexer on the other machine</div>
                <div className="flex items-center gap-2 rounded border bg-muted/40 px-3 py-2">
                  <code className="font-mono text-xs flex-1 select-all">make install</code>
                  <CopyButton value="make install" />
                </div>
              </div>
            </div>

            <div className="flex gap-3">
              <Badge variant="outline" tone="mono" className="shrink-0 mt-0.5">2</Badge>
              <div className="space-y-1.5 min-w-0">
                <div className="text-sm font-medium">Run the pairing command</div>
                <div className="flex items-center gap-2 rounded border bg-muted/40 px-3 py-2">
                  <code className="font-mono text-xs flex-1 select-all">mcplexer pair</code>
                  <CopyButton value="mcplexer pair" />
                </div>
              </div>
            </div>

            <div className="flex gap-3">
              <Badge variant="outline" tone="mono" className="shrink-0 mt-0.5">3</Badge>
              <div className="space-y-1.5 min-w-0">
                <div className="text-sm font-medium">Paste the remote peer ID here</div>
                <div className="flex gap-2">
                  <Input
                    placeholder="12D3KooW..."
                    value={remotePeerID}
                    onChange={(e) => setRemotePeerID(e.target.value)}
                    className="font-mono text-xs"
                    data-testid="pairing-remote-peer-input"
                  />
                  <Button
                    onClick={handlePair}
                    disabled={pairing || !remotePeerID.trim()}
                    data-testid="pairing-remote-peer-submit"
                    size="sm"
                  >
                    {pairing && <Loader2 className="mr-1.5 h-3.5 w-3.5 animate-spin" />}
                    Pair
                  </Button>
                </div>
                {pairResult && (
                  <div
                    className={`text-xs ${pairResult.ok ? 'text-emerald-500' : 'text-destructive'}`}
                    data-testid="pairing-remote-peer-result"
                  >
                    {pairResult.msg}
                  </div>
                )}
              </div>
            </div>
          </div>
        </div>

        <div className="rounded border border-dashed bg-muted/20 px-4 py-3 text-xs text-muted-foreground">
          You can also use the 6-digit code flow below: click "Pair this device" to
          show a code, then enter it on the other machine.
        </div>
      </CardContent>
    </Card>
  )
}

function PeerRowItem({
  peer,
  collides,
  onRevoke,
}: {
  peer: PeerRow
  collides: boolean
  onRevoke: () => void
}) {
  const revoked = !!peer.revoked_at
  const [mode, setMode] = useState<ConnectionMode>(peer.connection_mode ?? null)
  const [reconnect, setReconnect] = useState<{
    state?: PeerStatus['reconnect_state']
    lastAttemptAt?: string
    lastError?: string
  }>({
    state: peer.reconnect_state,
    lastAttemptAt: peer.last_dial_attempt_at,
    lastError: peer.last_dial_error,
  })

  useEffect(() => {
    // Hydrate from /status so the row stays fresh between list refreshes.
    // Poll every 10s — without this, a peer that flips searching → connected
    // (via libp2p auto-dial after a transient disconnect) leaves the badge
    // stuck at "Searching" until the user manually refreshes the page.
    if (revoked) return
    let active = true
    const tick = () => {
      p2pFetch<PeerStatus>(`/peers/${encodeURIComponent(peer.peer_id)}/status`)
        .then((res) => {
          if (!active) return
          const m = res.connection_mode === 'none' ? '' : res.connection_mode
          setMode((m as ConnectionMode) ?? null)
          setReconnect({
            state: res.reconnect_state,
            lastAttemptAt: res.last_dial_attempt_at,
            lastError: res.last_dial_error,
          })
        })
        .catch(() => { /* status endpoint optional; badges stay hidden */ })
    }
    tick()
    const id = setInterval(tick, 10000)
    return () => {
      active = false
      clearInterval(id)
    }
  }, [peer.peer_id, revoked])

  const suffix = shortPeerSuffix(peer.peer_id)
  const primary = peer.display_name || `peer-${suffix}`
  const showPeerPrefix = collides && !!peer.display_name
  return (
    <div
      className="flex items-center justify-between border-b border-border/40 py-3 last:border-0"
      data-testid={`peer-row-${peer.peer_id}`}
    >
      <div className="min-w-0">
        <div className={`flex items-center gap-2 text-sm font-medium ${revoked ? 'text-muted-foreground line-through' : ''}`}>
          <span
            className="truncate"
            data-testid={`peer-display-name-${peer.peer_id}`}
          >
            {primary}
            {showPeerPrefix && (
              <span className="ml-1.5 font-mono text-xs text-muted-foreground">
                ({suffix.slice(-3)}…)
              </span>
            )}
          </span>
          {!revoked && <PeerConnectionBadge mode={mode} className="text-[10px]" />}
          {!revoked && (
            <ReconnectBadge
              state={reconnect.state}
              lastAttemptAt={reconnect.lastAttemptAt}
              lastError={reconnect.lastError}
              className="text-[10px]"
            />
          )}
        </div>
        <div className="text-xs text-muted-foreground">
          peer-{suffix} · paired {formatRelative(peer.paired_at)} · last seen {formatRelative(peer.last_seen)}
          {reconnect.lastAttemptAt && (
            <> · last try {formatRelative(reconnect.lastAttemptAt)}</>
          )}
          {revoked && ' · revoked'}
        </div>
      </div>
      {!revoked && (
        <Button
          variant="ghost"
          size="sm"
          onClick={onRevoke}
          data-testid={`peer-revoke-${peer.peer_id}`}
          aria-label={`Revoke ${primary}`}
          className="text-destructive hover:bg-destructive/10 hover:text-destructive"
        >
          <Trash2 className="mr-1.5 h-3.5 w-3.5" />
          Revoke
        </Button>
      )}
    </div>
  )
}

// PairingPage is the M1.2 entry point: shows trusted peers, lets the user
// pair a new one (either by displaying or by entering a code). Note we
// surface ONLY display names + relative timestamps to the user — no peer
// IDs, no ports, no IPs. Identity verification rides on the 6-digit code.
export function PairingPage() {
  const [peers, setPeers] = useState<PeerRow[]>([])
  const [users, setUsers] = useState<UserWithPeers[]>([])
  const [loading, setLoading] = useState(true)
  const [usersLoading, setUsersLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [usersError, setUsersError] = useState<string | null>(null)
  const [searchParams, setSearchParams] = useSearchParams()
  const [gsDismissed, setGsDismissed] = useState(
    () => localStorage.getItem(GETTING_STARTED_DISMISSED_KEY) === '1',
  )

  // pastePayload is set when the page is opened via the Electron deeplink
  // (mcplexer://pair/...) — main.ts loads /pairing?paste=<url>. We forward
  // the URL into EnterCodeModal as its initialPayload so the user just
  // confirms the 6-digit code. We strip the query param after consuming
  // it so a refresh doesn't replay an old URL.
  const pastePayload = useMemo(() => searchParams.get('paste') ?? '', [searchParams])
  useEffect(() => {
    if (!pastePayload) return
    const next = new URLSearchParams(searchParams)
    next.delete('paste')
    setSearchParams(next, { replace: true })
  }, [pastePayload, searchParams, setSearchParams])

  const refresh = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const res = await p2pFetch<ListPeersResponse>('/peers')
      setPeers(res.peers ?? [])
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setLoading(false)
    }
  }, [])

  const refreshUsers = useCallback(async () => {
    setUsersLoading(true)
    setUsersError(null)
    try {
      const res = await listUsers()
      const rows = await Promise.all((res.users ?? []).map((u) => getUser(u.user_id)))
      setUsers(rows)
    } catch (e) {
      setUsersError(e instanceof Error ? e.message : String(e))
    } finally {
      setUsersLoading(false)
    }
  }, [])

  useEffect(() => { void refresh() }, [refresh])
  useEffect(() => { void refreshUsers() }, [refreshUsers])

  const revoke = useCallback(async (peerID: string) => {
    try {
      await p2pFetch<void>(`/peers/${encodeURIComponent(peerID)}`, { method: 'DELETE' })
      toast.success('Peer revoked')
      await refresh()
    } catch (e) {
      toast.error(`Revoke failed: ${e instanceof Error ? e.message : String(e)}`)
    }
  }, [refresh])

  const dismissGettingStarted = useCallback(() => {
    setGsDismissed(true)
    localStorage.setItem(GETTING_STARTED_DISMISSED_KEY, '1')
  }, [])

  const showGettingStarted = !loading && !error && peers.length === 0 && !gsDismissed

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight">Paired devices</h1>
        <p className="mt-1 text-sm text-muted-foreground">
          Link other MCPlexer devices with a 6-digit code. No ports, IPs, or
          peer IDs to manage.
        </p>
      </div>
      {showGettingStarted && (
        <GettingStartedCard onDismiss={dismissGettingStarted} />
      )}
      <div className="flex gap-2">
        <ShowCodeModal onComplete={() => { void refresh() }} />
        <EnterCodeModal
          onComplete={() => { void refresh() }}
          initialPayload={pastePayload}
          autoOpen={!!pastePayload}
        />
      </div>
      <Card>
        <CardHeader>
          <CardTitle className="text-base">Trusted peers</CardTitle>
        </CardHeader>
        <CardContent>
          {loading && (
            <div className="flex items-center gap-2 py-6 text-sm text-muted-foreground">
              <Loader2 className="h-4 w-4 animate-spin" />
              Loading peers...
            </div>
          )}
          {error && (
            <div className="flex items-center gap-2 py-6 text-sm text-destructive">
              <ShieldOff className="h-4 w-4" />
              {error}
            </div>
          )}
          {!loading && !error && peers.length === 0 && (
            <p className="py-6 text-sm text-muted-foreground">
              No paired devices yet. Tap "Pair this device" above to start.
            </p>
          )}
          {!loading && (() => {
            // Names that appear more than once across the active peer list
            // get a short peer suffix appended so users can tell them apart.
            const counts: Record<string, number> = {}
            for (const p of peers) {
              if (!p.revoked_at && p.display_name) {
                counts[p.display_name] = (counts[p.display_name] ?? 0) + 1
              }
            }
            return peers.map((p) => (
              <PeerRowItem
                key={p.peer_id}
                peer={p}
                collides={(counts[p.display_name] ?? 0) > 1}
                onRevoke={() => revoke(p.peer_id)}
              />
            ))
          })()}
        </CardContent>
      </Card>
      <Card>
        <CardHeader>
          <CardTitle className="text-base">Human identities</CardTitle>
        </CardHeader>
        <CardContent>
          {usersLoading && (
            <div className="flex items-center gap-2 py-6 text-sm text-muted-foreground">
              <Loader2 className="h-4 w-4 animate-spin" />
              Loading identities...
            </div>
          )}
          {usersError && (
            <div className="flex items-center gap-2 py-6 text-sm text-destructive">
              <ShieldOff className="h-4 w-4" />
              {usersError}
            </div>
          )}
          {!usersLoading && !usersError && users.length === 0 && (
            <p className="py-6 text-sm text-muted-foreground">
              No human identities recorded yet. Pair another device to link an owner.
            </p>
          )}
          {!usersLoading && !usersError && users.length > 0 && (
            <div className="divide-y divide-border/40">
              {users.map((user) => (
                <div key={user.user_id} className="py-3" data-testid={`user-row-${user.user_id}`}>
                  <div className="flex items-center gap-2 text-sm font-medium">
                    <UserRound className="h-4 w-4 text-muted-foreground" />
                    <span>{user.display_name || user.user_id}</span>
                    {user.is_self && (
                      <span className="rounded border border-primary/30 bg-primary/10 px-1.5 py-0.5 text-[10px] font-medium text-primary">
                        this device
                      </span>
                    )}
                  </div>
                  <div className="mt-1 text-xs text-muted-foreground">
                    {user.peers.length === 0
                      ? 'No linked devices'
                      : user.peers.map((p) => p.display_name || `peer-${shortPeerSuffix(p.peer_id)}`).join(', ')}
                  </div>
                  <div className="mt-0.5 font-mono text-[10px] text-muted-foreground/70">
                    {user.user_id}
                  </div>
                </div>
              ))}
            </div>
          )}
        </CardContent>
      </Card>
    </div>
  )
}
