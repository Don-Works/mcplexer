import { useEffect, useRef } from 'react'
import { useSearchParams } from 'react-router-dom'
import type { StoredNotification } from '@/api/notifications'
import { useSignal } from '@/components/notifications/use-signal'
import { SignalFilterRail } from '@/components/notifications/SignalFilterRail'
import { SignalRow } from '@/components/notifications/SignalRow'
import { SignalEmpty } from '@/components/notifications/SignalEmpty'
import type { NotificationFilter } from '@/components/notifications/store'

// SignalsPage — full-page version of the Signal tray. The tray is the
// always-one-keystroke-away surface; this page is the "I want to live
// here for a while" surface. Same row component, denser layout,
// browser back-button works to retrace your steps through events.
//
// Route: /signals?filter=mesh|approval|system|secret&unread=true
// Cmd+K commands route here when the user wants to drill into a kind.

const VALID_FILTERS: NotificationFilter[] = ['all', 'mesh', 'approval', 'system', 'secret', 'task', 'worker']

export function SignalsPage() {
  const [searchParams, setSearchParams] = useSearchParams()
  const {
    events, filteredEvents, counts, filter, setFilter,
    unreadCount, loaded, markRead, markAllRead, openLink,
  } = useSignal()

  // Read filter + ?unread from URL so deep links land filtered.
  useEffect(() => {
    const urlFilter = searchParams.get('filter') as NotificationFilter | null
    if (urlFilter && VALID_FILTERS.includes(urlFilter) && urlFilter !== filter) {
      setFilter(urlFilter)
    }
  }, [searchParams, filter, setFilter])

  // Write filter back to URL so back/forward + reload preserve state.
  function changeFilter(f: NotificationFilter) {
    setFilter(f)
    const next = new URLSearchParams(searchParams)
    if (f === 'all') next.delete('filter')
    else next.set('filter', f)
    setSearchParams(next, { replace: true })
  }

  const unreadOnly = searchParams.get('unread') !== 'false'
  const shown = unreadOnly ? filteredEvents.filter((e) => !e.read_at) : filteredEvents

  // Keyboard nav: j/k arrows, ↵ to open, r to read. Mirror the tray.
  const containerRef = useRef<HTMLDivElement | null>(null)
  const activeRef = useRef(-1)
  useEffect(() => {
    function onKey(e: KeyboardEvent) {
      if (e.target instanceof HTMLInputElement || e.target instanceof HTMLTextAreaElement) return
      if (e.key === 'j' || e.key === 'ArrowDown') {
        e.preventDefault()
        activeRef.current = Math.min(shown.length - 1, activeRef.current + 1)
        focusActive(containerRef.current, activeRef.current)
      } else if (e.key === 'k' || e.key === 'ArrowUp') {
        e.preventDefault()
        activeRef.current = Math.max(0, activeRef.current - 1)
        focusActive(containerRef.current, activeRef.current)
      } else if (e.key === 'Enter') {
        const evt = shown[activeRef.current]
        if (!evt) return
        e.preventDefault()
        if (!evt.read_at) void markRead(evt.id)
        openLink(evt.link)
      } else if (e.key === 'r') {
        const evt = shown[activeRef.current]
        if (!evt) return
        e.preventDefault()
        if (!evt.read_at) void markRead(evt.id)
      } else if (e.key === 'R') {
        e.preventDefault()
        void markAllRead()
      } else if (e.key >= '1' && e.key <= '5') {
        e.preventDefault()
        const idx = parseInt(e.key, 10) - 1
        if (idx >= 0 && idx < VALID_FILTERS.length) changeFilter(VALID_FILTERS[idx])
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [shown, markRead, markAllRead, openLink])

  useEffect(() => {
    activeRef.current = -1
  }, [filter, unreadOnly])

  function toggleUnreadOnly() {
    const next = new URLSearchParams(searchParams)
    if (unreadOnly) next.set('unread', 'false')
    else next.delete('unread')
    setSearchParams(next, { replace: true })
  }

  return (
    <div className="space-y-4 font-mono">
      <header className="flex items-end justify-between gap-6">
        <div>
          <h1 className="text-2xl font-bold tracking-tight">Notifications</h1>
          <p className="mt-1 max-w-2xl text-sm text-muted-foreground">
            Events from your agents, tools, and peers.
          </p>
        </div>
        <div className="flex items-center gap-2">
          <button
            type="button"
            onClick={toggleUnreadOnly}
            data-testid="signals-toggle-unread"
            className={`border px-2.5 py-1 font-mono text-[11px] uppercase tracking-wider transition-colors ${
              unreadOnly
                ? 'border-primary/60 bg-primary/5 text-foreground'
                : 'border-border text-muted-foreground hover:text-foreground'
            }`}
          >
            {unreadOnly ? 'unread only ✓' : 'unread only'}
          </button>
          {unreadCount > 0 && (
            <button
              type="button"
              onClick={() => void markAllRead()}
              data-testid="signals-mark-all-read"
              className="border border-border px-2.5 py-1 font-mono text-[11px] uppercase tracking-wider text-muted-foreground transition-colors hover:text-foreground"
            >
              mark all read
            </button>
          )}
        </div>
      </header>

      <SignalFilterRail filter={filter} counts={counts} onChange={changeFilter} />

      <div className="border border-border bg-card/30">
        {!loaded ? (
          <div className="px-4 py-10 text-center font-mono text-[12px] text-muted-foreground/70">
            <span className="animate-pulse">loading signal…</span>
          </div>
        ) : shown.length === 0 ? (
          unreadOnly && filteredEvents.length > 0 ? (
            <div className="px-4 py-10 text-center font-mono text-[12px] text-muted-foreground">
              Nothing unread under{' '}
              <code className="text-foreground/80">{filter}</code>. Toggle "unread only" off to see
              read events.
            </div>
          ) : (
            <SignalEmpty filter={filter} />
          )
        ) : (
          <div ref={containerRef}>{renderGrouped(shown, { onMarkRead: (id) => void markRead(id), onOpen: (evt) => { if (!evt.read_at) void markRead(evt.id); openLink(evt.link) } })}</div>
        )}
      </div>

      <p className="text-[10.5px] text-muted-foreground/60">
        Shown: <span className="tabular-nums text-foreground/80">{shown.length}</span> of{' '}
        <span className="tabular-nums text-foreground/80">{events.length}</span> · keyboard:{' '}
        <code className="text-foreground/70">j k</code> nav,{' '}
        <code className="text-foreground/70">↵</code> open,{' '}
        <code className="text-foreground/70">r</code> mark read,{' '}
        <code className="text-foreground/70">R</code> mark all,{' '}
        <code className="text-foreground/70">1–5</code> filter.
      </p>
    </div>
  )
}

function renderGrouped(
  events: StoredNotification[],
  cbs: { onMarkRead: (id: number) => void; onOpen: (e: StoredNotification) => void },
): React.ReactNode {
  const now = new Date()
  const startOfToday = new Date(now.getFullYear(), now.getMonth(), now.getDate())
  const startOfYesterday = new Date(startOfToday.getTime() - 24 * 3600 * 1000)
  const buckets: { label: string; rows: StoredNotification[] }[] = [
    { label: 'Today', rows: [] },
    { label: 'Yesterday', rows: [] },
    { label: 'Earlier', rows: [] },
  ]
  for (const e of events) {
    const t = new Date(e.created_at).getTime()
    if (t >= startOfToday.getTime()) buckets[0].rows.push(e)
    else if (t >= startOfYesterday.getTime()) buckets[1].rows.push(e)
    else buckets[2].rows.push(e)
  }
  let runningIndex = 0
  return buckets
    .filter((b) => b.rows.length > 0)
    .map((b) => (
      <section key={b.label} className="border-b border-border/40 last:border-0">
        <div className="px-3 pb-1.5 pt-3 text-[10px] font-semibold uppercase tracking-widest text-muted-foreground/60">
          {b.label}
        </div>
        {b.rows.map((evt) => {
          const i = runningIndex++
          return (
            <SignalRow
              key={`${evt.message_id}-${evt.id}`}
              evt={evt}
              dataIndex={i}
              onMarkRead={cbs.onMarkRead}
              onOpen={cbs.onOpen}
            />
          )
        })}
      </section>
    ))
}

function focusActive(container: HTMLElement | null, index: number) {
  if (!container) return
  const el = container.querySelector<HTMLElement>(`[data-signal-index="${index}"]`)
  if (el) {
    el.focus()
    el.scrollIntoView({ block: 'nearest' })
  }
}
