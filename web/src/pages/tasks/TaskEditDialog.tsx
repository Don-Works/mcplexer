// TaskEditDialog — one modal that does create AND update. The two
// flows share enough fields (title, description, status, priority,
// due_at, tags, assignee, meta) that splitting them produces twin
// dialogs that drift apart. The submit handler picks the right API
// call from `mode`.

import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Loader2, Plus, X } from 'lucide-react'
import { toast } from 'sonner'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import type { Workspace } from '@/api/types'
import {
  createTask,
  updateTask,
  type CreateTaskBody,
  type Task,
  type UpdateTaskBody,
} from '@/api/tasks'
import { listUsers } from '@/api/client'
import { useApi } from '@/hooks/use-api'
import { cn } from '@/lib/utils'

interface BaseProps {
  open: boolean
  onOpenChange: (open: boolean) => void
  workspaces: Workspace[]
  onSaved: (task: Task) => void
}

interface CreateProps extends BaseProps {
  mode: 'create'
  defaultWorkspaceId?: string
  composeInto?: string
  initialAssignee?: string
}

interface EditProps extends BaseProps {
  mode: 'edit'
  task: Task
}

export type TaskEditDialogProps = CreateProps | EditProps

const PRIORITIES = [
  { id: 'low', label: 'Low' },
  { id: 'normal', label: 'Normal' },
  { id: 'high', label: 'High' },
  { id: 'critical', label: 'Critical' },
] as const

function toLocalInputValue(iso?: string | null): string {
  if (!iso) return ''
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return ''
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`
}

function fromLocalInputValue(s: string): string | null {
  if (!s) return null
  const d = new Date(s)
  if (Number.isNaN(d.getTime())) return null
  return d.toISOString()
}

function taskAssigneeInput(task: Task): string {
  if (task.assignee_user_id) return task.assignee_user_id
  if (task.assignee_peer_id && task.assignee_session_id) {
    return `${task.assignee_peer_id}:${task.assignee_session_id}`
  }
  return task.assignee_session_id || task.assignee_peer_id || ''
}

function taskAssigneePayload(s: string): CreateTaskBody['assignee'] {
  const v = s.trim()
  if (!v) return undefined
  if (v.startsWith('user:')) return { user_id: v.slice('user:'.length).trim() }
  const idx = v.indexOf(':')
  if (idx > 0) {
    const peerID = v.slice(0, idx).trim()
    const sessionID = v.slice(idx + 1).trim()
    return { peer_id: peerID || undefined, session_id: sessionID || undefined }
  }
  return { session_id: v }
}

export function TaskEditDialog(props: TaskEditDialogProps) {
  const isEdit = props.mode === 'edit'
  const editTask = isEdit ? props.task : null
  const editTagsKey = editTask?.tags?.join('\n') ?? ''
  const resetKey = isEdit
    ? `edit:${props.task.workspace_id}:${props.task.id}`
    : `create:${props.defaultWorkspaceId ?? ''}:${props.composeInto ?? ''}:${props.initialAssignee ?? ''}`
  const initial = useMemo<{
    workspaceId: string
    title: string
    description: string
    status: string
    priority: string
    dueAtLocal: string
    tags: string[]
    meta: string
    assignee: string
    composeInto: string
  }>(() => {
    if (isEdit) {
      const task = props.task
      return {
        workspaceId: task.workspace_id,
        title: task.title,
        description: task.description ?? '',
        status: task.status,
        priority: task.priority || 'normal',
        dueAtLocal: toLocalInputValue(task.due_at),
        tags: task.tags ?? [],
        meta: task.meta ?? '',
        assignee: taskAssigneeInput(task),
        composeInto: '',
      }
    }
    return {
      workspaceId: props.defaultWorkspaceId ?? '',
      title: '',
      description: '',
      status: 'open',
      priority: 'normal',
      dueAtLocal: '',
      tags: [],
      meta: '',
      assignee: props.initialAssignee ?? '',
      composeInto: props.composeInto ?? '',
    }
  }, [
    isEdit,
    editTask?.workspace_id,
    editTask?.id,
    editTask?.title,
    editTask?.description,
    editTask?.status,
    editTask?.priority,
    editTask?.due_at,
    editTagsKey,
    editTask?.meta,
    editTask?.assignee_user_id,
    editTask?.assignee_peer_id,
    editTask?.assignee_session_id,
    props.mode === 'create' ? props.defaultWorkspaceId : undefined,
    props.mode === 'create' ? props.initialAssignee : undefined,
    props.mode === 'create' ? props.composeInto : undefined,
  ])

  const [workspaceId, setWorkspaceId] = useState(initial.workspaceId)
  const [title, setTitle] = useState(initial.title)
  const [description, setDescription] = useState(initial.description)
  const [status, setStatus] = useState(initial.status)
  const [priority, setPriority] = useState(initial.priority)
  const [dueAtLocal, setDueAtLocal] = useState(initial.dueAtLocal)
  const [tagInput, setTagInput] = useState('')
  const [tags, setTags] = useState<string[]>(initial.tags)
  const [meta, setMeta] = useState(initial.meta)
  // `assigneeKind` lets the operator switch between session-id and
  // human-user input on the same text field. "none" means no assignee
  // will be sent (preserves the existing unassigned state); "user"
  // routes the value through `assignee.user_id` (migration 105); the
  // legacy default routes it through `assignee.session_id`.
  const [assigneeKind, setAssigneeKind] = useState<'session' | 'user' | 'none'>(
    isEdit && props.task.assignee_user_id
      ? 'user'
      : isEdit && props.task.assignee_session_id
        ? 'session'
        : 'none',
  )
  const [assignee, setAssignee] = useState(initial.assignee)
  const [composeInto, setComposeInto] = useState(initial.composeInto)
  const [busy, setBusy] = useState(false)
  const usersFetcher = useCallback(() => listUsers(), [])
  const { data: usersResponse } = useApi(usersFetcher)
  const humanUsers = usersResponse?.users ?? []
  const wasOpenRef = useRef(false)
  const lastResetKeyRef = useRef<string | null>(null)

  useEffect(() => {
    if (!props.open) {
      wasOpenRef.current = false
      return
    }
    const shouldReset = !wasOpenRef.current || lastResetKeyRef.current !== resetKey
    wasOpenRef.current = true
    if (!shouldReset) return
    lastResetKeyRef.current = resetKey

    setWorkspaceId(initial.workspaceId)
    setTitle(initial.title)
    setDescription(initial.description)
    setStatus(initial.status)
    setPriority(initial.priority)
    setDueAtLocal(initial.dueAtLocal)
    setTagInput('')
    setTags(initial.tags)
    setMeta(initial.meta)
    setAssignee(initial.assignee)
    setComposeInto(initial.composeInto)
    setAssigneeKind(
      isEdit && props.task.assignee_user_id
        ? 'user'
        : isEdit && props.task.assignee_session_id
          ? 'session'
          : 'none',
    )
  }, [props.open, resetKey, initial, isEdit, props])

  function commitTag() {
    const v = tagInput.trim()
    if (!v) return
    if (!tags.includes(v)) setTags([...tags, v])
    setTagInput('')
  }

  async function saveTask() {
    if (!workspaceId) {
      toast.error('workspace is required')
      return
    }
    if (!title.trim()) {
      toast.error('title is required')
      return
    }
    setBusy(true)
    try {
      if (isEdit) {
        const patch: UpdateTaskBody = {}
        if (title !== props.task.title) patch.title = title
        if (description !== (props.task.description ?? '')) patch.description = description
        if (status !== props.task.status) patch.status = status
        if (priority !== props.task.priority) patch.priority = priority
        const dueIso = fromLocalInputValue(dueAtLocal)
        if (dueIso !== (props.task.due_at ?? null)) patch.due_at = dueIso
        const origTags = (props.task.tags ?? []).join('\n')
        if (tags.join('\n') !== origTags) patch.tags = tags
        if (meta !== (props.task.meta ?? '')) patch.meta = meta
        // Assignee patching: emit `assignee` only when the operator
        // changed the field, and route to the right column via
        // assigneeKind. The "clear" affordance emits `clear:
        // ["assignee"]` so the server resets every identity column
        // (sess + peer + user) at once.
        if (assigneeKind === 'none' && (props.task.assignee_session_id || props.task.assignee_peer_id || props.task.assignee_user_id)) {
          patch.clear = [...(patch.clear ?? []), 'assignee']
        } else if (assigneeKind === 'user') {
          const nextUser = assignee.trim()
          if (props.task.assignee_user_id !== nextUser) {
            if (nextUser) patch.assignee = { user_id: nextUser }
            else patch.clear = [...(patch.clear ?? []), 'assignee']
          }
        } else if (assigneeKind === 'session') {
          const originalAssignee = taskAssigneeInput(props.task)
          if (assignee.trim() !== originalAssignee) {
            const nextAssignee = taskAssigneePayload(assignee)
            if (nextAssignee) patch.assignee = nextAssignee
            else patch.clear = [...(patch.clear ?? []), 'assignee']
          }
        }
        const saved = await updateTask(props.task.workspace_id, props.task.id, patch)
        props.onSaved(saved)
      } else {
        const body: CreateTaskBody = {
          workspace_id: workspaceId,
          title,
          description: description || undefined,
          status: status || undefined,
          priority,
          due_at: fromLocalInputValue(dueAtLocal),
          tags,
          meta: meta || undefined,
          compose_into: composeInto || undefined,
        }
        if (assigneeKind === 'user' && assignee.trim()) {
          body.assignee = { user_id: assignee.trim() }
        } else if (assigneeKind === 'session' && assignee.trim()) {
          body.assignee = taskAssigneePayload(assignee)
        }
        const created = await createTask(body)
        props.onSaved(created)
      }
      props.onOpenChange(false)
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Save failed')
    } finally {
      setBusy(false)
    }
  }

  return (
    <Dialog open={props.open} onOpenChange={props.onOpenChange}>
      <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-2xl">
        <form onSubmit={(e) => e.preventDefault()} className="space-y-4">
          <DialogHeader>
            <DialogTitle>{isEdit ? 'Edit task' : 'New task'}</DialogTitle>
            <DialogDescription>
              {isEdit
                ? 'Patch existing fields. The status_history audit will record this change.'
                : 'Create an operational task in a workspace. Agents will see it via task__list.'}
            </DialogDescription>
          </DialogHeader>

          {!isEdit ? (
            <Field label="Workspace" hint="Required. Tasks are scoped per workspace.">
              <select
                value={workspaceId}
                onChange={(e) => setWorkspaceId(e.target.value)}
                className="h-8 w-full border border-border bg-background px-2 text-sm"
                required
              >
                <option value="">— select workspace —</option>
                {props.workspaces.map((w) => (
                  <option key={w.id} value={w.id}>
                    {w.name}
                  </option>
                ))}
              </select>
            </Field>
          ) : null}

          <Field label="Title" hint="One line. Imperative voice plays best.">
            <Input
              value={title}
              onChange={(e) => setTitle(e.target.value)}
              autoFocus
              required
              placeholder="Patch the audit redaction for peer IDs"
            />
          </Field>

          <Field label="Description" hint="Markdown-ish; rendered as preformatted text on the detail page.">
            <Textarea
              value={description}
              onChange={(e) => setDescription(e.target.value)}
              rows={4}
              placeholder="What needs doing, and any pointers for an agent who picks it up."
            />
          </Field>

          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
            <Field label="Status" hint="Freeform. Workspace vocabulary appears as suggestions.">
              <Input
                value={status}
                onChange={(e) => setStatus(e.target.value)}
                placeholder="open"
                list="task-status-suggestions"
                className="font-mono text-sm"
              />
              <datalist id="task-status-suggestions">
                {['open', 'doing', 'blocked', 'review', 'done', 'cancelled'].map((s) => (
                  <option key={s} value={s} />
                ))}
              </datalist>
            </Field>

            <Field label="Priority">
              <div className="inline-flex border border-border">
                {PRIORITIES.map((p) => (
                  <button
                    key={p.id}
                    type="button"
                    onClick={() => setPriority(p.id)}
                    className={cn(
                      'border-r border-border px-3 py-1 text-xs last:border-r-0',
                      priority === p.id
                        ? 'bg-accent text-accent-foreground'
                        : 'bg-transparent text-muted-foreground hover:bg-muted/40 hover:text-foreground',
                    )}
                  >
                    {p.label}
                  </button>
                ))}
              </div>
            </Field>
          </div>

          <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
            <Field label="Due" hint="Local timezone; stored as RFC3339.">
              <input
                type="datetime-local"
                value={dueAtLocal}
                onChange={(e) => setDueAtLocal(e.target.value)}
                className="h-8 w-full border border-border bg-background px-2 text-sm"
              />
            </Field>

            <Field
              label="Assignee"
              hint={
                assigneeKind === 'user'
                  ? 'Human identity. People can own tasks across many devices.'
                  : 'Session id or peer:session. Blank = unassigned.'
              }
            >
              <div className="flex flex-col gap-1.5">
                <div className="inline-flex border border-border">
                  {(
                    [
                      { id: 'none', label: 'unassigned' },
                      { id: 'session', label: 'session' },
                      { id: 'user', label: 'human' },
                    ] as const
                  ).map((o) => (
                    <button
                      key={o.id}
                      type="button"
                      onClick={() => setAssigneeKind(o.id)}
                      className={cn(
                        'border-r border-border px-2.5 py-1 text-[11px] font-mono last:border-r-0',
                        assigneeKind === o.id
                          ? 'bg-accent text-accent-foreground'
                          : 'bg-transparent text-muted-foreground hover:bg-muted/40 hover:text-foreground',
                      )}
                    >
                      {o.label}
                    </button>
                  ))}
                </div>
                {assigneeKind !== 'none' ? (
                  assigneeKind === 'user' && humanUsers.length > 0 ? (
                    <select
                      value={assignee}
                      onChange={(e) => setAssignee(e.target.value)}
                      className="h-8 w-full border border-border bg-background px-2 text-sm"
                    >
                      <option value="">select human identity</option>
                      {assignee && !humanUsers.some((u) => u.user_id === assignee) ? (
                        <option value={assignee}>{assignee}</option>
                      ) : null}
                      {humanUsers.map((u) => (
                        <option key={u.user_id} value={u.user_id}>
                          {u.display_name || u.user_id}{u.is_self ? ' (you)' : ''}
                        </option>
                      ))}
                    </select>
                  ) : (
                    <Input
                      value={assignee}
                      onChange={(e) => setAssignee(e.target.value)}
                      placeholder={assigneeKind === 'user' ? 'user_id' : 'sess_xxx or peer:sess_xxx'}
                      className="font-mono text-sm"
                    />
                  )
                ) : null}
              </div>
            </Field>
          </div>

          <Field label="Tags" hint="Press Enter or comma to commit. Used in filters.">
            <div className="flex flex-wrap items-center gap-1.5 border border-border bg-background px-2 py-1.5">
              {tags.map((t) => (
                <span
                  key={t}
                  className="inline-flex items-center gap-1 border border-border bg-muted/30 px-1.5 py-0.5 font-mono text-[11px]"
                >
                  {t}
                  <button
                    type="button"
                    onClick={() => setTags(tags.filter((x) => x !== t))}
                    aria-label={`remove ${t}`}
                  >
                    <X className="h-3 w-3" />
                  </button>
                </span>
              ))}
              <input
                value={tagInput}
                onChange={(e) => setTagInput(e.target.value)}
                onKeyDown={(e) => {
                  if (e.key === 'Enter' || e.key === ',') {
                    e.preventDefault()
                    commitTag()
                  } else if (e.key === 'Backspace' && !tagInput && tags.length) {
                    setTags(tags.slice(0, -1))
                  }
                }}
                onBlur={commitTag}
                className="min-w-[120px] flex-1 bg-transparent text-sm outline-none"
                placeholder={tags.length === 0 ? 'add tag…' : ''}
              />
            </div>
          </Field>

          {!isEdit ? (
            <Field
              label="Compose into (parent epic)"
              hint="Optional task id (full ULID). Adds this task to that parent's composes list."
            >
              <Input
                value={composeInto}
                onChange={(e) => setComposeInto(e.target.value)}
                placeholder="01HXX..."
                className="font-mono text-sm"
              />
            </Field>
          ) : null}

          <Field label="Meta" hint="Frontmatter-style. composes / composed_by lines are read by the UI.">
            <Textarea
              value={meta}
              onChange={(e) => setMeta(e.target.value)}
              rows={3}
              placeholder="composes: task:abc, task:def"
              className="font-mono text-xs"
            />
          </Field>

          <DialogFooter>
            <Button type="button" variant="ghost" onClick={() => props.onOpenChange(false)} disabled={busy}>
              Cancel
            </Button>
            <Button type="button" disabled={busy} onClick={() => void saveTask()}>
              {busy ? <Loader2 className="h-4 w-4 animate-spin" /> : isEdit ? null : <Plus className="h-4 w-4" />}
              {isEdit ? 'Save changes' : 'Create task'}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

function Field({ label, hint, children }: { label: string; hint?: string; children: React.ReactNode }) {
  return (
    <div className="space-y-1">
      <div className="flex items-baseline justify-between">
        <Label className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">{label}</Label>
        {hint ? <span className="text-[10px] text-muted-foreground/70">{hint}</span> : null}
      </div>
      {children}
    </div>
  )
}
