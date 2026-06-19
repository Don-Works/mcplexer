import { useCallback, useEffect, useState } from 'react'
import type { TaskEvent } from './use-tasks-stream'

// Persisted "last N events" buffer for the tasks live-activity card.
// SSE alone leaves the activity strip blank on every page refresh until
// the next event arrives; this hook hydrates from localStorage so the
// operator opens /tasks and sees what's been happening, immediately.
//
// Capacity is 50 — large enough to span a normal session, small enough
// that the serialized blob stays well under the per-origin storage cap
// even with full Task / Offer payloads embedded.

const STORAGE_KEY = 'mcplexer.tasks.recent_activity'
const MAX_ITEMS = 50

function load(): TaskEvent[] {
  if (typeof window === 'undefined') return []
  try {
    const raw = window.localStorage.getItem(STORAGE_KEY)
    if (!raw) return []
    const parsed = JSON.parse(raw)
    if (!Array.isArray(parsed)) return []
    return parsed.slice(0, MAX_ITEMS) as TaskEvent[]
  } catch {
    return []
  }
}

function persist(events: TaskEvent[]) {
  if (typeof window === 'undefined') return
  try {
    window.localStorage.setItem(STORAGE_KEY, JSON.stringify(events))
  } catch {
    // Quota exceeded / private mode — silently drop. The page still
    // works, the next refresh just won't pre-fill.
  }
}

// dedupeKey collapses retransmits of the same event so a reconnecting
// SSE stream that replays a backlog doesn't bloat the buffer.
function dedupeKey(evt: TaskEvent): string {
  const id = evt.task?.id ?? evt.offer?.id ?? evt.note?.id ?? ''
  return `${evt.kind}:${id}:${evt.at}`
}

export function useRecentTaskActivity(): {
  events: TaskEvent[]
  push: (evt: TaskEvent) => void
} {
  const [events, setEvents] = useState<TaskEvent[]>(() => load())

  // Resync across tabs — when the operator has /tasks open in two
  // windows, both should agree on the activity log.
  useEffect(() => {
    if (typeof window === 'undefined') return
    const onStorage = (e: StorageEvent) => {
      if (e.key !== STORAGE_KEY) return
      setEvents(load())
    }
    window.addEventListener('storage', onStorage)
    return () => window.removeEventListener('storage', onStorage)
  }, [])

  const push = useCallback((evt: TaskEvent) => {
    setEvents((prev) => {
      const key = dedupeKey(evt)
      const filtered = prev.filter((e) => dedupeKey(e) !== key)
      const next = [evt, ...filtered].slice(0, MAX_ITEMS)
      persist(next)
      return next
    })
  }, [])

  return { events, push }
}
