// VitalsStrip — the dashboard's morning-glance row. Six dense chips
// arranged in a single line that says "this is what your gateway is
// doing right now". Replaces the old SaaS-cliche big-number cards.
//
// Visual register: sharp corners (consistent with the rest of the
// post-typography-pass surface), system-ui mono for the numbers,
// tone via the existing Badge tone system. Calm by default — only
// the "needs you" counts (pending approvals) flip to warn/critical.
//
// Each chip is a Link into its native surface so the dashboard
// behaves like a wayfinding board instead of a dead read-only widget.

import { Link } from 'react-router-dom'
import { cn } from '@/lib/utils'

export type VitalTone = 'idle' | 'live' | 'info' | 'warn' | 'critical'

export interface VitalItem {
  label: string
  value: number | string
  detail?: string
  href: string
  tone?: VitalTone
  testid?: string
}

const valueToneClass: Record<VitalTone, string> = {
  idle: 'text-foreground',
  live: 'text-emerald-400',
  info: 'text-sky-300',
  warn: 'text-amber-300',
  critical: 'text-red-300',
}

const borderToneClass: Record<VitalTone, string> = {
  idle: 'border-border hover:border-border/80',
  live: 'border-emerald-500/30 hover:border-emerald-500/50',
  info: 'border-sky-500/30 hover:border-sky-500/50',
  warn: 'border-amber-500/40 hover:border-amber-500/60',
  critical: 'border-red-500/40 hover:border-red-500/60',
}

export function VitalsStrip({ items }: { items: VitalItem[] }) {
  return (
    <div
      className="grid grid-cols-2 gap-2 sm:grid-cols-3 lg:grid-cols-6"
      data-testid="dash-vitals-strip"
    >
      {items.map((item) => (
        <VitalChip key={item.label} item={item} />
      ))}
    </div>
  )
}

function VitalChip({ item }: { item: VitalItem }) {
  const tone = item.tone ?? 'idle'
  return (
    <Link
      to={item.href}
      data-testid={item.testid ?? `dash-vital-${item.label.toLowerCase().replace(/\s+/g, '-')}`}
      title={item.detail ?? item.label}
      className={cn(
        'group block border bg-card/40 px-3 py-2.5 transition-colors',
        'focus-visible:outline-none focus-visible:border-primary/60',
        borderToneClass[tone],
      )}
    >
      <div className="flex items-center justify-between">
        <span className="text-[10px] font-semibold uppercase tracking-[0.14em] text-muted-foreground">
          {item.label}
        </span>
        {tone === 'warn' || tone === 'critical' ? (
          <span className="relative flex h-1.5 w-1.5">
            <span
              className={cn(
                'absolute inline-flex h-full w-full animate-pulse-slow rounded-full opacity-70',
                tone === 'critical' ? 'bg-red-400' : 'bg-amber-400',
              )}
            />
            <span
              className={cn(
                'relative inline-flex h-1.5 w-1.5 rounded-full',
                tone === 'critical' ? 'bg-red-400' : 'bg-amber-400',
              )}
            />
          </span>
        ) : tone === 'live' ? (
          <span className="inline-flex h-1.5 w-1.5 rounded-full bg-emerald-400" />
        ) : null}
      </div>
      <div
        className={cn(
          'mt-1.5 font-mono text-2xl font-semibold leading-none tabular-nums',
          valueToneClass[tone],
        )}
      >
        {item.value}
      </div>
      {item.detail && (
        <div className="mt-1 truncate text-[10.5px] text-muted-foreground/80">
          {item.detail}
        </div>
      )}
    </Link>
  )
}
