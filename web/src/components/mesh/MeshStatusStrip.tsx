import { Link } from 'react-router-dom'
import type { MeshAgent } from '@/api/types'
import { PEER_PREFIX } from './AgentOriginBadge'
import { cn } from '@/lib/utils'

// MeshStatusStrip replaces the three "icon + big number" stat cards
// that used to sit at the top of the page. Those were the textbook
// hero-metric AI-slop pattern: they consumed ~120px of vertical real
// estate to repeat numbers already visible in the panel headers below.
//
// The strip is a single dense line of operator-relevant numbers: agent
// counts (local vs remote), live message count, and peer health (with
// any offline peers called out inline). Every number is the same
// information as before, but at one-eighth the visual cost — and the
// disconnect banner is no longer pushed below the fold by chrome.

interface Props {
  agents: MeshAgent[]
  liveMessages: number
  peerCount: number | null // null when p2p isn't built in
  offlinePeerCount: number
}

export function MeshStatusStrip({
  agents,
  liveMessages,
  peerCount,
  offlinePeerCount,
}: Props): React.ReactElement {
  const local = agents.filter((a) => !a.origin || a.origin === 'local').length
  const remote = agents.filter((a) => a.origin?.startsWith(PEER_PREFIX)).length

  return (
    <div
      className="flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-muted-foreground"
      data-testid="mesh-status-strip"
    >
      <Stat
        label={agents.length === 1 ? 'agent' : 'agents'}
        value={agents.length}
        sublabel={`${local} local · ${remote} remote`}
      />
      <Divider />
      <Stat
        label={liveMessages === 1 ? 'message live' : 'messages live'}
        value={liveMessages}
      />
      {peerCount !== null && (
        <>
          <Divider />
          <Stat
            label={peerCount === 1 ? 'paired peer' : 'paired peers'}
            value={peerCount}
            sublabel={
              offlinePeerCount > 0 ? (
                <Link to="/pairing?tab=devices" className="text-amber-600 hover:underline dark:text-amber-400">
                  {offlinePeerCount} offline
                </Link>
              ) : undefined
            }
          />
        </>
      )}
    </div>
  )
}

function Stat({
  label,
  value,
  sublabel,
}: {
  label: string
  value: number
  sublabel?: React.ReactNode
}) {
  return (
    <span className="inline-flex items-baseline gap-1.5">
      <span
        className={cn(
          'text-base font-semibold tabular-nums leading-none',
          value === 0 ? 'text-muted-foreground/60' : 'text-foreground',
        )}
      >
        {value}
      </span>
      <span className="text-xs">{label}</span>
      {sublabel ? (
        <span className="text-xs text-muted-foreground/80">({sublabel})</span>
      ) : null}
    </span>
  )
}

function Divider() {
  return <span className="text-muted-foreground/30" aria-hidden>·</span>
}
