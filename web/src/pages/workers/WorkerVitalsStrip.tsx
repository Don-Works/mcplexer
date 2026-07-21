// WorkerVitalsStrip — the top "cockpit" row on the detail page. Four
// large KPIs the operator wants to know at a glance:
//   - Next run in 23m 14s  (or "Paused")
//   - Runs today
//   - $ today
//   - Success rate 7d
//
// Each KPI is centered, with the countdown deliberately the largest
// element — temporal anchor for the whole page.

import { useMemo } from 'react'
import { CalendarClock, DollarSign, Target, TrendingUp } from 'lucide-react'

import type { Worker, WorkerRun } from '@/api/workers'
import { useCountdown } from './use-countdown'
import { isTriggerOnlySchedule, startOfLocalDay } from './worker-utils'

interface Props {
  worker: Worker
  runs: WorkerRun[]
}

export function WorkerVitalsStrip({ worker, runs }: Props) {
  const countdown = useCountdown(worker.schedule_spec, lastRunFinishedAt(runs), worker.enabled)
  const stats = useMemo(() => computeStats(runs), [runs])
  const triggerOnly = worker.enabled && isTriggerOnlySchedule(worker.schedule_spec)

  return (
    <div
      className="grid grid-cols-2 gap-px overflow-hidden border border-border bg-border md:grid-cols-4"
      data-testid="worker-vitals-strip"
    >
      <Tile
        icon={<CalendarClock className="h-4 w-4" />}
        label={!worker.enabled ? 'Status' : triggerOnly ? 'Fires on' : 'Next run in'}
        value={
          !worker.enabled
            ? worker.auto_paused_reason ? 'Auto-paused' : 'Paused'
            : triggerOnly
              ? 'Trigger'
              : countdown.humanCountdown
        }
        tone={worker.enabled ? 'primary' : 'muted'}
        sub={
          triggerOnly
            ? worker.schedule_spec
            : worker.enabled && countdown.nextRunDate
              ? countdown.nextRunDate.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
              : worker.schedule_spec
        }
      />
      <Tile
        icon={<Target className="h-4 w-4" />}
        label="Runs today"
        value={String(stats.runsToday)}
        sub={stats.runsToday === 0 ? 'no runs yet' : `last: ${stats.lastRunRelative}`}
      />
      <Tile
        icon={<DollarSign className="h-4 w-4" />}
        label="$ today"
        value={formatCost(stats.costToday)}
        sub={`${stats.costToday > 0 ? 'spent today' : 'no spend yet'}`}
      />
      <Tile
        icon={<TrendingUp className="h-4 w-4" />}
        label="Success rate 7d"
        value={
          stats.totalLastWeek === 0
            ? '—'
            : `${Math.round((stats.successLastWeek / stats.totalLastWeek) * 100)}%`
        }
        sub={
          stats.totalLastWeek === 0
            ? 'no runs in last 7d'
            : `${stats.successLastWeek}/${stats.totalLastWeek} successful`
        }
        tone={
          stats.totalLastWeek === 0
            ? 'muted'
            : stats.successLastWeek / stats.totalLastWeek >= 0.9
              ? 'success'
              : stats.successLastWeek / stats.totalLastWeek >= 0.5
                ? 'warn'
                : 'danger'
        }
      />
    </div>
  )
}

interface TileProps {
  icon: React.ReactNode
  label: string
  value: string
  sub?: string
  tone?: 'primary' | 'success' | 'warn' | 'danger' | 'muted'
}

function Tile({ icon, label, value, sub, tone = 'muted' }: TileProps) {
  const toneClass =
    tone === 'primary'
      ? 'text-primary'
      : tone === 'success'
        ? 'text-emerald-400'
        : tone === 'warn'
          ? 'text-amber-400'
          : tone === 'danger'
            ? 'text-destructive'
            : 'text-foreground'
  return (
    <div className="flex flex-col gap-1 bg-card/60 p-4">
      <div className="flex items-center gap-1.5 text-[10px] uppercase tracking-wider text-muted-foreground/70">
        <span className="text-muted-foreground/60">{icon}</span>
        {label}
      </div>
      <div className={`truncate font-mono text-2xl font-bold leading-none tabular-nums md:text-3xl ${toneClass}`}>{value}</div>
      {sub && <div className="truncate text-[11px] text-muted-foreground/70">{sub}</div>}
    </div>
  )
}

interface Stats {
  runsToday: number
  costToday: number
  successLastWeek: number
  totalLastWeek: number
  lastRunRelative: string
}

function computeStats(runs: WorkerRun[]): Stats {
  const dayStart = startOfLocalDay().getTime()
  const weekStart = Date.now() - 7 * 24 * 60 * 60 * 1000
  let runsToday = 0
  let costToday = 0
  let successLastWeek = 0
  let totalLastWeek = 0
  let lastT = 0
  for (const r of runs) {
    const t = new Date(r.started_at).getTime()
    if (Number.isNaN(t)) continue
    if (t > lastT) lastT = t
    if (t >= dayStart) {
      runsToday++
      costToday += r.cost_usd || 0
    }
    if (t >= weekStart) {
      if (
        r.status === 'success' ||
        r.status === 'failure' ||
        r.status === 'cap_exceeded' ||
        r.status === 'rejected'
      ) {
        totalLastWeek++
        if (r.status === 'success') successLastWeek++
      }
    }
  }
  return {
    runsToday,
    costToday,
    successLastWeek,
    totalLastWeek,
    lastRunRelative: lastT > 0 ? relativeTimeShort(Date.now() - lastT) : '—',
  }
}

function lastRunFinishedAt(runs: WorkerRun[]): string | undefined {
  // Use the most recent finished_at (or started_at when still running)
  // as the anchor for the next-run countdown.
  let best: string | undefined
  for (const r of runs) {
    const t = r.finished_at ?? r.started_at
    if (!t) continue
    if (!best || new Date(t).getTime() > new Date(best).getTime()) best = t
  }
  return best
}

// Cost formatter: $X.XX for >= $0.01, <$0.01 for sub-cent, $0 for nothing.
function formatCost(c: number): string {
  if (c <= 0) return '$0'
  if (c < 0.01) return '<$0.01'
  if (c < 100) return `$${c.toFixed(2)}`
  return `$${Math.round(c)}`
}

function relativeTimeShort(diffMs: number): string {
  const s = Math.floor(diffMs / 1000)
  if (s < 60) return `${s}s ago`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m ago`
  const h = Math.floor(m / 60)
  if (h < 24) return `${h}h ago`
  const d = Math.floor(h / 24)
  return `${d}d ago`
}
