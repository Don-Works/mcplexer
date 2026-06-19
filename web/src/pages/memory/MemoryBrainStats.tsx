// MemoryBrainStats — the "shape of the brain" header that crowns the
// /memory landing page.
//
// Surfaces big numerals + short labels for: brain age, total memories,
// type-mix (donut), 30-day write series (sparkline), recency strip,
// network reach + decay pressure. Keeps the lid on motion — a slow pulse
// on the live ring + a hover-only ramp on numerals.
//
// All data comes from /api/v1/memory/stats. Backend computes everything
// in SQL — we never load the full memory set just to count.

import { useEffect, useMemo } from 'react'
import { Link } from 'react-router-dom'
import {
  ArrowUpRight,
  BookOpen,
  Brain,
  Cpu,
  Flame,
  Hourglass,
  Network,
  Snowflake,
  TriangleAlert,
} from 'lucide-react'

import { cn } from '@/lib/utils'
import { useMemoryStats } from '@/hooks/use-memory'
import { useSignal } from '@/components/notifications/use-signal'
import { isMemoryEvent } from './memory-utils'
import type {
  MemoryDailyCount,
  MemoryRecencyBuckets,
  MemoryStats,
  MemoryTagCount,
} from '@/api/memory'

// Local alias — type_mix is a freeform string→count map on the server,
// so we keep TS lenient at the call sites.
type MemoryTypeMix = Record<string, number>

export function MemoryBrainStats() {
  const { data, loading, error, refetch } = useMemoryStats()
  const { events } = useSignal()

  // Live-refresh — the brain-stats header is the one surface that must
  // never go stale next to the live activity feed below it. When the
  // newest memory event changes, refetch the aggregate. useApi keeps the
  // existing numbers on screen during the refetch (stale-while-revalidate),
  // so this never flashes a spinner.
  const latestMemoryEventId = useMemo(() => {
    const ev = events.find((e) => isMemoryEvent(e.kind, e.source))
    return ev ? ev.message_id : ''
  }, [events])
  useEffect(() => {
    if (latestMemoryEventId) refetch()
  }, [latestMemoryEventId, refetch])

  // A failed fetch with no data must NOT collapse into the all-zeros
  // "empty brain" render — that actively misinforms the operator that
  // their persistent brain is empty. Show a distinct, retryable error
  // instead. The zero-fallback below only stands in for a genuinely empty
  // or still-loading brain.
  if (error && !data) {
    return <BrainStatsError message={error} onRetry={refetch} />
  }

  // Loading skeleton — keep layout stable so nothing jumps when the
  // numbers arrive. Empty? Drop in zero values so the rest of the
  // landing page composes cleanly without a flash of "missing tile".
  const stats: MemoryStats = data ?? {
    brain_age_days: 0,
    brain_age_born_at: null,
    total_memories: 0,
    total_bytes: 0,
    pages_equivalent: 0,
    type_mix: {},
    recency_buckets: { fresh: 0, warm: 0, cold: 0, dormant: 0 },
    writes_per_day_30d: [],
    network_reach: { shared_memory_count: 0, peer_count: 0 },
    top_tags: [],
    decay_pressure: 0,
  }

  return (
    <section
      aria-label="Brain stats"
      className={cn(
        'grid grid-cols-1 gap-3 border border-border bg-card/30 p-4 transition-opacity md:grid-cols-2 lg:grid-cols-6',
        loading && !data ? 'opacity-70' : 'opacity-100',
      )}
    >
      <BrainAgeBlock stats={stats} />
      <TotalMemoriesBlock stats={stats} />
      <TypeMixBlock mix={stats.type_mix} />
      <ActivityBlock series={stats.writes_per_day_30d} />
      <RecencyBlock buckets={stats.recency_buckets} />
      <ReachAndDecayBlock stats={stats} />
      {stats.top_tags.length > 0 && (
        <TopTagsRow tags={stats.top_tags} className="md:col-span-2 lg:col-span-6" />
      )}
    </section>
  )
}

// BrainStatsError — distinct failed state for the header. Deliberately
// NOT a zero-filled brain: a failed request and a genuinely empty brain
// must read differently. Keeps the bordered-strip footprint so the page
// layout below it does not jump.
function BrainStatsError({
  message,
  onRetry,
}: {
  message: string
  onRetry: () => void
}) {
  return (
    <section
      aria-label="Brain stats"
      className="flex items-center justify-between gap-3 border border-destructive/40 bg-destructive/5 p-4"
    >
      <div className="flex min-w-0 items-center gap-2.5">
        <TriangleAlert className="h-4 w-4 shrink-0 text-destructive" />
        <div className="min-w-0">
          <div className="text-[13px] font-semibold text-foreground">
            Could not load brain stats
          </div>
          <div
            className="truncate font-mono text-[11px] text-muted-foreground"
            title={message}
          >
            {message}
          </div>
        </div>
      </div>
      <button
        type="button"
        onClick={onRetry}
        className="shrink-0 border border-border px-2.5 py-1 text-[11px] font-medium text-foreground transition-colors hover:bg-card"
      >
        Retry
      </button>
    </section>
  )
}

// ----- numeric blocks --------------------------------------------------

function BrainAgeBlock({ stats }: { stats: MemoryStats }) {
  const born = stats.brain_age_born_at
    ? new Date(stats.brain_age_born_at)
    : null
  const bornLabel = born
    ? born.toLocaleDateString(undefined, {
        year: 'numeric',
        month: 'short',
        day: 'numeric',
      })
    : '—'
  return (
    <Stat
      icon={<Brain className="h-4 w-4" />}
      label="Brain age"
      value={stats.brain_age_days > 0 ? `${stats.brain_age_days}d` : '—'}
      detail={born ? `born ${bornLabel}` : 'no memories yet'}
    />
  )
}

function TotalMemoriesBlock({ stats }: { stats: MemoryStats }) {
  const pages = Math.round(stats.pages_equivalent)
  return (
    <Stat
      icon={<BookOpen className="h-4 w-4" />}
      label="Memories"
      value={stats.total_memories.toLocaleString()}
      detail={
        pages > 0
          ? `${formatBytes(stats.total_bytes)} · ~${pages.toLocaleString()} pages`
          : formatBytes(stats.total_bytes)
      }
    />
  )
}

function TypeMixBlock({ mix }: { mix: MemoryTypeMix }) {
  const entries = useMemo(() => {
    const arr = Object.entries(mix).map(([kind, count]) => ({ kind, count }))
    arr.sort((a, b) => b.count - a.count)
    return arr
  }, [mix])
  const total = entries.reduce((acc, e) => acc + e.count, 0)
  return (
    <div className="flex items-center gap-3 px-3 py-3">
      <TypeDonut entries={entries} size={48} />
      <div className="min-w-0">
        <div className="text-[10px] font-semibold uppercase tracking-[0.14em] text-muted-foreground">
          Type mix
        </div>
        {entries.length === 0 ? (
          <div className="mt-1 font-mono text-sm text-muted-foreground/60">—</div>
        ) : (
          <ul className="mt-1 space-y-0.5">
            {entries.slice(0, 3).map((e) => (
              <li
                key={e.kind}
                className="flex items-center gap-2 font-mono text-[11px] text-muted-foreground"
              >
                <span
                  className="inline-block h-1.5 w-1.5 shrink-0 rounded-full"
                  style={{ backgroundColor: colorForKind(e.kind) }}
                />
                <span className="truncate">{e.kind}</span>
                <span className="ml-auto tabular-nums text-foreground/80">
                  {e.count}
                </span>
                <span className="tabular-nums text-muted-foreground/50">
                  {total > 0 ? `${Math.round((e.count / total) * 100)}%` : ''}
                </span>
              </li>
            ))}
          </ul>
        )}
      </div>
    </div>
  )
}

function ActivityBlock({ series }: { series: MemoryDailyCount[] }) {
  const last7 = series.slice(-7).reduce((acc, d) => acc + d.count, 0)
  const prev7 = series.slice(-14, -7).reduce((acc, d) => acc + d.count, 0)
  const delta = last7 - prev7
  const trend = delta === 0 ? '·' : delta > 0 ? `+${delta}` : `${delta}`
  const trendTone =
    delta > 0
      ? 'text-emerald-400'
      : delta < 0
        ? 'text-rose-400/80'
        : 'text-muted-foreground/60'
  return (
    <div className="flex flex-col gap-1.5 px-3 py-3">
      <div className="flex items-center justify-between text-[10px] font-semibold uppercase tracking-[0.14em] text-muted-foreground">
        <span className="inline-flex items-center gap-1.5">
          <Cpu className="h-3.5 w-3.5" />
          30-day writes
        </span>
        <span className={cn('font-mono text-[10px]', trendTone)}>{trend}</span>
      </div>
      <Sparkline points={series} />
      <div className="font-mono text-[11px] text-muted-foreground tabular-nums">
        {last7} this week
      </div>
    </div>
  )
}

function RecencyBlock({ buckets }: { buckets: MemoryRecencyBuckets }) {
  const total =
    buckets.fresh + buckets.warm + buckets.cold + buckets.dormant
  const segs = [
    { key: 'fresh', count: buckets.fresh, color: '#34d399', label: '≤7d' },
    { key: 'warm', count: buckets.warm, color: '#fbbf24', label: '≤30d' },
    { key: 'cold', count: buckets.cold, color: '#60a5fa', label: '≤180d' },
    { key: 'dormant', count: buckets.dormant, color: '#64748b', label: '>180d' },
  ]
  return (
    <div className="flex flex-col gap-1.5 px-3 py-3">
      <div className="flex items-center gap-1.5 text-[10px] font-semibold uppercase tracking-[0.14em] text-muted-foreground">
        <Hourglass className="h-3.5 w-3.5" />
        Recency
      </div>
      <div
        className="flex h-1.5 w-full overflow-hidden border border-border/40 bg-background/40"
        role="img"
        aria-label={`Recency: fresh ${buckets.fresh}, warm ${buckets.warm}, cold ${buckets.cold}, dormant ${buckets.dormant}`}
      >
        {total === 0 ? (
          <div className="flex-1 bg-muted/30" />
        ) : (
          segs.map((s) => (
            <div
              key={s.key}
              className="h-full"
              style={{
                width: `${(s.count / total) * 100}%`,
                backgroundColor: s.color,
              }}
              title={`${s.key} ${s.label}: ${s.count}`}
            />
          ))
        )}
      </div>
      <ul className="grid grid-cols-2 gap-x-3 gap-y-0.5 font-mono text-[10.5px] text-muted-foreground">
        {segs.map((s) => (
          <li key={s.key} className="flex items-center gap-1.5">
            <span
              className="inline-block h-1.5 w-1.5 rounded-full"
              style={{ backgroundColor: s.color }}
            />
            <span className="truncate">{s.label}</span>
            <span className="ml-auto tabular-nums text-foreground/80">
              {s.count}
            </span>
          </li>
        ))}
      </ul>
    </div>
  )
}

function ReachAndDecayBlock({ stats }: { stats: MemoryStats }) {
  const peers = stats.network_reach.peer_count
  const shared = stats.network_reach.shared_memory_count
  const decay = stats.decay_pressure
  return (
    <div className="flex flex-col gap-3 px-3 py-3">
      <MiniRow
        icon={<Network className="h-3.5 w-3.5" />}
        label="Network reach"
        value={peers > 0 ? `${peers} peer${peers === 1 ? '' : 's'}` : '—'}
        detail={
          shared > 0
            ? `${shared} shared memor${shared === 1 ? 'y' : 'ies'}`
            : 'local only'
        }
      />
      <MiniRow
        icon={
          decay > 0 ? (
            <Flame className="h-3.5 w-3.5 text-amber-400/80" />
          ) : (
            <Snowflake className="h-3.5 w-3.5" />
          )
        }
        label="Decay pressure"
        value={decay > 0 ? decay.toLocaleString() : '0'}
        detail={
          decay > 0 ? 'overdue for review (>180d, unpinned)' : 'all current'
        }
        href={decay > 0 ? '/memory/all?stale=1' : undefined}
        accent={decay > 0 ? 'warn' : undefined}
      />
    </div>
  )
}

function TopTagsRow({
  tags,
  className,
}: {
  tags: MemoryTagCount[]
  className?: string
}) {
  return (
    <div className={cn('flex flex-wrap items-center gap-2 border-t border-border/40 pt-3', className)}>
      <span className="text-[10px] font-semibold uppercase tracking-[0.14em] text-muted-foreground">
        Top tags
      </span>
      <ul className="flex flex-wrap items-center gap-1.5">
        {tags.map((t) => (
          <li
            key={t.tag}
            className="inline-flex items-center gap-1.5 border border-border/60 bg-background/40 px-2 py-0.5 font-mono text-[10.5px]"
          >
            <span className="text-foreground/90">{t.tag}</span>
            <span className="tabular-nums text-muted-foreground">
              {t.count}
            </span>
          </li>
        ))}
      </ul>
    </div>
  )
}

// ----- primitives ------------------------------------------------------

function Stat({
  icon,
  label,
  value,
  detail,
  href,
  accent,
}: {
  icon: React.ReactNode
  label: string
  value: string
  detail?: string
  href?: string
  accent?: 'warn'
}) {
  const inner = (
    <>
      <div className="flex items-center justify-between text-[10px] font-semibold uppercase tracking-[0.14em] text-muted-foreground">
        <span className="inline-flex items-center gap-1.5">
          {icon}
          {label}
        </span>
        {href && (
          <ArrowUpRight className="h-3 w-3 opacity-0 transition-opacity group-hover:opacity-100" />
        )}
      </div>
      <div
        className={cn(
          'mt-1.5 font-mono text-[26px] font-semibold leading-none tracking-tight tabular-nums',
          accent === 'warn' ? 'text-amber-300' : 'text-foreground',
        )}
      >
        {value}
      </div>
      {detail && (
        <div className="mt-1 text-[11px] leading-snug text-muted-foreground">
          {detail}
        </div>
      )}
    </>
  )
  if (href) {
    return (
      <Link
        to={href}
        className="group block px-3 py-3 transition-colors hover:bg-card/60"
      >
        {inner}
      </Link>
    )
  }
  return <div className="px-3 py-3">{inner}</div>
}

function MiniRow({
  icon,
  label,
  value,
  detail,
  href,
  accent,
}: {
  icon: React.ReactNode
  label: string
  value: string
  detail?: string
  href?: string
  accent?: 'warn'
}) {
  const body = (
    <div className="flex items-center gap-2">
      <span className="inline-flex h-6 w-6 shrink-0 items-center justify-center border border-border/40 bg-background/40 text-muted-foreground">
        {icon}
      </span>
      <div className="min-w-0 flex-1">
        <div className="text-[10px] uppercase tracking-[0.14em] text-muted-foreground/80">
          {label}
        </div>
        <div
          className={cn(
            'font-mono text-[15px] font-semibold leading-tight tabular-nums',
            accent === 'warn' ? 'text-amber-300' : 'text-foreground',
          )}
        >
          {value}
        </div>
        {detail && (
          <div className="text-[10.5px] leading-snug text-muted-foreground/80">
            {detail}
          </div>
        )}
      </div>
    </div>
  )
  if (href) {
    return (
      <Link to={href} className="block transition-colors hover:opacity-90">
        {body}
      </Link>
    )
  }
  return body
}

// ----- charts ----------------------------------------------------------

// Sparkline — small SVG line of the 30-day write series. Auto-scales to
// max(series, 1) so a single write still produces a visible bar.
function Sparkline({ points }: { points: MemoryDailyCount[] }) {
  const W = 140
  const H = 28
  if (points.length === 0) {
    return <div className="h-7 bg-muted/20" />
  }
  const max = Math.max(1, ...points.map((p) => p.count))
  const stepX = points.length > 1 ? W / (points.length - 1) : 0
  const line = points
    .map((p, i) => {
      const x = i * stepX
      const y = H - (p.count / max) * (H - 2) - 1
      return `${i === 0 ? 'M' : 'L'}${x.toFixed(1)},${y.toFixed(1)}`
    })
    .join(' ')
  const area = `${line} L${W.toFixed(1)},${H} L0,${H} Z`
  const lastIdx = points.length - 1
  const lastPoint = points[lastIdx]
  const lastX = lastIdx * stepX
  const lastY = H - (lastPoint.count / max) * (H - 2) - 1
  return (
    <svg
      width="100%"
      height={H}
      viewBox={`0 0 ${W} ${H}`}
      preserveAspectRatio="none"
      className="block"
      role="img"
      aria-label="30-day write activity sparkline"
    >
      <path d={area} fill="currentColor" className="text-primary/15" />
      <path
        d={line}
        fill="none"
        stroke="currentColor"
        strokeWidth={1.25}
        strokeLinecap="round"
        strokeLinejoin="round"
        className="text-primary/90"
      />
      {/* Today marker — tiny circle on the rightmost point. */}
      <circle
        cx={lastX}
        cy={lastY}
        r={1.6}
        fill="currentColor"
        className="text-primary"
      />
    </svg>
  )
}

// Tiny donut for the type-mix block. Two-or-more slices animate softly
// in CSS via fill; one slice or zero gets a passive ring.
function TypeDonut({
  entries,
  size,
}: {
  entries: { kind: string; count: number }[]
  size: number
}) {
  const total = entries.reduce((acc, e) => acc + e.count, 0)
  const r = size / 2 - 4
  const cx = size / 2
  const cy = size / 2
  if (total === 0) {
    return (
      <svg width={size} height={size} viewBox={`0 0 ${size} ${size}`} aria-hidden>
        <circle
          cx={cx}
          cy={cy}
          r={r}
          fill="none"
          stroke="currentColor"
          strokeWidth={3}
          className="text-muted/40"
        />
      </svg>
    )
  }
  // Precompute prefix sums so each slice's rotation is derivable from a
  // pure (index → rotation) lookup. The React Compiler rejects
  // mid-render mutation of an outer accumulator inside the JSX map; this
  // shape keeps render purely functional.
  const prefix = entries.map((_, i) =>
    entries.slice(0, i).reduce((acc, e) => acc + e.count, 0),
  )
  return (
    <svg
      width={size}
      height={size}
      viewBox={`0 0 ${size} ${size}`}
      role="img"
      aria-label="Memory type distribution"
    >
      <circle
        cx={cx}
        cy={cy}
        r={r}
        fill="none"
        stroke="currentColor"
        strokeWidth={3}
        className="text-muted/30"
      />
      {entries.map((e, i) => {
        const frac = e.count / total
        const dash = frac * (2 * Math.PI * r)
        const gap = 2 * Math.PI * r - dash
        const rotation = (prefix[i] / total) * 360 - 90
        return (
          <circle
            key={e.kind}
            cx={cx}
            cy={cy}
            r={r}
            fill="none"
            stroke={colorForKind(e.kind)}
            strokeWidth={3}
            strokeDasharray={`${dash} ${gap}`}
            transform={`rotate(${rotation} ${cx} ${cy})`}
          />
        )
      })}
      <text
        x={cx}
        y={cy + 3}
        textAnchor="middle"
        className="fill-foreground font-mono"
        style={{ fontSize: 10 }}
      >
        {total}
      </text>
    </svg>
  )
}

// ----- helpers ---------------------------------------------------------

// colorForKind maps memory kinds to the dashboard's accent palette.
// Fact / note are the canonical pair; future-coined kinds get hashed
// into a small set of fallback hues so they at least stay distinguishable.
function colorForKind(kind: string): string {
  switch (kind) {
    case 'fact':
      return '#60a5fa' // info / blue
    case 'note':
      return '#a78bfa' // accent / violet
    case 'preference':
      return '#34d399'
    case 'context':
      return '#fbbf24'
    case 'decision':
      return '#f472b6'
    default: {
      const fallbacks = [
        '#94a3b8',
        '#22d3ee',
        '#fb7185',
        '#facc15',
        '#4ade80',
      ]
      let h = 0
      for (let i = 0; i < kind.length; i++) {
        h = (h * 31 + kind.charCodeAt(i)) >>> 0
      }
      return fallbacks[h % fallbacks.length]
    }
  }
}

// formatBytes shrinks the raw byte count down to a short human label.
function formatBytes(n: number): string {
  if (n < 1024) return `${n}B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)}KB`
  return `${(n / (1024 * 1024)).toFixed(1)}MB`
}
