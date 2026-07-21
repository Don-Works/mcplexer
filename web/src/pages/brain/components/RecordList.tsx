import { useMemo } from 'react'
import { Badge } from '@/components/ui/badge'
import { CopyButton } from '@/components/ui/copy-button'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { cn } from '@/lib/utils'
import type { BrainTaskRecord, BrainMemoryRecord } from '@/api/brainBrowser'
import { TasksEmpty, MemoriesEmpty, NotesEmpty } from './RecordEmptyStates'

export type RecordTab = 'tasks' | 'memory' | 'notes'

interface Props {
  tasks: BrainTaskRecord[]
  memories: BrainMemoryRecord[]
  notes: BrainMemoryRecord[]
  activeTab: RecordTab
  statusFilter: string | null
  selectedId: string | null
  onStatusFilter: (status: string | null) => void
  onSelect: (kind: 'task' | 'memory', id: string) => void
  onNew: () => void
}

const STATUS_FILTERS = ['open', 'doing', 'review', 'done']

// priorityTone maps a task priority onto the Badge tone vocabulary.
const PRIORITY_TONE: Record<string, 'high' | 'warn' | 'info'> = { high: 'high', med: 'warn', low: 'info' }
function priorityTone(p?: string): 'high' | 'warn' | 'info' | 'none' {
  return (p && PRIORITY_TONE[p]) || 'none'
}

// RecordList is the Ledger Console's typed record list (DESIGN §3.1): a
// divide-y flat list (never a card per row), with the open/doing/review/done
// status filter, mono copyable IDs, Badge(tone) priority, mono tag + source
// chips, and inline counts. animate-shimmer marks rows holding a LIVE LEASE
// (an agent touching the record right now); animate-pulse-slow marks rows the
// indexer flagged (your agent cannot see it yet).
export function RecordList({
  tasks,
  memories,
  notes,
  activeTab,
  statusFilter,
  selectedId,
  onStatusFilter,
  onSelect,
  onNew,
}: Props) {
  const filteredTasks = useMemo(
    () => (statusFilter ? tasks.filter((t) => t.status === statusFilter) : tasks),
    [tasks, statusFilter],
  )

  // Inline counts: total open + total doing across the unfiltered task list.
  const openCt = useMemo(() => tasks.filter((t) => t.status === 'open').length, [tasks])
  const doingCt = useMemo(() => tasks.filter((t) => t.status === 'doing').length, [tasks])

  return (
    <div className="flex h-full flex-col">
      {activeTab === 'tasks' && (
        <div className="flex items-center justify-between border-b border-border px-3 py-1.5">
          <StatusFilterBar active={statusFilter} onChange={onStatusFilter} />
          <span className="font-mono text-[11px] text-muted-foreground">
            {openCt} open · {doingCt} doing
          </span>
        </div>
      )}

      <div className="min-h-0 flex-1 overflow-y-auto">
        {activeTab === 'tasks' && (
          <TaskRows tasks={filteredTasks} selectedId={selectedId} onSelect={onSelect} onNew={onNew} />
        )}
        {activeTab === 'memory' && (
          <MemoryRows rows={memories} kindLabel="memories" selectedId={selectedId} onSelect={onSelect} onNew={onNew} />
        )}
        {activeTab === 'notes' && (
          <MemoryRows rows={notes} kindLabel="notes" selectedId={selectedId} onSelect={onSelect} onNew={onNew} />
        )}
      </div>
    </div>
  )
}

function StatusFilterBar({
  active,
  onChange,
}: {
  active: string | null
  onChange: (status: string | null) => void
}) {
  return (
    <div className="flex items-center gap-0.5 font-mono text-[11px]">
      <FilterChip label="all" on={active === null} onClick={() => onChange(null)} />
      {STATUS_FILTERS.map((s, i) => (
        <span key={s} className="flex items-center gap-0.5">
          <span className="text-muted-foreground/40" aria-hidden>
            {i === 0 ? '' : '▸'}
          </span>
          <FilterChip label={s} on={active === s} onClick={() => onChange(s)} />
        </span>
      ))}
    </div>
  )
}

function FilterChip({ label, on, onClick }: { label: string; on: boolean; onClick: () => void }) {
  return (
    <button
      type="button"
      onClick={onClick}
      aria-pressed={on}
      className={cn(
        'px-1.5 py-0.5 transition-colors',
        on ? 'text-primary' : 'text-muted-foreground hover:text-foreground',
      )}
    >
      {label}
    </button>
  )
}

function TaskRows({
  tasks,
  selectedId,
  onSelect,
  onNew,
}: {
  tasks: BrainTaskRecord[]
  selectedId: string | null
  onSelect: (kind: 'task' | 'memory', id: string) => void
  onNew: () => void
}) {
  if (tasks.length === 0) return <TasksEmpty onNew={onNew} />
  return (
    <ul className="divide-y divide-border">
      {tasks.map((t) => (
        <RowShell
          key={t.id}
          id={t.id}
          live={Boolean(t.live_lease)}
          flagged={Boolean(t.validation_error)}
          active={selectedId === t.id}
          onClick={() => onSelect('task', t.id)}
        >
          <div className="flex min-w-0 items-center gap-2">
            <span className="truncate text-sm font-medium">{t.title || '(untitled)'}</span>
            {t.pinned && (
              <Badge tone="mono" className="text-[9px]">
                pinned
              </Badge>
            )}
          </div>
          <div className="mt-1 flex flex-wrap items-center gap-1.5 text-xs">
            <Badge tone="mono" className="text-[9px]">
              {t.status}
            </Badge>
            {t.priority && (
              <Badge tone={priorityTone(t.priority)} className="text-[9px]">
                {t.priority}
              </Badge>
            )}
            {t.tags?.slice(0, 3).map((tag) => (
              <span key={tag} className="font-mono text-[10px] text-muted-foreground">
                #{tag}
              </span>
            ))}
            {t.index_source && (
              <span className="ml-auto font-mono text-[10px] text-muted-foreground/70">
                {t.index_source}
              </span>
            )}
          </div>
        </RowShell>
      ))}
    </ul>
  )
}

function MemoryRows({
  rows,
  kindLabel,
  selectedId,
  onSelect,
  onNew,
}: {
  rows: BrainMemoryRecord[]
  kindLabel: 'memories' | 'notes'
  selectedId: string | null
  onSelect: (kind: 'task' | 'memory', id: string) => void
  onNew: () => void
}) {
  if (rows.length === 0)
    return kindLabel === 'memories' ? <MemoriesEmpty onNew={onNew} /> : <NotesEmpty onNew={onNew} />
  return (
    <ul className="divide-y divide-border">
      {rows.map((m) => (
        <RowShell
          key={m.id}
          id={m.id}
          live={false}
          flagged={Boolean(m.validation_error)}
          active={selectedId === m.id}
          onClick={() => onSelect('memory', m.id)}
        >
          <div className="flex min-w-0 items-center gap-2">
            <span className="truncate font-mono text-sm font-medium">{m.name || '(unnamed)'}</span>
            {m.pinned && (
              <Badge tone="mono" className="text-[9px]">
                pinned
              </Badge>
            )}
          </div>
          <div className="mt-1 flex flex-wrap items-center gap-1.5 text-xs">
            <Badge tone="mono" className="text-[9px]">
              {m.kind}
            </Badge>
            {m.tags?.slice(0, 3).map((tag) => (
              <span key={tag} className="font-mono text-[10px] text-muted-foreground">
                #{tag}
              </span>
            ))}
            {m.index_source && (
              <span className="ml-auto font-mono text-[10px] text-muted-foreground/70">
                {m.index_source}
              </span>
            )}
          </div>
        </RowShell>
      ))}
    </ul>
  )
}

// RowShell is the shared divide-y row geometry: the clickable record body, the
// mono copyable ID gutter, plus the live-lease shimmer / validation pulse-slow
// markers. Animations are aria-hidden; the meaning is also carried in text via
// aria-label (DESIGN §7).
function RowShell({
  id,
  live,
  flagged,
  active,
  onClick,
  children,
}: {
  id: string
  live: boolean
  flagged: boolean
  active: boolean
  onClick: () => void
  children: React.ReactNode
}) {
  return (
    <li className="group/row relative">
      {live && (
        <span
          className="animate-shimmer pointer-events-none absolute inset-0 opacity-30"
          aria-hidden
        />
      )}
      <div className="flex items-stretch">
        <button
          type="button"
          onClick={onClick}
          aria-label={live ? 'agent active on this record' : undefined}
          className={cn(
            'min-w-0 flex-1 px-3 py-2 text-left transition-colors hover:bg-muted/60',
            active ? 'bg-muted' : '',
          )}
        >
          {children}
          <div className="mt-1 flex items-center gap-1.5">
            {flagged && (
              <Tooltip>
                <TooltipTrigger asChild>
                  <span
                    className="animate-pulse-slow inline-block h-1.5 w-1.5 bg-red-400/90"
                    aria-label="not indexed: your agent cannot see this record yet"
                  />
                </TooltipTrigger>
                <TooltipContent>not indexed: your agent cannot see this record yet</TooltipContent>
              </Tooltip>
            )}
            <span className="truncate font-mono text-[10px] text-muted-foreground/70">{id}</span>
          </div>
        </button>
        {id && (
          <div className="flex items-center px-1 opacity-0 transition-opacity group-hover/row:opacity-100">
            <CopyButton value={id} />
          </div>
        )}
      </div>
    </li>
  )
}
