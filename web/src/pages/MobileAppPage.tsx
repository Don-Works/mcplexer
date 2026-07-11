import { useCallback, useEffect, useMemo, useState } from 'react'
import { Link } from 'react-router-dom'
import {
  Bell,
  BellRing,
  CheckCircle2,
  Filter,
  Inbox,
  ListTodo,
  Plus,
  Search,
  ShieldCheck,
} from 'lucide-react'
import { toast } from 'sonner'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { EmptyState } from '@/components/ui/empty-state'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { PendingCard } from '@/pages/approvals/PendingCard'
import { ApprovalDetailSheet } from '@/pages/approvals/ApprovalDetailSheet'
import { TaskEditDialog } from '@/pages/tasks/TaskEditDialog'
import {
  assigneeLabel,
  dueState,
  formatAbsolute,
  formatRelative,
  priorityVisual,
  shortTaskId,
  statusVisual,
  useNow,
} from '@/pages/tasks/task-utils'
import { getHealth, listApprovals, listUsers, listWorkspaces, type HealthResponse } from '@/api/client'
import {
  getPushPublicKey,
  subscribePush,
  unsubscribePush,
  type BrowserPushSubscriptionJSON,
} from '@/api/notifications'
import { listTasks, type Task, type TaskListFilter } from '@/api/tasks'
import type { ToolApproval, User, Workspace } from '@/api/types'
import { useApi } from '@/hooks/use-api'
import { useApprovalStream } from '@/hooks/use-approval-stream'
import { useTasksStream } from '@/hooks/use-tasks-stream'
import { cn } from '@/lib/utils'

type TaskView = 'human' | 'mine' | 'all'
type PriorityFilter = 'all' | 'critical' | 'high' | 'normal' | 'low'
type PushState = 'checking' | 'unsupported' | 'needs_https' | 'blocked' | 'off' | 'on'

const TASK_VIEWS: Array<{ id: TaskView; label: string }> = [
  { id: 'human', label: 'Human' },
  { id: 'mine', label: 'Mine' },
  { id: 'all', label: 'All open' },
]

const PRIORITY_FILTERS: Array<{ id: PriorityFilter; label: string }> = [
  { id: 'all', label: 'All' },
  { id: 'critical', label: 'Critical' },
  { id: 'high', label: 'High' },
  { id: 'normal', label: 'Normal' },
  { id: 'low', label: 'Low' },
]

const PRIORITY_RANK: Record<string, number> = {
  critical: 0,
  high: 1,
  normal: 2,
  low: 3,
}

interface NavigatorWithStandalone extends Navigator {
  standalone?: boolean
}

function useNowTick(): number {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const id = window.setInterval(() => setNow(Date.now()), 1000)
    return () => window.clearInterval(id)
  }, [])
  return now
}

function useDebouncedValue<T>(value: T, delayMs: number): T {
  const [debounced, setDebounced] = useState(value)
  useEffect(() => {
    const id = window.setTimeout(() => setDebounced(value), delayMs)
    return () => window.clearTimeout(id)
  }, [value, delayMs])
  return debounced
}

function isStandaloneMode(): boolean {
  if (typeof window === 'undefined') return false
  const nav = window.navigator as NavigatorWithStandalone
  return Boolean(nav.standalone) || window.matchMedia('(display-mode: standalone)').matches
}

function normalizeHost(host: string): string {
  return host.trim().toLowerCase().replace(/^\[/, '').replace(/\]$/, '').replace(/\.$/, '')
}

function loopbackHost(host: string): boolean {
  const h = normalizeHost(host)
  return h === 'localhost' || h === '::1' || h.startsWith('127.')
}

function hostnameTrusted(host: string, trustedHosts?: string[]): boolean {
  const h = normalizeHost(host)
  if (loopbackHost(h)) return true
  return (trustedHosts ?? []).map(normalizeHost).includes(h)
}

function secureOrigin(): boolean {
  if (typeof window === 'undefined') return false
  if (window.isSecureContext) return true
  return loopbackHost(window.location.hostname)
}

function mergeApprovals(live: ToolApproval[], loaded: ToolApproval[] | null, resolvedIds: string[]): ToolApproval[] {
  const seen = new Set<string>()
  const resolved = new Set(resolvedIds)
  const out: ToolApproval[] = []
  for (const approval of [...live, ...(loaded ?? [])]) {
    if (resolved.has(approval.id)) continue
    if (seen.has(approval.id)) continue
    seen.add(approval.id)
    out.push(approval)
  }
  return out
}

function sortTasks(rows: Task[]): Task[] {
  return [...rows].sort((a, b) => {
    const pa = PRIORITY_RANK[a.priority] ?? 2
    const pb = PRIORITY_RANK[b.priority] ?? 2
    if (pa !== pb) return pa - pb
    const da = a.due_at ? new Date(a.due_at).getTime() : Number.POSITIVE_INFINITY
    const db = b.due_at ? new Date(b.due_at).getTime() : Number.POSITIVE_INFINITY
    if (da !== db) return da - db
    return new Date(b.updated_at).getTime() - new Date(a.updated_at).getTime()
  })
}

function workspaceLabel(workspaces: Workspace[], id: string): string {
  return workspaces.find((w) => w.id === id)?.name || id
}

function humanLabel(task: Task, users: Map<string, User>): string {
  const userID = task.assignee_user_id?.trim()
  if (!userID) return assigneeLabel(task)
  const user = users.get(userID)
  return `@${user?.display_name || userID}`
}

function urlBase64ToArrayBuffer(value: string): ArrayBuffer {
  const padding = '='.repeat((4 - (value.length % 4)) % 4)
  const base64 = (value + padding).replace(/-/g, '+').replace(/_/g, '/')
  const raw = window.atob(base64)
  const out = new Uint8Array(raw.length)
  for (let i = 0; i < raw.length; i++) out[i] = raw.charCodeAt(i)
  return out.buffer as ArrayBuffer
}

export function MobileAppPage() {
  const { pending, connected, resolvedIds } = useApprovalStream()
  const now = useNowTick()
  useNow(30_000)

  const pendingFetcher = useCallback(() => listApprovals('pending'), [])
  const { data: dbPending, refetch: refetchPending } = useApi(pendingFetcher)

  const usersFetcher = useCallback(() => listUsers(), [])
  const { data: usersResponse } = useApi(usersFetcher)
  const users = useMemo(() => usersResponse?.users ?? [], [usersResponse?.users])
  const selfUser = users.find((u) => u.is_self)
  const usersById = useMemo(() => new Map(users.map((u) => [u.user_id, u])), [users])

  const workspacesFetcher = useCallback((signal: AbortSignal) => listWorkspaces({ signal }), [])
  const { data: workspacesData } = useApi(workspacesFetcher)
  const workspaces = workspacesData ?? []

  const healthFetcher = useCallback(() => getHealth().catch(() => null), [])
  const { data: health } = useApi<HealthResponse | null>(healthFetcher)

  const [taskView, setTaskView] = useState<TaskView>('human')
  const [workspaceFilter, setWorkspaceFilter] = useState('all')
  const [priorityFilter, setPriorityFilter] = useState<PriorityFilter>('all')
  const [query, setQuery] = useState('')
  const debouncedQuery = useDebouncedValue(query.trim(), 250)
  const [createOpen, setCreateOpen] = useState(false)
  const [selectedApproval, setSelectedApproval] = useState<ToolApproval | null>(null)

  const effectiveTaskView = taskView === 'mine' && !selfUser ? 'human' : taskView
  const tasksFetcher = useCallback(() => {
    const filter: TaskListFilter = {
      state: 'open',
      limit: 200,
      workspace_id: workspaceFilter === 'all' ? undefined : workspaceFilter,
      q: debouncedQuery || undefined,
    }
    if (effectiveTaskView === 'human' || effectiveTaskView === 'mine') {
      filter.assignee_origin_kind = 'human'
    }
    if (effectiveTaskView === 'mine' && selfUser?.user_id) {
      filter.assignee_user_id = selfUser.user_id
    }
    return listTasks(filter)
  }, [debouncedQuery, effectiveTaskView, selfUser?.user_id, workspaceFilter])
  const { data: tasksData, loading: tasksLoading, error: tasksError, refetch: refetchTasks } = useApi(tasksFetcher)

  useTasksStream({
    workspaceId: workspaceFilter === 'all' ? undefined : workspaceFilter,
    onEvent: () => refetchTasks(),
  })

  const allPending = useMemo(() => mergeApprovals(pending, dbPending, resolvedIds), [pending, dbPending, resolvedIds])
  const filteredTasks = useMemo(() => {
    const rows = tasksData ?? []
    const priorityRows = priorityFilter === 'all'
      ? rows
      : rows.filter((task) => task.priority === priorityFilter)
    return sortTasks(priorityRows)
  }, [priorityFilter, tasksData])

  const defaultWorkspaceId = workspaceFilter !== 'all'
    ? workspaceFilter
    : workspaces[0]?.id

  const handleApprovalResolved = useCallback(() => {
    setSelectedApproval(null)
    refetchPending()
  }, [refetchPending])

  const handleTaskSaved = useCallback((task: Task) => {
    toast.success(`Created task ${shortTaskId(task.id)}`)
    refetchTasks()
  }, [refetchTasks])

  return (
    <div className="mx-auto flex min-h-[calc(100dvh-7rem)] w-full max-w-6xl flex-col gap-5 pb-14">
      <header className="flex flex-col gap-3 sm:flex-row sm:items-end sm:justify-between">
        <div className="min-w-0 space-y-1">
          <h1 className="text-xl font-semibold text-foreground">
            Human inbox
          </h1>
          <p className="text-xs text-muted-foreground">
            Approvals and human-assigned tasks.
          </p>
        </div>
        <div className="flex gap-2">
          <Button size="sm" variant="outline" onClick={() => setCreateOpen(true)} disabled={workspaces.length === 0}>
            <Plus className="h-4 w-4" />
            New task
          </Button>
        </div>
      </header>

      <StatusStrip
        health={health}
        connected={connected}
        pendingCount={allPending.length}
        taskCount={filteredTasks.length}
      />

      <section className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_minmax(400px,1fr)]">
        <ApprovalsPanel
          approvals={allPending}
          connected={connected}
          now={now}
          onResolved={handleApprovalResolved}
          onOpenDetail={setSelectedApproval}
        />

        <TasksPanel
          tasks={filteredTasks}
          tasksLoading={tasksLoading}
          tasksError={tasksError}
          taskView={taskView}
          setTaskView={setTaskView}
          workspaceFilter={workspaceFilter}
          setWorkspaceFilter={setWorkspaceFilter}
          priorityFilter={priorityFilter}
          setPriorityFilter={setPriorityFilter}
          query={query}
          setQuery={setQuery}
          workspaces={workspaces}
          usersById={usersById}
          selfUser={selfUser}
        />
      </section>

      <ApprovalDetailSheet
        approval={selectedApproval}
        now={now}
        onOpenChange={(open) => !open && setSelectedApproval(null)}
        onResolved={handleApprovalResolved}
      />

      <TaskEditDialog
        mode="create"
        open={createOpen}
        onOpenChange={setCreateOpen}
        workspaces={workspaces}
        defaultWorkspaceId={defaultWorkspaceId}
        initialAssignee={selfUser?.user_id ?? ''}
        initialAssigneeKind="user"
        onSaved={handleTaskSaved}
      />
    </div>
  )
}

function StatusStrip({
  health,
  connected,
  pendingCount,
  taskCount,
}: {
  health: HealthResponse | null
  connected: boolean
  pendingCount: number
  taskCount: number
}) {
  const [standalone, setStandalone] = useState(() => isStandaloneMode())
  const [pushState, setPushState] = useState<PushState>('checking')
  const [pushBusy, setPushBusy] = useState(false)
  const [expanded, setExpanded] = useState(false)
  const secure = secureOrigin()
  const trusted = hostnameTrusted(window.location.hostname, health?.system?.trusted_hosts)

  const refreshPushState = useCallback(async () => {
    if (typeof window === 'undefined') return
    if (!('Notification' in window) || !('serviceWorker' in navigator) || !('PushManager' in window)) {
      setPushState('unsupported')
      return
    }
    if (!secureOrigin()) { setPushState('needs_https'); return }
    if (window.Notification.permission === 'denied') { setPushState('blocked'); return }
    try {
      const registration = await navigator.serviceWorker.ready
      const sub = await registration.pushManager.getSubscription()
      if (!sub) {
        setPushState('off')
        return
      }
      // Refresh the server row on load. This repairs subscriptions the push
      // service temporarily disabled without asking for permission again.
      await subscribePush(
        sub.toJSON() as BrowserPushSubscriptionJSON,
        isStandaloneMode() ? 'PWA' : 'Browser',
      )
      setPushState('on')
    } catch { setPushState('off') }
  }, [])

  useEffect(() => {
    const mql = window.matchMedia('(display-mode: standalone)')
    const onChange = () => setStandalone(isStandaloneMode())
    mql.addEventListener('change', onChange)
    return () => mql.removeEventListener('change', onChange)
  }, [])

  useEffect(() => { void refreshPushState() }, [refreshPushState])

  async function togglePush() {
    if (!('Notification' in window) || !('serviceWorker' in navigator) || !('PushManager' in window)) {
      toast.error('Push notifications are not supported'); return
    }
    if (!secureOrigin()) { toast.error('Push needs HTTPS or localhost'); return }
    if (pushState === 'blocked') {
      toast.error('Allow notifications for this site in browser settings, then reload')
      return
    }
    setPushBusy(true)
    try {
      if (pushState === 'on') {
        const registration = await navigator.serviceWorker.ready
        const sub = await registration.pushManager.getSubscription()
        if (sub) { await sub.unsubscribe(); await unsubscribePush(sub.endpoint) }
        await refreshPushState()
        toast.success('Push disabled')
      } else {
        const result = await window.Notification.requestPermission()
        if (result !== 'granted') {
          setPushState(result === 'denied' ? 'blocked' : 'off')
          toast.error(result === 'denied'
            ? 'Notifications are blocked in browser settings'
            : 'Notification permission was not granted')
          return
        }
        const registration = await navigator.serviceWorker.ready
        let sub = await registration.pushManager.getSubscription()
        if (!sub) {
          const key = await getPushPublicKey()
          sub = await registration.pushManager.subscribe({
            userVisibleOnly: true,
            applicationServerKey: urlBase64ToArrayBuffer(key.public_key),
          })
        }
        await subscribePush(sub.toJSON() as BrowserPushSubscriptionJSON, isStandaloneMode() ? 'PWA' : 'Browser')
        await refreshPushState()
        toast.success('Push enabled')
      }
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Push failed')
    } finally { setPushBusy(false) }
  }

  const appLabel = standalone ? 'installed' : secure ? 'browser' : 'no HTTPS'
  const pushLabel = pushState === 'on'
    ? 'on'
    : pushState === 'blocked'
      ? 'blocked'
      : pushState === 'checking'
        ? 'checking'
        : pushState === 'unsupported' || pushState === 'needs_https'
          ? 'unavailable'
          : 'off'

  return (
    <div className="space-y-1">
      <button
        type="button"
        onClick={() => setExpanded(!expanded)}
        aria-expanded={expanded}
        aria-controls="pwa-notification-status"
        className="flex w-full items-center gap-3 border border-border bg-card px-3 py-2 text-left text-sm transition-colors hover:bg-muted/30 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
      >
        <span aria-hidden="true" className={cn('h-2 w-2', connected ? 'bg-emerald-400' : 'bg-amber-400 animate-pulse')} />
        <span className="font-mono text-xs text-muted-foreground">{pendingCount > 0 ? `${pendingCount} pending` : 'clear'}</span>
        <span aria-hidden="true" className="text-border">|</span>
        <span className="font-mono text-xs text-muted-foreground">{taskCount} tasks</span>
        <span aria-hidden="true" className="text-border">|</span>
        <span className="font-mono text-xs text-muted-foreground">{appLabel}</span>
        <span aria-hidden="true" className="ml-auto text-[10px] text-muted-foreground">{expanded ? 'hide' : 'status'}</span>
      </button>

      {expanded ? (
        <div id="pwa-notification-status" className="flex flex-wrap items-center justify-between gap-3 border border-border bg-card p-3">
          <div className="min-w-0 space-y-2">
            <div className="flex flex-wrap items-center gap-1.5">
              <Badge tone={connected ? 'success' : 'warn'} variant="outline" className="font-mono text-[10px]">
                stream {connected ? 'live' : 'syncing'}
              </Badge>
              <Badge tone={standalone ? 'success' : secure ? 'info' : 'warn'} variant="outline" className="font-mono text-[10px]">
                {appLabel}
              </Badge>
              <Badge tone={secure ? 'success' : 'warn'} variant="outline" className="font-mono text-[10px]">
                {secure ? 'HTTPS' : 'HTTP'}
              </Badge>
              <Badge tone={trusted ? 'success' : 'warn'} variant="outline" className="font-mono text-[10px]">
                {trusted ? 'trusted' : 'untrusted'}
              </Badge>
            </div>
            <p className="max-w-[62ch] text-xs leading-relaxed text-muted-foreground">
              Only approvals, human task assignments, due tasks, memory offers, and high-severity alerts.
            </p>
          </div>
          <div className="flex items-center gap-2">
            <span aria-live="polite" className="flex items-center gap-1.5 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">
              <Bell className="h-3.5 w-3.5" />
              Push {pushLabel}
            </span>
            <Button
              size="sm"
              variant={pushState === 'on' ? 'default' : 'outline'}
              onClick={togglePush}
              disabled={pushBusy}
            >
              {pushState === 'on' ? <CheckCircle2 className="mr-1 h-4 w-4" /> : <BellRing className="mr-1 h-4 w-4" />}
              {pushBusy ? 'Working' : pushState === 'on' ? 'Disable' : pushState === 'blocked' ? 'How to enable' : 'Enable'}
            </Button>
          </div>
        </div>
      ) : null}
    </div>
  )
}

function ApprovalsPanel({
  approvals,
  connected,
  now,
  onResolved,
  onOpenDetail,
}: {
  approvals: ToolApproval[]
  connected: boolean
  now: number
  onResolved: () => void
  onOpenDetail: (approval: ToolApproval) => void
}) {
  return (
    <section className="min-w-0 space-y-3">
      <div className="flex items-center justify-between gap-3">
        <div>
          <h2 className="flex items-center gap-2 text-sm font-semibold uppercase tracking-wider text-muted-foreground">
            <ShieldCheck className="h-4 w-4" />
            Approvals
          </h2>
          <p className="mt-1 text-xs text-muted-foreground">
            {connected ? 'Live approval stream' : 'Reconnecting to approval stream'}
          </p>
        </div>
        <Badge tone={approvals.length > 0 ? 'warn' : 'success'} variant="outline" className="font-mono">
          {approvals.length} pending
        </Badge>
      </div>

      {approvals.length > 0 ? (
        <div className="grid gap-3">
          {approvals.map((approval) => (
            <PendingCard
              key={approval.id}
              approval={approval}
              onResolved={onResolved}
              now={now}
              onOpenDetail={() => onOpenDetail(approval)}
            />
          ))}
        </div>
      ) : (
        <EmptyState
          icon={<Inbox className="h-6 w-6" />}
          title="No approvals waiting"
          description="New approval requests appear here with approve and deny actions."
          testid="approvals-empty"
        />
      )}
    </section>
  )
}

function TasksPanel({
  tasks,
  tasksLoading,
  tasksError,
  taskView,
  setTaskView,
  workspaceFilter,
  setWorkspaceFilter,
  priorityFilter,
  setPriorityFilter,
  query,
  setQuery,
  workspaces,
  usersById,
  selfUser,
}: {
  tasks: Task[]
  tasksLoading: boolean
  tasksError: string | null
  taskView: TaskView
  setTaskView: (view: TaskView) => void
  workspaceFilter: string
  setWorkspaceFilter: (value: string) => void
  priorityFilter: PriorityFilter
  setPriorityFilter: (value: PriorityFilter) => void
  query: string
  setQuery: (value: string) => void
  workspaces: Workspace[]
  usersById: Map<string, User>
  selfUser?: User
}) {
  const filtersActive =
    query.trim() !== '' || workspaceFilter !== 'all' || priorityFilter !== 'all' || taskView !== 'human'
  const clearFilters = () => {
    setQuery('')
    setWorkspaceFilter('all')
    setPriorityFilter('all')
    setTaskView('human')
  }

  return (
    <section className="min-w-0 space-y-3">
      <div className="flex items-center justify-between gap-3">
        <div>
          <h2 className="flex items-center gap-2 text-sm font-semibold uppercase tracking-wider text-muted-foreground">
            <ListTodo className="h-4 w-4" />
            Tasks
          </h2>
          <p className="mt-1 text-xs text-muted-foreground">
            Human-owned work plus the wider open queue.
          </p>
        </div>
        <Badge tone="info" variant="outline" className="font-mono">
          {tasks.length} shown
        </Badge>
      </div>

      <div className="space-y-2 border border-border bg-card p-3">
        <label className="relative block">
          <Search className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
          <Input
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="Search tasks"
            className="h-10 pl-9 text-base sm:text-sm"
            aria-label="Search tasks"
          />
        </label>

        <SegmentedControl
          icon={<Filter className="h-3.5 w-3.5" />}
          items={TASK_VIEWS}
          value={taskView}
          onChange={setTaskView}
          disabled={(item) => item.id === 'mine' && !selfUser}
        />

        <div className="grid gap-2 sm:grid-cols-2">
          <Select value={workspaceFilter} onValueChange={setWorkspaceFilter}>
            <SelectTrigger className="h-10 w-full" aria-label="Workspace filter">
              <SelectValue placeholder="All workspaces" />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">All workspaces</SelectItem>
              {workspaces.map((workspace) => (
                <SelectItem key={workspace.id} value={workspace.id}>
                  {workspace.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Select value={priorityFilter} onValueChange={(value) => setPriorityFilter(value as PriorityFilter)}>
            <SelectTrigger className="h-10 w-full" aria-label="Priority filter">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              {PRIORITY_FILTERS.map((p) => (
                <SelectItem key={p.id} value={p.id}>
                  {p.id === 'all' ? 'All priorities' : `${p.label} priority`}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
      </div>

      {tasksError ? (
        <div className="border border-destructive/40 bg-destructive/10 p-3 text-sm text-destructive">
          {tasksError}
        </div>
      ) : null}

      {tasksLoading && tasks.length === 0 ? (
        <div className="border border-border bg-card p-5 text-sm text-muted-foreground">
          Loading tasks
        </div>
      ) : tasks.length > 0 ? (
        <div className="grid gap-2">
          {tasks.map((task) => (
            <TaskRow
              key={`${task.workspace_id}:${task.id}`}
              task={task}
              workspaces={workspaces}
              usersById={usersById}
            />
          ))}
        </div>
      ) : (
        <EmptyState
          icon={<ListTodo className="h-6 w-6" />}
          title={filtersActive ? 'No tasks match these filters' : 'No open tasks'}
          description={
            filtersActive
              ? 'Nothing matches the current view, workspace, priority, or search.'
              : 'Human-assigned tasks and the wider open queue appear here.'
          }
          action={
            filtersActive ? (
              <Button size="sm" variant="outline" onClick={clearFilters}>
                Clear filters
              </Button>
            ) : undefined
          }
          testid="tasks-empty"
        />
      )}
    </section>
  )
}

function SegmentedControl<T extends string>({
  icon,
  items,
  value,
  onChange,
  disabled,
}: {
  icon?: React.ReactNode
  items: Array<{ id: T; label: string }>
  value: T
  onChange: (value: T) => void
  disabled?: (item: { id: T; label: string }) => boolean
}) {
  return (
    <div className="flex min-w-0 overflow-x-auto border border-border">
      {icon ? (
        <div className="flex h-10 items-center border-r border-border px-3 text-muted-foreground">
          {icon}
        </div>
      ) : null}
      {items.map((item) => {
        const active = item.id === value
        const isDisabled = disabled?.(item) ?? false
        return (
          <button
            key={item.id}
            type="button"
            disabled={isDisabled}
            onClick={() => onChange(item.id)}
            className={cn(
              'h-10 shrink-0 border-r border-border px-3 text-xs font-medium last:border-r-0',
              active
                ? 'bg-accent text-accent-foreground'
                : 'text-muted-foreground hover:bg-muted/40 hover:text-foreground',
              isDisabled && 'cursor-not-allowed opacity-40 hover:bg-transparent hover:text-muted-foreground',
            )}
          >
            {item.label}
          </button>
        )
      })}
    </div>
  )
}

function TaskRow({
  task,
  workspaces,
  usersById,
}: {
  task: Task
  workspaces: Workspace[]
  usersById: Map<string, User>
}) {
  const priority = priorityVisual(task.priority)
  const status = statusVisual(task.status, Boolean(task.closed_at))
  const due = dueState(task.due_at, task.closed_at)
  const href = `/tasks/${encodeURIComponent(task.id)}?workspace=${encodeURIComponent(task.workspace_id)}`
  return (
    <Link
      to={href}
      className="group block border border-border bg-card p-3 transition-colors hover:border-primary/40 hover:bg-muted/20"
    >
      <div className="flex min-w-0 items-start justify-between gap-3">
        <div className="min-w-0 space-y-1">
          <div className="flex min-w-0 items-center gap-2">
            <span className={cn('h-2.5 w-2.5 shrink-0', priority.dot)} />
            <h3 className="line-clamp-2 text-sm font-medium leading-snug text-foreground group-hover:text-primary">
              {task.title}
            </h3>
          </div>
          <div className="flex flex-wrap items-center gap-1.5 text-[11px] text-muted-foreground">
            <span className="font-mono">task:{shortTaskId(task.id)}</span>
            <span className="hidden sm:inline">{workspaceLabel(workspaces, task.workspace_id)}</span>
            <span>{humanLabel(task, usersById)}</span>
            <span title={formatAbsolute(task.updated_at)}>updated {formatRelative(task.updated_at)}</span>
          </div>
        </div>
        <div className="flex shrink-0 flex-col items-end gap-1">
          <Badge tone={priority.tone} variant="outline" className="font-mono uppercase">
            {task.priority}
          </Badge>
          <Badge tone={status.tone} variant="outline" className={cn('font-mono uppercase', status.mono && 'tracking-wider')}>
            {task.status}
          </Badge>
        </div>
      </div>
      {task.description ? (
        <p className="mt-2 line-clamp-2 text-sm leading-relaxed text-muted-foreground">
          {task.description}
        </p>
      ) : null}
      <div className="mt-2 flex flex-wrap items-center gap-1.5">
        {due.state !== 'none' ? (
          <Badge
            tone={due.state === 'overdue' ? 'critical' : due.state === 'soon' ? 'warn' : 'muted'}
            variant="outline"
            className="font-mono"
          >
            due {due.label}
          </Badge>
        ) : null}
        {(task.tags ?? []).slice(0, 3).map((tag) => (
          <Badge key={tag} tone="muted" variant="outline" className="font-mono">
            {tag}
          </Badge>
        ))}
        {(task.tags?.length ?? 0) > 3 ? (
          <Badge tone="muted" variant="outline" className="font-mono">
            +{(task.tags?.length ?? 0) - 3}
          </Badge>
        ) : null}
      </div>
    </Link>
  )
}
