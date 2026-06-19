import type { Task, TaskStatusHistoryEntry } from '@/api/tasks'
import type { TaskEvent, TaskEventKind } from '@/hooks/use-tasks-stream'

const MAX_ACTIVITY_EVENTS = 50

export type TaskActivityTone = 'info' | 'warn' | 'success' | 'critical' | 'muted'

// buildTaskHistoryEvents turns the durable per-task status_history rows
// into the same shape the live SSE activity card already renders. This
// keeps /tasks useful after refresh/restart instead of waiting for the
// next in-memory bus event.
export function buildTaskHistoryEvents(
  tasks: Task[],
  limit = MAX_ACTIVITY_EVENTS,
): TaskEvent[] {
  const events: TaskEvent[] = []
  for (const task of tasks) {
    for (const history of task.status_history ?? []) {
      if (!history.at || !history.evt) continue
      events.push(historyToEvent(task, history))
    }
  }
  return events.sort(compareEventsDesc).slice(0, limit)
}

export function mergeTaskActivityEvents(
  liveEvents: TaskEvent[],
  historyEvents: TaskEvent[],
  limit = MAX_ACTIVITY_EVENTS,
): TaskEvent[] {
  const historyBaseKeys = new Set(historyEvents.map(baseEventKey))
  const seen = new Set<string>()
  const merged: TaskEvent[] = []
  for (const raw of [...liveEvents, ...historyEvents]) {
    const event = hydrateLiveHistory(raw)
    // Old localStorage rows predate the durable history hydrate and do
    // not carry a history entry. Prefer the durable row for the same
    // task+timestamp so the card can name the real action.
    if (!event.history && historyBaseKeys.has(baseEventKey(event))) continue
    const key = fullEventKey(event)
    if (seen.has(key)) continue
    seen.add(key)
    merged.push(event)
  }
  return merged.sort(compareEventsDesc).slice(0, limit)
}

function historyToEvent(task: Task, history: TaskStatusHistoryEntry): TaskEvent {
  return {
    kind: historyEventKind(history.evt),
    workspace_id: task.workspace_id,
    task,
    history,
    at: history.at,
  }
}

function historyEventKind(evt: string): TaskEventKind {
  switch (evt) {
    case 'created':
      return 'task_created'
    case 'deleted':
      return 'task_deleted'
    default:
      return 'task_updated'
  }
}

function hydrateLiveHistory(event: TaskEvent): TaskEvent {
  if (event.history || !event.task?.status_history?.length) return event
  const history = latestHistoryNear(event.task.status_history, event.at)
  return history ? { ...event, history } : event
}

function latestHistoryNear(
  history: TaskStatusHistoryEntry[],
  at: string,
): TaskStatusHistoryEntry | undefined {
  const target = Date.parse(at)
  let fallback: TaskStatusHistoryEntry | undefined
  for (const row of history) {
    if (!row.at) continue
    if (!fallback || eventTime(row.at) >= eventTime(fallback.at)) fallback = row
    if (Number.isFinite(target) && Math.abs(eventTime(row.at) - target) <= 1000) {
      fallback = row
    }
  }
  return fallback
}

function fullEventKey(event: TaskEvent): string {
  return `${baseEventKey(event)}:${event.history?.evt ?? ''}`
}

function baseEventKey(event: TaskEvent): string {
  const id = event.task?.id ?? event.offer?.id ?? event.note?.id ?? ''
  return `${event.kind}:${id}:${event.at}`
}

function compareEventsDesc(a: TaskEvent, b: TaskEvent): number {
  return eventTime(b.at) - eventTime(a.at)
}

function eventTime(value: string): number {
  const t = Date.parse(value)
  return Number.isFinite(t) ? t : 0
}

export function describeTaskHistoryAction(
  history: TaskStatusHistoryEntry,
  task?: Task,
): { verb: string; tone: TaskActivityTone } | null {
  switch (history.evt) {
    case 'created':
      return { verb: 'created', tone: 'info' }
    case 'status_changed':
      return {
        verb: history.to ? `moved to ${history.to}` : 'status changed',
        tone: statusMoveTone(history.to || task?.status),
      }
    case 'closed':
      return { verb: 'closed', tone: 'success' }
    case 'reopened':
      return { verb: 'reopened', tone: 'info' }
    case 'assigned':
      return { verb: 'assigned', tone: 'success' }
    case 'unassigned':
      return { verb: 'unassigned', tone: 'muted' }
    case 'work_context_updated':
      return { verb: 'context updated', tone: 'muted' }
    case 'lease_expired':
      return { verb: 'lease expired', tone: 'warn' }
    case 'composed':
      return { verb: 'composed', tone: 'info' }
    case 'decomposed':
      return { verb: 'decomposed', tone: 'muted' }
    case 'received_gossip':
      return { verb: 'received', tone: 'info' }
    case '':
      return null
    default:
      return { verb: history.evt.replace(/_/g, ' '), tone: 'muted' }
  }
}

function statusMoveTone(status?: string): TaskActivityTone {
  const s = (status || '').toLowerCase()
  if (s === 'done' || s === 'closed' || s === 'completed') return 'success'
  if (s === 'blocked' || s === 'stuck') return 'warn'
  if (s === 'cancelled' || s === 'canceled' || s === 'wontfix') return 'muted'
  if (s === 'doing' || s === 'in_progress' || s === 'in-progress' || s === 'working') return 'success'
  return 'info'
}
