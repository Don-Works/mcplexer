import { useState } from 'react'
import { Crosshair, ExternalLink, Folder } from 'lucide-react'
import type { MeshAgent } from '@/api/types'
import { focusMeshAgent } from '@/api/client'
import { cn } from '@/lib/utils'
import { AgentOriginBadge, PEER_PREFIX } from './AgentOriginBadge'
import { Pill } from './Pill'

// AgentRow renders a single entry in the Mesh page's activity panel.
// Layout, left to right:
//   [tier-dot] [name + origin-badge + workspace-pill]      [Focus] [time]
//              [role · client_type]
//              [status — italic]
//
// The tier dot encodes recency without hiding any row: green (<2min),
// amber (2-10min), grey (10-30min — still in the active window). For
// peer-origin agents the dot drops to grey when the peer's libp2p
// connection is down (honest about transport state).
//
// The Focus button uses different icons for local vs remote so the
// operator never accidentally fires off an SSH window thinking it'll
// just switch their tmux pane.

export function formatAgo(dateStr: string): string {
  const d = Date.now() - new Date(dateStr).getTime()
  const mins = Math.floor(d / 60000)
  if (mins < 1) return 'just now'
  if (mins < 60) return `${mins}m ago`
  const hours = Math.floor(mins / 60)
  if (hours < 24) return `${hours}h ago`
  return `${Math.floor(hours / 24)}d ago`
}

function isPeerOrigin(origin?: string): boolean {
  return !!origin && origin.startsWith(PEER_PREFIX)
}

const TIER_FRESH_MAX_MIN = 2
const TIER_IDLE_MAX_MIN = 10

type Tier = 'fresh' | 'idle' | 'stale'

function tierFromAge(lastSeen: string): Tier {
  const mins = Math.floor((Date.now() - new Date(lastSeen).getTime()) / 60000)
  if (mins < TIER_FRESH_MAX_MIN) return 'fresh'
  if (mins < TIER_IDLE_MAX_MIN) return 'idle'
  return 'stale'
}

const tierDot: Record<Tier, string> = {
  fresh: 'bg-emerald-500',
  idle: 'bg-amber-500',
  stale: 'bg-muted-foreground/40',
}

const tierTitle: Record<Tier, string> = {
  fresh: 'Active — last seen within 2 minutes',
  idle: 'Idle — no activity in the last few minutes',
  stale: 'Stale — no activity in 10+ minutes',
}

interface Props {
  agent: MeshAgent
  peerNames: Record<string, string>
  peerConnected?: boolean
  // workspaceFilter, when matching this agent's workspace, marks the
  // workspace pill as "active". onWorkspaceClick toggles the page-wide
  // workspace filter when the pill is clicked.
  workspaceFilter?: string
  onWorkspaceClick?: (workspaceName: string) => void
}

export function AgentRow({
  agent,
  peerNames,
  peerConnected = true,
  workspaceFilter,
  onWorkspaceClick,
}: Props): React.ReactElement {
  const name = agent.name || agent.client_type || agent.session_id.slice(0, 8)
  const showOrigin = isPeerOrigin(agent.origin)
  const showClient = agent.client_type && agent.client_type !== name

  let tier = tierFromAge(agent.last_seen_at)
  if (showOrigin && !peerConnected && tier === 'fresh') tier = 'idle'
  if (showOrigin && !peerConnected) tier = 'stale'

  const subBits: string[] = []
  if (agent.role) subBits.push(agent.role)
  if (showClient && agent.client_type) subBits.push(agent.client_type)
  const sub = subBits.join(' · ')

  const hasLocator = !!(agent.tmux_session && agent.tmux_pane)
  const workspaceLabel = agent.workspace_name || agent.workspace_id
  const workspaceFilterActive = !!workspaceLabel && workspaceLabel === workspaceFilter

  return (
    <div className="flex items-start justify-between gap-3 border-b border-border/40 py-2.5 last:border-0">
      <div className="flex min-w-0 flex-1 items-start gap-2.5">
        <span
          className={cn('mt-1.5 inline-block h-2 w-2 shrink-0 rounded-full', tierDot[tier])}
          title={tierTitle[tier]}
          aria-label={tierTitle[tier]}
        />
        <div className="min-w-0 flex-1">
          <div className="flex min-w-0 flex-wrap items-center gap-1.5">
            <span
              className="truncate text-sm font-medium"
              data-testid={`agent-name-${agent.session_id}`}
              title={name}
            >
              {name}
            </span>
            {showOrigin && (
              <AgentOriginBadge origin={agent.origin} peerNames={peerNames} />
            )}
            {workspaceLabel && (
              <Pill
                icon={Folder}
                label={workspaceLabel}
                title={
                  workspaceFilterActive
                    ? `Clear workspace filter (${workspaceLabel})`
                    : `Filter to workspace: ${workspaceLabel}`
                }
                active={workspaceFilterActive}
                tone="workspace"
                maxLabelCh={14}
                onClick={
                  onWorkspaceClick ? () => onWorkspaceClick(workspaceLabel) : undefined
                }
                testId={`agent-workspace-${agent.session_id}`}
              />
            )}
          </div>
          {sub && (
            <div
              className="mt-0.5 truncate text-[11px] text-muted-foreground"
              title={sub}
            >
              {sub}
            </div>
          )}
          {agent.status && (
            <div
              className="mt-0.5 truncate text-[11px] italic text-muted-foreground/80"
              title={agent.status}
              data-testid={`agent-status-${agent.session_id}`}
            >
              {agent.status}
            </div>
          )}
        </div>
      </div>
      <div className="flex shrink-0 items-center gap-2 pt-0.5">
        <FocusButton agent={agent} disabled={!hasLocator} />
        <span className="whitespace-nowrap text-[11px] text-muted-foreground tabular-nums">
          {formatAgo(agent.last_seen_at)}
        </span>
      </div>
    </div>
  )
}

function FocusButton({ agent, disabled }: { agent: MeshAgent; disabled: boolean }) {
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const remote = isPeerOrigin(agent.origin)
  const Icon = remote ? ExternalLink : Crosshair
  const onClick = async () => {
    if (disabled || pending) return
    setPending(true)
    setError(null)
    try {
      await focusMeshAgent(agent.session_id)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
      setTimeout(() => setError(null), 5000)
    } finally {
      setPending(false)
    }
  }
  // Distinct titles per origin so the operator's intent matches reality:
  // local focus moves the tmux client; remote focus spawns a new SSH
  // window. Same icon family would silently merge two different actions.
  const title = disabled
    ? "No tmux locator — this agent didn't register from inside a tmux pane"
    : error
      ? `Focus failed: ${error}`
      : remote
        ? 'Open SSH — spawns a new local tmux window connected to this peer'
        : 'Focus — switch your tmux to this agent\'s pane'
  return (
    <button
      type="button"
      onClick={onClick}
      disabled={disabled || pending}
      title={title}
      className={cn(
        'inline-flex h-6 w-6 items-center justify-center rounded transition-colors',
        disabled
          ? 'cursor-not-allowed text-muted-foreground/30'
          : remote
            ? 'text-muted-foreground hover:bg-amber-500/10 hover:text-amber-600 dark:hover:text-amber-400'
            : 'text-muted-foreground hover:bg-emerald-500/10 hover:text-emerald-600 dark:hover:text-emerald-400',
        error && 'text-rose-500 hover:text-rose-500',
      )}
      data-testid={`agent-focus-${agent.session_id}`}
      aria-label={title}
    >
      <Icon className={cn('h-3.5 w-3.5', pending && 'animate-pulse')} />
    </button>
  )
}
