import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Badge } from '@/components/ui/badge'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Button } from '@/components/ui/button'
import { useApi } from '@/hooks/use-api'
import { getFileClaims, getMeshStatus, getP2PPeers, getSettings } from '@/api/client'
import { p2pFetch, type ListPeersResponse } from '@/components/pairing/api'
import type { FileClaim, MeshAgent, MeshMessage, MeshStatusResponse, P2PPeerMode } from '@/api/types'
import { Check, Copy, FileLock, Folder, Loader2, Radio, Search, X } from 'lucide-react'
import { Link, useSearchParams } from 'react-router-dom'
import { P2PDebugPanel } from '@/components/p2p-debug-panel'
import { formatAgo } from '@/components/mesh/AgentRow'
import { AgentActivity } from '@/components/mesh/AgentActivity'
import { MeshStatusStrip } from '@/components/mesh/MeshStatusStrip'
import { Pill } from '@/components/mesh/Pill'
import { DisconnectedPeersBanner, classifyAll } from '@/components/mesh/DisconnectedPeersBanner'
import { EmptyState } from '@/components/ui/empty-state'
import { Markdown } from '@/lib/markdown'
import { cn } from '@/lib/utils'

// splitTags splits a comma-separated tags string into a clean array,
// trimming whitespace and dropping empties.
function splitTags(s: string): string[] {
  if (!s) return []
  return s
    .split(',')
    .map((t) => t.trim())
    .filter(Boolean)
}

function PriorityBadge({ priority }: { priority: string }) {
  const tone =
    priority === 'critical'
      ? 'critical'
      : priority === 'high'
        ? 'high'
        : priority === 'normal'
          ? 'info'
          : 'muted'
  return (
    <Badge variant="outline" tone={tone} className="text-[10px]">
      {priority}
    </Badge>
  )
}

function KindBadge({ kind }: { kind: string }) {
  return (
    <Badge variant="outline" tone="mono" className="text-[10px]">
      {kind}
    </Badge>
  )
}

const MessageRow = ({
  message,
  highlighted,
  rowRef,
  onTagClick,
  activeTag,
  workspaceFilter,
  onWorkspaceClick,
}: {
  message: MeshMessage
  highlighted?: boolean
  rowRef?: (el: HTMLDivElement | null) => void
  onTagClick?: (tag: string) => void
  activeTag?: string
  workspaceFilter?: string
  onWorkspaceClick?: (workspaceName: string) => void
}) => {
  const [copied, setCopied] = useState(false)
  const onCopy = useCallback(async () => {
    try {
      await navigator.clipboard.writeText(message.content)
      setCopied(true)
      setTimeout(() => setCopied(false), 1800)
    } catch {
      // best-effort; no toast plumbing in this page yet
    }
  }, [message.content])
  const tags = splitTags(message.tags)
  const workspaceLabel = message.workspace_name
  return (
    <div
      ref={rowRef}
      data-message-id={message.id}
      className={cn(
        'py-3 border-b border-border/40 last:border-0 group transition-colors duration-700',
        highlighted && 'bg-primary/10 ring-1 ring-primary/40 rounded-md -mx-2 px-2',
      )}
    >
      <div className="flex flex-wrap items-center gap-2 mb-1">
        <PriorityBadge priority={message.priority} />
        <KindBadge kind={message.kind} />
        <span className="text-xs text-muted-foreground">
          from <span className="font-medium text-foreground">{message.agent_name || 'unknown'}</span>
        </span>
        {workspaceLabel && (
          <Pill
            icon={Folder}
            label={workspaceLabel}
            title={
              workspaceFilter === workspaceLabel
                ? `Clear workspace filter (${workspaceLabel})`
                : message.workspace_path
                  ? `Filter to workspace: ${workspaceLabel} (${message.workspace_path})`
                  : `Filter to workspace: ${workspaceLabel}`
            }
            active={workspaceFilter === workspaceLabel}
            tone="workspace"
            maxLabelCh={16}
            onClick={
              onWorkspaceClick ? () => onWorkspaceClick(workspaceLabel) : undefined
            }
            testId={`message-workspace-${message.id}`}
          />
        )}
        <span className="ml-auto text-xs text-muted-foreground">{formatAgo(message.created_at)}</span>
        <Button
          type="button"
          variant="ghost"
          size="icon"
          className="h-6 w-6 opacity-0 transition-opacity group-hover:opacity-100"
          onClick={onCopy}
          aria-label="Copy message content"
          data-testid={`mesh-message-copy-${message.id}`}
          title={copied ? 'Copied' : 'Copy content'}
        >
          {copied ? <Check className="h-3.5 w-3.5 text-emerald-500" /> : <Copy className="h-3.5 w-3.5" />}
        </Button>
      </div>
      <Markdown source={message.content} className="text-sm leading-relaxed" />
      <div className="flex flex-wrap items-center gap-1.5 mt-1.5">
        {tags.map((tag) => {
          const active = activeTag === tag
          return (
            <Pill
              key={tag}
              label={`#${tag}`}
              active={active}
              tone="muted"
              maxLabelCh={20}
              onClick={() => onTagClick?.(tag)}
              title={active ? `Clear filter for "${tag}"` : `Filter by "${tag}"`}
              testId={`mesh-message-tag-${tag}`}
            />
          )
        })}
        {message.reply_count > 0 && (
          <span className="text-[11px] text-muted-foreground">
            {message.reply_count} {message.reply_count === 1 ? 'reply' : 'replies'}
          </span>
        )}
        <span className="ml-auto font-mono text-[10px] text-muted-foreground/60">{message.id.slice(0, 10)}</span>
      </div>
    </div>
  )
}

function formatRemaining(seconds: number): string {
  if (seconds <= 0) return 'expired'
  if (seconds < 60) return `${seconds}s`
  const m = Math.floor(seconds / 60)
  if (m < 60) return `${m}m`
  const h = Math.floor(m / 60)
  return `${h}h${m % 60}m`
}

function ClaimRow({ claim }: { claim: FileClaim }) {
  const claimer =
    claim.claimer_display_name ||
    claim.claimer_user_id ||
    (claim.claimer_peer_id ? `peer:${claim.claimer_peer_id.slice(-10)}` : 'unknown')
  return (
    <div className="py-3 border-b border-border/40 last:border-0">
      <div className="flex items-center gap-2 mb-1">
        <FileLock className="h-3.5 w-3.5 text-amber-500" />
        <span className="text-sm font-medium">{claimer}</span>
        <span className="text-xs text-muted-foreground">
          on <span className="font-mono">{claim.repo}@{claim.branch}</span>
        </span>
        <span className="ml-auto text-xs text-muted-foreground">
          {formatRemaining(claim.seconds_remaining)} left
        </span>
      </div>
      {claim.intent && (
        <p className="text-xs text-muted-foreground mb-1.5 italic">{claim.intent}</p>
      )}
      <div className="flex flex-wrap gap-1">
        {claim.paths.map((p) => (
          <span
            key={p}
            className="font-mono text-[10px] bg-muted px-1.5 py-0.5 rounded border border-border"
          >
            {p}
          </span>
        ))}
      </div>
    </div>
  )
}

// Note: AgentsRow used to live here as a Local | Remote two-column split.
// The critique flagged the split as geography pretending to be workflow —
// the workspace badge + origin badge already carry the local/remote bit
// per row, so the column split was paying for the same information twice.
// Replaced by AgentActivity (one workspace-grouped panel).

function MessagesPanel({
  messages,
  highlightId,
  registerRow,
  workspaceFilter,
  onWorkspaceClick,
}: {
  messages: MeshMessage[]
  highlightId: string | null
  registerRow: (id: string, el: HTMLDivElement | null) => void
  workspaceFilter: string
  onWorkspaceClick: (workspaceName: string) => void
}) {
  const [query, setQuery] = useState('')
  // Active tag chip — derived from query when it matches a single tag
  // (e.g. "#handshake"). Lets the UI mark the chip "pressed" + clicking
  // a chip toggles the filter without typing.
  const activeTag = query.startsWith('#') ? query.slice(1).trim() : ''
  const onTagClick = useCallback(
    (tag: string) => {
      setQuery((prev) => (prev === `#${tag}` ? '' : `#${tag}`))
    },
    [],
  )
  // Sort newest-first by created_at. The store orders by
  // priority_order(priority), id DESC — so criticals can jump above
  // earlier-arriving normals server-side. The user wants a pure
  // chronological log here; priority is already conveyed by the
  // PriorityBadge color so we don't need it in row order.
  const sorted = useMemo(() => {
    return [...messages].sort((a, b) => {
      const ta = new Date(a.created_at).getTime()
      const tb = new Date(b.created_at).getTime()
      return tb - ta
    })
  }, [messages])
  const filtered = useMemo(() => {
    // Page-wide workspace filter is applied first so the local search +
    // tag chip both operate within the chosen workspace's slice.
    //
    // IMPORTANT: untagged messages (workspace_name empty) are
    // ALWAYS included, even when a workspace pill is selected. Most
    // historical messages predate the workspace_path-on-send wiring
    // so they have no workspace metadata; excluding them on filter
    // makes the filter feel broken ("clicking workspace = empty").
    // The selected pill narrows TO that workspace + untagged; agents
    // that DO send workspace_path get their own slice while history
    // stays visible.
    const wsScoped = workspaceFilter
      ? sorted.filter((m) => {
          const wsName = m.workspace_name || ''
          return wsName === '' || wsName === workspaceFilter
        })
      : sorted
    const q = query.trim().toLowerCase()
    if (!q) return wsScoped
    // `#tag` queries match the parsed tag list exactly (any tag matching
    // wins). Other queries do the previous full-text search.
    if (q.startsWith('#')) {
      const want = q.slice(1)
      return wsScoped.filter((m) => splitTags(m.tags).some((t) => t.toLowerCase() === want))
    }
    return wsScoped.filter((m) => {
      const haystack =
        (m.content || '') +
        ' ' + (m.agent_name || '') +
        ' ' + (m.tags || '') +
        ' ' + (m.kind || '') +
        ' ' + (m.priority || '') +
        ' ' + (m.workspace_name || '') +
        ' ' + (m.workspace_path || '') +
        ' ' + m.id
      return haystack.toLowerCase().includes(q)
    })
  }, [sorted, query, workspaceFilter])
  return (
    <Card>
      <CardHeader className="space-y-3">
        <div className="flex items-center justify-between">
          <CardTitle className="text-sm font-medium uppercase tracking-wider text-muted-foreground">
            Recent Messages
          </CardTitle>
          <span className="font-mono text-[11px] tabular-nums text-muted-foreground">
            {query ? `${filtered.length} / ${messages.length}` : messages.length}
          </span>
        </div>
        <div className="relative">
          <Search className="pointer-events-none absolute left-2.5 top-2.5 h-4 w-4 text-muted-foreground" />
          <Input
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="Search content, sender, workspace, tags, kind, id… (or click a #tag)"
            className="pl-8"
            data-testid="mesh-messages-search"
          />
        </div>
      </CardHeader>
      <CardContent>
        {messages.length === 0 ? (
          <p className="py-4 text-center text-sm text-muted-foreground">No messages yet</p>
        ) : filtered.length === 0 ? (
          <p className="py-4 text-center text-sm text-muted-foreground">
            No messages match "{query}".
          </p>
        ) : (
          filtered.map((m) => (
            <MessageRow
              key={m.id}
              message={m}
              highlighted={m.id === highlightId}
              rowRef={(el) => registerRow(m.id, el)}
              onTagClick={onTagClick}
              activeTag={activeTag}
              workspaceFilter={workspaceFilter}
              onWorkspaceClick={onWorkspaceClick}
            />
          ))
        )}
      </CardContent>
    </Card>
  )
}

function MeshDisabled() {
  return (
    <EmptyState
      testid="mesh-disabled"
      icon={<Radio className="h-10 w-10" />}
      title="Agent Mesh is disabled"
      description={
        <>
          Enable inter-agent messaging in{' '}
          <Link to="/settings" className="text-primary hover:underline" data-testid="mesh-enable-link">
            Settings
          </Link>{' '}
          to let agents coordinate work across sessions. Requires a restart after enabling.
        </>
      }
    />
  )
}

export function MeshPage() {
  const settingsFetcher = useCallback(() => getSettings(), [])
  const { data: settingsData, loading: settingsLoading } = useApi(settingsFetcher)
  const meshEnabled = settingsData?.settings?.mesh_enabled ?? false

  const meshFetcher = useCallback(() => getMeshStatus(), [])
  const { data: meshData, loading: meshLoading, refetch } = useApi(meshFetcher)

  const claimsFetcher = useCallback(() => getFileClaims(), [])
  const { data: claimsData, refetch: refetchClaims } = useApi(claimsFetcher)

  // Deep-link target: /mesh?msg=<id> from a clicked notify toast. We highlight
  // and scroll once the target row has rendered, then clear the highlight
  // after a beat so the visual cue doesn't stick.
  const [searchParams, setSearchParams] = useSearchParams()
  const targetMsg = searchParams.get('msg')
  const [highlightId, setHighlightId] = useState<string | null>(null)
  const rowRefs = useRef<Map<string, HTMLDivElement>>(new Map())
  const registerRow = useCallback((id: string, el: HTMLDivElement | null) => {
    if (el) rowRefs.current.set(id, el)
    else rowRefs.current.delete(id)
  }, [])
  useEffect(() => {
    if (!targetMsg) return
    setHighlightId(targetMsg)
    const scroll = () => {
      const el = rowRefs.current.get(targetMsg)
      if (el) {
        el.scrollIntoView({ behavior: 'smooth', block: 'center' })
        return true
      }
      return false
    }
    // Row may not be rendered yet on first paint — try a couple of rAFs.
    let attempts = 0
    const tick = () => {
      if (scroll() || ++attempts > 20) return
      requestAnimationFrame(tick)
    }
    requestAnimationFrame(tick)
    const clearParam = setTimeout(() => {
      setSearchParams((prev) => {
        const next = new URLSearchParams(prev)
        next.delete('msg')
        return next
      }, { replace: true })
    }, 200)
    const clearHighlight = setTimeout(() => setHighlightId(null), 3500)
    return () => {
      clearTimeout(clearParam)
      clearTimeout(clearHighlight)
    }
  }, [targetMsg, setSearchParams])

  // Deep-link target: /mesh?queue=1 from an outbound-queued signal
  // (internal/mesh/outbound_queue.go publishEnqueueNotice). Surface a
  // dismissible banner so the click lands on something meaningful
  // instead of dropping the user into the bare message list.
  const queueLanding = searchParams.get('queue') === '1'
  const dismissQueueLanding = useCallback(() => {
    setSearchParams(
      (prev) => {
        const next = new URLSearchParams(prev)
        next.delete('queue')
        return next
      },
      { replace: true },
    )
  }, [setSearchParams])

  const [peerNames, setPeerNames] = useState<Record<string, string>>({})
  const [peerModes, setPeerModes] = useState<P2PPeerMode[]>([])
  useEffect(() => {
    if (!meshEnabled) return
    const fetchNames = async () => {
      const res = await p2pFetch<ListPeersResponse>('/peers').catch(() => null)
      if (!res) return
      const map: Record<string, string> = {}
      for (const row of res.peers ?? []) {
        if (row.display_name) map[row.peer_id] = row.display_name
      }
      setPeerNames(map)
    }
    const fetchModes = async () => {
      const res = await getP2PPeers().catch(() => null)
      if (!res) return
      setPeerModes(res.peers ?? [])
    }
    void fetchNames()
    void fetchModes()
    const namesId = setInterval(() => void fetchNames(), 30000)
    // Modes refresh on the same 10s cadence as the rest of the page so
    // the per-agent tier dot reacts quickly when a peer drops off.
    const modesId = setInterval(() => void fetchModes(), 10000)
    return () => {
      clearInterval(namesId)
      clearInterval(modesId)
    }
  }, [meshEnabled])

  // Auto-refresh every 10 seconds when enabled.
  const [, setTick] = useState(0)
  useEffect(() => {
    if (!meshEnabled) return
    const id = setInterval(() => {
      refetch()
      refetchClaims()
      setTick((t) => t + 1)
    }, 10000)
    return () => clearInterval(id)
  }, [meshEnabled, refetch, refetchClaims])

  // Derived data — recomputed unconditionally so the hook count stays
  // stable across the loading / disabled / enabled render branches.
  // (React #310 fired here before — moving these BEFORE the early
  // returns is the cure.)
  const status: MeshStatusResponse = meshData ?? { agents: [], messages: [], live_messages: 0 }
  const agents: MeshAgent[] = status.agents ?? []
  const messages: MeshMessage[] = status.messages ?? []
  const claims: FileClaim[] = claimsData?.claims ?? []

  const workspaceFilter = searchParams.get('workspace') ?? ''
  const setWorkspaceFilter = useCallback(
    (ws: string) => {
      setSearchParams(
        (prev) => {
          const next = new URLSearchParams(prev)
          if (ws) next.set('workspace', ws)
          else next.delete('workspace')
          return next
        },
        { replace: true },
      )
    },
    [setSearchParams],
  )
  const onWorkspaceClick = useCallback(
    (ws: string) => setWorkspaceFilter(ws === workspaceFilter ? '' : ws),
    [workspaceFilter, setWorkspaceFilter],
  )

  // Peer health for the status strip. Computed once here from the same
  // sources the disconnect banner uses, so the strip can call out
  // offline peers without re-fetching.
  const offlinePeerCount = useMemo(() => {
    if (peerModes.length === 0 && Object.keys(peerNames).length === 0) return 0
    const paired = Object.entries(peerNames).map(([peer_id, display_name]) => ({
      peer_id,
      display_name,
      paired_at: '',
      trust_level: 0,
      scopes: [],
    }))
    return classifyAll(paired, peerModes, agents).length
  }, [peerNames, peerModes, agents])

  // Early returns now happen AFTER every hook so the hook count is
  // stable across renders.
  if (settingsLoading) {
    return (
      <div className="flex items-center justify-center py-24">
        <Loader2 className="h-6 w-6 animate-spin text-muted-foreground" />
      </div>
    )
  }
  if (!meshEnabled) {
    return <MeshDisabled />
  }
  const peerCount = Object.keys(peerNames).length || null

  const filteredClaims = workspaceFilter
    ? claims.filter((c) => c.repo === workspaceFilter || c.branch === workspaceFilter)
    : claims

  // Debug panel only shows when the user explicitly asks via ?debug=1.
  // Used to render unconditionally in production layout — that trained
  // operators to scroll past "real" content. Critique flagged it as a
  // P2: the panel is literally named "Debug" and shouldn't be a peer
  // of "Recent Messages" in the default view.
  const debugEnabled = searchParams.get('debug') === '1'

  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-baseline justify-between gap-3">
        <div className="flex items-center gap-3">
          <h1 className="text-2xl font-bold">Agent Mesh</h1>
          {meshLoading && <Loader2 className="h-4 w-4 animate-spin text-muted-foreground" />}
        </div>
        <MeshStatusStrip
          agents={agents}
          liveMessages={status.live_messages}
          peerCount={peerCount}
          offlinePeerCount={offlinePeerCount}
        />
      </div>

      <DisconnectedPeersBanner agents={agents} />

      {queueLanding && (
        <div className="flex items-start justify-between gap-3 rounded-md border border-amber-500/30 bg-amber-500/10 p-3 text-sm">
          <div className="space-y-0.5">
            <div className="font-medium text-amber-300">Message queued for offline peer</div>
            <div className="text-xs text-muted-foreground">
              A mesh message could not be delivered immediately. It is parked in the outbound queue and will deliver when the peer reconnects.
            </div>
          </div>
          <button
            type="button"
            onClick={dismissQueueLanding}
            className="inline-flex h-5 items-center gap-0.5 rounded px-1 text-muted-foreground hover:bg-muted hover:text-foreground"
            title="Dismiss"
          >
            <X className="h-3 w-3" />
          </button>
        </div>
      )}

      {workspaceFilter && (
        <div className="flex items-center gap-2 text-xs">
          <span className="text-muted-foreground">Filtered to workspace</span>
          <Pill
            icon={Folder}
            label={workspaceFilter}
            tone="brand"
            active
            maxLabelCh={24}
            onClick={() => setWorkspaceFilter('')}
            title="Clear workspace filter"
            testId="workspace-filter-chip"
          />
          <button
            type="button"
            onClick={() => setWorkspaceFilter('')}
            className="inline-flex h-5 items-center gap-0.5 rounded px-1 text-muted-foreground hover:bg-muted hover:text-foreground"
            title="Clear filter"
          >
            <X className="h-3 w-3" />
            <span>clear</span>
          </button>
        </div>
      )}

      <AgentActivity
        agents={agents}
        peerNames={peerNames}
        peerModes={peerModes}
        workspaceFilter={workspaceFilter}
        onWorkspaceClick={onWorkspaceClick}
      />

      <MessagesPanel
        messages={messages}
        highlightId={highlightId}
        registerRow={registerRow}
        workspaceFilter={workspaceFilter}
        onWorkspaceClick={onWorkspaceClick}
      />

      {/* File claims: only render when there's anything to show, or
          when the user has actively filtered (so they know it's empty
          for that scope, not just absent). */}
      {(filteredClaims.length > 0 || workspaceFilter) && (
        <Card>
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-sm font-medium uppercase tracking-wider text-muted-foreground">
              <FileLock className="h-4 w-4" />
              <span>File Claims</span>
              <span className="font-mono text-[11px] tabular-nums text-muted-foreground/60">
                {filteredClaims.length}
              </span>
            </CardTitle>
          </CardHeader>
          <CardContent>
            {filteredClaims.length === 0 ? (
              <p className="py-4 text-center text-sm text-muted-foreground">
                No active claims{workspaceFilter ? ` in ${workspaceFilter}` : ''}.
              </p>
            ) : (
              filteredClaims.map((c) => <ClaimRow key={c.claim_id} claim={c} />)
            )}
          </CardContent>
        </Card>
      )}

      {debugEnabled && <P2PDebugPanel />}
    </div>
  )
}
