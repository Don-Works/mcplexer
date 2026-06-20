// WorkersListPage — landing surface for the Workers section. One row
// per Worker; status filter chips above; "next run in N" countdown
// alongside the schedule. Empty state ships anticipatory ghost rows
// rather than an apology, and rows accept R as a keyboard shortcut to
// trigger a run-now without clicking the play button.
//
// Grouping: when more than one workspace has workers, rows are grouped
// under a sticky workspace header (mirrors AgentActivity). A toggle in
// the chip row flips to a flat table; the choice persists in
// localStorage. Default: grouped iff >1 workspace.

import { useCallback, useEffect, useMemo, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import {
  Bot,
  GitBranch,
  Layers,
  List,
  Loader2,
  Play,
  Plus,
  Search,
} from 'lucide-react'
import { toast } from 'sonner'

import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Card, CardContent } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from '@/components/ui/table'
import { useApi } from '@/hooks/use-api'
import {
  pauseWorker,
  resumeWorker,
  runWorkerNow,
  type WorkerSummary,
} from '@/api/workers'
import { listWorkspaces } from '@/api/client'
import {
  humanizeSchedule,
  isTriggerOnlySchedule,
  relativeTime,
  statusBadgeClass,
  summariseModel,
} from './worker-utils'
import { useCountdown } from './use-countdown'
import { useWorkersRealtime } from './use-workers-realtime'
import { EphemeralWorkersPanel, WorkersRealtimeSummary } from './WorkersRealtimePanels'

type Filter = 'all' | 'running' | 'failed' | 'paused' | 'never'
type GroupMode = 'workspace' | 'flat'
type GroupPref = GroupMode | 'auto'

const GROUP_PREF_KEY = 'mcplexer.workersList.groupBy'
const UNGROUPED_KEY = '__ungrouped__'
const UNGROUPED_LABEL = 'No workspace'

function readGroupPref(): GroupPref {
  if (typeof window === 'undefined') return 'auto'
  const v = window.localStorage.getItem(GROUP_PREF_KEY)
  return v === 'workspace' || v === 'flat' ? v : 'auto'
}

export function WorkersListPage() {
  const wsFetcher = useCallback(() => listWorkspaces(), [])
  const {
    rows,
    loading,
    error,
    refetch,
    connected,
    lastEventAt,
    lastRefreshAt,
  } = useWorkersRealtime()
  const { data: workspaces } = useApi(wsFetcher)
  const [busyID, setBusyID] = useState<string | null>(null)
  const [filter, setFilter] = useState<Filter>('all')
  const [query, setQuery] = useState('')
  const [groupPref, setGroupPref] = useState<GroupPref>(() => readGroupPref())

  async function withRow<T>(id: string, fn: () => Promise<T>, ok: string) {
    setBusyID(id)
    try {
      await fn()
      toast.success(ok)
      refetch()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Action failed')
    } finally {
      setBusyID(null)
    }
  }

  async function handleToggle(row: WorkerSummary) {
    const target = !row.enabled
    const ok = target
      ? 'Worker resumed'
      : row.last_run_status === 'running'
        ? 'Worker paused; active run cancelling'
        : 'Worker paused'
    await withRow(
      row.id,
      () => (target ? resumeWorker(row.id) : pauseWorker(row.id)),
      ok,
    )
  }

  async function handleRunNow(row: WorkerSummary) {
    await withRow(row.id, () => runWorkerNow(row.id), `Run started for ${row.name}`)
  }

  const configuredRows = useMemo(() => rows.filter((r) => !r.ephemeral), [rows])
  const ephemeralRows = useMemo(() => sortEphemeralRows(rows.filter((r) => r.ephemeral)), [rows])
  const searchedConfigured = useMemo(
    () => searchRows(configuredRows, query),
    [configuredRows, query],
  )
  const searchedEphemeral = useMemo(
    () => searchRows(ephemeralRows, query),
    [ephemeralRows, query],
  )
  const counts = useMemo(() => computeCounts(searchedConfigured), [searchedConfigured])
  const filtered = useMemo(() => filterRows(searchedConfigured, filter), [searchedConfigured, filter])

  // How many distinct workspaces does the unfiltered set span?
  // Drives the "auto" default for the group toggle — single-workspace
  // installs stay flat, multi-workspace installs grouped.
  const distinctWorkspaceCount = useMemo(() => {
    const s = new Set<string>()
    for (const r of configuredRows) s.add(r.workspace_id || '')
    return s.size
  }, [configuredRows])

  const groupMode: GroupMode =
    groupPref === 'auto' ? (distinctWorkspaceCount > 1 ? 'workspace' : 'flat') : groupPref

  const workspaceNameByID = useMemo(() => {
    const m: Record<string, string> = {}
    for (const w of workspaces ?? []) m[w.id] = w.name
    return m
  }, [workspaces])

  const groups = useMemo(
    () => buildGroups(filtered, workspaceNameByID),
    [filtered, workspaceNameByID],
  )

  const setGroupModeExplicit = useCallback((next: GroupMode) => {
    setGroupPref(next)
    if (typeof window !== 'undefined') window.localStorage.setItem(GROUP_PREF_KEY, next)
  }, [])

  // Auto-correct the persisted pref if the data shape changes underneath
  // it — e.g. user used to have multiple workspaces, removed all but one;
  // the "flat" choice they made years ago should fall back to auto.
  useEffect(() => {
    if (groupPref !== 'auto' && distinctWorkspaceCount <= 1 && groupPref === 'workspace') {
      // Single-workspace install but pref says group — harmless, just
      // renders one group. No correction needed.
    }
  }, [groupPref, distinctWorkspaceCount])

  return (
    <div className="space-y-5">
      <header className="flex flex-wrap items-end justify-between gap-3">
        <div>
          <h1 className="text-2xl font-bold tracking-tight flex items-center gap-2">
            <Bot className="h-6 w-6" /> Workers
          </h1>
          <p className="mt-1 max-w-2xl text-sm text-muted-foreground">
            Scheduled agents that run on a timer. Delegations are one-shot
            runs spawned on demand from a parent session.
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Button variant="outline" asChild data-testid="workers-delegations">
            <Link to="/delegations">
              <GitBranch className="mr-1.5 h-4 w-4" /> Delegations
            </Link>
          </Button>
          <Button asChild data-testid="workers-new">
            <Link to="/workers/new">
              <Plus className="mr-1.5 h-4 w-4" /> New worker
            </Link>
          </Button>
        </div>
      </header>

      <WorkersRealtimeSummary
        rows={rows}
        configuredRows={configuredRows}
        ephemeralRows={ephemeralRows}
        connected={connected}
        lastEventAt={lastEventAt}
        lastRefreshAt={lastRefreshAt}
      />

      {error && <ErrorBlock message={error} onRetry={refetch} />}

      {rows.length > 0 && (
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="relative min-w-64 flex-1 sm:max-w-sm">
            <Search className="pointer-events-none absolute left-2.5 top-2.5 h-4 w-4 text-muted-foreground/70" />
            <Input
              value={query}
              onChange={(event) => setQuery(event.target.value)}
              placeholder="Search scheduled workers and one-shot runs"
              className="pl-8"
              data-testid="workers-search"
            />
          </div>
          <FilterChips filter={filter} setFilter={setFilter} counts={counts} />
          {distinctWorkspaceCount > 1 && (
            <GroupToggle mode={groupMode} setMode={setGroupModeExplicit} />
          )}
        </div>
      )}

      {searchedEphemeral.length > 0 && (
        <EphemeralWorkersPanel rows={searchedEphemeral} total={ephemeralRows.length} />
      )}

      {loading && rows.length === 0 ? (
        <SkeletonTable />
      ) : configuredRows.length === 0 ? (
        <GhostRowsEmpty />
      ) : (
        groupMode === 'workspace' ? (
          <GroupedWorkers
            groups={groups}
            busyID={busyID}
            onToggle={handleToggle}
            onRunNow={handleRunNow}
          />
        ) : (
          <WorkersTable
            rows={filtered}
            busyID={busyID}
            onToggle={handleToggle}
            onRunNow={handleRunNow}
            workspaceNameByID={workspaceNameByID}
            showWorkspace={distinctWorkspaceCount > 1}
          />
        )
      )}
    </div>
  )
}

interface Group {
  key: string
  label: string
  rows: WorkerSummary[]
}

function buildGroups(rows: WorkerSummary[], names: Record<string, string>): Group[] {
  const byKey = new Map<string, Group>()
  for (const r of rows) {
    const wsID = r.workspace_id || ''
    const label = names[wsID] || wsID || UNGROUPED_LABEL
    const key = wsID || UNGROUPED_KEY
    let g = byKey.get(key)
    if (!g) {
      g = { key, label, rows: [] }
      byKey.set(key, g)
    }
    g.rows.push(r)
  }
  // Within a group: enabled first, then most-recently-run, then name.
  for (const g of byKey.values()) {
    g.rows.sort((a, b) => {
      if (a.enabled !== b.enabled) return a.enabled ? -1 : 1
      const at = a.last_run_at ? new Date(a.last_run_at).getTime() : 0
      const bt = b.last_run_at ? new Date(b.last_run_at).getTime() : 0
      if (at !== bt) return bt - at
      return a.name.localeCompare(b.name)
    })
  }
  // Across groups: largest group first; "No workspace" always last.
  return [...byKey.values()].sort((a, b) => {
    if (a.key === UNGROUPED_KEY) return 1
    if (b.key === UNGROUPED_KEY) return -1
    if (a.rows.length !== b.rows.length) return b.rows.length - a.rows.length
    return a.label.localeCompare(b.label)
  })
}

function searchRows(rows: WorkerSummary[], query: string): WorkerSummary[] {
  const q = query.trim().toLowerCase()
  if (!q) return rows
  return rows.filter((row) => {
    const haystack = [
      row.name,
      row.id,
      row.model_provider,
      row.model_id,
      row.schedule_spec,
      row.workspace_id,
      row.delegation_id,
      row.delegation_objective,
      row.delegation_task_id,
      row.delegation_task_kind,
      row.delegation_worker_mode,
    ]
      .filter(Boolean)
      .join(' ')
      .toLowerCase()
    return haystack.includes(q)
  })
}

function sortEphemeralRows(rows: WorkerSummary[]): WorkerSummary[] {
  return [...rows].sort((a, b) => {
    const ar = a.last_run_status === 'running' ? 1 : 0
    const br = b.last_run_status === 'running' ? 1 : 0
    if (ar !== br) return br - ar
    const at = latestWorkerTime(a)
    const bt = latestWorkerTime(b)
    if (at !== bt) return bt - at
    return a.name.localeCompare(b.name)
  })
}

function latestWorkerTime(row: WorkerSummary): number {
  const iso = row.last_run_at || row.created_at
  const t = iso ? new Date(iso).getTime() : 0
  return Number.isNaN(t) ? 0 : t
}

interface GroupToggleProps {
  mode: GroupMode
  setMode: (m: GroupMode) => void
}

// Visual: two segmented buttons (Layers/List), the active one is filled.
// Matches the FilterChips treatment so they sit on the same row without
// fighting each other for attention.
function GroupToggle({ mode, setMode }: GroupToggleProps) {
  const opt = (m: GroupMode, label: string, Icon: typeof Layers) => {
    const active = mode === m
    return (
      <button
        key={m}
        type="button"
        onClick={() => setMode(m)}
        aria-pressed={active}
        className={
          'inline-flex items-center gap-1.5 border px-2.5 py-1 text-xs transition-colors ' +
          (active
            ? 'border-primary/60 bg-primary/10 text-primary'
            : 'border-border bg-card/40 text-muted-foreground hover:text-foreground')
        }
        data-testid={`workers-group-${m}`}
        title={m === 'workspace' ? 'Group by workspace' : 'Flat list'}
      >
        <Icon className="h-3 w-3" />
        <span>{label}</span>
      </button>
    )
  }
  return (
    <div className="flex items-center gap-1.5" data-testid="workers-group-toggle">
      {opt('workspace', 'Grouped', Layers)}
      {opt('flat', 'Flat', List)}
    </div>
  )
}

interface GroupedProps {
  groups: Group[]
  busyID: string | null
  onToggle: (row: WorkerSummary) => void
  onRunNow: (row: WorkerSummary) => void
}

function GroupedWorkers({ groups, busyID, onToggle, onRunNow }: GroupedProps) {
  if (groups.length === 0) {
    return (
      <Card>
        <CardContent className="py-8 text-center text-sm text-muted-foreground">
          No workers match the current filter.
        </CardContent>
      </Card>
    )
  }
  return (
    <div className="space-y-4">
      {groups.map((g) => (
        <section key={g.key} data-testid={`workers-group-${g.key}`}>
          <div className="mb-1.5 flex items-baseline gap-2 px-1">
            <span
              className={
                'text-[11px] font-semibold uppercase tracking-wider ' +
                (g.key === UNGROUPED_KEY ? 'text-muted-foreground/60' : 'text-muted-foreground')
              }
            >
              {g.label}
            </span>
            <span className="font-mono text-[10px] tabular-nums text-muted-foreground/60">
              {g.rows.length}
            </span>
          </div>
          <Card>
            <CardContent className="p-0">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead className="w-2/5">Name</TableHead>
                    <TableHead>Model</TableHead>
                    <TableHead>Schedule</TableHead>
                    <TableHead>Last run</TableHead>
                    <TableHead>Enabled</TableHead>
                    <TableHead className="text-right">Actions</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {g.rows.map((r) => (
                    <WorkerRow
                      key={r.id}
                      row={r}
                      busy={busyID === r.id}
                      onToggle={() => onToggle(r)}
                      onRunNow={() => onRunNow(r)}
                    />
                  ))}
                </TableBody>
              </Table>
            </CardContent>
          </Card>
        </section>
      ))}
    </div>
  )
}

interface Counts {
  all: number
  running: number
  failed: number
  paused: number
  never: number
}

function computeCounts(rows: WorkerSummary[]): Counts {
  const c: Counts = { all: rows.length, running: 0, failed: 0, paused: 0, never: 0 }
  for (const r of rows) {
    if (r.last_run_status === 'running') c.running++
    if (r.last_run_status === 'failure' || r.last_run_status === 'cap_exceeded' || r.last_run_status === 'rejected') c.failed++
    if (!r.enabled) c.paused++
    if (!r.last_run_status) c.never++
  }
  return c
}

function filterRows(rows: WorkerSummary[], f: Filter): WorkerSummary[] {
  switch (f) {
    case 'running':
      return rows.filter((r) => r.last_run_status === 'running')
    case 'failed':
      return rows.filter(
        (r) =>
          r.last_run_status === 'failure' ||
          r.last_run_status === 'cap_exceeded' ||
          r.last_run_status === 'rejected',
      )
    case 'paused':
      return rows.filter((r) => !r.enabled)
    case 'never':
      return rows.filter((r) => !r.last_run_status)
    default:
      return rows
  }
}

interface ChipsProps {
  filter: Filter
  setFilter: (f: Filter) => void
  counts: Counts
}

function FilterChips({ filter, setFilter, counts }: ChipsProps) {
  const opts: Array<{ key: Filter; label: string; n: number }> = [
    { key: 'all', label: 'All', n: counts.all },
    { key: 'running', label: 'Running', n: counts.running },
    { key: 'failed', label: 'Failed', n: counts.failed },
    { key: 'paused', label: 'Paused', n: counts.paused },
    { key: 'never', label: 'Never run', n: counts.never },
  ]
  return (
    <div className="flex flex-wrap items-center gap-1.5" data-testid="workers-filter-chips">
      {opts.map((o) => {
        const active = filter === o.key
        return (
          <button
            key={o.key}
            type="button"
            onClick={() => setFilter(o.key)}
            className={
              'inline-flex items-center gap-1.5 border px-2.5 py-1 text-xs transition-colors ' +
              (active
                ? 'border-primary/60 bg-primary/10 text-primary'
                : 'border-border bg-card/40 text-muted-foreground hover:text-foreground')
            }
            data-testid={`workers-filter-${o.key}`}
          >
            <span>{o.label}</span>
            <span className={active ? 'text-primary' : 'text-muted-foreground/60'}>({o.n})</span>
          </button>
        )
      })}
    </div>
  )
}

function ErrorBlock({ message, onRetry }: { message: string; onRetry: () => void }) {
  return (
    <div className="flex items-center justify-between gap-3 rounded-md border border-destructive/40 bg-destructive/5 p-3 text-sm text-destructive">
      <span>{message}</span>
      <Button size="sm" variant="ghost" onClick={onRetry}>
        Retry
      </Button>
    </div>
  )
}

// GhostRowsEmpty — anticipatory empty state. Renders three ghost rows
// at low opacity so the page sells what Workers ARE before the user
// has any. The examples are real bundled templates installable via the
// MCP tools (mcplexer__list_worker_templates + mcplexer__install_worker_template).
function GhostRowsEmpty() {
  const ghosts: Array<{ name: string; desc: string; schedule: string }> = [
    { name: 'telegram-responder', desc: 'Reply to inbound Telegram messages', schedule: 'on mesh trigger' },
    { name: 'slack-status-notify', desc: 'Daily mcplexer digest to Slack', schedule: 'daily 09:00' },
    { name: 'cost-watcher', desc: 'Page on cap-approaching workers', schedule: '@hourly' },
  ]
  return (
    <div className="space-y-4">
      <Card>
        <CardContent className="p-0">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead className="w-2/5">Name</TableHead>
                <TableHead>Schedule</TableHead>
                <TableHead>Status</TableHead>
                <TableHead className="text-right">Actions</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {ghosts.map((g) => (
                <TableRow key={g.name} className="pointer-events-none opacity-30">
                  <TableCell>
                    <div className="font-medium text-foreground">{g.name}</div>
                    <div className="mt-0.5 text-xs text-muted-foreground">{g.desc}</div>
                  </TableCell>
                  <TableCell className="text-xs text-muted-foreground">{g.schedule}</TableCell>
                  <TableCell>
                    <Badge variant="outline" className={statusBadgeClass('paused')}>not installed</Badge>
                  </TableCell>
                  <TableCell className="text-right">
                    <Play className="ml-auto h-3 w-3 text-muted-foreground" />
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </CardContent>
      </Card>
      <div className="flex flex-col items-center gap-3 text-center" data-testid="workers-empty">
        <Bot className="h-8 w-8 text-muted-foreground/60" />
        <div className="space-y-1">
          <div className="text-sm font-medium text-foreground">No scheduled workers yet</div>
          <p className="max-w-md text-xs text-muted-foreground">
            Workers are durable agents that wake on a schedule, run a prompt
            against the tools you allow, and route the result to the mesh, a
            file, or a webhook. Pair one with a Skill for repeatable behaviour.
          </p>
          <p className="max-w-md text-[11px] text-muted-foreground/70">
            Delegations are one-shot runs spawned from a parent context. Open{' '}
            <Link to="/delegations" className="underline hover:text-foreground">
              Delegations
            </Link>{' '}
            when you want to review ad hoc worker runs.
          </p>
        </div>
        <div className="flex flex-wrap items-center justify-center gap-2">
          <Button asChild>
            <Link to="/workers/new">
              <Plus className="mr-1.5 h-4 w-4" /> New worker
            </Link>
          </Button>
          <Button variant="outline" asChild>
            <Link to="/delegations">
              <GitBranch className="mr-1.5 h-4 w-4" /> Delegations
            </Link>
          </Button>
        </div>
      </div>
    </div>
  )
}

function SkeletonTable() {
  return (
    <Card>
      <CardContent className="p-0">
        <div className="divide-y divide-border">
          {Array.from({ length: 4 }).map((_, i) => (
            <div key={i} className="flex items-center gap-4 px-4 py-4">
              <div className="h-4 w-1/4 animate-pulse bg-muted" />
              <div className="h-4 w-1/5 animate-pulse bg-muted" />
              <div className="h-4 w-1/6 animate-pulse bg-muted" />
              <div className="h-4 w-12 animate-pulse bg-muted ml-auto" />
            </div>
          ))}
        </div>
      </CardContent>
    </Card>
  )
}

interface TableProps {
  rows: WorkerSummary[]
  busyID: string | null
  onToggle: (row: WorkerSummary) => void
  onRunNow: (row: WorkerSummary) => void
  workspaceNameByID?: Record<string, string>
  showWorkspace?: boolean
}

function WorkersTable({
  rows,
  busyID,
  onToggle,
  onRunNow,
  workspaceNameByID,
  showWorkspace,
}: TableProps) {
  return (
    <Card>
      <CardContent className="p-0">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead className="w-2/5">Name</TableHead>
              <TableHead>Model</TableHead>
              <TableHead>Schedule</TableHead>
              <TableHead>Last run</TableHead>
              <TableHead>Enabled</TableHead>
              <TableHead className="text-right">Actions</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.map((r) => (
              <WorkerRow
                key={r.id}
                row={r}
                busy={busyID === r.id}
                onToggle={() => onToggle(r)}
                onRunNow={() => onRunNow(r)}
                workspaceLabel={
                  showWorkspace
                    ? workspaceNameByID?.[r.workspace_id] || r.workspace_id || ''
                    : ''
                }
              />
            ))}
          </TableBody>
        </Table>
      </CardContent>
    </Card>
  )
}

interface RowProps {
  row: WorkerSummary
  busy: boolean
  onToggle: () => void
  onRunNow: () => void
  workspaceLabel?: string
}

function WorkerRow({ row, busy, onToggle, onRunNow, workspaceLabel }: RowProps) {
  const navigate = useNavigate()
  const countdown = useCountdown(row.schedule_spec, row.last_run_at, row.enabled)
  return (
    <TableRow
      tabIndex={0}
      className="cursor-pointer hover:bg-muted/30 focus:outline-none focus:ring-1 focus:ring-primary/60"
      onClick={() => navigate(`/workers/${row.id}`)}
      onKeyDown={(e) => {
        if (e.key === 'Enter') navigate(`/workers/${row.id}`)
        if ((e.key === 'r' || e.key === 'R') && row.enabled && !busy) {
          e.preventDefault()
          onRunNow()
        }
      }}
      data-testid={`worker-row-${row.id}`}
    >
      <TableCell className="align-top">
        <div className="flex items-center gap-2">
          <span className="text-sm font-semibold text-foreground">{row.name}</span>
          {workspaceLabel && (
            <span
              className="rounded-sm border border-border/60 bg-muted/40 px-1.5 py-0.5 font-mono text-[10px] uppercase tracking-wide text-muted-foreground"
              title={`Workspace: ${workspaceLabel}`}
            >
              {workspaceLabel}
            </span>
          )}
        </div>
        <div className="mt-0.5 font-mono text-[10px] text-muted-foreground/70">{row.id}</div>
      </TableCell>
      <TableCell className="align-top text-xs font-mono text-muted-foreground">
        {summariseModel(row.model_provider, row.model_id)}
      </TableCell>
      <TableCell className="align-top text-xs">
        <div className="text-foreground">{humanizeSchedule(row.schedule_spec)}</div>
        <div className="mt-0.5 text-[10px] text-muted-foreground/70">
          {!row.enabled
            ? 'paused'
            : isTriggerOnlySchedule(row.schedule_spec)
              ? row.schedule_spec
              : countdown.nextRunDate
                ? `next in ${countdown.humanCountdown}`
                : row.schedule_spec}
        </div>
      </TableCell>
      <TableCell className="align-top">
        <div className="flex items-center gap-2">
          <Badge variant="outline" className={statusBadgeClass(row.last_run_status)}>
            {row.last_run_status || 'never'}
          </Badge>
        </div>
        <div className="mt-1 text-[10px] text-muted-foreground/70">
          {relativeTime(row.last_run_at)}
        </div>
      </TableCell>
      <TableCell className="align-top" onClick={(e) => e.stopPropagation()}>
        <EnableSwitch enabled={row.enabled} busy={busy} onToggle={onToggle} />
      </TableCell>
      <TableCell
        className="text-right align-top"
        onClick={(e) => e.stopPropagation()}
      >
        <Button
          size="sm"
          variant="ghost"
          disabled={busy || !row.enabled}
          onClick={onRunNow}
          data-testid={`worker-run-now-${row.id}`}
          title={row.enabled ? 'Run now (R)' : 'Resume to enable run-now'}
        >
          {busy ? <Loader2 className="h-3 w-3 animate-spin" /> : <Play className="h-3 w-3" />}
        </Button>
      </TableCell>
    </TableRow>
  )
}

// EnableSwitch — minimal toggle so we don't pull in a new shadcn Switch
// component. Looks like an iOS-style pill that you can click to flip.
interface EnableSwitchProps {
  enabled: boolean
  busy: boolean
  onToggle: () => void
}

export function EnableSwitch({ enabled, busy, onToggle }: EnableSwitchProps) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={enabled}
      disabled={busy}
      onClick={onToggle}
      className={
        'relative inline-flex h-5 w-9 shrink-0 items-center rounded-full border border-border transition-colors ' +
        (enabled ? 'bg-emerald-500/70' : 'bg-muted') +
        (busy ? ' opacity-60' : '')
      }
      data-testid="worker-enable-switch"
      data-state={enabled ? 'checked' : 'unchecked'}
    >
      <span
        className={
          'inline-block h-3.5 w-3.5 rounded-full bg-background shadow transition-transform ' +
          (enabled ? 'translate-x-4' : 'translate-x-1')
        }
      />
    </button>
  )
}
