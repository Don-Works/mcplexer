// Shared utilities for the tasks UI: time formatting that defers to
// the local timezone, priority/status visual mapping, and the
// freeform-text autolinker that turns `task:<id>` references in mesh
// messages / audit rows / notes / worker output into clickable Links.

import { Link } from 'react-router-dom'
import { ListTodo } from 'lucide-react'
import { useEffect, useState, type ReactNode } from 'react'
import { cn } from '@/lib/utils'
import { readMetaList, type StatusKind, type Task, type TaskStatusHistoryEntry } from '@/api/tasks'

// StatusKindMap — what useStatusVocab returns: status_text → kind.
// Passed into isWorkingStatus / kindOfStatus / cumulativeTimeWorked so
// freeform statuses (triaging, coding, paused, …) classify correctly
// without each page hardcoding the bucket vocabulary.
export type StatusKindMap = ReadonlyMap<string, StatusKind | string>

// WORKING_STATUSES is the **fallback** set used when the workspace's
// task_status_vocabulary hasn't classified a status. Status is
// freeform per workspace, so the canonical answer to "is this
// 'working'?" should come from the vocab table — agents are free to
// coin `triaging`, `coding`, `paused`, etc., and the vocab declares
// which ones count as in-progress. This fallback exists only so a
// fresh install with no vocab declarations still classifies the six
// suggested defaults sensibly. Once the vocab kind column lands
// (see [[task-status-vocab-kind]]), components should prefer the
// vocab lookup and fall through to this set only when silent.
const WORKING_STATUSES = new Set(['doing', 'in_progress', 'in-progress', 'running', 'active', 'wip'])

// CLOSED_EVENTS are status_history evt values that should be treated
// as terminal for time-worked accumulation. Anything in this set ends
// an open "working" interval at the entry's timestamp.
const CLOSED_EVENTS = new Set(['closed'])

// HEARTBEAT_TTL_MS — how long we treat a recent updated_at as proof
// the assignee is still actively engaged. Five minutes mirrors the
// planned backend lease TTL so the frontend "abandoned" heuristic
// stays consistent once the proper lease column lands.
export const HEARTBEAT_TTL_MS = 5 * 60 * 1000

// ULID format: 26 chars in Crockford base32 (case-insensitive in
// practice — store generates uppercase via ulid.Make). Bracket the
// match so a trailing punctuation char doesn't get consumed.
const TASK_ID_PATTERN = /task:([0-9A-HJKMNP-TV-Za-hjkmnp-tv-z]{26})\b/

// Conservative fallback for older/test IDs that don't follow ULID.
// Reserved for explicit task:`anything` mentions where ULID failed.
// Combined into one regex so we don't double-match.
const TASK_REF_PATTERN = /task:([0-9A-HJKMNP-TV-Za-hjkmnp-tv-z]{26}|[A-Za-z0-9_-]{6,40})/g

export interface AutolinkOptions {
  workspaceId?: string
  // resolveWorkspace — given a task id, return its workspace_id if
  // known. Lets a caller that has access to a list of currently-known
  // tasks avoid passing an empty workspace_id in the link URL.
  resolveWorkspace?: (taskId: string) => string | undefined
  className?: string
}

// linkifyTaskRefs splits free text into ReactNodes, replacing every
// `task:<id>` token with a Link to /tasks/:id. Non-match runs render
// as plain strings. Pure function — no side effects.
export function linkifyTaskRefs(text: string, opts: AutolinkOptions = {}): ReactNode {
  if (!text) return text
  // Reset regex state — TASK_REF_PATTERN has the /g flag.
  const re = new RegExp(TASK_REF_PATTERN.source, 'g')
  const parts: ReactNode[] = []
  let lastIndex = 0
  let match: RegExpExecArray | null
  let key = 0
  while ((match = re.exec(text)) !== null) {
    if (match.index > lastIndex) {
      parts.push(text.slice(lastIndex, match.index))
    }
    const id = match[1]
    const ws = opts.resolveWorkspace?.(id) ?? opts.workspaceId ?? ''
    const href = ws
      ? `/tasks/${encodeURIComponent(id)}?workspace=${encodeURIComponent(ws)}`
      : `/tasks?focus=${encodeURIComponent(id)}`
    parts.push(
      <Link
        key={`ref-${key++}`}
        to={href}
        className={cn(
          'inline-flex items-center gap-1 px-1 py-px font-mono text-[11px] text-primary hover:underline align-baseline',
          opts.className,
        )}
        title={`Open task ${id}`}
        onClick={(e) => e.stopPropagation()}
      >
        <ListTodo className="h-3 w-3" />
        {shortTaskId(id)}
      </Link>,
    )
    lastIndex = re.lastIndex
  }
  if (lastIndex < text.length) {
    parts.push(text.slice(lastIndex))
  }
  // Avoid changing single-node text into an array unless we found a
  // match — keeps React happier and the DOM smaller for the common
  // "no task refs" case.
  if (parts.length === 0) return text
  if (parts.length === 1 && typeof parts[0] === 'string') return parts[0]
  return parts
}

// hasTaskRef — cheap pre-check for callers that want to skip the
// linkify path when there's no work to do.
export function hasTaskRef(text: string): boolean {
  if (!text) return false
  return TASK_ID_PATTERN.test(text)
}

// shortTaskId — the last 6 chars of a ULID, prefixed for legibility.
// Used in chips and inline links where the full 26 chars would dominate.
export function shortTaskId(id: string): string {
  if (!id) return ''
  if (id.length <= 8) return id
  return id.slice(-6)
}

export type TaskCompositionFilter = 'all' | 'epics' | 'children' | 'standalone'

export interface TaskCompositionFlags {
  isEpic: boolean
  isChild: boolean
}

export function buildTaskChildParentIds(rows: Pick<Task, 'meta'>[]): Set<string> {
  const parentIds = new Set<string>()
  for (const task of rows) {
    for (const parentId of readMetaList(task.meta, 'composed_by')) {
      parentIds.add(parentId)
    }
  }
  return parentIds
}

export function taskCompositionFlags(
  task: Pick<Task, 'id' | 'meta'>,
  childParentIds: ReadonlySet<string> = new Set(),
): TaskCompositionFlags {
  return {
    isEpic: readMetaList(task.meta, 'composes').length > 0 || childParentIds.has(task.id),
    isChild: readMetaList(task.meta, 'composed_by').length > 0,
  }
}

export function matchesTaskCompositionFilter(
  task: Pick<Task, 'id' | 'meta'>,
  filter: TaskCompositionFilter,
  childParentIds: ReadonlySet<string> = new Set(),
): boolean {
  if (filter === 'all') return true
  const flags = taskCompositionFlags(task, childParentIds)
  switch (filter) {
    case 'epics':
      return flags.isEpic
    case 'children':
      return flags.isChild
    case 'standalone':
      return !flags.isEpic && !flags.isChild
  }
}

// priorityClass returns a tone for the Badge + a hard color for the
// priority dot in the row. Critical = red, high = amber, normal = sky,
// low = muted.
export function priorityVisual(priority: string): { tone: 'critical' | 'warn' | 'info' | 'muted'; dot: string } {
  switch (priority) {
    case 'critical':
      return { tone: 'critical', dot: 'bg-red-500' }
    case 'high':
      return { tone: 'warn', dot: 'bg-amber-500' }
    case 'low':
      return { tone: 'muted', dot: 'bg-muted-foreground/40' }
    case 'normal':
    default:
      return { tone: 'info', dot: 'bg-sky-500' }
  }
}

// statusVisual returns colors for status chips. Terminal vocab entries
// resolve to muted/success; open statuses get a more saturated chip.
export function statusVisual(status: string, isTerminal: boolean): { tone: 'success' | 'info' | 'warn' | 'muted'; mono?: boolean } {
  if (isTerminal) {
    if (status === 'cancelled' || status === 'abandoned' || status === 'rejected') {
      return { tone: 'muted' }
    }
    return { tone: 'success' }
  }
  switch (status) {
    case 'doing':
    case 'in_progress':
    case 'running':
      return { tone: 'info' }
    case 'blocked':
    case 'review':
    case 'awaiting_approval':
      return { tone: 'warn' }
    case 'open':
    case 'todo':
      return { tone: 'info', mono: true }
    default:
      return { tone: 'info', mono: true }
  }
}

// formatRelative — ISO → short relative string. Mirrors the helper in
// pages/workers/worker-utils.ts so the tasks UI feels native to the
// rest of the dashboard.
export function formatRelative(iso?: string | null): string {
  if (!iso) return '—'
  const t = new Date(iso).getTime()
  if (Number.isNaN(t)) return '—'
  const diff = Date.now() - t
  if (diff < -60_000) {
    // Future timestamp — render approximate distance "in N".
    const ahead = -diff
    const m = Math.floor(ahead / 60_000)
    if (m < 60) return `in ${m}m`
    const h = Math.floor(m / 60)
    if (h < 24) return `in ${h}h`
    const d = Math.floor(h / 24)
    return `in ${d}d`
  }
  const s = Math.floor(diff / 1000)
  if (s < 60) return s <= 5 ? 'just now' : `${s}s ago`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m ago`
  const h = Math.floor(m / 60)
  if (h < 24) return `${h}h ago`
  const d = Math.floor(h / 24)
  if (d < 7) return `${d}d ago`
  return new Date(iso).toLocaleDateString()
}

// formatAbsolute — full date + time, used for tooltips on relative
// labels so a hover reveals the exact moment.
export function formatAbsolute(iso?: string | null): string {
  if (!iso) return ''
  const t = new Date(iso)
  if (Number.isNaN(t.getTime())) return ''
  return t.toLocaleString(undefined, {
    year: 'numeric',
    month: 'short',
    day: 'numeric',
    hour: '2-digit',
    minute: '2-digit',
  })
}

// formatDueAt — overdue-aware. Returns the time itself and an
// over/upcoming flag the row chip can color.
export interface DueState {
  label: string
  state: 'overdue' | 'soon' | 'later' | 'none'
}

export function dueState(dueAt: string | null | undefined, closedAt: string | null | undefined): DueState {
  if (!dueAt) return { label: '', state: 'none' }
  if (closedAt) return { label: formatRelative(dueAt), state: 'later' } // closed task is never "overdue" anymore
  const t = new Date(dueAt).getTime()
  if (Number.isNaN(t)) return { label: '', state: 'none' }
  const diff = t - Date.now()
  if (diff < 0) return { label: formatRelative(dueAt), state: 'overdue' }
  if (diff < 24 * 60 * 60 * 1000) return { label: formatRelative(dueAt), state: 'soon' }
  return { label: formatRelative(dueAt), state: 'later' }
}

// useNow — re-renders the calling component every `intervalMs` so
// derived "live" labels (time-in-state, time-worked, abandoned-for)
// tick without re-fetching the underlying data.
export function useNow(intervalMs = 30_000): number {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), intervalMs)
    return () => clearInterval(id)
  }, [intervalMs])
  return now
}

// formatDuration — humanizes a millisecond span. Tight by design:
// hours+minutes max, no seconds past one minute. Used by time-in-state
// + time-worked chips that live in dense rows.
export function formatDuration(ms: number): string {
  if (ms < 0) ms = 0
  const s = Math.floor(ms / 1000)
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m`
  const h = Math.floor(m / 60)
  const mm = m % 60
  if (h < 24) return mm > 0 ? `${h}h ${mm}m` : `${h}h`
  const d = Math.floor(h / 24)
  const hh = h % 24
  return hh > 0 ? `${d}d ${hh}h` : `${d}d`
}

// timeInCurrentState — milliseconds since the last status change. Walk
// status_history backwards and find the most recent `status_changed`
// (or `created` if none); the gap from there to now is the answer.
export function timeInCurrentState(history: TaskStatusHistoryEntry[] | null | undefined, now: number): number {
  if (!history || history.length === 0) return 0
  for (let i = history.length - 1; i >= 0; i--) {
    const h = history[i]
    if (h.evt === 'status_changed' || h.evt === 'created') {
      const t = new Date(h.at).getTime()
      if (Number.isNaN(t)) return 0
      return Math.max(0, now - t)
    }
  }
  return 0
}

// cumulativeTimeWorked — total time the task spent in a working
// status across its lifetime. Walks status_history forward, opens a
// timer when entering a working status, closes it when leaving. The
// final open interval (still working) accumulates up to `now`. Pass
// the workspace vocab map to honour per-workspace working
// declarations (kind="working") on freeform statuses.
export function cumulativeTimeWorked(
  history: TaskStatusHistoryEntry[] | null | undefined,
  now: number,
  vocab?: StatusKindMap,
): number {
  if (!history || history.length === 0) return 0
  let total = 0
  let openedAt: number | null = null
  // Sort defensively — incoming arrays should be chronological but
  // server-side reverse-render means we don't want to rely on it.
  const sorted = [...history].sort((a, b) => new Date(a.at).getTime() - new Date(b.at).getTime())
  for (const h of sorted) {
    const t = new Date(h.at).getTime()
    if (Number.isNaN(t)) continue
    if (h.evt === 'status_changed') {
      // Closing a previously-working interval.
      if (openedAt !== null && (!h.to || !isWorkingStatus(h.to, vocab))) {
        total += t - openedAt
        openedAt = null
      }
      // Opening a new working interval.
      if (h.to && isWorkingStatus(h.to, vocab) && openedAt === null) {
        openedAt = t
      }
    } else if (CLOSED_EVENTS.has(h.evt) && openedAt !== null) {
      total += t - openedAt
      openedAt = null
    }
  }
  if (openedAt !== null) total += Math.max(0, now - openedAt)
  return total
}

// kindOfStatus — returns the canonical bucket for a freeform status.
// Consults the workspace vocab map first (status_text → kind); falls
// back to the hardcoded WORKING_STATUSES heuristic when the map is
// absent or silent on this status. Returns null when the status is
// unknown to both the map and the fallback set.
export function kindOfStatus(
  status: string,
  vocab?: StatusKindMap,
): StatusKind | null {
  if (!status) return null
  if (vocab) {
    const k = vocab.get(status)
    if (k === 'open' || k === 'working' || k === 'blocked' || k === 'review' || k === 'done' || k === 'cancelled') {
      return k
    }
  }
  if (WORKING_STATUSES.has(status)) return 'working'
  return null
}

// isWorkingStatus — true if the given freeform status_text is one of
// the "actively working" values the lease/heartbeat logic treats as
// in-progress. Pass the workspace vocab map to honour per-workspace
// declarations (kind="working" on `triaging`, `coding`, etc.); falls
// back to the hardcoded WORKING_STATUSES set when no map is supplied
// or the status is absent from it.
export function isWorkingStatus(status: string, vocab?: StatusKindMap): boolean {
  if (vocab) {
    const k = vocab.get(status)
    if (k === 'working') return true
    // Explicit non-working classification from vocab wins over the
    // hardcoded fallback — a workspace that declared `doing → blocked`
    // (weird but legal) should NOT be treated as working.
    if (k === 'open' || k === 'blocked' || k === 'review' || k === 'done' || k === 'cancelled') return false
  }
  return WORKING_STATUSES.has(status)
}

// leaseStaleness — derives the visible "is the assignee actually
// still working on this?" state from agent-presence, NOT elapsed
// time. The honest abandonment signal is: assignee_session_id no
// longer appears in the mesh's active-agents directory.
//
// A `doing` task with no recent updates does NOT mean abandoned —
// the agent might just be working quietly. Only the agent vanishing
// from the mesh's heartbeat registry counts.
//
// States:
//   * idle       — closed, non-working status, or unassigned
//   * live       — assignee is in the active mesh agents set
//   * abandoned  — assignee not in active agents (the AI went away)
//
// `activeSessions` is the set returned by useActiveMeshAgents.
// Pass `null` when not yet loaded — leaseStaleness returns `idle`
// in that case to avoid flashing "abandoned" badges on first render.
//
// `updatedAt` is retained on the signature only so callers with
// stale call-sites still compile; it is no longer consulted for the
// abandoned decision.
export function leaseStaleness(
  status: string,
  assigneeSessionID: string | undefined | null,
  closedAt: string | null | undefined,
  activeSessions: Set<string> | null,
  vocab?: StatusKindMap,
  assigneeUserID?: string | null,
): { state: 'live' | 'abandoned' | 'idle' } {
  if (closedAt) return { state: 'idle' }
  if (assigneeUserID?.trim()) return { state: 'idle' }
  if (!isWorkingStatus(status, vocab)) return { state: 'idle' }
  if (!assigneeSessionID) return { state: 'idle' }
  // Initial load — keep silent, don't flash abandoned chips.
  if (activeSessions === null) return { state: 'idle' }
  if (activeSessions.has(assigneeSessionID)) return { state: 'live' }
  return { state: 'abandoned' }
}

// assigneeLabel — the canonical short name we render in chips.
//   ""              → "unassigned"
//   sess only       → first 8 chars of sess (mono)
//   peer only       → "peer:abcd" (last 4 chars of peer id)
//   peer + sess     → "peer:abcd/sess1234"
//   user            → "@<id>" — human owner (migration 105). The `@`
//                     prefix keeps the human assignee visually distinct
//                     from the agent session id glyph; the prefix is
//                     also what the list-page filter chip keys off.
export function assigneeLabel(t: {
  assignee_session_id?: string
  assignee_peer_id?: string
  assignee_user_id?: string
}): string {
  const user = t.assignee_user_id?.trim() ?? ''
  const sess = t.assignee_session_id?.trim() ?? ''
  const peer = t.assignee_peer_id?.trim() ?? ''
  if (user) return `@${user}`
  if (!sess && !peer) return 'unassigned'
  if (peer) {
    const short = peer.slice(-4)
    if (sess) return `peer:${short}/${sess.slice(0, 8)}`
    return `peer:${short}`
  }
  return sess.slice(0, 8)
}

// isHumanAssigned — true when the task is owned by a human user (per
// migration 105). The dashboard uses this to gate "you" / "human" UI
// affordances and to drive the `assignee_origin_kind=human` filter.
export function isHumanAssigned(t: { assignee_user_id?: string }): boolean {
  return !!t.assignee_user_id?.trim()
}
