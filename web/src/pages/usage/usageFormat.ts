import type { ObservedTotals, UsageWindow } from '@/api/usage'

export function observedTokens(observed: ObservedTotals): number {
  return observed.total_tokens ?? (observed.input_tokens + observed.output_tokens)
}

export function lineageStatusLabel(status: string): string {
  switch (status) {
    case 'ok': return 'Available'
    case 'partial': return 'Partial'
    case 'unconfigured': return 'Unconfigured'
    case 'unavailable': return 'Unavailable'
    case 'error': return 'Error'
    default: return status
  }
}

export function statusVariant(status: string): 'default' | 'secondary' | 'destructive' | 'outline' {
  switch (status) {
    case 'ok': return 'default'
    case 'partial': return 'secondary'
    case 'unconfigured': return 'outline'
    case 'unavailable': return 'outline'
    case 'error': return 'destructive'
    default: return 'outline'
  }
}

export function formatWindowUsed(w: UsageWindow): string {
  if (w.used == null && w.limit != null) {
    switch (w.unit) {
      case 'percent': return `Limit ${w.limit}%`
      case 'requests': return `Limit ${formatNumber(w.limit)} requests`
      case 'credits': return `Limit ${formatNumber(w.limit)} credits`
      case 'usd': return `Limit $${w.limit.toFixed(2)}`
      case 'tokens': return `Limit ${formatTokens(w.limit)}`
      default: return `Limit ${w.limit}`
    }
  }
  if (w.used == null) return ''
  switch (w.unit) {
    case 'percent': return `${w.used}% used`
    case 'requests': return `${formatNumber(w.used)} requests`
    case 'credits': return `${formatNumber(w.used)} credits`
    case 'usd': return `$${w.used.toFixed(2)} used`
    case 'tokens': return `${formatTokens(w.used)} used`
    default: return `${w.used}`
  }
}

export function formatResetsAt(iso: string): string {
  try {
    const d = new Date(iso)
    const now = new Date()
    const diffMs = d.getTime() - now.getTime()
    if (diffMs <= 0) return 'Soon'
    const diffH = Math.floor(diffMs / 3_600_000)
    if (diffH < 24) return `in ${diffH}h`
    const diffD = Math.floor(diffH / 24)
    return `in ${diffD}d`
  } catch {
    return ''
  }
}

export function formatFreshness(iso: string): string {
  try {
    const d = new Date(iso)
    const now = new Date()
    const diffMs = now.getTime() - d.getTime()
    const diffMin = Math.floor(diffMs / 60_000)
    if (diffMin < 1) return 'just now'
    if (diffMin < 60) return `${diffMin}m ago`
    const diffH = Math.floor(diffMin / 60)
    if (diffH < 24) return `${diffH}h ago`
    return d.toLocaleDateString()
  } catch {
    return iso
  }
}

export function formatNumber(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`
  if (n >= 1_000) return `${(n / 1_000).toFixed(1)}K`
  return String(n)
}

export function formatTokens(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`
  if (n >= 1_000) return `${(n / 1_000).toFixed(1)}K`
  return String(n)
}

export function progressColor(pct: number): string {
  if (pct >= 90) return 'bg-destructive h-full'
  if (pct >= 75) return 'bg-amber-500 h-full'
  return 'bg-emerald-500/70 h-full'
}
