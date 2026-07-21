// RecentActivityGrid — slim columns of recent work the operator
// might want to drill into: tasks (live work tracker), worker runs,
// memories (what was just learned), mesh signals.
//
// The Tasks + Memories columns fetch their own dashboard-tile-shaped
// payloads from /api/v1/dashboard/activity/{tasks,memories}. The
// other two read from data the parent has already fetched.
//
// All rows are one-line links into their deep surface — operator
// clicks once and lands on the detail view. Aim is Linear-inbox feel:
// fast scan, action-first, no chrome bloat.

import { useCallback, useEffect, useRef, useState } from 'react'
import { Link } from 'react-router-dom'
import { Bot, Brain, ChevronDown, ChevronRight, ListChecks, Radio } from 'lucide-react'
import { cn } from '@/lib/utils'
import { useApi } from '@/hooks/use-api'
import { useInterval } from '@/hooks/use-interval'
import { useTasksStream } from '@/hooks/use-tasks-stream'
import { useSignal } from '@/components/notifications/use-signal'
import type { WorkerSummary } from '@/api/workers'
import type { StoredNotification } from '@/api/notifications'
import {
  listRecentMemoryActivity,
  type MemoryActivityRow,
} from '@/api/memory'
import { listRecentTasks, type TaskActivityRow } from '@/api/tasks'

const REFRESH_MS = 15_000

interface Props {
  workers: WorkerSummary[]
  signals: StoredNotification[]
}

export function RecentActivityGrid({ workers, signals }: Props) {
  return (
    <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-4">
      <RecentTasks />
      <WorkerRunsColumn workers={workers} />
      <RecentMemories />
      <MeshSignalsColumn signals={signals} />
    </div>
  )
}

// --- shared shell ---------------------------------------------------------

function Column({
  icon,
  title,
  href,
  children,
  testid,
}: {
  icon: React.ReactNode
  title: string
  href: string
  children: React.ReactNode
  testid: string
}) {
  return (
    <section data-testid={testid} className="border border-border bg-card/40">
      <header className="flex items-center justify-between border-b border-border/60 px-3 py-2">
        <Link
          to={href}
          className="group inline-flex items-center gap-2 text-[11px] font-semibold uppercase tracking-[0.14em] text-muted-foreground transition-colors hover:text-foreground"
        >
          <span className="text-muted-foreground/70 group-hover:text-foreground">{icon}</span>
          {title}
          <span className="text-muted-foreground/40 transition-colors group-hover:text-primary">›</span>
        </Link>
      </header>
      <ul className="divide-y divide-border/30">{children}</ul>
    </section>
  )
}

function EmptyRow({ message }: { message: string }) {
  return <li className="px-3 py-2.5 text-[11.5px] text-muted-foreground/70">{message}</li>
}

function ErrorRow({ message }: { message: string }) {
  return (
    <li className="px-3 py-2.5 text-[11.5px] text-red-400/80">
      <span className="font-mono">×</span> {message}
    </li>
  )
}

function LoadingRow() {
  return (
    <li className="px-3 py-2.5 text-[11.5px] text-muted-foreground/40">Loading…</li>
  )
}

// --- recent tasks --------------------------------------------------------

function RecentTasks() {
  const fetcher = useCallback(() => listRecentTasks(6), [])
  const { data, loading, error, refetch } = useApi(fetcher)
  // Slow background poll as a safety net — primary refresh comes from
  // the TASK_EVENT SSE subscription below.
  useInterval(refetch, REFRESH_MS)
  // Live refresh on any task event across any workspace. Omitting
  // workspaceId opens the multiplexed cross-workspace stream so the
  // dashboard sees activity from every workspace at once. Refetching
  // the aggregated payload is cheaper than maintaining the merged list
  // client-side (cross-workspace ordering + status_history changes).
  useTasksStream({ onEvent: refetch })

  const rows = data?.tasks ?? []
  return (
    <Column
      icon={<ListChecks className="h-3.5 w-3.5" />}
      title="Recent tasks"
      href="/tasks"
      testid="dash-recent-tasks"
    >
      {loading && rows.length === 0 ? (
        <LoadingRow />
      ) : error ? (
        <ErrorRow message="Failed to load. Will retry." />
      ) : rows.length === 0 ? (
        <EmptyRow message="No tasks yet. Create one in /tasks to track work." />
      ) : (
        rows.map((t) => <TaskActivityRowItem key={t.id} task={t} />)
      )}
    </Column>
  )
}

function TaskActivityRowItem({ task }: { task: TaskActivityRow }) {
  const statusClass = taskStatusClass(task.status)
  const eventLabel = formatTaskEvent(task.last_event, task.status)
  return (
    <li>
      <Link
        to={`/tasks/${encodeURIComponent(task.id)}?workspace=${encodeURIComponent(task.workspace_id)}`}
        className="group flex items-center gap-2 px-3 py-2 hover:bg-accent/15"
      >
        <span className={cn('h-1.5 w-1.5 shrink-0 rounded-full', statusClass.dot)} />
        <span className="min-w-0 flex-1 truncate text-[12px] text-foreground group-hover:text-primary">
          {task.title}
        </span>
        {eventLabel && (
          <span className={cn('hidden shrink-0 font-mono text-[10px] uppercase tracking-wider lg:inline', statusClass.text)}>
            {eventLabel}
          </span>
        )}
        <span className="shrink-0 font-mono text-[10px] tabular-nums text-muted-foreground/60">
          {formatRelative(task.updated_at)}
        </span>
      </Link>
    </li>
  )
}

function taskStatusClass(status: string): { dot: string; text: string } {
  const s = status.toLowerCase()
  if (s === 'doing' || s === 'in_progress' || s === 'in-progress' || s === 'working')
    return { dot: 'bg-emerald-400 animate-pulse-slow', text: 'text-emerald-400' }
  if (s === 'review' || s === 'in_review')
    return { dot: 'bg-amber-400/80', text: 'text-amber-300' }
  if (s === 'blocked' || s === 'stuck')
    return { dot: 'bg-red-500', text: 'text-red-300' }
  if (s === 'done' || s === 'closed' || s === 'completed')
    return { dot: 'bg-emerald-500/40', text: 'text-muted-foreground' }
  if (s === 'cancelled' || s === 'canceled' || s === 'wontfix' || s === 'wont_fix')
    return { dot: 'bg-muted-foreground/30', text: 'text-muted-foreground/70' }
  if (s === 'open' || s === 'todo' || s === 'backlog')
    return { dot: 'bg-sky-400/80', text: 'text-sky-300' }
  return { dot: 'bg-muted-foreground/40', text: 'text-muted-foreground/70' }
}

// formatTaskEvent normalises the last_event into a short verb so the
// tile reads "task X — claimed 3m" instead of "task X — assigned 3m"
// (the raw status_history evt). Falls back to the status itself when
// the event is missing or generic.
function formatTaskEvent(event: string, status: string): string {
  switch (event) {
    case 'status_changed':
      return status
    case 'created':
      return 'new'
    case 'assigned':
      return 'assigned'
    case 'unassigned':
      return 'unassigned'
    case 'closed':
      return 'closed'
    case 'reopened':
      return 'reopened'
    case 'work_context_updated':
      return 'context'
    case 'lease_expired':
      return 'idle'
    case 'composed':
      return 'composed'
    case 'decomposed':
      return 'decomposed'
    case '':
      return status
    default:
      return event
  }
}

// --- worker runs ---------------------------------------------------------

function WorkerRunsColumn({ workers }: { workers: WorkerSummary[] }) {
  const rows = [...workers]
    .filter((w) => w.last_run_at)
    .sort((a, b) => new Date(b.last_run_at ?? 0).getTime() - new Date(a.last_run_at ?? 0).getTime())
    .slice(0, 5)
  return (
    <Column
      icon={<Bot className="h-3.5 w-3.5" />}
      title="Recent worker runs"
      href="/workers"
      testid="dash-recent-workers"
    >
      {rows.length === 0 ? (
        <EmptyRow message="No worker runs yet. Schedule one or run-now from /workers." />
      ) : (
        rows.map((w) => <WorkerRunRow key={w.id} worker={w} />)
      )}
    </Column>
  )
}

function WorkerRunRow({ worker }: { worker: WorkerSummary }) {
  const status = worker.last_run_status || 'idle'
  const statusClass = workerStatusClass(status)
  return (
    <li>
      <Link
        to={`/workers/${worker.id}`}
        className="group flex items-center gap-2 px-3 py-2 hover:bg-accent/15"
      >
        <span className={cn('h-1.5 w-1.5 shrink-0 rounded-full', statusClass.dot)} />
        <span className="min-w-0 flex-1 truncate text-[12px] text-foreground group-hover:text-primary">
          {worker.name}
        </span>
        <span className={cn('shrink-0 font-mono text-[10px] uppercase tracking-wider', statusClass.text)}>
          {status}
        </span>
        <span className="shrink-0 font-mono text-[10px] tabular-nums text-muted-foreground/60">
          {worker.last_run_at ? formatRelative(worker.last_run_at) : '—'}
        </span>
      </Link>
    </li>
  )
}

function workerStatusClass(status: string): { dot: string; text: string } {
  if (status === 'running') return { dot: 'bg-emerald-400 animate-pulse-slow', text: 'text-emerald-400' }
  if (status === 'success') return { dot: 'bg-emerald-500/70', text: 'text-muted-foreground' }
  if (status === 'failure' || status === 'cap_exceeded' || status === 'rejected')
    return { dot: 'bg-red-500', text: 'text-red-300' }
  if (status === 'awaiting_approval') return { dot: 'bg-amber-500', text: 'text-amber-300' }
  if (status === 'paused') return { dot: 'bg-muted-foreground/40', text: 'text-muted-foreground' }
  return { dot: 'bg-muted-foreground/40', text: 'text-muted-foreground/70' }
}

// --- memories (re-skinned: "what was just learned") ---------------------

function RecentMemories() {
  const fetcher = useCallback(() => listRecentMemoryActivity(6), [])
  const { data, loading, error, refetch } = useApi(fetcher)
  // Slow background poll as a safety net — primary refresh comes from
  // the MEMORY signal stream below.
  useInterval(refetch, REFRESH_MS)
  // Memory events flow through the shared signal/notification bus
  // (memory.written / memory.invalidated / memory.consolidated). When a
  // memory-shaped signal lands, refetch the aggregated tile data.
  const { events } = useSignal()
  const latestMemoryEventID = events.find(
    (e) => e.source === 'memory' || (e.kind || '').startsWith('memory'),
  )?.id
  const latestMemoryEvent = latestMemoryEventID != null ? String(latestMemoryEventID) : undefined
  // Re-fetch whenever the newest memory event id changes — covers writes,
  // invalidations (consolidator runs), and forget operations.
  useMemoryEventRefresh(latestMemoryEvent, refetch)

  const rows = data?.memories ?? []
  return (
    <Column
      icon={<Brain className="h-3.5 w-3.5" />}
      title="Just learned"
      href="/memory/all"
      testid="dash-recent-memories"
    >
      {loading && rows.length === 0 ? (
        <LoadingRow />
      ) : error ? (
        <ErrorRow message="Failed to load. Will retry." />
      ) : rows.length === 0 ? (
        <EmptyRow message="No memories yet. Open /memory to learn how to fill this in." />
      ) : (
        rows.map((m) => <MemoryActivityRowItem key={m.id} memory={m} />)
      )}
    </Column>
  )
}

function MemoryActivityRowItem({ memory }: { memory: MemoryActivityRow }) {
  const [expanded, setExpanded] = useState(false)
  const expandable = memory.body.length > memory.summary.length
  const dotClass = memory.kind === 'fact' ? 'bg-sky-400/80' : 'bg-violet-400/80'
  const onToggle = (e: React.MouseEvent | React.KeyboardEvent) => {
    e.preventDefault()
    e.stopPropagation()
    setExpanded((v) => !v)
  }
  return (
    <li className="group">
      <div className="px-3 py-2 hover:bg-accent/15">
        <div className="flex items-center gap-2">
          <span className={cn('h-1.5 w-1.5 shrink-0 rounded-full', dotClass)} />
          <Link
            to={`/memory/all?selected=${memory.id}`}
            className="min-w-0 flex-1 truncate text-[12px] text-foreground group-hover:text-primary"
            title={memory.name}
          >
            {memory.summary || memory.name}
          </Link>
          {expandable && (
            <button
              type="button"
              aria-label={expanded ? 'Collapse memory' : 'Expand memory'}
              aria-expanded={expanded}
              onClick={onToggle}
              className="shrink-0 text-muted-foreground/60 transition-colors hover:text-foreground"
            >
              {expanded ? (
                <ChevronDown className="h-3.5 w-3.5" />
              ) : (
                <ChevronRight className="h-3.5 w-3.5" />
              )}
            </button>
          )}
          <span className="shrink-0 font-mono text-[10px] tabular-nums text-muted-foreground/60">
            {formatRelative(memory.created_at)}
          </span>
        </div>
        <div className="mt-0.5 flex items-center gap-2 pl-3.5 text-[10.5px] text-muted-foreground/70">
          <span className="truncate">{memory.agent_display || memory.source_kind}</span>
          <span className="text-muted-foreground/30">·</span>
          <span className="truncate">{memory.scope_label}</span>
        </div>
        {expanded && (
          <pre className="mt-2 ml-3.5 max-h-48 overflow-y-auto whitespace-pre-wrap break-words rounded border border-border/40 bg-background/40 px-2 py-1.5 text-[11px] text-muted-foreground">
            {memory.body}
          </pre>
        )}
      </div>
    </li>
  )
}

// --- mesh signals -------------------------------------------------------

function MeshSignalsColumn({ signals }: { signals: StoredNotification[] }) {
  const rows = signals
    .filter((s) => s.source === 'mesh' || (s.kind || '').includes('mesh'))
    .slice(0, 5)
  return (
    <Column
      icon={<Radio className="h-3.5 w-3.5" />}
      title="Recent mesh signals"
      href="/mesh"
      testid="dash-recent-mesh"
    >
      {rows.length === 0 ? (
        <EmptyRow message="No mesh chatter yet. Connect a peer in /pairing to start." />
      ) : (
        rows.map((s) => <MeshSignalRow key={`${s.id}-${s.message_id}`} event={s} />)
      )}
    </Column>
  )
}

function MeshSignalRow({ event }: { event: StoredNotification }) {
  return (
    <li>
      <Link
        to={`/mesh?msg=${event.message_id}`}
        className="group flex items-center gap-2 px-3 py-2 hover:bg-accent/15"
      >
        <span
          className={cn(
            'h-1.5 w-1.5 shrink-0 rounded-full',
            event.priority === 'critical'
              ? 'bg-red-400'
              : event.priority === 'high'
                ? 'bg-orange-400'
                : 'bg-emerald-400/80',
          )}
        />
        <span className="min-w-0 flex-1 truncate text-[12px] text-foreground group-hover:text-primary">
          {event.title}
        </span>
        {event.agent_name && (
          <span className="hidden shrink-0 truncate text-[10.5px] text-muted-foreground/70 lg:inline">
            {event.agent_name}
          </span>
        )}
        <span className="shrink-0 font-mono text-[10px] tabular-nums text-muted-foreground/60">
          {formatRelative(event.created_at)}
        </span>
      </Link>
    </li>
  )
}

// --- helpers -------------------------------------------------------------

// useMemoryEventRefresh fires onChange exactly once per new memory event
// id. Skips the initial render so a freshly-mounted tile doesn't double-
// fetch alongside its initial useApi call.
function useMemoryEventRefresh(eventID: string | undefined, onChange: () => void) {
  const lastSeen = useRef<string | undefined>(eventID)
  useEffect(() => {
    if (!eventID) return
    if (lastSeen.current === eventID) return
    lastSeen.current = eventID
    onChange()
  }, [eventID, onChange])
}

function formatRelative(iso: string): string {
  const ms = Date.now() - new Date(iso).getTime()
  if (ms < 60_000) return 'now'
  const mins = Math.floor(ms / 60_000)
  if (mins < 60) return `${mins}m`
  const hours = Math.floor(mins / 60)
  if (hours < 24) return `${hours}h`
  const days = Math.floor(hours / 24)
  return `${days}d`
}
