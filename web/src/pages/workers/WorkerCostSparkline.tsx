// WorkerCostSparkline — daily cost aggregation for the last 30 days.
// Uses recharts, which is already a project dependency. Falls back to
// a numeric total when there's nothing to plot.
//
// The series is bucketed by local-day so the chart aligns with what
// the user actually saw on their wall clock. Workers with no cost yet
// show a flat zero line — the legend's total still reads $0.00 which
// is the truthful answer.

import { useId, useMemo } from 'react'
import {
  Area,
  AreaChart,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from 'recharts'

import type { WorkerRun } from '@/api/workers'

interface DailyPoint {
  day: string
  cost: number
}

interface CostSparklineProps {
  runs: WorkerRun[]
  // When true, hide both axes — used for the per-row sparkline in the
  // cost dashboard table where shape matters but precision doesn't.
  compact?: boolean
}

export function CostSparkline({ runs, compact = false }: CostSparklineProps) {
  const { series, total } = useMemo(() => buildDaily(runs, 30), [runs])
  const hasAny = total > 0
  // Per-render unique id so SVG <defs id=...> never collides with
  // another CostSparkline mount on the same page.
  const gradientID = useId().replace(/[^A-Za-z0-9_-]/g, '-') + '-cost-fill'
  // Dynamic Y-axis precision: sub-dollar values need 4 decimals so the
  // axis doesn't collapse to "$0.00" rows. Above $1 the user wants the
  // bigger picture and 2 decimals is plenty.
  const tickFormat = (v: number) => (v < 1 ? `$${v.toFixed(4)}` : `$${v.toFixed(2)}`)

  if (compact) {
    return hasAny ? (
      <div className="h-10">
        <ResponsiveContainer width="100%" height="100%">
          <AreaChart data={series} margin={{ top: 2, right: 4, bottom: 2, left: 4 }}>
            <defs>
              <linearGradient id={gradientID} x1="0" y1="0" x2="0" y2="1">
                <stop offset="0%" stopColor="currentColor" stopOpacity={0.35} />
                <stop offset="100%" stopColor="currentColor" stopOpacity={0} />
              </linearGradient>
            </defs>
            <Area
              type="monotone"
              dataKey="cost"
              stroke="currentColor"
              strokeWidth={1.5}
              fill={`url(#${gradientID})`}
              className="text-primary"
            />
          </AreaChart>
        </ResponsiveContainer>
      </div>
    ) : (
      <span className="text-xs text-muted-foreground/60">no spend</span>
    )
  }

  return (
    <div>
      <div className="flex items-baseline justify-between gap-3">
        <div>
          <div className="text-xs text-muted-foreground">Total · last 30 days</div>
          <div className="font-mono text-lg text-foreground">${total.toFixed(4)}</div>
        </div>
        <div className="text-[10px] text-muted-foreground/60">
          {runs.length} run{runs.length === 1 ? '' : 's'} included
        </div>
      </div>
      {hasAny ? (
        <div className="mt-3 h-32">
          <ResponsiveContainer width="100%" height="100%">
            <AreaChart data={series} margin={{ top: 4, right: 8, bottom: 0, left: 0 }}>
              <defs>
                <linearGradient id={gradientID} x1="0" y1="0" x2="0" y2="1">
                  <stop offset="0%" stopColor="currentColor" stopOpacity={0.4} />
                  <stop offset="100%" stopColor="currentColor" stopOpacity={0} />
                </linearGradient>
              </defs>
              <XAxis
                dataKey="day"
                tick={{ fontSize: 10 }}
                interval="preserveStartEnd"
                stroke="currentColor"
                opacity={0.4}
              />
              <YAxis
                tick={{ fontSize: 10 }}
                width={48}
                stroke="currentColor"
                opacity={0.4}
                tickFormatter={tickFormat}
              />
              <Tooltip
                contentStyle={{
                  background: 'rgba(0,0,0,0.8)',
                  border: '1px solid rgba(255,255,255,0.1)',
                  fontSize: 11,
                }}
                formatter={(value) => [`$${Number(value ?? 0).toFixed(4)}`, 'cost']}
              />
              <Area
                type="monotone"
                dataKey="cost"
                stroke="currentColor"
                strokeWidth={1.5}
                fill={`url(#${gradientID})`}
                className="text-primary"
              />
            </AreaChart>
          </ResponsiveContainer>
        </div>
      ) : (
        <div className="mt-3 text-xs text-muted-foreground">
          No billable runs in the last 30 days.
        </div>
      )}
    </div>
  )
}

// buildDaily buckets WorkerRun.cost_usd by local day for the past
// `days` days. Days with no runs sit at 0 so the chart stays evenly
// spaced.
function buildDaily(runs: WorkerRun[], days: number): { series: DailyPoint[]; total: number } {
  const buckets = new Map<string, number>()
  const today = new Date()
  for (let i = days - 1; i >= 0; i--) {
    const d = new Date(today)
    d.setDate(today.getDate() - i)
    buckets.set(dayKey(d), 0)
  }
  let total = 0
  for (const r of runs) {
    const t = r.finished_at ?? r.started_at
    if (!t) continue
    const day = dayKey(new Date(t))
    if (!buckets.has(day)) continue
    const cost = r.cost_usd || 0
    buckets.set(day, (buckets.get(day) ?? 0) + cost)
    total += cost
  }
  const series: DailyPoint[] = Array.from(buckets.entries()).map(([day, cost]) => ({
    day: day.slice(5), // strip year
    cost,
  }))
  return { series, total }
}

function dayKey(d: Date): string {
  const y = d.getFullYear()
  const m = (d.getMonth() + 1).toString().padStart(2, '0')
  const dd = d.getDate().toString().padStart(2, '0')
  return `${y}-${m}-${dd}`
}
