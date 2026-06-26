// TasksListPage — the operator's view of every task across every
// workspace. Each row shows what an operator triages by: priority dot,
// title, status, assignee/lease, due, tags, composition. Filter strip
// up top is URL-backed end-to-end so a shared link reproduces the same
// view.
//
// Two view modes: Flat (one row per task) and Tree (children indented
// under their parent epic). Tree mode is the natural framing once the
// task corpus carries `compose_into` relationships.
//
// FTS hit highlighting wraps any substring of the search query found in
// title / description / status / tag with <mark>, so a `q` search lands
// the eye on the matched word.

import { useCallback, useEffect, useMemo, useState } from 'react'
import { Link, useNavigate, useSearchParams } from 'react-router-dom'
import {
  ChevronDown,
  ChevronRight,
  Circle,
  ListTodo,
  Pin,
  Plus,
  Search,
  Tag,
  X,
} from 'lucide-react'
import { toast } from 'sonner'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { EmptyState } from '@/components/ui/empty-state'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Checkbox } from '@/components/ui/checkbox'
import { useApi } from '@/hooks/use-api'
import { useTasksStream } from '@/hooks/use-tasks-stream'
import { useStatusVocab } from '@/hooks/use-status-vocab'
import { useActiveMeshAgents } from '@/hooks/use-active-mesh-agents'
import { listWorkspaces } from '@/api/client'
import { deleteTask, listMilestones, listTasks, readMetaList, updateTask, type Task } from '@/api/tasks'
import { cn } from '@/lib/utils'
import {
  assigneeLabel,
  buildTaskChildParentIds,
  dueState,
  formatAbsolute,
  formatDuration,
  formatRelative,
  isHumanAssigned,
  isWorkingStatus,
  leaseStaleness,
  matchesTaskCompositionFilter,
  priorityVisual,
  shortTaskId,
  statusVisual,
  type TaskCompositionFilter,
  timeInCurrentState,
  useNow,
} from './task-utils'
import { MilestoneSection } from './MilestoneSection'
import { TaskEditDialog } from './TaskEditDialog'
import {
  BulkActionBar,
  HighlightedText,
  TaskCreateHint,
  ViewModeToggle,
  type SortMode,
  type ViewMode,
  toggleSetMember,
} from './TasksListWidgets'

type StateFilter = 'open' | 'closed' | 'all'
type PriorityFilter = 'all' | 'low' | 'normal' | 'high' | 'critical'

const SEARCH_DEBOUNCE_MS = 200
const COLLAPSED_KEY = 'mcplexer.tasksList.collapsed'
const VIEW_MODE_KEY = 'mcplexer.tasksList.view'

function parseCompositionFilter(v: string | null): TaskCompositionFilter {
  switch (v) {
    case 'epics':
    case 'children':
    case 'standalone':
      return v
    default:
      return 'all'
  }
}

function readCollapsed(): Set<string> {
  if (typeof window === 'undefined') return new Set()
  try {
    const raw = window.localStorage.getItem(COLLAPSED_KEY)
    if (!raw) return new Set()
    return new Set(JSON.parse(raw) as string[])
  } catch {
    return new Set()
  }
}

function writeCollapsed(s: Set<string>) {
  if (typeof window === 'undefined') return
  window.localStorage.setItem(COLLAPSED_KEY, JSON.stringify(Array.from(s)))
}

function readView(): ViewMode {
  if (typeof window === 'undefined') return 'flat'
  return (window.localStorage.getItem(VIEW_MODE_KEY) as ViewMode) || 'flat'
}

function writeView(m: ViewMode) {
  if (typeof window === 'undefined') return
  window.localStorage.setItem(VIEW_MODE_KEY, m)
}

export function TasksListPage() {
  const navigate = useNavigate()
  const [params, setParams] = useSearchParams()

  const workspaceFilter = params.get('workspace') ?? ''
  const stateFilter = (params.get('state') as StateFilter) || 'open'
  const statusFilter = params.get('status') ?? ''
  const priorityFilter = (params.get('priority') as PriorityFilter) || 'all'
  const compositionFilter = parseCompositionFilter(params.get('composition'))
  const assigneeFilter = params.get('assignee') ?? ''
  // `human=1` is the dedicated filter for tasks owned by a human user
  // (migration 105). It composes with `assignee=<id>`: setting both
  // narrows the assignee dropdown to a specific human; setting only
  // `human=1` shows every human-assigned task in the workspace.
  const humanFilter = params.get('human') === '1'
  const tagFilter = params.get('tag') ?? ''
  const focusedId = params.get('focus') ?? ''
  const sortMode = (params.get('sort') as SortMode) || 'updated'

  const [searchInput, setSearchInput] = useState(params.get('q') ?? '')
  const [searchQuery, setSearchQuery] = useState(params.get('q') ?? '')
  const now = useNow(30_000)
  const [createOpen, setCreateOpen] = useState(false)
  const [collapsed, setCollapsed] = useState<Set<string>>(() => readCollapsed())
  const [viewMode, setViewMode] = useState<ViewMode>(() => readView())
  const [selected, setSelected] = useState<Set<string>>(() => new Set())
  const [bulkBusy, setBulkBusy] = useState(false)

  useEffect(() => {
    const t = setTimeout(() => setSearchQuery(searchInput.trim()), searchInput ? SEARCH_DEBOUNCE_MS : 0)
    return () => clearTimeout(t)
  }, [searchInput])

  // Keyboard shortcuts. Skips when the user is typing into a form
  // control so '/' doesn't steal focus from the search box itself.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const target = e.target as HTMLElement | null
      const inForm = target && (target.tagName === 'INPUT' || target.tagName === 'TEXTAREA' || target.isContentEditable)
      if (inForm && e.key !== 'Escape') return
      if (e.key === '/') {
        const el = document.querySelector<HTMLInputElement>('input[placeholder^="search title"]')
        if (el) {
          e.preventDefault()
          el.focus()
          el.select()
        }
      } else if (e.key === 'c') {
        e.preventDefault()
        setCreateOpen(true)
      } else if (e.key === 'Escape') {
        if (selected.size > 0) setSelected(new Set())
        ;(target as HTMLElement | null)?.blur?.()
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [selected.size])

  useEffect(() => {
    const next = new URLSearchParams(params)
    if (searchQuery) next.set('q', searchQuery)
    else next.delete('q')
    if (next.toString() !== params.toString()) setParams(next, { replace: true })
  }, [searchQuery, params, setParams])

  const wsFetcher = useCallback(() => listWorkspaces(), [])
  const { data: workspaces } = useApi(wsFetcher)
  const tasksFetcher = useCallback(
    () => {
      // assigneeFilter can be:
      //   - "unassigned" → client-side filter (no server param)
      //   - "user:<id>"  → server-side assignee_user_id (human owner)
      //   - "human"      → server-side assignee_origin_kind=human (any human)
      //   - "<sess>"     → server-side assignee_session_id
      // The "user:" and "human" tokens are stripped from the URL when
      // the human toggle or specific user selection clears, so the URL
      // stays round-trippable.
      let assigneeSession: string | undefined
      let assigneeUser: string | undefined
      let assigneeOriginKind: 'human' | undefined
      if (assigneeFilter === 'unassigned') {
        // client-side filter only
      } else if (assigneeFilter === 'human') {
        assigneeOriginKind = 'human'
      } else if (assigneeFilter?.startsWith('user:')) {
        assigneeUser = assigneeFilter.slice('user:'.length)
        assigneeOriginKind = 'human'
      } else if (assigneeFilter) {
        assigneeSession = assigneeFilter
      }
      return listTasks({
        workspace_id: workspaceFilter || undefined,
        state: stateFilter === 'all' ? undefined : stateFilter,
        status: statusFilter || undefined,
        tag: tagFilter || undefined,
        assignee_session_id: assigneeSession,
        assignee_user_id: assigneeUser,
        assignee_origin_kind: assigneeOriginKind ?? (humanFilter ? 'human' : undefined),
        q: searchQuery || undefined,
        limit: 500,
      })
    },
    [workspaceFilter, stateFilter, statusFilter, tagFilter, assigneeFilter, humanFilter, searchQuery],
  )
  const { data: tasks, loading, error, refetch } = useApi(tasksFetcher)
  // Per-workspace vocab — fetched when the operator has filtered to a
  // single workspace. In the cross-workspace "all" view we fall back
  // to the hardcoded WORKING_STATUSES heuristic inside isWorkingStatus
  // (passing undefined here). Aggregating vocab across every workspace
  // would mean N parallel fetches on dashboard load — not worth it
  // until the operator narrows the view.
  const { vocab: statusVocab } = useStatusVocab(workspaceFilter || null)
  const activeAgents = useActiveMeshAgents()

  // Milestones — only fetch when a single workspace is in focus (the
  // endpoint requires workspace_id). When the user hasn't picked a
  // workspace yet and there's exactly one, default to that one so the
  // tile row renders without needing the user to filter first.
  const defaultWorkspaceId = workspaces?.length === 1 ? workspaces[0].id : ''
  const milestoneWorkspaceId = workspaceFilter || defaultWorkspaceId
  const milestonesFetcher = useCallback(
    () => (milestoneWorkspaceId ? listMilestones(milestoneWorkspaceId) : Promise.resolve([])),
    [milestoneWorkspaceId],
  )
  const { data: milestones, refetch: refetchMilestones } = useApi(milestonesFetcher)

  useTasksStream({
    workspaceId: workspaceFilter || undefined,
    onEvent: () => {
      refetch()
      refetchMilestones()
    },
  })

  // If ?focus= is set and the task is in the current data set, navigate.
  useEffect(() => {
    if (!focusedId || !tasks) return
    const t = tasks.find((x) => x.id === focusedId)
    if (t) {
      navigate(`/tasks/${encodeURIComponent(t.id)}?workspace=${encodeURIComponent(t.workspace_id)}`, { replace: true })
    }
  }, [focusedId, tasks, navigate])

  const workspaceNameByID = useMemo(() => {
    const m: Record<string, string> = {}
    for (const w of workspaces ?? []) m[w.id] = w.name
    return m
  }, [workspaces])

  const childParentIds = useMemo(() => buildTaskChildParentIds(tasks ?? []), [tasks])

  const filtered = useMemo(() => {
    let xs = tasks ?? []
    if (priorityFilter !== 'all') xs = xs.filter((t) => t.priority === priorityFilter)
    if (compositionFilter !== 'all') {
      xs = xs.filter((t) => matchesTaskCompositionFilter(t, compositionFilter, childParentIds))
    }
    if (assigneeFilter === 'unassigned') {
      // "unassigned" means none of the three identity columns is set —
      // session, peer, OR human user. Client-side filter only because
      // the server doesn't expose a "no assignee" shorthand; the
      // fetched set is already narrowed by workspace/state.
      xs = xs.filter((t) => !t.assignee_session_id && !t.assignee_peer_id && !t.assignee_user_id)
    }
    return xs
  }, [tasks, priorityFilter, compositionFilter, assigneeFilter, childParentIds])

  // Derive a vocabulary for autocomplete from the currently-loaded rows.
  // Client-side dedupe by name; drops the empty-string sessions that
  // appear in the discovery envelope as ghost entries.
  const knownTags = useMemo(() => {
    const s = new Set<string>()
    for (const t of filtered) for (const tag of t.tags ?? []) s.add(tag)
    return Array.from(s).sort((a, b) => a.localeCompare(b))
  }, [filtered])

  const knownAssignees = useMemo(() => {
    // Two parallel keys: a session-id map for the existing session
    // dropdown + a user-id map for human owners. Keeping them in the
    // same shape lets the AssigneeChip render both groups under one
    // dropdown — humans are visually distinct via the `@` prefix that
    // assigneeLabel emits.
    const m = new Map<string, { sess: string; label: string; kind: 'session' | 'user' }>()
    for (const t of filtered) {
      const sess = t.assignee_session_id?.trim()
      if (sess) {
        const key = sess
        if (!m.has(key)) m.set(key, { sess: key, label: assigneeLabel(t), kind: 'session' })
      }
      const user = t.assignee_user_id?.trim()
      if (user) {
        const key = `user:${user}`
        if (!m.has(key)) m.set(key, { sess: key, label: `@${user}`, kind: 'user' })
      }
    }
    return Array.from(m.values()).sort((a, b) => a.label.localeCompare(b.label))
  }, [filtered])

  const groups = useMemo(
    () => buildGroups(filtered, workspaceNameByID, sortMode, viewMode),
    [filtered, workspaceNameByID, sortMode, viewMode],
  )

  const totalCount = filtered.length
  const openCount = useMemo(() => filtered.filter((t) => !t.closed_at).length, [filtered])

  const toggleCollapsed = useCallback((wsId: string) => {
    setCollapsed((prev) => {
      const next = new Set(prev)
      if (next.has(wsId)) next.delete(wsId)
      else next.add(wsId)
      writeCollapsed(next)
      return next
    })
  }, [])

  const handleViewMode = useCallback((m: ViewMode) => {
    setViewMode(m)
    writeView(m)
  }, [])

  const setFilter = useCallback(
    (key: string, value: string | null) => {
      const next = new URLSearchParams(params)
      if (value === null || value === '') next.delete(key)
      else next.set(key, value)
      setParams(next, { replace: true })
    },
    [params, setParams],
  )

  const clearAllFilters = useCallback(() => {
    setParams(new URLSearchParams(), { replace: true })
    setSearchInput('')
    setSearchQuery('')
    setSelected(new Set())
  }, [setParams])

  const hasActiveFilter =
    workspaceFilter ||
    priorityFilter !== 'all' ||
    assigneeFilter ||
    compositionFilter !== 'all' ||
    humanFilter ||
    tagFilter ||
    searchQuery ||
    stateFilter !== 'open' ||
    statusFilter ||
    sortMode !== 'updated'

  const toggleSelected = useCallback((id: string) => {
    setSelected((prev) => toggleSetMember(prev, id))
  }, [])

  const clearSelected = useCallback(() => setSelected(new Set()), [])

  const handleBulkClose = useCallback(async () => {
    if (selected.size === 0) return
    setBulkBusy(true)
    try {
      const ids = Array.from(selected)
      const byTask = new Map((tasks ?? []).map((t) => [t.id, t.workspace_id] as const))
      await Promise.all(
        ids.map((id) => {
          const ws = byTask.get(id)
          if (!ws) return Promise.resolve()
          return updateTask(ws, id, { terminal: true })
        }),
      )
      toast.success(`Closed ${ids.length} task${ids.length === 1 ? '' : 's'}`)
      clearSelected()
      refetch()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Bulk close failed')
    } finally {
      setBulkBusy(false)
    }
  }, [selected, tasks, clearSelected, refetch])

  const handleBulkDelete = useCallback(async () => {
    if (selected.size === 0) return
    setBulkBusy(true)
    try {
      const ids = Array.from(selected)
      const byTask = new Map((tasks ?? []).map((t) => [t.id, t.workspace_id] as const))
      await Promise.all(
        ids.map((id) => {
          const ws = byTask.get(id)
          if (!ws) return Promise.resolve()
          return deleteTask(ws, id)
        }),
      )
      toast.success(`Deleted ${ids.length} task${ids.length === 1 ? '' : 's'}`)
      clearSelected()
      refetch()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Bulk delete failed')
    } finally {
      setBulkBusy(false)
    }
  }, [selected, tasks, clearSelected, refetch])

  return (
    <div className="space-y-5">
      <header className="flex flex-wrap items-end justify-between gap-3">
        <div>
          <h1 className="flex items-center gap-2 text-2xl font-bold tracking-tight">
            <ListTodo className="h-6 w-6" /> Tasks
          </h1>
          <p className="mt-1 max-w-2xl text-sm text-muted-foreground">
            Every task across your workspaces. Filter by state, epic, priority, assignee, or tag; agents create and claim, you triage what needs your eye.
          </p>
        </div>
        <div className="flex items-center gap-2">
          <ViewModeToggle value={viewMode} onChange={handleViewMode} />
          <Button onClick={() => setCreateOpen(true)} size="sm">
            <Plus className="h-4 w-4" />
            New task
          </Button>
        </div>
      </header>

      {/* Filter strip */}
      <div className="border border-border bg-card/40">
        <div className="flex flex-wrap items-center gap-2 border-b border-border px-3 py-2">
          <SearchInput value={searchInput} onChange={setSearchInput} />
          <StateToggle value={stateFilter} onChange={(v) => setFilter('state', v)} />
          <StatusFilterSelect
            value={statusFilter}
            vocab={statusVocab}
            onChange={(v) => setFilter('status', v)}
          />
          <PriorityFilterSelect
            value={priorityFilter}
            onChange={(v) => setFilter('priority', v === 'all' ? null : v)}
          />
          <CompositionFilterSelect
            value={compositionFilter}
            onChange={(v) => setFilter('composition', v === 'all' ? null : v)}
          />
          {workspaces && workspaces.length > 1 ? (
            <WorkspaceFilterSelect
              value={workspaceFilter}
              workspaces={workspaces.map((w) => ({ id: w.id, name: w.name }))}
              onChange={(v) => setFilter('workspace', v)}
            />
          ) : null}
          <SortSelect value={sortMode} onChange={(v) => setFilter('sort', v === 'updated' ? null : v)} />
          {hasActiveFilter ? (
            <Button variant="ghost" size="sm" onClick={clearAllFilters} className="h-8 text-xs">
              <X className="h-3 w-3" />
              Clear
            </Button>
          ) : null}
          <HumanToggle
            value={humanFilter}
            onChange={(v) => setFilter('human', v ? '1' : null)}
          />
          <div className="ml-auto text-[11px] text-muted-foreground">
            {loading && !tasks ? 'loading…' : `${openCount} open / ${totalCount} shown`}
          </div>
        </div>
        {(tagFilter || assigneeFilter || humanFilter || knownTags.length > 0 || knownAssignees.length > 0) ? (
          <div className="flex flex-wrap items-center gap-1.5 px-3 py-2 text-xs">
            <AssigneeChip
              value={assigneeFilter}
              options={knownAssignees}
              onChange={(v) => setFilter('assignee', v)}
            />
            <TagChip
              value={tagFilter}
              options={knownTags}
              onChange={(v) => setFilter('tag', v)}
            />
          </div>
        ) : null}
      </div>

      {error ? (
        <div className="border border-destructive/50 bg-destructive/10 px-3 py-2 text-sm text-destructive">{error}</div>
      ) : null}

      <MilestoneSection milestones={milestones} />

      {!loading && totalCount === 0 ? (
        <EmptyState
          icon={<ListTodo className="h-10 w-10 text-muted-foreground" />}
          title={hasActiveFilter ? 'No tasks match these filters' : 'No tasks yet'}
          description={
            hasActiveFilter ? (
              <button onClick={clearAllFilters} className="text-primary hover:underline">
                Clear all filters
              </button>
            ) : (
              <TaskCreateHint />
            )
          }
          action={
            !hasActiveFilter ? (
              <Button onClick={() => setCreateOpen(true)} size="sm">
                <Plus className="h-4 w-4" />
                Create the first task
              </Button>
            ) : null
          }
        />
      ) : (
        <div className="space-y-4">
          {groups.map((g) => (
            <TaskGroup
              key={g.id}
              groupId={g.id}
              label={g.label}
              tasks={g.tasks}
              collapsed={collapsed.has(g.id)}
              onToggleCollapsed={() => toggleCollapsed(g.id)}
              onTagClick={(tag) => setFilter('tag', tag)}
              onAssigneeClick={(sess) => setFilter('assignee', sess)}
              viewMode={viewMode}
              searchQuery={searchQuery}
              selected={selected}
              onToggleSelected={toggleSelected}
              now={now}
              statusVocab={statusVocab}
              activeAgents={activeAgents}
            />
          ))}
        </div>
      )}

      {selected.size > 0 ? (
        <BulkActionBar
          count={selected.size}
          busy={bulkBusy}
          onClear={clearSelected}
          onClose={handleBulkClose}
          onDelete={handleBulkDelete}
        />
      ) : null}

      <TaskEditDialog
        mode="create"
        open={createOpen}
        onOpenChange={setCreateOpen}
        workspaces={workspaces ?? []}
        defaultWorkspaceId={workspaceFilter || (workspaces?.[0]?.id ?? '')}
        onSaved={(t) => {
          toast.success(`Created ${shortTaskId(t.id)}`)
          refetch()
          navigate(`/tasks/${encodeURIComponent(t.id)}?workspace=${encodeURIComponent(t.workspace_id)}`)
        }}
      />
    </div>
  )
}

// — Subcomponents —

interface Group {
  id: string
  label: string
  tasks: TreeNode[]
}

interface TreeNode {
  task: Task
  children: TreeNode[]
  depth: number
}

function buildGroups(
  rows: Task[],
  workspaceNames: Record<string, string>,
  sort: SortMode,
  view: ViewMode,
): Group[] {
  const byWorkspace = new Map<string, Task[]>()
  for (const t of rows) {
    const id = t.workspace_id || '__no_ws__'
    let arr = byWorkspace.get(id)
    if (!arr) {
      arr = []
      byWorkspace.set(id, arr)
    }
    arr.push(t)
  }

  const out: Group[] = []
  for (const [id, ts] of byWorkspace) {
    const sorted = [...ts].sort(comparatorFor(sort))
    const nodes = view === 'tree' ? buildTree(sorted) : sorted.map((task) => ({ task, children: [], depth: 0 }))
    out.push({
      id,
      label: workspaceNames[id] ?? (id === '__no_ws__' ? 'No workspace' : id.slice(0, 8)),
      tasks: nodes,
    })
  }
  out.sort((a, b) => a.label.localeCompare(b.label))
  return out
}

function buildTree(rows: Task[]): TreeNode[] {
  const byId = new Map<string, Task>()
  for (const t of rows) byId.set(t.id, t)
  const childOf = new Map<string, string[]>()
  const isChild = new Set<string>()
  for (const t of rows) {
    const parents = readMetaList(t.meta, 'composed_by')
    for (const pid of parents) {
      if (!byId.has(pid)) continue
      const arr = childOf.get(pid) ?? []
      arr.push(t.id)
      childOf.set(pid, arr)
      isChild.add(t.id)
    }
  }
  const visit = (task: Task, depth: number, seen: Set<string>): TreeNode => {
    if (seen.has(task.id)) return { task, children: [], depth }
    const next = new Set(seen)
    next.add(task.id)
    const childIds = childOf.get(task.id) ?? []
    const children = childIds
      .map((id) => byId.get(id))
      .filter((c): c is Task => !!c)
      .map((c) => visit(c, depth + 1, next))
    return { task, children, depth }
  }
  const out: TreeNode[] = []
  for (const t of rows) {
    if (isChild.has(t.id)) continue
    out.push(visit(t, 0, new Set()))
  }
  return out
}

function comparatorFor(sort: SortMode): (a: Task, b: Task) => number {
  const pri: Record<string, number> = { critical: 0, high: 1, normal: 2, low: 3 }
  return (a, b) => {
    if (!!a.pinned !== !!b.pinned) return a.pinned ? -1 : 1
    switch (sort) {
      case 'created':
        return new Date(b.created_at).getTime() - new Date(a.created_at).getTime()
      case 'due': {
        const ad = a.due_at ? new Date(a.due_at).getTime() : Number.POSITIVE_INFINITY
        const bd = b.due_at ? new Date(b.due_at).getTime() : Number.POSITIVE_INFINITY
        return ad - bd
      }
      case 'priority':
        return (pri[a.priority] ?? 99) - (pri[b.priority] ?? 99)
      case 'status':
        return a.status.localeCompare(b.status)
      case 'updated':
      default:
        return new Date(b.updated_at).getTime() - new Date(a.updated_at).getTime()
    }
  }
}

function SearchInput({ value, onChange }: { value: string; onChange: (v: string) => void }) {
  return (
    <div className="relative w-64">
      <Search className="pointer-events-none absolute left-2 top-1/2 h-3.5 w-3.5 -translate-y-1/2 text-muted-foreground" />
      <Input
        value={value}
        onChange={(e) => onChange(e.target.value)}
        placeholder="search title + body + id"
        className="h-8 pl-7 font-mono text-xs"
      />
    </div>
  )
}

function StateToggle({ value, onChange }: { value: StateFilter; onChange: (v: StateFilter) => void }) {
  const options: { id: StateFilter; label: string }[] = [
    { id: 'open', label: 'Open' },
    { id: 'closed', label: 'Closed' },
    { id: 'all', label: 'All' },
  ]
  return (
    <div className="inline-flex border border-border">
      {options.map((o) => (
        <button
          key={o.id}
          onClick={() => onChange(o.id)}
          className={cn(
            'border-r border-border px-2.5 py-1 text-xs last:border-r-0',
            value === o.id
              ? 'bg-accent text-accent-foreground'
              : 'bg-transparent text-muted-foreground hover:bg-muted/40 hover:text-foreground',
          )}
        >
          {o.label}
        </button>
      ))}
    </div>
  )
}

function StatusFilterSelect({
  value,
  vocab,
  onChange,
}: {
  value: string
  vocab?: import('./task-utils').StatusKindMap
  onChange: (v: string | null) => void
}) {
  // Defaults cover the canonical statuses used by most workspaces. When
  // the workspace's vocab is known (single-workspace view), merge in any
  // custom statuses so the dropdown reflects what's actually in use.
  const defaults = ['open', 'doing', 'blocked', 'review', 'done', 'cancelled']
  const fromVocab = vocab ? Object.keys(vocab) : []
  const all = Array.from(new Set([...defaults, ...fromVocab, ...(value ? [value] : [])]))
  return (
    <Select value={value || 'all'} onValueChange={(v) => onChange(v === 'all' ? null : v)}>
      <SelectTrigger size="sm" className="h-8 text-xs">
        <SelectValue placeholder="All status" />
      </SelectTrigger>
      <SelectContent>
        <SelectItem value="all">All status</SelectItem>
        {all.map((s) => (
          <SelectItem key={s} value={s}>{s}</SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}

function PriorityFilterSelect({
  value,
  onChange,
}: {
  value: PriorityFilter
  onChange: (v: PriorityFilter) => void
}) {
  return (
    <Select value={value} onValueChange={(v) => onChange(v as PriorityFilter)}>
      <SelectTrigger size="sm" className="h-8 text-xs">
        <SelectValue placeholder="All priority" />
      </SelectTrigger>
      <SelectContent>
        <SelectItem value="all">All priority</SelectItem>
        <SelectItem value="critical">Critical</SelectItem>
        <SelectItem value="high">High</SelectItem>
        <SelectItem value="normal">Normal</SelectItem>
        <SelectItem value="low">Low</SelectItem>
      </SelectContent>
    </Select>
  )
}

function CompositionFilterSelect({
  value,
  onChange,
}: {
  value: TaskCompositionFilter
  onChange: (v: TaskCompositionFilter) => void
}) {
  return (
    <Select value={value} onValueChange={(v) => onChange(v as TaskCompositionFilter)}>
      <SelectTrigger size="sm" className="h-8 text-xs">
        <SelectValue placeholder="All composition" />
      </SelectTrigger>
      <SelectContent>
        <SelectItem value="all">All composition</SelectItem>
        <SelectItem value="epics">Epics</SelectItem>
        <SelectItem value="children">Children</SelectItem>
        <SelectItem value="standalone">Standalone</SelectItem>
      </SelectContent>
    </Select>
  )
}

function WorkspaceFilterSelect({
  value,
  workspaces,
  onChange,
}: {
  value: string
  workspaces: { id: string; name: string }[]
  onChange: (v: string) => void
}) {
  return (
    <Select value={value || '__all__'} onValueChange={(v) => onChange(v === '__all__' ? '' : v)}>
      <SelectTrigger size="sm" className="h-8 text-xs">
        <SelectValue placeholder="All workspaces" />
      </SelectTrigger>
      <SelectContent>
        <SelectItem value="__all__">All workspaces</SelectItem>
        {workspaces.map((w) => (
          <SelectItem key={w.id} value={w.id}>
            {w.name}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}

function SortSelect({ value, onChange }: { value: SortMode; onChange: (v: SortMode) => void }) {
  return (
    <Select value={value} onValueChange={(v) => onChange(v as SortMode)}>
      <SelectTrigger size="sm" className="h-8 text-xs">
        <SelectValue placeholder="Sort" />
      </SelectTrigger>
      <SelectContent>
        <SelectItem value="updated">Updated · newest</SelectItem>
        <SelectItem value="created">Created · newest</SelectItem>
        <SelectItem value="due">Due · soonest</SelectItem>
        <SelectItem value="priority">Priority</SelectItem>
        <SelectItem value="status">Status</SelectItem>
      </SelectContent>
    </Select>
  )
}

function TagChip({
  value,
  options,
  onChange,
}: {
  value: string
  options: string[]
  onChange: (v: string | null) => void
}) {
  if (!value && options.length === 0) return null
  if (value) {
    return (
      <button
        onClick={() => onChange(null)}
        className="inline-flex items-center gap-1 border border-border bg-muted/40 px-2 py-0.5 font-mono text-[11px] hover:border-destructive/60"
      >
        <Tag className="h-3 w-3" />
        {value}
        <X className="h-3 w-3" />
      </button>
    )
  }
  return (
    <Select value="__none__" onValueChange={(v) => onChange(v === '__none__' ? null : v)}>
      <SelectTrigger size="sm" className="h-7 text-[11px] font-mono text-muted-foreground/80">
        <Tag className="h-3 w-3" />
        <SelectValue placeholder="tag" />
      </SelectTrigger>
      <SelectContent>
        <SelectItem value="__none__">any tag</SelectItem>
        {options.map((t) => (
          <SelectItem key={t} value={t} className="font-mono">
            {t}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}

function AssigneeChip({
  value,
  options,
  onChange,
}: {
  value: string
  options: { sess: string; label: string; kind: 'session' | 'user' }[]
  onChange: (v: string | null) => void
}) {
  if (!value && options.length === 0) return null
  if (value) {
    return (
      <button
        onClick={() => onChange(null)}
        className="inline-flex items-center gap-1 border border-border bg-muted/40 px-2 py-0.5 font-mono text-[11px] hover:border-destructive/60"
      >
        assignee:
        {value === 'unassigned'
          ? 'unassigned'
          : value === 'human'
            ? 'human'
            : value.startsWith('user:')
              ? value
              : value.slice(0, 8)}
        <X className="h-3 w-3" />
      </button>
    )
  }
  return (
    <Select value="__none__" onValueChange={(v) => onChange(v === '__none__' ? null : v)}>
      <SelectTrigger size="sm" className="h-7 text-[11px] font-mono text-muted-foreground/80">
        <Circle className="h-2 w-2" />
        <SelectValue placeholder="assignee" />
      </SelectTrigger>
      <SelectContent>
        <SelectItem value="__none__">anyone</SelectItem>
        <SelectItem value="unassigned" className="font-mono">unassigned</SelectItem>
        <SelectItem value="human" className="font-mono">human (any)</SelectItem>
        {options.map((a) => (
          <SelectItem key={a.sess} value={a.sess} className="font-mono">
            {a.label}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  )
}

// HumanToggle — short-circuit button that surfaces "tasks assigned to
// humans" as a primary filter, independent of the assignee dropdown.
// Distinct from `assignee=human` so the URL stays semantic: a single
// `?human=1` query reproduces the filtered view via deep-link /
// reload.
function HumanToggle({ value, onChange }: { value: boolean; onChange: (v: boolean) => void }) {
  return (
    <button
      type="button"
      onClick={() => onChange(!value)}
      title="Show only tasks assigned to a human user"
      className={cn(
        'inline-flex h-8 items-center gap-1 border px-2 text-[11px] font-mono transition-colors',
        value
          ? 'border-primary/60 bg-primary/15 text-primary'
          : 'border-border text-muted-foreground hover:border-primary/40 hover:text-foreground',
      )}
    >
      <Circle
        className={cn(
          'h-2 w-2',
          value ? 'fill-primary text-primary' : 'text-muted-foreground/60',
        )}
      />
      human
    </button>
  )
}

function TaskGroup({
  groupId,
  label,
  tasks,
  collapsed,
  onToggleCollapsed,
  onTagClick,
  onAssigneeClick,
  viewMode,
  searchQuery,
  selected,
  onToggleSelected,
  now,
  statusVocab,
  activeAgents,
}: {
  groupId: string
  label: string
  tasks: TreeNode[]
  collapsed: boolean
  onToggleCollapsed: () => void
  onTagClick: (tag: string) => void
  onAssigneeClick: (sess: string) => void
  viewMode: ViewMode
  searchQuery: string
  selected: Set<string>
  onToggleSelected: (id: string) => void
  now: number
  statusVocab?: import('./task-utils').StatusKindMap
  activeAgents: import('@/hooks/use-active-mesh-agents').ActiveMeshAgents
}) {
  const flatten = (nodes: TreeNode[]): TreeNode[] => {
    const out: TreeNode[] = []
    const walk = (n: TreeNode) => {
      out.push(n)
      for (const c of n.children) walk(c)
    }
    for (const n of nodes) walk(n)
    return out
  }
  const flat = flatten(tasks)
  const openCount = flat.filter((n) => !n.task.closed_at).length
  const closedCount = flat.length - openCount

  return (
    <section className="border border-border bg-card/30">
      <button
        onClick={onToggleCollapsed}
        className="flex w-full items-center justify-between border-b border-border bg-card px-3 py-2 text-left hover:bg-card/80"
      >
        <div className="flex items-center gap-2">
          {collapsed ? <ChevronRight className="h-3.5 w-3.5" /> : <ChevronDown className="h-3.5 w-3.5" />}
          <span className="text-sm font-semibold">{label}</span>
          <span className="font-mono text-[10px] uppercase tracking-wider text-muted-foreground">{groupId.slice(0, 8)}</span>
        </div>
        <div className="flex items-center gap-1.5 text-[11px] text-muted-foreground">
          {openCount > 0 ? <span className="text-emerald-400">{openCount} open</span> : null}
          {openCount > 0 && closedCount > 0 ? <span>·</span> : null}
          {closedCount > 0 ? <span>{closedCount} closed</span> : null}
          {flat.length === 0 ? <span>empty</span> : null}
        </div>
      </button>
      {!collapsed ? (
        <ul className="divide-y divide-border">
          {flat.map((n) => (
            <TaskRow
              key={n.task.id}
              task={n.task}
              depth={viewMode === 'tree' ? n.depth : 0}
              hasChildren={n.children.length > 0}
              onTagClick={onTagClick}
              onAssigneeClick={onAssigneeClick}
              searchQuery={searchQuery}
              selected={selected.has(n.task.id)}
              onToggleSelected={() => onToggleSelected(n.task.id)}
              now={now}
              statusVocab={statusVocab}
              activeAgents={activeAgents}
            />
          ))}
        </ul>
      ) : null}
    </section>
  )
}

function TaskRow({
  task,
  depth,
  hasChildren,
  onTagClick,
  onAssigneeClick,
  searchQuery,
  selected,
  onToggleSelected,
  now,
  statusVocab,
  activeAgents,
}: {
  task: Task
  depth: number
  hasChildren: boolean
  onTagClick: (tag: string) => void
  onAssigneeClick: (sess: string) => void
  searchQuery: string
  selected: boolean
  onToggleSelected: () => void
  now: number
  statusVocab?: import('./task-utils').StatusKindMap
  activeAgents: import('@/hooks/use-active-mesh-agents').ActiveMeshAgents
}) {
  const pv = priorityVisual(task.priority)
  const sv = statusVisual(task.status, !!task.closed_at)
  const due = dueState(task.due_at ?? null, task.closed_at ?? null)
  const tags = task.tags ?? []
  const composes = readMetaList(task.meta, 'composes')
  const composedBy = readMetaList(task.meta, 'composed_by')
  const isEpic = composes.length > 0 || hasChildren
  const isChild = composedBy.length > 0
  const assignee = assigneeLabel(task)
  const hasAssignee = !!task.assignee_session_id || !!task.assignee_peer_id || !!task.assignee_user_id
  const humanOwner = isHumanAssigned(task)
  const href = `/tasks/${encodeURIComponent(task.id)}?workspace=${encodeURIComponent(task.workspace_id)}`

  // Lease state is derived from {status, lease_expires_at, updated_at,
  // assignee} via leaseStaleness. Post-migration 071 the function reads
  // the real lease_expires_at when present and falls back to updated_at
  // for pre-071 rows. A status=doing row whose lease has elapsed is
  // shown as abandoned with a warning chip — the backend sweep nulls
  // the assignee on its next tick.
  // Agent-presence-based abandonment: a `doing` row is abandoned ⇔
  // its assignee_session_id no longer appears in the mesh's active-
  // agents directory. Elapsed time since update is NOT a signal —
  // an agent might be working quietly. Only the agent vanishing
  // counts as abandonment.
  // Human owners have no lease concept (migration 105) — the lease
  // state is always `idle` for them; the visual cue is the HumanDot
  // glyph rendered instead of the lease dot.
  const stale = leaseStaleness(
    task.status,
    task.assignee_session_id,
    task.closed_at,
    activeAgents.ready ? activeAgents.sessionIds : null,
    statusVocab,
    task.assignee_user_id,
  )
  const inState = isWorkingStatus(task.status, statusVocab) ? timeInCurrentState(task.status_history ?? null, now) : 0
  const leaseState: 'live' | 'abandoned' | 'unassigned' | 'idle' = (() => {
    if (!hasAssignee) return 'unassigned'
    if (humanOwner) return 'idle'
    if (stale.state === 'abandoned') return 'abandoned'
    if (stale.state === 'live') return 'live'
    return 'idle'
  })()

  return (
    <li className={cn('group relative', selected ? 'bg-accent/30' : '')}>
      <div className="flex items-stretch gap-2 px-3 py-2.5 transition-colors hover:bg-muted/40">
        {/* Checkbox — appears on hover OR when any row is selected */}
        <div
          className={cn(
            'flex w-4 shrink-0 items-center justify-center self-center',
            selected ? 'opacity-100' : 'opacity-0 group-hover:opacity-100',
          )}
          onClick={(e) => e.stopPropagation()}
        >
          <Checkbox checked={selected} onCheckedChange={onToggleSelected} aria-label={`Select ${task.title}`} />
        </div>

        {/* Tree indent guides + priority dot. Each depth step adds a
            32px gutter with a hairline guide so the hierarchy reads
            instantly. The last segment renders an L-corner from guide
            to dot to anchor the child visually under its parent. */}
        {depth > 0 ? (
          <div className="flex shrink-0 self-stretch" aria-hidden>
            {Array.from({ length: depth - 1 }).map((_, i) => (
              <span key={i} className="w-8 border-l border-border/40" />
            ))}
            <span className="relative w-8 border-l border-border/40">
              <span className="absolute left-0 top-1/2 h-px w-6 bg-border/40" />
            </span>
          </div>
        ) : null}
        <div
          className="flex w-3 shrink-0 items-center justify-center self-center"
          title={`priority: ${task.priority}`}
        >
          <span className={cn('h-2 w-2 rounded-full', pv.dot)} />
        </div>

        <Link to={href} className={cn('flex min-w-0 flex-1 items-stretch gap-3', task.closed_at ? 'opacity-60' : '')}>
          {/* Title + meta */}
          <div className="min-w-0 flex-1">
            <div className="flex flex-wrap items-baseline gap-2">
              {task.pinned ? <Pin className="h-3 w-3 text-amber-400" /> : null}
              <span className={cn('truncate text-sm font-medium', task.closed_at ? 'line-through' : '')}>
                <HighlightedText text={task.title} query={searchQuery} />
              </span>
              <Badge variant="outline" tone={sv.tone} className={cn('text-[10px]', sv.mono ? 'font-mono' : '')}>
                {task.status}
                {inState > 0 ? (
                  <span className="ml-1 font-mono opacity-70">· {formatDuration(inState)}</span>
                ) : null}
              </Badge>
              {leaseState === 'abandoned' ? (
                <Badge variant="outline" tone="warn" className="text-[10px]">
                  agent gone
                </Badge>
              ) : null}
              {isEpic ? (
                <Badge variant="outline" tone="info" className="text-[10px]">
                  epic · {composes.length}
                </Badge>
              ) : null}
              {isChild ? (
                <Badge variant="outline" tone="muted" className="text-[10px]">
                  child
                </Badge>
              ) : null}
              {tags.slice(0, 3).map((tag) => (
                <button
                  key={tag}
                  onClick={(e) => {
                    e.preventDefault()
                    onTagClick(tag)
                  }}
                  className="inline-flex items-center gap-1 border border-border bg-muted/30 px-1.5 py-px text-[10px] text-muted-foreground hover:border-primary/40 hover:text-foreground"
                >
                  <Tag className="h-2.5 w-2.5" />
                  <HighlightedText text={tag} query={searchQuery} />
                </button>
              ))}
              {tags.length > 3 ? <span className="text-[10px] text-muted-foreground">+{tags.length - 3}</span> : null}
            </div>
            {task.description ? (
              <p className="mt-0.5 line-clamp-1 text-xs text-muted-foreground">
                <HighlightedText text={task.description} query={searchQuery} />
              </p>
            ) : null}
          </div>

          {/* Assignee + lease — always visible */}
          <div className="flex w-36 shrink-0 items-center justify-end self-center">
            <button
              onClick={(e) => {
                if (!hasAssignee) return
                e.preventDefault()
                // Human owners key off `user:<id>` so the AssigneeChip
                // dropdown groups them correctly. Agent sessions and
                // peer rows keep the legacy key.
                if (task.assignee_user_id) onAssigneeClick(`user:${task.assignee_user_id}`)
                else if (task.assignee_session_id) onAssigneeClick(task.assignee_session_id)
              }}
              className={cn(
                'inline-flex items-center gap-1.5 font-mono text-[11px]',
                hasAssignee ? 'text-foreground hover:underline' : 'text-muted-foreground/60',
                humanOwner && 'text-primary/90',
              )}
              title={leaseStateTitle(leaseState, hasAssignee, task.assignee_session_id, task.assignee_user_id)}
            >
              {humanOwner ? <HumanDot /> : <LeaseDot state={leaseState} />}
              {assignee}
            </button>
          </div>

          {/* Due + updated */}
          <div className="hidden w-32 shrink-0 flex-col items-end justify-center text-right text-[11px] md:flex">
            {due.label ? (
              <span
                className={cn(
                  'font-mono',
                  due.state === 'overdue' ? 'text-red-400' : due.state === 'soon' ? 'text-amber-300' : 'text-muted-foreground',
                )}
                title={`due ${formatAbsolute(task.due_at ?? undefined)}`}
              >
                {due.state === 'overdue' ? 'overdue · ' : 'due · '}
                {due.label}
              </span>
            ) : null}
            <span className="text-[10px] text-muted-foreground/70" title={`updated ${formatAbsolute(task.updated_at)}`}>
              {formatRelative(task.updated_at)}
            </span>
          </div>

          {/* Short ID */}
          <div className="hidden w-20 shrink-0 items-center justify-end self-center lg:flex">
            <span className="font-mono text-[10px] uppercase tracking-wider text-muted-foreground/70">
              {shortTaskId(task.id)}
            </span>
          </div>
        </Link>
      </div>
    </li>
  )
}

function LeaseDot({ state }: { state: 'live' | 'doing' | 'abandoned' | 'unassigned' | 'idle' }) {
  // Visual encoding of lease/agent ownership. `live` is reserved for
  // the future heartbeat-driven mode (chunk 4 backend). Until then:
  //  - doing    → solid primary dot (claimed + actively working)
  //  - idle     → outlined primary dot (claimed but quiet)
  //  - unassigned → muted hollow dot
  switch (state) {
    case 'live':
      return (
        <span className="relative inline-flex h-2 w-2 items-center justify-center">
          <span className="absolute inline-flex h-full w-full animate-pulse-slow rounded-full bg-primary/60" />
          <span className="relative inline-flex h-1.5 w-1.5 rounded-full bg-primary" />
        </span>
      )
    case 'doing':
      return <Circle className="h-2 w-2 fill-primary/80 text-primary/80" />
    case 'abandoned':
      return <Circle className="h-2 w-2 fill-amber-500/70 text-amber-500/70" />
    case 'idle':
      return <Circle className="h-2 w-2 text-primary/60" />
    case 'unassigned':
    default:
      return <Circle className="h-2 w-2 text-muted-foreground/40" />
  }
}

// HumanDot — small user glyph for tasks assigned to a human owner
// (migration 105). No lease/heartbeat semantics apply, so the dot is
// static; the @-prefixed assignee label does the rest of the work.
function HumanDot() {
  return (
    <span className="inline-flex h-2 w-2 items-center justify-center text-[10px] text-primary/80" aria-hidden>
      @
    </span>
  )
}

function leaseStateTitle(
  state: 'live' | 'doing' | 'abandoned' | 'unassigned' | 'idle',
  hasAssignee: boolean,
  sess?: string,
  userId?: string,
): string {
  if (!hasAssignee) return 'Unassigned — anyone can claim'
  if (userId) return `Assigned to human @${userId}`
  const who = sess ? `session ${sess.slice(0, 8)}` : 'an agent'
  switch (state) {
    case 'live':
      return `${who} just touched this — actively engaged`
    case 'doing':
      return `${who} claimed this and is working on it`
    case 'abandoned':
      return `${who} hasn't touched this in >5m — anyone may reclaim`
    case 'idle':
      return `${who} owns this task`
    case 'unassigned':
    default:
      return 'Unassigned'
  }
}
