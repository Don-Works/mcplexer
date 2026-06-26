import { useCallback, useEffect, useMemo, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Badge } from '@/components/ui/badge'
import { CopyButton } from '@/components/ui/copy-button'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Laptop, Link2, Loader2, ShieldOff, Trash2, UserRound, UsersRound, X } from 'lucide-react'
import { toast } from 'sonner'
import { deleteUser, getUser, listUsers, updateDeviceOwner } from '@/api/client'
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
const UNLINKED_OWNER_VALUE = '__unlinked__'
type PairingTab = 'people' | 'devices'

// shortPeerSuffix is the same 8-char tail we use server-side for the
// fallback "peer-…" label. Keeping this in sync is load-bearing — UI logic
// for collision disambiguation depends on it lining up with the server.
function shortPeerSuffix(peerID: string): string {
  return peerID.length > 8 ? peerID.slice(-8) : peerID
}

interface DeviceOwner {
  user_id: string
  display_name: string
  is_self: boolean
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
  owners,
  ownerChoices,
  ownersReady,
  ownerUpdating,
  onOwnerChange,
  onRevoke,
}: {
  peer: PeerRow
  collides: boolean
  owners: DeviceOwner[]
  ownerChoices: DeviceOwner[]
  ownersReady: boolean
  ownerUpdating: boolean
  onOwnerChange: (userID: string | null) => void
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
  const selectedOwner = owners[0]?.user_id ?? UNLINKED_OWNER_VALUE
  const ownerText = owners
    .map((owner) => owner.display_name || owner.user_id)
    .join(', ')
  return (
    <div
      className="flex flex-col gap-3 border-b border-border/40 py-3 last:border-0 sm:flex-row sm:items-center sm:justify-between"
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
          {!revoked && ownersReady && owners.length > 0 && (
            <Badge variant="outline" tone="info" className="text-[10px]">
              <UserRound className="h-3 w-3" />
              {ownerText}
            </Badge>
          )}
          {!revoked && ownersReady && owners.length === 0 && (
            <Badge variant="outline" tone="warn" className="text-[10px]">
              unlinked
            </Badge>
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
        <div className="flex shrink-0 items-center gap-2">
          {ownersReady && (
            <Select
              value={selectedOwner}
              disabled={ownerUpdating || ownerChoices.length === 0}
              onValueChange={(value) => {
                onOwnerChange(value === UNLINKED_OWNER_VALUE ? null : value)
              }}
            >
              <SelectTrigger
                className="h-8 w-[170px] text-xs"
                data-testid={`peer-owner-${peer.peer_id}`}
                aria-label={`Owner for ${primary}`}
              >
                {ownerUpdating ? (
                  <Loader2 className="h-3.5 w-3.5 animate-spin" />
                ) : (
                  <SelectValue placeholder="Owner" />
                )}
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={UNLINKED_OWNER_VALUE}>Unlinked</SelectItem>
                {ownerChoices.map((owner) => (
                  <SelectItem key={owner.user_id} value={owner.user_id}>
                    {owner.display_name || owner.user_id}{owner.is_self ? ' (you)' : ''}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          )}
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
        </div>
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
  const [deletingUserID, setDeletingUserID] = useState<string | null>(null)
  const [updatingOwnerPeerID, setUpdatingOwnerPeerID] = useState<string | null>(null)
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
  const activeTab = useMemo<PairingTab>(() => {
    if (pastePayload) return 'devices'
    return searchParams.get('tab') === 'people' ? 'people' : 'devices'
  }, [pastePayload, searchParams])
  useEffect(() => {
    if (!pastePayload) return
    const next = new URLSearchParams(searchParams)
    next.delete('paste')
    next.set('tab', 'devices')
    setSearchParams(next, { replace: true })
  }, [pastePayload, searchParams, setSearchParams])

  const changeTab = useCallback((value: string) => {
    const tab: PairingTab = value === 'people' ? 'people' : 'devices'
    const next = new URLSearchParams(searchParams)
    next.set('tab', tab)
    setSearchParams(next)
  }, [searchParams, setSearchParams])

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
      await Promise.all([refresh(), refreshUsers()])
    } catch (e) {
      toast.error(`Revoke failed: ${e instanceof Error ? e.message : String(e)}`)
    }
  }, [refresh, refreshUsers])

  const removeStaleIdentity = useCallback(async (user: UserWithPeers) => {
    if (user.is_self || user.peers.length > 0) return
    if (!window.confirm(`Remove stale identity "${user.display_name || user.user_id}"?`)) return
    setDeletingUserID(user.user_id)
    try {
      await deleteUser(user.user_id)
      toast.success('Identity removed')
      await refreshUsers()
    } catch (e) {
      toast.error(`Remove failed: ${e instanceof Error ? e.message : String(e)}`)
    } finally {
      setDeletingUserID(null)
    }
  }, [refreshUsers])

  const changeDeviceOwner = useCallback(async (peerID: string, userID: string | null) => {
    setUpdatingOwnerPeerID(peerID)
    try {
      await updateDeviceOwner(peerID, userID)
      toast.success(userID ? 'Device owner updated' : 'Device unlinked')
      await refreshUsers()
    } catch (e) {
      toast.error(`Owner update failed: ${e instanceof Error ? e.message : String(e)}`)
    } finally {
      setUpdatingOwnerPeerID(null)
    }
  }, [refreshUsers])

  const dismissGettingStarted = useCallback(() => {
    setGsDismissed(true)
    localStorage.setItem(GETTING_STARTED_DISMISSED_KEY, '1')
  }, [])

  const showGettingStarted = !loading && !error && peers.length === 0 && !gsDismissed
  const activePeers = useMemo(() => peers.filter((p) => !p.revoked_at), [peers])
  const ownerDataReady = !usersLoading && !usersError
  const ownerByPeerID = useMemo(() => {
    const byPeer = new Map<string, DeviceOwner[]>()
    for (const user of users) {
      for (const peer of user.peers ?? []) {
        const owners = byPeer.get(peer.peer_id) ?? []
        owners.push({
          user_id: user.user_id,
          display_name: user.display_name,
          is_self: user.is_self,
        })
        byPeer.set(peer.peer_id, owners)
      }
    }
    return byPeer
  }, [users])
  const ownerChoices = useMemo<DeviceOwner[]>(() => users.map((user) => ({
    user_id: user.user_id,
    display_name: user.display_name,
    is_self: user.is_self,
  })), [users])
  const linkedActiveDeviceCount = ownerDataReady
    ? activePeers.filter((p) => (ownerByPeerID.get(p.peer_id)?.length ?? 0) > 0).length
    : 0
  const unlinkedActivePeers = ownerDataReady
    ? activePeers.filter((p) => (ownerByPeerID.get(p.peer_id)?.length ?? 0) === 0)
    : []
  const refreshPeopleAndDevices = useCallback(() => {
    void refresh()
    void refreshUsers()
  }, [refresh, refreshUsers])

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight">People & devices</h1>
        <p className="mt-1 text-sm text-muted-foreground">
          Human identities own work. Devices are paired peers that can connect, run sessions, and sync for those people.
        </p>
      </div>
      <div className="grid gap-3 sm:grid-cols-3">
        <div className="border border-border bg-card/40 px-3 py-2">
          <div className="flex items-center gap-2 text-xs uppercase tracking-wider text-muted-foreground">
            <UsersRound className="h-3.5 w-3.5" />
            People
          </div>
          <div className="mt-1 text-xl font-semibold">{users.length}</div>
        </div>
        <div className="border border-border bg-card/40 px-3 py-2">
          <div className="flex items-center gap-2 text-xs uppercase tracking-wider text-muted-foreground">
            <Laptop className="h-3.5 w-3.5" />
            Devices
          </div>
          <div className="mt-1 text-xl font-semibold">{activePeers.length}</div>
        </div>
        <div className="border border-border bg-card/40 px-3 py-2">
          <div className="flex items-center gap-2 text-xs uppercase tracking-wider text-muted-foreground">
            <Link2 className="h-3.5 w-3.5" />
            Linked
          </div>
          <div className="mt-1 text-xl font-semibold">{ownerDataReady ? `${linkedActiveDeviceCount}/${activePeers.length}` : '...'}</div>
        </div>
      </div>
      <Tabs value={activeTab} onValueChange={changeTab} className="space-y-4">
        <TabsList variant="line" className="w-full justify-start overflow-x-auto border-b border-border">
          <TabsTrigger value="people" data-testid="pairing-tab-people">
            <UsersRound className="h-3.5 w-3.5" />
            People
          </TabsTrigger>
          <TabsTrigger value="devices" data-testid="pairing-tab-devices">
            <Laptop className="h-3.5 w-3.5" />
            Devices
          </TabsTrigger>
        </TabsList>

        <TabsContent value="people" className="space-y-4">
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
                      <div className="flex items-center justify-between gap-3">
                        <div className="flex min-w-0 items-center gap-2 text-sm font-medium">
                          <UserRound className="h-4 w-4 shrink-0 text-muted-foreground" />
                          <span className="truncate">{user.display_name || user.user_id}</span>
                          {user.is_self && (
                            <Badge variant="outline" tone="success" className="text-[10px]">you</Badge>
                          )}
                        </div>
                        {!user.is_self && user.peers.length === 0 ? (
                          <Button
                            variant="ghost"
                            size="sm"
                            onClick={() => { void removeStaleIdentity(user) }}
                            disabled={deletingUserID === user.user_id}
                            className="text-destructive hover:bg-destructive/10 hover:text-destructive"
                          >
                            {deletingUserID === user.user_id ? (
                              <Loader2 className="h-3.5 w-3.5 animate-spin" />
                            ) : (
                              <Trash2 className="h-3.5 w-3.5" />
                            )}
                            Remove
                          </Button>
                        ) : null}
                      </div>
                      {user.peers.length === 0 ? (
                        <div className="mt-1 text-xs text-muted-foreground">No linked devices</div>
                      ) : (
                        <div className="mt-2 flex flex-wrap gap-1.5">
                          {user.peers.map((peer) => (
                            <Badge key={peer.peer_id} variant="outline" tone="muted" className="text-[10px]">
                              <Laptop className="h-3 w-3" />
                              {peer.display_name || `peer-${shortPeerSuffix(peer.peer_id)}`}
                            </Badge>
                          ))}
                        </div>
                      )}
                      <div className="mt-0.5 font-mono text-[10px] text-muted-foreground/70">
                        {user.user_id}
                      </div>
                    </div>
                  ))}
                </div>
              )}
            </CardContent>
          </Card>
        </TabsContent>

        <TabsContent value="devices" className="space-y-4">
          {showGettingStarted && (
            <GettingStartedCard onDismiss={dismissGettingStarted} />
          )}
          <div className="flex flex-wrap gap-2">
            <ShowCodeModal onComplete={refreshPeopleAndDevices} />
            <EnterCodeModal
              onComplete={refreshPeopleAndDevices}
              initialPayload={pastePayload}
              autoOpen={!!pastePayload}
            />
          </div>
          <Card>
            <CardHeader>
              <CardTitle className="text-base">Devices</CardTitle>
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
              {!loading && !error && unlinkedActivePeers.length > 0 && (
                <div className="mb-3 border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-xs text-amber-200">
                  {unlinkedActivePeers.length} paired device{unlinkedActivePeers.length === 1 ? '' : 's'} are not linked to a human identity yet.
                </div>
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
                    owners={ownerByPeerID.get(p.peer_id) ?? []}
                    ownerChoices={ownerChoices}
                    ownersReady={ownerDataReady}
                    ownerUpdating={updatingOwnerPeerID === p.peer_id}
                    onOwnerChange={(userID) => { void changeDeviceOwner(p.peer_id, userID) }}
                    onRevoke={() => revoke(p.peer_id)}
                  />
                ))
              })()}
            </CardContent>
          </Card>
        </TabsContent>
      </Tabs>
    </div>
  )
}
