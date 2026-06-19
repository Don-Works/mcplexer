/**
 * PeerConnectionBadge — small badge component for the paired-peers list,
 * introduced by M1.2. Shows how the local node is currently reaching a
 * paired libp2p peer:
 *
 *   - "On LAN"        when connection_mode === "direct"
 *   - "Hole-punched"  when connection_mode === "hole-punched"
 *   - "Relayed"       when connection_mode === "relay"
 *
 * Returns null for unknown / empty modes so the caller can drop it into a
 * row without conditional wrapper logic.
 *
 * This is intentionally a self-contained presentational component: M1.2's
 * peers UI hasn't merged yet, but when it lands its row component can
 * import this directly:
 *
 *   <PeerConnectionBadge mode={peer.connection_mode} />
 */
import { Badge } from "@/components/ui/badge"

export type ConnectionMode = "direct" | "hole-punched" | "relay" | "" | null | undefined

export interface PeerConnectionBadgeProps {
  mode: ConnectionMode
  /** Optional className passthrough for layout fine-tuning. */
  className?: string
}

interface BadgeMeta {
  label: string
  variant: "default" | "secondary" | "outline"
  title: string
}

const META: Record<"direct" | "hole-punched" | "relay", BadgeMeta> = {
  "direct": {
    label: "On LAN",
    variant: "default",
    title: "Connected directly over the local network (mDNS-discovered)",
  },
  "hole-punched": {
    label: "Hole-punched",
    variant: "secondary",
    title: "Connected over the Internet via DCUtR NAT traversal",
  },
  "relay": {
    label: "Relayed",
    variant: "outline",
    title: "Connected via a libp2p circuit-v2 relay (slowest path)",
  },
}

export function PeerConnectionBadge({ mode, className }: PeerConnectionBadgeProps) {
  if (!mode) return null
  const meta = META[mode]
  if (!meta) return null
  return (
    <Badge variant={meta.variant} className={className} title={meta.title}>
      {meta.label}
    </Badge>
  )
}

export default PeerConnectionBadge
