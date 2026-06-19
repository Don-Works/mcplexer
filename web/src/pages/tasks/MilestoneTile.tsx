// MilestoneTile — one monochrome panel in the milestones scroll-row.
// Header: title (links to /tasks/<id>), due-date relative, N of M chip.
// Body: a sparkline of children-closed-over-time + dotted linear-ideal.
// Clicking the body navigates to the filtered task list focused on
// the milestone, so the user lands on the children grouping with the
// epic at the top.
//
// Visual identity: zero-radius dark panel. One accent line — electric
// cyan — for the actual burndown. Ideal line is dotted in low-contrast
// foreground. No gradients, no card shadows; we lean into the
// dashboard's monochrome plotter aesthetic.

import { useMemo } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { Line, LineChart, ReferenceLine, ResponsiveContainer, Tooltip, XAxis, YAxis } from 'recharts'

import { Badge } from '@/components/ui/badge'
import { cn } from '@/lib/utils'
import type { MilestoneBurndown } from '@/api/tasks'

import { formatAbsolute, formatRelative } from './task-utils'

interface MilestoneTileProps {
  milestone: MilestoneBurndown
}

export function MilestoneTile({ milestone }: MilestoneTileProps) {
  const navigate = useNavigate()
  const m = milestone
  const dueIso = m.task.due_at ?? undefined
  const dueLabel = formatRelative(dueIso)
  const dueAbsolute = formatAbsolute(dueIso)

  const series = useMemo(() => buildSeries(m), [m])
  const total = m.total_children
  const closed = m.closed_children
  const overdue = m.days_remaining < 0
  const complete = total > 0 && closed >= total

  const taskHref = `/tasks/${encodeURIComponent(m.task.id)}?workspace=${encodeURIComponent(m.task.workspace_id)}`
  const filteredHref = `/tasks?focus=${encodeURIComponent(m.task.id)}&workspace=${encodeURIComponent(m.task.workspace_id)}`

  return (
    <article
      className={cn(
        'flex h-44 w-72 shrink-0 flex-col border bg-card/40',
        complete
          ? 'border-border/60'
          : overdue
            ? 'border-red-500/40'
            : 'border-border',
      )}
    >
      <header className="flex items-start justify-between gap-2 border-b border-border px-3 py-2">
        <div className="min-w-0 flex-1">
          <Link
            to={taskHref}
            className="block truncate text-sm font-semibold hover:underline"
            title={m.task.title}
          >
            {m.task.title}
          </Link>
          <div className="mt-0.5 flex items-center gap-1.5 text-[10px] text-muted-foreground">
            <span title={`due ${dueAbsolute}`}
              className={cn(
                'font-mono',
                overdue && !complete ? 'text-red-400' : '',
              )}
            >
              {overdue && !complete ? 'overdue · ' : 'due · '}
              {dueLabel}
            </span>
            <span>·</span>
            <span className="font-mono">
              {m.days_remaining === 0
                ? 'today'
                : `${Math.abs(m.days_remaining)}d ${m.days_remaining < 0 ? 'past' : 'left'}`}
            </span>
          </div>
        </div>
        <Badge variant="outline" tone={complete ? 'success' : overdue ? 'critical' : 'info'} className="font-mono text-[10px]">
          {closed} / {total}
        </Badge>
      </header>
      <button
        onClick={() => navigate(filteredHref)}
        className="group relative h-full w-full select-none focus:outline-none focus-visible:bg-muted/30"
        aria-label={`View children of ${m.task.title}`}
      >
        {series.length > 0 ? (
          <ResponsiveContainer width="100%" height="100%">
            <LineChart data={series} margin={{ top: 12, right: 10, bottom: 6, left: 10 }}>
              <XAxis dataKey="date" hide />
              <YAxis hide domain={[0, total]} />
              <ReferenceLine y={0} stroke="currentColor" strokeOpacity={0.15} />
              <Line
                type="linear"
                dataKey="ideal"
                stroke="currentColor"
                strokeOpacity={0.4}
                strokeDasharray="3 3"
                strokeWidth={1}
                dot={false}
                isAnimationActive={false}
              />
              <Line
                type="monotone"
                dataKey="open"
                stroke="#22d3ee"
                strokeWidth={1.8}
                dot={false}
                isAnimationActive={false}
              />
              <Tooltip
                cursor={{ stroke: 'currentColor', strokeOpacity: 0.1 }}
                contentStyle={{
                  background: 'rgba(0,0,0,0.85)',
                  border: '1px solid rgba(255,255,255,0.08)',
                  borderRadius: 0,
                  fontSize: 10,
                  padding: '4px 6px',
                }}
                formatter={(value, name) => [String(value), name === 'open' ? 'open' : 'ideal']}
                labelFormatter={(label) => String(label)}
              />
            </LineChart>
          </ResponsiveContainer>
        ) : (
          <div className="flex h-full w-full items-center justify-center text-[10px] text-muted-foreground/70">
            no children yet
          </div>
        )}
      </button>
    </article>
  )
}

interface SeriesPoint {
  date: string
  open: number
  ideal: number
}

// buildSeries derives an open-children-per-day series plus a linear
// ideal that drops from total → 0 across the same date range. When the
// backend returned no points (open-ended milestone or due before
// created) we fall back to an empty series and the tile renders a
// placeholder.
function buildSeries(m: MilestoneBurndown): SeriesPoint[] {
  const pts = m.burndown_points
  if (!pts || pts.length === 0) return []
  const total = m.total_children
  const last = pts.length - 1
  return pts.map((p, i) => ({
    date: p.date,
    open: p.children_open,
    ideal: last === 0 ? total : Math.max(0, total - (total * i) / last),
  }))
}
