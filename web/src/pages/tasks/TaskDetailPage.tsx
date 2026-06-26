// TaskDetailPage — the working surface for one task. Main column:
// description (markdown-ish with autolinked task refs) + notes feed
// with composer. Sidebar: status history timeline, composition view,
// quick actions (claim, edit, delete, pin).

import { useCallback, useEffect, useRef, useState } from 'react'
import { Link, useLocation, useNavigate, useParams, useSearchParams } from 'react-router-dom'
import {
  AlertTriangle,
  ArrowLeft,
  ArrowUp,
  CheckCircle2,
  ChevronRight,
  Circle,
  Hand,
  Loader2,
  Pencil,
  Pin,
  PinOff,
  Trash2,
  Undo2,
} from 'lucide-react'
import { toast } from 'sonner'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { ConfirmDialog } from '@/components/ui/confirm-dialog'
import { CopyButton } from '@/components/ui/copy-button'
import { Textarea } from '@/components/ui/textarea'
import { useApi } from '@/hooks/use-api'
import { useTasksStream } from '@/hooks/use-tasks-stream'
import { useStatusVocab } from '@/hooks/use-status-vocab'
import { useActiveMeshAgents } from '@/hooks/use-active-mesh-agents'
import { listUsers, listWorkspaces } from '@/api/client'
import {
  appendTaskNote,
  claimTask,
  deleteTask,
  getTask,
  heartbeatTask,
  listTaskNotes,
  readMetaList,
  updateTask,
  type Task,
  type TaskNote,
  type TaskStatusHistoryEntry,
} from '@/api/tasks'
import { cn } from '@/lib/utils'
import { Markdown } from '@/lib/markdown'
import {
  assigneeLabel,
  cumulativeTimeWorked,
  dueState,
  formatAbsolute,
  formatDuration,
  formatRelative,
  isHumanAssigned,
  isWorkingStatus,
  leaseStaleness,
  priorityVisual,
  shortTaskId,
  statusVisual,
  timeInCurrentState,
  useNow,
} from './task-utils'
import { TaskRef } from './TaskRef'
import { TaskEditDialog } from './TaskEditDialog'
import { WorkContextCard } from './WorkContextCard'
import { TaskAttachments } from './TaskAttachments'

// dashboardSessionId returns a stable per-browser-tab identifier used
// when this UI claims a task on the operator's behalf. The id sticks
// across soft reloads within the tab so a refresh doesn't make the
// operator "lose" the lease — but it deliberately doesn't sync across
// tabs/devices: each open dashboard is its own claimant.
// prettyMeta pretty-prints task.meta when it parses as JSON, falls
// through to the raw string otherwise (legacy frontmatter format —
// still readable as-is). Closes task 01KSJ19GV1FWQCB61NGSE9AZ57.
function prettyMeta(meta: string): string {
  const trimmed = meta.trim()
  if (!trimmed.startsWith('{') && !trimmed.startsWith('[')) return meta
  try {
    return JSON.stringify(JSON.parse(trimmed), null, 2)
  } catch {
    return meta
  }
}

const DASHBOARD_SESSION_KEY = 'mcplexer.dashboardSessionId'
function dashboardSessionId(): string {
  if (typeof window === 'undefined') return 'dashboard'
  try {
    const existing = window.sessionStorage.getItem(DASHBOARD_SESSION_KEY)
    if (existing) return existing
    const fresh = `dashboard-${crypto.randomUUID().slice(0, 8)}`
    window.sessionStorage.setItem(DASHBOARD_SESSION_KEY, fresh)
    return fresh
  } catch {
    return 'dashboard'
  }
}

export function TaskDetailPage() {
  const { id = '' } = useParams<{ id: string }>()
  const navigate = useNavigate()
  const location = useLocation()
  const [params] = useSearchParams()
  const workspaceId = params.get('workspace') ?? ''

  // pulse=true is passed via Link state when the operator clicks an
  // activity row. Surface a brief primary-tinted ring on the header so
  // the eye lands on what changed, then clear it. Mirrors the mesh
  // page's targetMsg highlight contract: same duration, same tone.
  const [pulse, setPulse] = useState(false)
  useEffect(() => {
    const wantsPulse =
      typeof location.state === 'object' &&
      location.state !== null &&
      (location.state as { pulse?: boolean }).pulse === true
    if (!wantsPulse) return
    setPulse(true)
    // Scrub the navigation state so a back/forward nav doesn't replay
    // the cue. Replace in place; keep the URL intact.
    navigate(location.pathname + location.search, { replace: true, state: null })
    const clearVisual = window.setTimeout(() => setPulse(false), 2400)
    return () => window.clearTimeout(clearVisual)
  }, [location.state, location.pathname, location.search, navigate])

  const wsFetcher = useCallback(() => listWorkspaces(), [])
  const { data: workspaces } = useApi(wsFetcher)
  const usersFetcher = useCallback(() => listUsers(), [])
  const { data: usersResponse } = useApi(usersFetcher)

  const taskFetcher = useCallback(() => {
    if (!workspaceId || !id) return Promise.resolve(null as unknown as Task)
    return getTask(workspaceId, id)
  }, [workspaceId, id])

  const { data: task, loading, error, refetch } = useApi(taskFetcher)
  const now = useNow(30_000)
  const { vocab: statusVocab } = useStatusVocab(workspaceId || null)
  const activeAgents = useActiveMeshAgents()

  const notesFetcher = useCallback(() => {
    if (!workspaceId || !id) return Promise.resolve([] as TaskNote[])
    return listTaskNotes(workspaceId, id, 200)
  }, [workspaceId, id])
  const { data: notes, refetch: refetchNotes } = useApi(notesFetcher)

  // Live updates via SSE — replaces the previous 6s polling. Filter
  // server-side by workspace and then locally by task id so we ignore
  // sibling-task chatter in the same workspace.
  useTasksStream({
    workspaceId: workspaceId || undefined,
    disabled: !workspaceId || !id,
    onEvent: (evt) => {
      const tid = evt.task?.id ?? evt.note?.task_id
      if (tid !== id) return
      if (evt.kind === 'task_note_appended') {
        refetchNotes()
        return
      }
      refetch()
      refetchNotes()
    },
  })

  // Heartbeat the row's lease every 60s while this dashboard session
  // is the assignee. Silent no-op on the server when we're not the
  // owner, so it's safe to fire defensively. Without this the 5-minute
  // backend sweep would clear the assignee even though the human is
  // demonstrably present.
  useEffect(() => {
    if (!task || !workspaceId) return
    // Heartbeat only makes sense for the dashboard's own session lease —
    // human-assigned tasks (migration 105) have no lease to bump and
    // would 404 (or worse, race with the user's own actions) if the
    // dashboard tried to extend them.
    if (task.assignee_user_id) return
    const session = dashboardSessionId()
    if (task.assignee_session_id !== session) return
    if (task.closed_at) return
    // Fire one immediately so the lease window extends to "now + 5m"
    // the instant the dashboard takes ownership, then on a 60s cadence.
    let cancelled = false
    const fire = () => {
      heartbeatTask(workspaceId, task.id, session).catch(() => {
        // Network blip — the next tick (or the SSE reconnect) will
        // resync. No toast: heartbeats should be invisible.
      })
    }
    fire()
    const handle = window.setInterval(() => {
      if (cancelled) return
      fire()
    }, 60_000)
    return () => {
      cancelled = true
      window.clearInterval(handle)
    }
  }, [task, workspaceId])

  const [busy, setBusy] = useState<string | null>(null)
  const [editOpen, setEditOpen] = useState(false)
  const [deleteOpen, setDeleteOpen] = useState(false)
  const [noteInput, setNoteInput] = useState('')
  const claimInFlightRef = useRef(false)

  // Dashboard claims should use the local human identity when it exists:
  // people own tasks, devices/sessions only hold agent leases. The synthetic
  // dashboard session remains a fallback for pre-user bootstrap installs.
  const handleClaim = useCallback(async () => {
    if (!task || claimInFlightRef.current) return
    claimInFlightRef.current = true
    setBusy('claim')
    try {
      const selfUser = usersResponse?.users?.find((u) => u.is_self)
      if (selfUser?.user_id) {
        await updateTask(task.workspace_id, task.id, {
          assignee: { user_id: selfUser.user_id },
          status: isWorkingStatus(task.status, statusVocab) ? undefined : 'doing',
        })
        toast.success(`Claimed as @${selfUser.display_name || selfUser.user_id}`)
      } else {
        await claimTask(task.workspace_id, task.id, { session_id: dashboardSessionId() })
        toast.success('Claimed')
      }
      refetch()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Claim failed')
    } finally {
      claimInFlightRef.current = false
      setBusy(null)
    }
  }, [task, usersResponse, statusVocab, refetch])

  const handleTogglePin = useCallback(async () => {
    if (!task) return
    setBusy('pin')
    try {
      await updateTask(task.workspace_id, task.id, { pinned: !task.pinned })
      refetch()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Pin failed')
    } finally {
      setBusy(null)
    }
  }, [task, refetch])

  const handleToggleTerminal = useCallback(async () => {
    if (!task) return
    setBusy('terminal')
    try {
      await updateTask(task.workspace_id, task.id, { terminal: !task.closed_at })
      toast.success(task.closed_at ? 'Reopened' : 'Closed')
      refetch()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Update failed')
    } finally {
      setBusy(null)
    }
  }, [task, refetch])

  const handleDelete = useCallback(async () => {
    if (!task) return
    setBusy('delete')
    try {
      await deleteTask(task.workspace_id, task.id)
      toast.success('Deleted')
      navigate('/tasks')
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Delete failed')
      setBusy(null)
    }
  }, [task, navigate])

  const handleAppendNote = useCallback(async () => {
    if (!task || !noteInput.trim()) return
    setBusy('note')
    try {
      await appendTaskNote(task.workspace_id, task.id, {
        body: noteInput,
        author_kind: 'user',
      })
      setNoteInput('')
      refetchNotes()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Append failed')
    } finally {
      setBusy(null)
    }
  }, [task, noteInput, refetchNotes])

  if (!workspaceId) {
    return (
      <Card>
        <CardContent className="py-8 text-center text-sm text-muted-foreground">
          A task URL needs a workspace id. <Link to="/tasks" className="text-primary hover:underline">Back to all tasks</Link>.
        </CardContent>
      </Card>
    )
  }

  if (loading && !task) {
    return (
      <div className="flex items-center gap-2 text-sm text-muted-foreground">
        <Loader2 className="h-4 w-4 animate-spin" />
        loading task {shortTaskId(id)}…
      </div>
    )
  }

  if (error || !task) {
    return (
      <Card>
        <CardContent className="space-y-3 py-8 text-center text-sm">
          <p className="text-destructive">{error ?? 'Task not found'}</p>
          <Link to="/tasks" className="inline-flex items-center gap-1 text-primary hover:underline">
            <ArrowLeft className="h-3.5 w-3.5" />
            All tasks
          </Link>
        </CardContent>
      </Card>
    )
  }

  const pv = priorityVisual(task.priority)
  const sv = statusVisual(task.status, !!task.closed_at)
  const due = dueState(task.due_at ?? null, task.closed_at ?? null)
  const composes = readMetaList(task.meta, 'composes')
  const composedBy = readMetaList(task.meta, 'composed_by')
  const history: TaskStatusHistoryEntry[] = task.status_history ?? []
  const workspaceName = workspaces?.find((w) => w.id === task.workspace_id)?.name ?? shortTaskId(task.workspace_id)
  const inStateMs = timeInCurrentState(history, now)
  const workedMs = cumulativeTimeWorked(history, now)
  const humanOwner = isHumanAssigned(task)
  const lease = leaseStaleness(
    task.status,
    task.assignee_session_id,
    task.closed_at,
    activeAgents.ready ? activeAgents.sessionIds : null,
    statusVocab,
    task.assignee_user_id,
  )
  const myDashboardSession = dashboardSessionId()
  const iOwnTheLease = !humanOwner && task.assignee_session_id === myDashboardSession
  const abandonedAssigneeLabel = task.assignee_session_id?.startsWith('dashboard')
    ? 'dashboard session'
    : assigneeLabel(task)

  return (
    <div className="space-y-5">
      {/* Breadcrumb + actions */}
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex items-center gap-2 text-sm text-muted-foreground">
          <Link to="/tasks" className="inline-flex items-center gap-1 hover:text-foreground">
            <ArrowLeft className="h-3.5 w-3.5" />
            Tasks
          </Link>
          <ChevronRight className="h-3.5 w-3.5" />
          <Link
            to={`/tasks/all?workspace=${encodeURIComponent(task.workspace_id)}`}
            className="hover:text-foreground"
          >
            {workspaceName}
          </Link>
          <ChevronRight className="h-3.5 w-3.5" />
          <span className="font-mono text-xs">{shortTaskId(task.id)}</span>
          <CopyButton value={task.id} className="h-5 w-5" />
        </div>
        <div className="flex flex-wrap items-center gap-1.5">
          <Button variant="ghost" size="sm" onClick={handleTogglePin} disabled={busy === 'pin'}>
            {task.pinned ? <PinOff className="h-4 w-4" /> : <Pin className="h-4 w-4" />}
            {task.pinned ? 'Unpin' : 'Pin'}
          </Button>
          {!task.assignee_session_id && !task.assignee_user_id ? (
            <Button variant="ghost" size="sm" onClick={handleClaim} disabled={busy === 'claim'}>
              {busy === 'claim' ? <Loader2 className="h-4 w-4 animate-spin" /> : <Hand className="h-4 w-4" />}
              Claim
            </Button>
          ) : null}
          <Button variant="ghost" size="sm" onClick={handleToggleTerminal} disabled={busy === 'terminal'}>
            {task.closed_at ? <Undo2 className="h-4 w-4" /> : <CheckCircle2 className="h-4 w-4" />}
            {task.closed_at ? 'Reopen' : 'Close'}
          </Button>
          <Button variant="ghost" size="sm" onClick={() => setEditOpen(true)}>
            <Pencil className="h-4 w-4" />
            Edit
          </Button>
          <Button variant="ghost" size="sm" onClick={() => setDeleteOpen(true)} className="text-destructive hover:text-destructive">
            <Trash2 className="h-4 w-4" />
            Delete
          </Button>
        </div>
      </div>

      {/* Abandoned banner — visible when a task is status=doing but the
          assignee hasn't touched it in HEARTBEAT_TTL_MS. Lets any
          operator reclaim it without ceremony. */}
      {lease.state === 'abandoned' && !task.closed_at ? (
        <div className="flex flex-wrap items-center justify-between gap-3 border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-sm text-amber-200">
          <span className="inline-flex items-center gap-2">
            <AlertTriangle className="h-4 w-4" />
            <span>
              Assignee <span className="font-mono">{abandonedAssigneeLabel}</span> is no longer active on the mesh. This task can be reclaimed.
            </span>
          </span>
          <Button size="sm" variant="ghost" onClick={handleClaim} disabled={busy === 'claim'}>
            {busy === 'claim' ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <Hand className="h-3.5 w-3.5" />}
            Reclaim
          </Button>
        </div>
      ) : null}

      {/* Header */}
      <header
        className={cn(
          'space-y-2 border border-border bg-card/40 p-4 transition-colors duration-700',
          pulse && 'bg-primary/10 ring-1 ring-primary/40',
        )}
      >
        <div className="flex flex-wrap items-baseline gap-2">
          <span className={cn('mt-1 inline-block h-2.5 w-2.5 shrink-0 rounded-full', pv.dot)} title={`priority: ${task.priority}`} />
          <h1 className={cn('text-xl font-semibold tracking-tight', task.closed_at ? 'line-through opacity-70' : '')}>
            {task.title}
          </h1>
          <Badge variant="outline" tone={sv.tone} className={cn('text-[11px]', sv.mono ? 'font-mono' : '')}>
            {task.status}
            {isWorkingStatus(task.status, statusVocab) && inStateMs > 0 ? (
              <span className="ml-1 font-mono text-foreground/60">· {formatDuration(inStateMs)}</span>
            ) : null}
          </Badge>
          <Badge variant="outline" tone={pv.tone} className="text-[11px]">
            {task.priority}
          </Badge>
          {iOwnTheLease ? (
            <Badge variant="outline" tone="info" className="text-[11px]">
              <Circle className="h-3 w-3 fill-primary text-primary" />
              you
            </Badge>
          ) : null}
          {humanOwner ? (
            <Badge variant="outline" tone="info" className="text-[11px]">
              <span className="mr-0.5 font-mono text-[11px]">@</span>
              human
            </Badge>
          ) : null}
          {task.pinned ? (
            <Badge variant="outline" tone="warn" className="text-[11px]">
              <Pin className="h-3 w-3" />
              pinned
            </Badge>
          ) : null}
        </div>
        <div className="flex flex-wrap items-center gap-x-4 gap-y-1 text-xs text-muted-foreground">
          <Stat
            label="assignee"
            value={
              <span className="inline-flex items-center gap-1 font-mono">
                {humanOwner ? (
                  <span className="text-[10px] text-primary/80">@</span>
                ) : (
                  <Circle
                    className={cn(
                      'h-2 w-2',
                      task.assignee_session_id || task.assignee_peer_id
                        ? 'fill-primary/80 text-primary/80'
                        : 'text-muted-foreground/40',
                    )}
                  />
                )}
                <span className={cn(humanOwner && 'text-primary/90')}>
                  {humanOwner ? `human:${task.assignee_user_id}` : assigneeLabel(task)}
                </span>
              </span>
            }
          />
          {due.label ? (
            <Stat
              label="due"
              value={
                <span
                  className={cn(
                    'font-mono',
                    due.state === 'overdue' ? 'text-red-400' : due.state === 'soon' ? 'text-amber-300' : '',
                  )}
                  title={formatAbsolute(task.due_at)}
                >
                  {due.label}
                </span>
              }
            />
          ) : null}
          <Stat label="created" value={<span title={formatAbsolute(task.created_at)}>{formatRelative(task.created_at)}</span>} />
          <Stat label="updated" value={<span title={formatAbsolute(task.updated_at)}>{formatRelative(task.updated_at)}</span>} />
          {workedMs > 0 ? (
            <Stat
              label="worked"
              value={
                <span className="font-mono" title="Cumulative time the task spent in a working status">
                  {formatDuration(workedMs)}
                </span>
              }
            />
          ) : null}
          <Stat label="source" value={<span className="font-mono uppercase tracking-wider">{task.source_kind}</span>} />
          {task.origin_peer_id ? (
            <Stat label="from peer" value={<span className="font-mono">{task.origin_peer_id.slice(-6)}</span>} />
          ) : null}
        </div>
        {(task.tags ?? []).length > 0 ? (
          <div className="flex flex-wrap items-center gap-1 pt-1">
            {(task.tags ?? []).map((t) => (
              <Link
                key={t}
                to={`/tasks?tag=${encodeURIComponent(t)}`}
                className="inline-flex items-center gap-1 border border-border bg-muted/30 px-1.5 py-0.5 font-mono text-[10px] text-muted-foreground hover:border-primary/40 hover:text-foreground"
              >
                {t}
              </Link>
            ))}
          </div>
        ) : null}
      </header>

      <div className="grid grid-cols-1 gap-5 lg:grid-cols-[1fr_320px]">
        <div className="space-y-5 min-w-0">
          {/* Description */}
          <section>
            <SectionLabel>Description</SectionLabel>
            <div className="border border-border bg-card/40 p-4">
              {task.description ? (
                <Markdown source={task.description} workspaceId={task.workspace_id} />
              ) : (
                <p className="text-xs text-muted-foreground/70">No description.</p>
              )}
            </div>
          </section>

          {/* Notes */}
          <section>
            <SectionLabel>
              Notes <span className="font-mono text-[10px] text-muted-foreground/60">({notes?.length ?? 0})</span>
            </SectionLabel>
            <div className="space-y-3">
              <div className="border border-border bg-card/40 p-3">
                <Textarea
                  value={noteInput}
                  onChange={(e) => setNoteInput(e.target.value)}
                  rows={2}
                  placeholder="append a note (visible to anyone with workspace access)…"
                  className="resize-y"
                />
                <div className="mt-2 flex items-center justify-between text-[11px] text-muted-foreground">
                  <span>Notes are append-only and audited.</span>
                  <Button size="sm" onClick={handleAppendNote} disabled={busy === 'note' || !noteInput.trim()}>
                    {busy === 'note' ? <Loader2 className="h-3 w-3 animate-spin" /> : <ArrowUp className="h-3 w-3" />}
                    Post
                  </Button>
                </div>
              </div>

              {(notes ?? []).length === 0 ? (
                <p className="px-3 py-4 text-center text-xs text-muted-foreground/60">No notes yet.</p>
              ) : (
                <ul className="space-y-2">
                  {(notes ?? []).map((n) => (
                    <NoteRow key={n.id} note={n} workspaceId={task.workspace_id} />
                  ))}
                </ul>
              )}
            </div>
          </section>
        </div>

        <aside className="space-y-5">
          {/* History timeline */}
          <section>
            <SectionLabel>Status history</SectionLabel>
            <ol className="relative space-y-2 border-l border-border pl-4">
              {[...history].reverse().map((h, i) => (
                <li key={i} className="relative">
                  <span className="absolute -left-[19px] top-1.5 inline-block h-2 w-2 rounded-full bg-primary/70" />
                  <div className="text-xs">
                    <span className="font-mono uppercase tracking-wider text-muted-foreground">{h.evt}</span>
                    {h.from || h.to ? (
                      <span className="ml-1 text-muted-foreground/80">
                        {h.from ? <span className="font-mono">{h.from}</span> : null}
                        {h.from && h.to ? <span className="mx-1 opacity-50">→</span> : null}
                        {h.to ? <span className="font-mono text-foreground">{h.to}</span> : null}
                      </span>
                    ) : null}
                  </div>
                  <div className="text-[10px] text-muted-foreground/70" title={formatAbsolute(h.at)}>
                    {formatRelative(h.at)}
                    {h.by_session ? <span className="ml-1 font-mono">· {h.by_session.slice(0, 8)}</span> : null}
                    {h.by_peer ? <span className="ml-1 font-mono">· peer:{h.by_peer.slice(-4)}</span> : null}
                  </div>
                </li>
              ))}
              {history.length === 0 ? (
                <li className="text-[11px] text-muted-foreground/60">No events yet.</li>
              ) : null}
            </ol>
          </section>

          {/* Work context — branch / PR / worktree / peer / linear / etc. */}
          <WorkContextCard task={task} onUpdate={refetch} />

          {/* Attachments — drag-drop + list (C2.4). */}
          <TaskAttachments taskId={task.id} />

          {/* Composition */}
          {composedBy.length > 0 || composes.length > 0 ? (
            <section>
              <SectionLabel>Composition</SectionLabel>
              <div className="space-y-3 text-xs">
                {composedBy.length > 0 ? (
                  <div>
                    <div className="mb-1 text-[10px] uppercase tracking-wider text-muted-foreground">part of</div>
                    <ul className="space-y-1">
                      {composedBy.map((pid) => (
                        <li key={pid}>
                          <TaskRef id={pid} workspaceId={task.workspace_id} variant="inline" />
                        </li>
                      ))}
                    </ul>
                  </div>
                ) : null}
                {composes.length > 0 ? (
                  <div>
                    <div className="mb-1 text-[10px] uppercase tracking-wider text-muted-foreground">epic of</div>
                    <ul className="space-y-1">
                      {composes.map((cid) => (
                        <li key={cid}>
                          <TaskRef id={cid} workspaceId={task.workspace_id} variant="inline" />
                        </li>
                      ))}
                    </ul>
                  </div>
                ) : null}
              </div>
            </section>
          ) : null}

          {/* Raw meta — pretty-print when it's JSON, otherwise show as-is. */}
          {task.meta ? (
            <section>
              <SectionLabel>Meta</SectionLabel>
              <pre className="border border-border bg-card/40 p-2 font-mono text-[10px] text-muted-foreground/80 overflow-x-auto whitespace-pre-wrap">{prettyMeta(task.meta)}</pre>
            </section>
          ) : null}
        </aside>
      </div>

      <TaskEditDialog
        mode="edit"
        task={task}
        open={editOpen}
        onOpenChange={setEditOpen}
        workspaces={workspaces ?? []}
        onSaved={(t) => {
          toast.success('Saved')
          refetch()
          // If workspace changes (shouldn't happen here) keep URL in sync.
          if (t.workspace_id !== workspaceId) {
            navigate(`/tasks/${encodeURIComponent(t.id)}?workspace=${encodeURIComponent(t.workspace_id)}`, { replace: true })
          }
        }}
      />

      <ConfirmDialog
        open={deleteOpen}
        onOpenChange={setDeleteOpen}
        title="Delete this task?"
        description={`Soft-delete ${shortTaskId(task.id)}. The row stays in the DB with deleted_at set so the audit trail survives; this is reversible (an admin can call task__update with deleted_at=null via the gateway's admin MCP tools).`}
        confirmLabel="Delete"
        variant="destructive"
        onConfirm={handleDelete}
      />
    </div>
  )
}

function SectionLabel({ children }: { children: React.ReactNode }) {
  return (
    <h2 className="mb-2 text-[11px] font-semibold uppercase tracking-wider text-muted-foreground">{children}</h2>
  )
}

function Stat({ label, value }: { label: string; value: React.ReactNode }) {
  return (
    <span className="inline-flex items-center gap-1">
      <span className="text-[10px] uppercase tracking-wider text-muted-foreground/70">{label}</span>
      {value}
    </span>
  )
}

function NoteRow({ note, workspaceId }: { note: TaskNote; workspaceId: string }) {
  return (
    <li className="border border-border bg-card/40 p-3">
      <div className="mb-1 flex items-center justify-between text-[10px] text-muted-foreground/70">
        <span className="inline-flex items-center gap-2">
          <span className="font-mono uppercase tracking-wider">{note.author_kind ?? 'agent'}</span>
          {note.author_session_id ? <span className="font-mono">· {note.author_session_id.slice(0, 8)}</span> : null}
        </span>
        <span title={formatAbsolute(note.created_at)}>{formatRelative(note.created_at)}</span>
      </div>
      <Markdown source={note.body} workspaceId={workspaceId} />

    </li>
  )
}
