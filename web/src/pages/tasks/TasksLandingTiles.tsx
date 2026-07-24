// TasksLandingTiles — presentational tiles + activity helpers used by
// TasksLandingPage. Kept local to the tasks module so we don't reach
// across into pages/memory/* for shared visual primitives; the shapes
// are tuned for task-specific signals (open vs doing vs blocked
// counts, milestone progress, offer badges).

import { Link, useNavigate } from 'react-router-dom'
import { ArrowUpRight, Folder, ListTodo } from 'lucide-react'
import {
  Bar,
  BarChart,
  CartesianGrid,
  Cell,
  Line,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from 'recharts'
import { Card, CardContent } from '@/components/ui/card'
import { EmptyState } from '@/components/ui/empty-state'
import { Badge } from '@/components/ui/badge'
import { Pill } from '@/components/mesh/Pill'
import type { Task, TaskOffer } from '@/api/tasks'
import type { TaskEvent } from '@/hooks/use-tasks-stream'
import { cn } from '@/lib/utils'
import { describeTaskHistoryAction } from './task-activity'
import {
  formatRelative,
  isWorkingStatus,
  priorityVisual,
  shortTaskId,
  statusVisual,
} from './task-utils'

export function VitalsTile({
  icon,
  label,
  value,
  detail,
  href,
  dim,
  accent,
}: {
  icon: React.ReactNode
  label: string
  value: string
  detail: string
  href: string
  dim?: boolean
  accent?: 'awaiting' | 'idle' | 'live'
}) {
  return (
    <Link
      to={href}
      className={cn(
        'group block border border-border bg-card/40 px-4 py-3.5 transition-colors',
        'hover:border-border/80 hover:bg-card focus-visible:border-primary/60 focus-visible:outline-none',
      )}
    >
      <div className="flex items-center justify-between text-[10px] font-semibold uppercase tracking-[0.14em] text-muted-foreground">
        <span className="inline-flex items-center gap-1.5">
          {icon}
          {label}
        </span>
        <ArrowUpRight className="h-3 w-3 opacity-0 transition-opacity group-hover:opacity-100" />
      </div>
      <div
        className={cn(
          'mt-2 font-mono text-3xl font-semibold tabular-nums tracking-tight',
          dim ? 'text-muted-foreground/70' : 'text-foreground',
          accent === 'awaiting' && 'text-amber-300',
          accent === 'live' && 'text-emerald-300',
        )}
      >
        {value}
      </div>
      <div
        className={cn(
          'mt-1 flex items-center gap-1.5 text-[11px]',
          accent === 'awaiting' && 'text-amber-300/80',
          accent === 'live' && 'text-emerald-300/80',
          (!accent || accent === 'idle') && 'text-muted-foreground',
        )}
      >
        {accent === 'awaiting' && (
          <span className="relative flex h-1.5 w-1.5">
            <span className="absolute inline-flex h-full w-full animate-pulse-slow rounded-full bg-amber-400/60" />
            <span className="relative inline-flex h-1.5 w-1.5 rounded-full bg-amber-400" />
          </span>
        )}
        {accent === 'live' && (
          <span className="relative flex h-1.5 w-1.5">
            <span className="absolute inline-flex h-full w-full animate-pulse-slow rounded-full bg-emerald-400/60" />
            <span className="relative inline-flex h-1.5 w-1.5 rounded-full bg-emerald-400" />
          </span>
        )}
        {detail}
      </div>
    </Link>
  )
}

export function QuickLink({
  to,
  title,
  body,
  accent,
}: {
  to: string
  title: string
  body: string
  accent?: boolean
}) {
  return (
    <Link
      to={to}
      className={cn(
        'group flex flex-col gap-1 border border-border bg-card/40 px-4 py-3 transition-colors',
        'hover:border-border/80 hover:bg-card',
        accent && 'border-sky-500/40',
      )}
    >
      <div className="flex items-center justify-between">
        <span
          className={cn(
            'text-[13px] font-semibold',
            accent ? 'text-sky-300' : 'text-foreground',
          )}
        >
          {title}
        </span>
        <ArrowUpRight className="h-3.5 w-3.5 text-muted-foreground/50 transition-colors group-hover:text-foreground" />
      </div>
      <p className="text-[11.5px] leading-relaxed text-muted-foreground">{body}</p>
    </Link>
  )
}

// ActivityCard — the live tasks event log. Hydrated from localStorage on
// mount so the operator doesn't get a blank page on refresh, then
// appended to in real time as the SSE stream delivers events. Rows are
// click-through to whatever surface owns that event (task detail, offers
// tray) and pass state.pulse=true so the destination can briefly
// highlight the target.
//
// liveCount tells the row renderer how many entries at the top arrived
// in *this* session — those get the audit-in entrance animation; the
// rest (hydrated from storage) render statically.
export function ActivityCard({
  events,
  workspaceNameByID,
  liveCount = 0,
}: {
  events: TaskEvent[]
  workspaceNameByID: Record<string, string>
  liveCount?: number
}) {
  return (
    <Card className="overflow-hidden">
      <CardContent className="space-y-3 p-4">
        <div className="flex items-center justify-between">
          <h2 className="text-[13px] font-semibold uppercase tracking-[0.12em] text-muted-foreground">
            Live activity
          </h2>
          {events.length > 0 ? (
            <span className="text-[10px] uppercase tracking-wider text-muted-foreground/60">
              {events.length} event{events.length === 1 ? '' : 's'}
            </span>
          ) : null}
        </div>
        {events.length === 0 ? (
          <EmptyState
            icon={<ListTodo className="h-7 w-7" />}
            title="No task events yet"
            description="When an agent creates, claims, or closes a task, or a paired peer offers you one, you'll see it here in real-time."
            density="card"
            testid="tasks-activity-empty"
          />
        ) : (
          <ul className="divide-y divide-border/30 border border-border/40 bg-background/40">
            {events.map((e, i) => (
              <ActivityRow
                key={activityRowKey(e, i)}
                event={e}
                workspaceName={
                  e.workspace_id ? workspaceNameByID[e.workspace_id] : undefined
                }
                isLive={i < liveCount}
              />
            ))}
          </ul>
        )}
      </CardContent>
    </Card>
  )
}

function activityRowKey(e: TaskEvent, i: number): string {
  return `${e.at}:${e.kind}:${e.task?.id ?? e.offer?.id ?? e.note?.id ?? i}`
}

// describeAction — one place that maps a raw bus event onto the
// verb-first label, tone, and destination shown in the row. Keeping
// this centralized makes the activity card the single source of truth
// for "how do we name what just happened in tasks".
type ActionTone = 'info' | 'warn' | 'success' | 'critical' | 'muted'

interface Action {
  verb: string
  tone: ActionTone
  title: string
  status?: string
  taskId?: string
  workspaceLabel?: string
  href?: string
  // pulseState — passed to the destination via Link.state. The detail /
  // offers page reads it to flash the target on arrival.
  pulseState?: Record<string, unknown>
}

const ACTION_DOT: Record<ActionTone, string> = {
  info: 'bg-sky-400/85',
  warn: 'bg-amber-400/85',
  success: 'bg-emerald-400/85',
  critical: 'bg-red-400/80',
  muted: 'bg-muted-foreground/45',
}

const ACTION_TEXT: Record<ActionTone, string> = {
  info: 'text-sky-300/90',
  warn: 'text-amber-300/90',
  success: 'text-emerald-300/90',
  critical: 'text-red-300/90',
  muted: 'text-muted-foreground/80',
}

function describeAction(evt: TaskEvent, workspaceName?: string): Action {
  const t = evt.task
  const o = evt.offer
  const wsLabel =
    workspaceName ||
    o?.remote_workspace_name ||
    (o?.workspace_id ? undefined : undefined)

  // Offer events route to the tray and carry the offer id as pulse
  // target so the offers page can scroll + flash that card.
  if (evt.kind === 'task_offer_updated' && o) {
    const offerAction = describeOffer(o)
    return {
      ...offerAction,
      title: o.title || '(untitled offer)',
      taskId: o.task_id || o.remote_task_id,
      workspaceLabel: wsLabel,
      href: '/tasks/offers',
      pulseState: { pulseOfferId: o.id },
    }
  }

  // Standard task events. closed_at on a task_updated means the
  // mutation flipped the task closed — surface that explicitly because
  // "closed" is what the operator cares about, not "updated".
  let verb = 'updated'
  let tone: ActionTone = 'muted'
  switch (evt.kind) {
    case 'task_created':
      verb = 'created'
      tone = 'info'
      break
    case 'task_claimed':
      verb = 'claimed'
      tone = 'success'
      break
    case 'task_deleted':
      verb = 'deleted'
      tone = 'critical'
      break
    case 'task_note_appended':
      verb = 'noted'
      tone = 'muted'
      break
    case 'task_updated':
      {
        const historyAction = evt.history ? describeTaskHistoryAction(evt.history, t) : null
        if (historyAction) {
          verb = historyAction.verb
          tone = historyAction.tone
          break
        }
      }
      if (t?.closed_at) {
        verb = 'closed'
        tone = 'success'
      } else {
        verb = 'updated'
        tone = 'muted'
      }
      break
  }

  return {
    verb,
    tone,
    title: t?.title || '(unknown task)',
    status: t?.status,
    taskId: t?.id,
    workspaceLabel: wsLabel,
    href:
      t && t.workspace_id && !t.deleted_at
        ? `/tasks/${encodeURIComponent(t.id)}?workspace=${encodeURIComponent(t.workspace_id)}`
        : undefined,
    pulseState: { pulse: true },
  }
}

function describeOffer(o: TaskOffer): { verb: string; tone: ActionTone } {
  const incoming = o.direction === 'incoming'
  switch (o.state) {
    case 'pending':
      return incoming
        ? { verb: 'offered to you', tone: 'warn' }
        : { verb: 'offer sent', tone: 'info' }
    case 'accepted':
    case 'auto_accepted':
      return { verb: 'offer accepted', tone: 'success' }
    case 'declined':
      return { verb: 'offer declined', tone: 'muted' }
    case 'expired':
      return { verb: 'offer expired', tone: 'muted' }
    case 'rejected_unscoped':
      return { verb: 'offer rejected (scope)', tone: 'critical' }
    case 'rejected_throttle':
      return { verb: 'offer rejected (throttle)', tone: 'critical' }
    default:
      return { verb: o.state.replace(/_/g, ' '), tone: 'muted' }
  }
}

function ActivityRow({
  event,
  workspaceName,
  isLive,
}: {
  event: TaskEvent
  workspaceName?: string
  isLive: boolean
}) {
  const action = describeAction(event, workspaceName)
  const rowClass = cn(
    'flex items-start gap-3 px-3 py-2.5 transition-colors',
    action.href ? 'hover:bg-muted/30' : '',
    isLive ? 'animate-[audit-in_0.45s_ease-out]' : '',
  )

  const dot = (
    <span
      className={cn('mt-[7px] inline-flex h-1.5 w-1.5 shrink-0', ACTION_DOT[action.tone])}
      aria-hidden
    />
  )

  const main = (
    <div className="min-w-0 flex-1">
      <div className="flex min-w-0 items-baseline gap-2">
        <span className="truncate text-[13px] font-medium text-foreground">
          {action.title}
        </span>
      </div>
      <div className="mt-1 flex min-w-0 flex-wrap items-center gap-1.5 text-[10.5px] text-muted-foreground/80">
        <span className={cn('font-mono uppercase tracking-wider', ACTION_TEXT[action.tone])}>
          {action.verb}
        </span>
        {action.workspaceLabel ? (
          <>
            <span className="text-muted-foreground/40">·</span>
            <Pill
              icon={Folder}
              label={action.workspaceLabel}
              tone="workspace"
              maxLabelCh={18}
            />
          </>
        ) : null}
        {action.status ? (
          <>
            <span className="text-muted-foreground/40">·</span>
            <span className="font-mono lowercase text-muted-foreground/85">
              {action.status}
            </span>
          </>
        ) : null}
        {action.taskId ? (
          <>
            <span className="text-muted-foreground/40">·</span>
            <span className="font-mono text-muted-foreground/60">
              {shortTaskId(action.taskId)}
            </span>
          </>
        ) : null}
      </div>
    </div>
  )

  const ts = (
    <span className="shrink-0 self-start pt-[2px] font-mono text-[10px] tabular-nums text-muted-foreground/60">
      {formatRelative(event.at)}
    </span>
  )

  if (action.href) {
    return (
      <li>
        <Link to={action.href} state={action.pulseState} className={rowClass}>
          {dot}
          {main}
          {ts}
        </Link>
      </li>
    )
  }
  return (
    <li className={rowClass}>
      {dot}
      {main}
      {ts}
    </li>
  )
}

// WorkspaceBreakdown — per-workspace open + doing + blocked counts.
// Click-through filters the list page to that workspace. Renders only
// workspaces that currently hold at least one task; an empty install
// just shows the empty hint above.
export function WorkspaceBreakdown({
  tasks,
  workspaceNameByID,
}: {
  tasks: Task[]
  workspaceNameByID: Record<string, string>
}) {
  const rows = aggregateByWorkspace(tasks, workspaceNameByID)
  if (rows.length === 0) return null
  return (
    <Card className="overflow-hidden">
      <CardContent className="space-y-3 p-4">
        <h2 className="text-[13px] font-semibold uppercase tracking-[0.12em] text-muted-foreground">
          By workspace
        </h2>
        <ul className="divide-y divide-border/40 border border-border/40 bg-background/40">
          {rows.map((r) => (
            <li key={r.id}>
              <Link
                to={`/tasks/all?workspace=${encodeURIComponent(r.id)}`}
                className="flex items-center justify-between gap-3 px-3 py-2.5 transition-colors hover:bg-muted/30"
              >
                <span className="truncate font-mono text-[12.5px] text-foreground">
                  {r.name}
                </span>
                <span className="flex shrink-0 items-center gap-2">
                  <Counter label="open" value={r.open} />
                  {r.doing > 0 && (
                    <Counter label="doing" value={r.doing} accent="live" />
                  )}
                  {r.blocked > 0 && (
                    <Counter label="blocked" value={r.blocked} accent="warn" />
                  )}
                </span>
              </Link>
            </li>
          ))}
        </ul>
      </CardContent>
    </Card>
  )
}

function Counter({
  label,
  value,
  accent,
}: {
  label: string
  value: number
  accent?: 'live' | 'warn'
}) {
  return (
    <span
      className={cn(
        'inline-flex items-center gap-1 font-mono text-[11px] tabular-nums',
        accent === 'live' && 'text-emerald-300',
        accent === 'warn' && 'text-amber-300',
        !accent && 'text-muted-foreground',
      )}
    >
      <span className="font-semibold">{value}</span>
      <span className="text-[9px] uppercase tracking-wider opacity-70">{label}</span>
    </span>
  )
}

interface WsRow {
  id: string
  name: string
  open: number
  doing: number
  blocked: number
}

function aggregateByWorkspace(
  tasks: Task[],
  nameByID: Record<string, string>,
): WsRow[] {
  const byId = new Map<string, WsRow>()
  for (const t of tasks) {
    if (t.closed_at) continue
    const id = t.workspace_id
    if (!id) continue
    let row = byId.get(id)
    if (!row) {
      row = {
        id,
        name: nameByID[id] || id.slice(0, 8),
        open: 0,
        doing: 0,
        blocked: 0,
      }
      byId.set(id, row)
    }
    row.open += 1
    const status = (t.status || '').toLowerCase()
    if (status === 'blocked') row.blocked += 1
    else if (isWorkingStatus(t.status)) row.doing += 1
  }
  return Array.from(byId.values()).sort((a, b) => b.open - a.open)
}

// WorkspaceParetoChart — Pareto (sorted bar + cumulative % line) of
// open tasks by workspace. Makes the 80/20 instantly visible: a few
// workspaces hold most of the open tasks. Click-through on bars.
export function WorkspaceParetoChart({
  tasks,
  workspaceNameByID,
}: {
  tasks: Task[]
  workspaceNameByID: Record<string, string>
}) {
  const navigate = useNavigate()
  const rows = aggregateByWorkspace(tasks, workspaceNameByID)
  if (rows.length === 0) return null

  const total = rows.reduce((s, r) => s + r.open, 0)
  let cumulative = 0
  const data = rows.map((r) => {
    cumulative += r.open
    return {
      id: r.id,
      name: r.name.length > 16 ? r.name.slice(0, 15) + '…' : r.name,
      fullName: r.name,
      count: r.open,
      cumulativePct: Math.round((cumulative / total) * 100),
    }
  })

  return (
    <Card className="overflow-hidden">
      <CardContent className="space-y-3 p-4">
        <div className="flex items-center justify-between">
          <h2 className="text-[13px] font-semibold uppercase tracking-[0.12em] text-muted-foreground">
            Tasks by workspace
          </h2>
          <span className="font-mono text-[11px] tabular-nums text-muted-foreground/60">
            {total} open
          </span>
        </div>
        <div className="h-[280px] w-full">
          <ResponsiveContainer width="100%" height="100%">
            <BarChart
              data={data}
              margin={{ top: 8, right: 48, left: 0, bottom: 0 }}
              onClick={(e) => {
                const idx = typeof e?.activeTooltipIndex === 'number' ? e.activeTooltipIndex : undefined
                const item = idx != null ? data[idx] : undefined
                if (item?.id) navigate(`/tasks/all?workspace=${encodeURIComponent(item.id)}`)
              }}
            >
              <CartesianGrid
                strokeDasharray="3 3"
                stroke="hsl(224 12% 18%)"
                vertical={false}
              />
              <XAxis
                dataKey="name"
                tick={{ fontSize: 10, fill: 'hsl(220 10% 55%)', fontFamily: 'JetBrains Mono, monospace' }}
                axisLine={{ stroke: 'hsl(224 12% 18%)' }}
                tickLine={false}
                interval={data.length > 15 ? 'preserveStartEnd' : 0}
                angle={data.length > 10 ? -45 : 0}
                textAnchor={data.length > 10 ? 'end' : 'middle'}
                height={data.length > 10 ? 60 : 30}
              />
              <YAxis
                yAxisId="count"
                tick={{ fontSize: 10, fill: 'hsl(220 10% 55%)', fontFamily: 'JetBrains Mono, monospace' }}
                axisLine={false}
                tickLine={false}
                width={32}
              />
              <YAxis
                yAxisId="pct"
                orientation="right"
                domain={[0, 100]}
                tick={{ fontSize: 10, fill: 'hsl(38 92% 55%)', fontFamily: 'JetBrains Mono, monospace' }}
                axisLine={false}
                tickLine={false}
                width={36}
                tickFormatter={(v: number) => `${v}%`}
              />
              <Tooltip content={<ParetoTooltip />} cursor={{ fill: 'hsl(224 14% 9%)' }} />
              <Bar
                yAxisId="count"
                dataKey="count"
                radius={[2, 2, 0, 0]}
                maxBarSize={40}
                cursor="pointer"
              >
                {data.map((_, i) => (
                  <Cell
                    key={i}
                    fill={i === 0 ? '#22d3ee' : i < 3 ? 'hsl(217 95% 55%)' : 'hsl(217 95% 55% / 0.6)'}
                  />
                ))}
              </Bar>
              <Line
                yAxisId="pct"
                type="monotone"
                dataKey="cumulativePct"
                stroke="#f59e0b"
                strokeWidth={2}
                dot={{ r: 3, fill: '#f59e0b', stroke: '#f59e0b' }}
                activeDot={{ r: 5 }}
              />
            </BarChart>
          </ResponsiveContainer>
        </div>
      </CardContent>
    </Card>
  )
}

function ParetoTooltip({
  active,
  payload,
}: {
  active?: boolean
  payload?: Array<{ payload: { fullName: string; count: number; cumulativePct: number } }>
}) {
  if (!active || !payload?.length) return null
  const d = payload[0].payload
  return (
    <div className="border border-border bg-card px-3 py-2 shadow-lg">
      <p className="text-[12px] font-medium text-foreground">{d.fullName}</p>
      <p className="mt-1 font-mono text-[11px] tabular-nums text-muted-foreground">
        <span className="text-foreground">{d.count}</span> open
        <span className="mx-1.5 text-muted-foreground/40">·</span>
        <span className="text-amber-300">{d.cumulativePct}%</span> cumulative
      </p>
    </div>
  )
}

// PriorityHint — narrow widget for the "next to look at" panel.
// Renders the highest-priority open task with a quick dot + tone.
// Kept here so TasksLandingPage stays under the 300-line guideline.
export function NextUpRow({ task }: { task: Task }) {
  const prio = priorityVisual(task.priority)
  const status = statusVisual(task.status, !!task.closed_at)
  return (
    <Link
      to={`/tasks/${encodeURIComponent(task.id)}?workspace=${encodeURIComponent(task.workspace_id)}`}
      className="flex items-start gap-2.5 px-3 py-2 transition-colors hover:bg-muted/30"
    >
      <span className={cn('mt-1.5 inline-flex h-2 w-2 shrink-0 rounded-full', prio.dot)} />
      <div className="min-w-0 flex-1">
        <p className="truncate text-[12.5px] font-medium text-foreground">{task.title}</p>
        <p className="truncate text-[10.5px] text-muted-foreground/80">
          {task.status} · {shortTaskId(task.id)}
        </p>
      </div>
      <Badge variant="outline" tone={status.tone} className="font-mono text-[9px] uppercase">
        {task.priority}
      </Badge>
    </Link>
  )
}
