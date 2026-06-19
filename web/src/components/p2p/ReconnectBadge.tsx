/**
 * ReconnectBadge — small badge that surfaces the current state of the
 * daemon's reconnect loop for a paired peer. Sits alongside
 * <PeerConnectionBadge /> on the peers list and mirrors its styling
 * conventions.
 *
 * Color encoding:
 *   - green  → "Connected"        (state="connected")
 *   - amber  → "Searching"        (searching_dht / dht_unavailable)
 *   - red    → "Dial failed"      (state="dial_failed")
 *   - amber  → "Not found"        (state="not_found_in_dht")
 *
 * Returns null for unknown / empty states so callers can drop it into a
 * row without conditional wrapping. The relative timestamp passed via
 * `lastAttemptAt` is rendered as a tooltip so the row stays compact.
 */
import { Badge } from '@/components/ui/badge'
import type { ReconnectState } from '@/components/pairing/api'

export interface ReconnectBadgeProps {
  state: ReconnectState | undefined
  lastAttemptAt?: string
  lastError?: string
  className?: string
}

interface BadgeMeta {
  label: string
  // Tailwind color classes — explicit so the build picks them up.
  className: string
}

const META: Record<Exclude<ReconnectState, ''>, BadgeMeta> = {
  connected: {
    label: 'Connected',
    className: 'bg-emerald-500/10 text-emerald-600 border-emerald-500/30',
  },
  searching_dht: {
    label: 'Searching',
    className: 'bg-amber-500/10 text-amber-600 border-amber-500/30',
  },
  dial_failed: {
    label: 'Dial failed',
    className: 'bg-red-500/10 text-red-600 border-red-500/30',
  },
  not_found_in_dht: {
    label: 'Not found',
    className: 'bg-amber-500/10 text-amber-600 border-amber-500/30',
  },
  dht_unavailable: {
    label: 'DHT off',
    className: 'bg-muted text-muted-foreground border-border',
  },
}

function relativeTime(iso: string): string {
  const d = Date.now() - new Date(iso).getTime()
  if (Number.isNaN(d)) return iso
  const s = Math.floor(d / 1000)
  if (s < 60) return `${s}s ago`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m ago`
  const h = Math.floor(m / 60)
  if (h < 24) return `${h}h ago`
  return `${Math.floor(h / 24)}d ago`
}

export function ReconnectBadge({
  state,
  lastAttemptAt,
  lastError,
  className,
}: ReconnectBadgeProps) {
  if (!state) return null
  const meta = META[state as Exclude<ReconnectState, ''>]
  if (!meta) return null
  const parts: string[] = [meta.label]
  if (lastAttemptAt) parts.push(`last try ${relativeTime(lastAttemptAt)}`)
  if (lastError) parts.push(lastError)
  const title = parts.join(' · ')
  return (
    <Badge
      variant="outline"
      className={`${meta.className} ${className ?? ''}`}
      title={title}
      data-testid={`reconnect-badge-${state}`}
    >
      {meta.label}
    </Badge>
  )
}

export default ReconnectBadge
