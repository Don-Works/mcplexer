import type { StoredNotification } from '@/api/notifications'

// Signal store — a tiny vanilla event emitter with useSyncExternalStore
// support. Keeping the codebase free of Zustand / Jotai / Redux; this
// is a single global notification feed, not a state-tree concern.
//
// State lives in module-level closures. Subscribers (one per React tree
// component using useSignal*) receive snapshot updates on every change.

interface State {
  events: StoredNotification[] // newest first, max 500
  unreadCount: number
  loaded: boolean // true once the initial REST backfill has settled
  trayOpen: boolean
  filter: NotificationFilter
  // The most-recent critical/high event we have NOT yet flashed.
  // Consumed by SignalFlash; once shown it transitions to null.
  pendingFlash: StoredNotification | null
}

export type NotificationFilter = 'all' | 'mesh' | 'approval' | 'system' | 'secret' | 'task' | 'worker'

const MAX = 500

let state: State = {
  events: [],
  unreadCount: 0,
  loaded: false,
  trayOpen: false,
  filter: 'all',
  pendingFlash: null,
}

type Listener = () => void
const listeners = new Set<Listener>()

function emit() {
  for (const l of listeners) l()
}

function set(patch: Partial<State>) {
  state = { ...state, ...patch }
  emit()
}

export function getSnapshot(): State {
  return state
}

export function subscribe(l: Listener): () => void {
  listeners.add(l)
  return () => listeners.delete(l)
}

// Mutations — kept narrow on purpose.

// FLASH_ON_LOAD_WINDOW_MS controls when a backfilled event still gets
// a flash strip on page load. If the most-recent unread critical/high
// arrived within this window, flash it — the user genuinely just
// missed it. Older unread events stay quiet; the sidebar counter +
// tray are enough.
const FLASH_ON_LOAD_WINDOW_MS = 5 * 60 * 1000 // 5 minutes

export function setLoaded(events: StoredNotification[], unreadCount: number) {
  const sliced = events.slice(0, MAX)
  // Promote the most-recent unread critical/high (if any, and recent
  // enough) into pendingFlash so the user sees it even though it
  // arrived before the page mounted.
  const now = Date.now()
  let pendingFlash: StoredNotification | null = state.pendingFlash
  if (!pendingFlash) {
    for (const e of sliced) {
      if (e.read_at) continue
      if (e.priority !== 'critical' && e.priority !== 'high') continue
      const age = now - new Date(e.created_at).getTime()
      if (age <= FLASH_ON_LOAD_WINDOW_MS) {
        pendingFlash = e
        break
      }
    }
  }
  set({ events: sliced, unreadCount, loaded: true, pendingFlash })
}

export function pushLive(evt: StoredNotification) {
  // Dedup by message_id — the SSE channel can re-fire on reconnect and
  // we don't want duplicates piling up the unread counter.
  if (state.events.some((e) => e.message_id === evt.message_id)) return
  const next = [evt, ...state.events].slice(0, MAX)
  const isUnread = !evt.read_at
  const pendingFlash =
    isUnread && (evt.priority === 'critical' || evt.priority === 'high')
      ? evt
      : state.pendingFlash
  set({
    events: next,
    unreadCount: state.unreadCount + (isUnread ? 1 : 0),
    pendingFlash,
  })
}

export function markReadLocal(ids: number[]) {
  if (ids.length === 0) return
  const idSet = new Set(ids)
  let droppedUnread = 0
  const now = new Date().toISOString()
  const events = state.events.map((e) => {
    if (idSet.has(e.id) && !e.read_at) {
      droppedUnread++
      return { ...e, read_at: now }
    }
    return e
  })
  set({
    events,
    unreadCount: Math.max(0, state.unreadCount - droppedUnread),
  })
}

export function markAllReadLocal() {
  if (state.unreadCount === 0) return
  const now = new Date().toISOString()
  const events = state.events.map((e) => (e.read_at ? e : { ...e, read_at: now }))
  set({ events, unreadCount: 0 })
}

export function setUnreadCount(n: number) {
  if (n === state.unreadCount) return
  set({ unreadCount: Math.max(0, n) })
}

export function setTrayOpen(open: boolean) {
  if (open === state.trayOpen) return
  set({ trayOpen: open })
}

export function setFilter(f: NotificationFilter) {
  if (f === state.filter) return
  set({ filter: f })
}

export function consumeFlash() {
  if (state.pendingFlash) set({ pendingFlash: null })
}

function sourceOf(e: StoredNotification): NotificationFilter {
  // Backend Source field is the authoritative axis. Legacy events
  // without it fall back to 'mesh' — that was the only source firing
  // notify events before v0.7.0.
  const s = e.source as NotificationFilter
  if (s === 'mesh' || s === 'approval' || s === 'system' || s === 'secret' || s === 'task' || s === 'worker') return s
  return 'mesh'
}

export function selectFiltered(snap: State): StoredNotification[] {
  if (snap.filter === 'all') return snap.events
  return snap.events.filter((e) => sourceOf(e) === snap.filter)
}

export function countsByKind(snap: State): Record<NotificationFilter, number> {
  const out: Record<NotificationFilter, number> = {
    all: snap.events.length,
    mesh: 0,
    approval: 0,
    system: 0,
    secret: 0,
    task: 0,
    worker: 0,
  }
  for (const e of snap.events) {
    out[sourceOf(e)]++
  }
  return out
}
