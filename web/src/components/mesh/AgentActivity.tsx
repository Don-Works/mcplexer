import { useMemo } from 'react'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import type { MeshAgent, P2PPeerMode } from '@/api/types'
import { AgentRow } from './AgentRow'
import { PEER_PREFIX } from './AgentOriginBadge'
import { peerConnectedFromModes } from './DisconnectedPeersBanner'

// AgentActivity replaces the old "Local Agents | Remote Agents" two-
// column split. The split was geography (where is the process running?)
// pretending to be workflow (what's happening in repo X right now?).
// Once every agent had a workspace badge AND an origin badge, the two
// columns were paying for the same information twice — and worse, two
// teammates collaborating on the same workspace from different machines
// ended up in different columns.
//
// The new model is one panel, grouped by workspace, with a sticky
// header per group. Tier dot + origin badge already carry the
// local/remote bit per row, so no information is lost. Within each
// workspace, rows sort by tier (fresh > idle > stale) then most-recent
// first — so the operator's eye lands on what's active.
//
// When workspaceFilter is set, only matching agents render. Clicking a
// workspace pill in any row toggles this filter at the page level.

interface Props {
  agents: MeshAgent[]
  peerNames: Record<string, string>
  peerModes: P2PPeerMode[]
  workspaceFilter: string
  onWorkspaceClick: (workspaceName: string) => void
}

const UNGROUPED_KEY = '__ungrouped__'
const UNGROUPED_LABEL = 'No workspace'

interface Group {
  key: string
  label: string
  agents: MeshAgent[]
}

export function AgentActivity({
  agents,
  peerNames,
  peerModes,
  workspaceFilter,
  onWorkspaceClick,
}: Props): React.ReactElement {
  const groups = useMemo(() => buildGroups(agents, workspaceFilter), [agents, workspaceFilter])
  const total = groups.reduce((n, g) => n + g.agents.length, 0)

  return (
    <Card>
      <CardHeader>
        <CardTitle className="flex items-center justify-between text-sm font-medium uppercase tracking-wider text-muted-foreground">
          <span>{workspaceFilter ? `Agents · ${workspaceFilter}` : 'Active Agents'}</span>
          <span className="font-mono text-[11px] tabular-nums">{total}</span>
        </CardTitle>
      </CardHeader>
      <CardContent className="pt-0">
        {total === 0 ? (
          <p className="py-6 text-center text-sm text-muted-foreground">
            {workspaceFilter
              ? `No agents in workspace "${workspaceFilter}".`
              : 'No agents connected. Open Claude Code or OpenCode to populate.'}
          </p>
        ) : (
          groups.map((g) => (
            <WorkspaceGroup
              key={g.key}
              group={g}
              peerNames={peerNames}
              peerModes={peerModes}
              workspaceFilter={workspaceFilter}
              onWorkspaceClick={onWorkspaceClick}
            />
          ))
        )}
      </CardContent>
    </Card>
  )
}

function WorkspaceGroup({
  group,
  peerNames,
  peerModes,
  workspaceFilter,
  onWorkspaceClick,
}: {
  group: Group
  peerNames: Record<string, string>
  peerModes: P2PPeerMode[]
  workspaceFilter: string
  onWorkspaceClick: (workspaceName: string) => void
}) {
  const labelClickable = group.key !== UNGROUPED_KEY
  return (
    <div className="mb-4 last:mb-0">
      <div className="sticky top-0 z-10 -mx-6 mb-1 flex items-center gap-2 bg-card/95 px-6 py-1.5 backdrop-blur supports-[backdrop-filter]:bg-card/80">
        {labelClickable ? (
          <button
            type="button"
            onClick={() => onWorkspaceClick(group.label)}
            className="text-[11px] font-semibold uppercase tracking-wider text-muted-foreground hover:text-foreground"
            data-testid={`workspace-group-header-${group.label}`}
            title={workspaceFilter === group.label ? 'Clear filter' : `Filter to ${group.label}`}
          >
            {group.label}
          </button>
        ) : (
          <span className="text-[11px] font-semibold uppercase tracking-wider text-muted-foreground/60">
            {group.label}
          </span>
        )}
        <span className="font-mono text-[10px] tabular-nums text-muted-foreground/60">
          {group.agents.length}
        </span>
      </div>
      {group.agents.map((agent) => {
        const peerId = agent.origin?.startsWith(PEER_PREFIX)
          ? agent.origin.slice(PEER_PREFIX.length)
          : ''
        const connected = peerId ? peerConnectedFromModes(peerId, peerModes) : true
        return (
          <AgentRow
            key={agent.session_id}
            agent={agent}
            peerNames={peerNames}
            peerConnected={connected}
            workspaceFilter={workspaceFilter}
            onWorkspaceClick={onWorkspaceClick}
          />
        )
      })}
    </div>
  )
}

const TIER_RANK: Record<string, number> = { fresh: 0, idle: 1, stale: 2 }

function buildGroups(agents: MeshAgent[], workspaceFilter: string): Group[] {
  const byKey = new Map<string, Group>()
  for (const a of agents) {
    const label = a.workspace_name || a.workspace_id || ''
    if (workspaceFilter && label !== workspaceFilter) continue
    const key = label || UNGROUPED_KEY
    const displayLabel = label || UNGROUPED_LABEL
    let g = byKey.get(key)
    if (!g) {
      g = { key, label: displayLabel, agents: [] }
      byKey.set(key, g)
    }
    g.agents.push(a)
  }
  // Per-group sort: tier first, then last_seen desc.
  for (const g of byKey.values()) {
    g.agents.sort((a, b) => {
      const ta = TIER_RANK[tierOf(a.last_seen_at)] ?? 99
      const tb = TIER_RANK[tierOf(b.last_seen_at)] ?? 99
      if (ta !== tb) return ta - tb
      return new Date(b.last_seen_at).getTime() - new Date(a.last_seen_at).getTime()
    })
  }
  // Group order: groups with the most fresh agents first, ungrouped last.
  return [...byKey.values()].sort((a, b) => {
    if (a.key === UNGROUPED_KEY) return 1
    if (b.key === UNGROUPED_KEY) return -1
    const freshA = a.agents.filter((x) => tierOf(x.last_seen_at) === 'fresh').length
    const freshB = b.agents.filter((x) => tierOf(x.last_seen_at) === 'fresh').length
    if (freshA !== freshB) return freshB - freshA
    return a.label.localeCompare(b.label)
  })
}

function tierOf(lastSeen: string): 'fresh' | 'idle' | 'stale' {
  const mins = Math.floor((Date.now() - new Date(lastSeen).getTime()) / 60000)
  if (mins < 2) return 'fresh'
  if (mins < 10) return 'idle'
  return 'stale'
}
