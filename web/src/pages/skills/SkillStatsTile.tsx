// SkillStatsTile (W6) — compact reputation card the SkillDetailPane
// renders below the skill body. Pulls /api/v1/skills/{name}/stats with
// a 30-day rolling window.
//
// Empty state: when invocations=0, render a calm "no runs yet" hint
// rather than a row of zeros — the empty state is honest about there
// being no signal.

import { useCallback, type ReactNode } from 'react'
import { Card, CardContent } from '@/components/ui/card'
import { Activity, CheckCircle2, Clock, Wrench } from 'lucide-react'
import { useApi } from '@/hooks/use-api'
import { getSkillStats, type SkillStats } from '@/api/skill-stats'
import { cn } from '@/lib/utils'

interface Props {
  name: string
  embedded?: boolean
}

export function SkillStatsTile({ name, embedded = false }: Props) {
  const fetcher = useCallback(() => getSkillStats(name), [name])
  const { data, loading, error } = useApi(fetcher)

  if (loading && !data) {
    return <StatsShell embedded={embedded}>Loading reputation...</StatsShell>
  }
  if (error) {
    return (
      <StatsShell embedded={embedded} className="text-destructive">
        Failed to load reputation: {error}
      </StatsShell>
    )
  }
  if (!data) return null

  const stats: SkillStats = data.stats
  if (stats.invocations === 0) {
    return (
      <StatsShell embedded={embedded}>
        <span className="flex items-center gap-2">
          <Activity className="h-3 w-3" />
          No runs in the last {stats.window_days} day{stats.window_days === 1 ? '' : 's'}
        </span>
      </StatsShell>
    )
  }

  return (
    <StatsShell embedded={embedded} className="block">
        <div className="mb-2 flex items-center justify-between">
          <span className="font-mono text-[10px] uppercase tracking-wider text-muted-foreground">
            Reputation · {stats.window_days}d
          </span>
          {stats.last_run_at && (
            <span className="font-mono text-[10px] text-muted-foreground/70">
              last run {formatRelative(stats.last_run_at)}
            </span>
          )}
        </div>
        <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
          <Stat
            icon={<Activity className="h-3 w-3" />}
            label="Invocations"
            value={String(stats.invocations)}
          />
          <Stat
            icon={<CheckCircle2 className="h-3 w-3" />}
            label="Success"
            value={formatPct(stats.success_rate)}
            tone={successTone(stats.success_rate)}
          />
          <Stat
            icon={<Clock className="h-3 w-3" />}
            label="p95"
            value={formatDuration(stats.p95_duration_ms)}
          />
          <Stat
            icon={<Wrench className="h-3 w-3" />}
            label="p50"
            value={formatDuration(stats.p50_duration_ms)}
          />
        </div>
        {stats.top_tools_used.length > 0 && (
          <div className="mt-3 flex flex-wrap items-center gap-1.5">
            <span className="font-mono text-[10px] uppercase tracking-wider text-muted-foreground/70">
              top tools
            </span>
            {stats.top_tools_used.slice(0, 5).map((t) => (
              <span
                key={t.name}
                className="border border-border px-1.5 py-0.5 font-mono text-[10px] text-muted-foreground"
                title={`${t.count} call${t.count === 1 ? '' : 's'}`}
              >
                {t.name}
                <span className="ml-1 text-muted-foreground/60">{t.count}</span>
              </span>
            ))}
          </div>
        )}
    </StatsShell>
  )
}

function StatsShell({
  embedded,
  className,
  children,
}: {
  embedded: boolean
  className?: string
  children: ReactNode
}) {
  const contentClass = cn(
    'px-5 py-3 text-xs text-muted-foreground',
    embedded && 'border-t border-border/60 bg-muted/15',
    className,
  )
  if (embedded) {
    return <div className={contentClass}>{children}</div>
  }
  return (
    <Card>
      <CardContent className={contentClass}>{children}</CardContent>
    </Card>
  )
}

interface StatProps {
  icon: React.ReactNode
  label: string
  value: string
  tone?: 'good' | 'warn' | 'bad'
}

function Stat({ icon, label, value, tone }: StatProps) {
  const toneClass =
    tone === 'good'
      ? 'text-emerald-500'
      : tone === 'warn'
        ? 'text-amber-500'
        : tone === 'bad'
          ? 'text-destructive'
          : 'text-foreground'
  return (
    <div className="space-y-1">
      <div className="flex items-center gap-1 font-mono text-[10px] uppercase tracking-wider text-muted-foreground">
        {icon}
        <span>{label}</span>
      </div>
      <div className={`tabular-nums text-base font-semibold ${toneClass}`}>{value}</div>
    </div>
  )
}

function formatPct(rate: number): string {
  if (!Number.isFinite(rate)) return '—'
  return `${Math.round(rate * 100)}%`
}

function formatDuration(ms: number): string {
  if (ms === 0) return '—'
  if (ms < 1000) return `${ms}ms`
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)}s`
  return `${(ms / 60_000).toFixed(1)}m`
}

function successTone(rate: number): 'good' | 'warn' | 'bad' {
  if (rate >= 0.9) return 'good'
  if (rate >= 0.6) return 'warn'
  return 'bad'
}

function formatRelative(iso: string): string {
  const t = new Date(iso).getTime()
  if (!Number.isFinite(t)) return iso
  const diff = Date.now() - t
  const days = Math.floor(diff / 86_400_000)
  if (days > 0) return `${days}d ago`
  const hours = Math.floor(diff / 3_600_000)
  if (hours > 0) return `${hours}h ago`
  const mins = Math.floor(diff / 60_000)
  if (mins > 0) return `${mins}m ago`
  return 'just now'
}
