// AgentOriginBadge surfaces whether an agent on the Mesh page is reached
// through this daemon's stdio MCP socket ("local") or through libp2p from
// a paired peer ("peer:<peer_id>"). Wired off `MeshAgent.origin` from the
// REST API.
//
// Why this exists: before this badge, the Active Agents list silently
// showed only local agents whenever the libp2p mesh was broken — actively
// misleading. The pill makes the distinction explicit so a glance at the
// Mesh tab tells the truth about cross-machine reachability.

interface AgentOriginBadgeProps {
  // origin from the API: "local" | "peer:<peer_id>" | "" (legacy)
  origin?: string
  // Optional peer-id → display-name lookup so a remote peer's pill shows
  // "peer:peer-laptop" instead of "peer:6N1XnNqBpa".
  peerNames?: Record<string, string>
}

const localStyle =
  'bg-emerald-500/10 text-emerald-600 border-emerald-500/30'
const peerStyle = 'bg-muted text-muted-foreground border-border'

export const PEER_PREFIX = 'peer:'

// formatPeerLabel renders the user-facing tail of a "peer:<peer_id>" origin:
// prefer the friendly display_name from the paired-peers table; fall back
// to the last 10 chars of the libp2p peer ID.
export function formatPeerLabel(
  origin: string,
  peerNames?: Record<string, string>,
): string {
  if (!origin.startsWith(PEER_PREFIX)) return origin
  const peerID = origin.slice(PEER_PREFIX.length)
  const name = peerNames?.[peerID]
  if (name) return `${PEER_PREFIX}${name}`
  if (peerID.length > 10) return `${PEER_PREFIX}${peerID.slice(-10)}`
  return origin
}

export function AgentOriginBadge({
  origin,
  peerNames,
}: AgentOriginBadgeProps): React.ReactElement {
  const isLocal = !origin || origin === 'local'
  const label = isLocal ? 'local' : formatPeerLabel(origin, peerNames)
  const style = isLocal ? localStyle : peerStyle
  return (
    <span
      className={`inline-flex items-center rounded-md border px-1.5 py-0.5 text-[10px] font-medium ${style}`}
      data-testid={
        isLocal ? 'agent-origin-local' : 'agent-origin-peer'
      }
      title={origin || 'local'}
    >
      {label}
    </span>
  )
}
