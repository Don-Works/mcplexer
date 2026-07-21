import { useEffect, useRef } from 'react'
import { X } from 'lucide-react'
import { cn } from '@/lib/utils'
import type { StoredNotification } from '@/api/notifications'
import { useSignal } from './use-signal'
import { SignalFilterRail } from './SignalFilterRail'
import { SignalRow } from './SignalRow'
import { SignalEmpty } from './SignalEmpty'

// SignalTray — terminal-native right-edge tray. Cmd+J sibling of cmd+K.
// Width matches the sidebar (w-80 reads denser than the nav's w-56
// because rows carry body excerpts). border-l + bg-sidebar-background
// — visually a mirror of the left sidebar.

export function SignalTray() {
  const {
    trayOpen, closeTray,
    filteredEvents, events, counts, filter, setFilter,
    loaded, unreadCount,
    markRead, markAllRead, openLink,
  } = useSignal()

  const containerRef = useRef<HTMLDivElement | null>(null)

  // Inline keyboard handling — j/k navigation, ↵ to open, r to read,
  // / to focus search (deferred), 1-5 to switch filter, esc to close.
  // No focus management beyond what's necessary for an open tray.
  const activeRef = useRef(-1)

  useEffect(() => {
    if (!trayOpen) return
    function onKey(e: KeyboardEvent) {
      // Only listen when the tray has focus context — but we accept any
      // window-level keypress so the user doesn't need to click into
      // the tray after opening it.
      if (e.target instanceof HTMLInputElement || e.target instanceof HTMLTextAreaElement) return
      if (e.key === 'Escape') {
        e.preventDefault()
        closeTray()
        return
      }
      if (e.key === 'j' || e.key === 'ArrowDown') {
        e.preventDefault()
        activeRef.current = Math.min(filteredEvents.length - 1, activeRef.current + 1)
        focusActive(containerRef.current, activeRef.current)
        return
      }
      if (e.key === 'k' || e.key === 'ArrowUp') {
        e.preventDefault()
        activeRef.current = Math.max(0, activeRef.current - 1)
        focusActive(containerRef.current, activeRef.current)
        return
      }
      if (e.key === 'Enter') {
        e.preventDefault()
        const evt = filteredEvents[activeRef.current]
        if (!evt) return
        if (!evt.read_at) void markRead(evt.id)
        openLink(evt.link)
        return
      }
      if (e.key === 'r') {
        e.preventDefault()
        const evt = filteredEvents[activeRef.current]
        if (!evt) return
        if (!evt.read_at) void markRead(evt.id)
        return
      }
      if (e.key === 'R') {
        e.preventDefault()
        void markAllRead()
        return
      }
      if (e.key >= '1' && e.key <= '5') {
        e.preventDefault()
        const filters = ['all', 'mesh', 'approval', 'system', 'secret'] as const
        const idx = parseInt(e.key, 10) - 1
        if (idx >= 0 && idx < filters.length) setFilter(filters[idx])
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [trayOpen, filteredEvents, markRead, markAllRead, setFilter, openLink, closeTray])

  // Reset active row on filter change.
  useEffect(() => {
    activeRef.current = -1
  }, [filter])

  if (!trayOpen) return null

  return (
    <aside
      ref={containerRef}
      data-testid="signal-tray"
      role="complementary"
      aria-label="Signal — notification log"
      className="flex h-full w-full flex-col border-l border-sidebar-border bg-sidebar-background font-mono shadow-xl shadow-black/40 md:w-80"
    >
      <header className="flex h-12 shrink-0 items-center justify-between border-b border-sidebar-border px-3">
        <div className="flex items-center gap-2">
          <span className="text-[13px] text-primary/80" aria-hidden>
            ›
          </span>
          <span className="text-[13px] font-semibold text-foreground">Signal</span>
          {unreadCount > 0 && (
            <span className="font-mono text-[10px] tabular-nums text-muted-foreground">
              · {unreadCount} unread
            </span>
          )}
        </div>
        <div className="flex items-center gap-2">
          {unreadCount > 0 && (
            <button
              type="button"
              onClick={() => void markAllRead()}
              data-testid="signal-mark-all-read"
              className="font-mono text-[10px] uppercase tracking-wider text-muted-foreground transition-colors hover:text-foreground"
            >
              mark all read
            </button>
          )}
          <kbd className="hidden border border-border bg-background/40 px-1.5 py-0.5 font-mono text-[10px] text-muted-foreground md:inline-flex">
            ⌘J
          </kbd>
          <button
            type="button"
            onClick={closeTray}
            aria-label="Close Signal tray"
            className="grid h-9 w-9 place-items-center text-muted-foreground transition-colors hover:bg-accent/50 hover:text-foreground md:h-8 md:w-8"
          >
            <X className="h-4 w-4" />
          </button>
        </div>
      </header>

      <SignalFilterRail filter={filter} counts={counts} onChange={setFilter} />

      <div className="flex-1 overflow-y-auto">
        {!loaded ? (
          <div className="px-4 py-8 text-center font-mono text-[11px] text-muted-foreground/70">
            <span className="animate-pulse">loading signal…</span>
          </div>
        ) : filteredEvents.length === 0 ? (
          <SignalEmpty filter={filter} />
        ) : (
          renderGrouped(filteredEvents, {
            onMarkRead: (id) => void markRead(id),
            onOpen: (evt) => {
              if (!evt.read_at) void markRead(evt.id)
              openLink(evt.link)
            },
          })
        )}
      </div>

      {/* tmux-style status bar — same idiom as cmd+K palette */}
      <footer
        className={cn(
          'hidden shrink-0 items-center justify-between border-t border-sidebar-border bg-muted/20 px-3 py-1.5 font-mono text-[10px] md:flex',
          'text-muted-foreground/80',
        )}
      >
        <div className="flex items-center gap-3">
          <Hint k="j k" label="nav" />
          <Hint k="↵" label="open" />
          <Hint k="r" label="read" />
          <Hint k="1–5" label="filter" />
        </div>
        <span className="tabular-nums">
          {events.length} total
        </span>
      </footer>
    </aside>
  )
}

function Hint({ k, label }: { k: string; label: string }) {
  return (
    <span className="inline-flex items-center gap-1">
      <kbd className="border border-border bg-background/40 px-1 py-px font-mono text-[9px] text-foreground/80">
        {k}
      </kbd>
      <span>{label}</span>
    </span>
  )
}

function renderGrouped(
  events: StoredNotification[],
  cbs: { onMarkRead: (id: number) => void; onOpen: (e: StoredNotification) => void },
): React.ReactNode {
  // Split into TODAY / YESTERDAY / EARLIER by created_at against the
  // user's local clock. Relative time fits a tool the user has open
  // all day; absolute date isn't useful at this density.
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
      <div key={b.label} className="pb-1">
        <div className="px-3 pb-0.5 pt-3 text-[10px] font-semibold uppercase tracking-widest text-muted-foreground/60">
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
      </div>
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
