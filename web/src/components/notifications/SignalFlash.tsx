import { useEffect, useState } from 'react'
import { cn } from '@/lib/utils'
import { useSignal } from './use-signal'
import { glyphFor } from './source-glyph'

// SignalFlash — full-width inline strip at the top of main content for
// critical + high priority events only. Not a toast: pushes content
// down, doesn't overlay. 4s dwell, sticks on hover. Dismissable inline.
// After fading, the event still lives in the tray — flash is a "you
// can't miss this" preview, not a separate channel.

const DWELL_MS = 4_000

export function SignalFlash() {
  const { pendingFlash, consumeFlash, markRead, openLink } = useSignal()
  const [active, setActive] = useState<typeof pendingFlash>(null)
  const [hovering, setHovering] = useState(false)
  const [exiting, setExiting] = useState(false)

  // Promote a pending flash into the visible slot. We consume from the
  // store immediately so back-to-back events don't accumulate
  // mid-display; if a new flash arrives while one is up, it replaces.
  useEffect(() => {
    if (!pendingFlash) return
    setActive(pendingFlash)
    setExiting(false)
    consumeFlash()
  }, [pendingFlash, consumeFlash])

  useEffect(() => {
    if (!active || hovering) return
    const id = setTimeout(() => {
      setExiting(true)
      // brief animation tail before unmounting
      setTimeout(() => setActive(null), 200)
    }, DWELL_MS)
    return () => clearTimeout(id)
  }, [active, hovering])

  if (!active) return null

  const isCritical = active.priority === 'critical'

  function dismiss() {
    if (active && !active.read_at && active.id > 0) void markRead(active.id)
    setExiting(true)
    setTimeout(() => setActive(null), 150)
  }

  function open() {
    if (active && !active.read_at && active.id > 0) void markRead(active.id)
    if (active) openLink(active.link)
    setExiting(true)
    setTimeout(() => setActive(null), 150)
  }

  return (
    <div
      role="alert"
      aria-live="assertive"
      data-testid="signal-flash"
      onMouseEnter={() => setHovering(true)}
      onMouseLeave={() => setHovering(false)}
      className={cn(
        'flex items-center gap-3 border-b px-4 py-2 font-mono text-[12px] transition-all duration-150',
        isCritical
          ? 'border-destructive/60 bg-destructive/[0.06] text-destructive'
          : 'border-amber-500/60 bg-amber-500/[0.06] text-amber-200',
        exiting && 'opacity-0',
      )}
    >
      <span aria-hidden className={cn('font-mono text-[14px]', isCritical ? 'text-destructive' : 'text-amber-400')}>
        {glyphFor(active.source || 'mesh')}
      </span>
      <span className="shrink-0 uppercase tracking-wider text-[10px] opacity-80">
        {active.priority}
      </span>
      <span className="min-w-0 flex-1 truncate text-foreground/90">
        {active.title}
        {active.agent_name && (
          <span className="text-muted-foreground/70"> · {active.agent_name}</span>
        )}
      </span>
      {active.link && (
        <button
          type="button"
          onClick={open}
          data-testid="signal-flash-open"
          className="shrink-0 border border-current/40 px-2 py-0.5 text-[10px] uppercase tracking-wider transition-colors hover:bg-current/10"
        >
          open
        </button>
      )}
      <button
        type="button"
        onClick={dismiss}
        data-testid="signal-flash-dismiss"
        aria-label="Dismiss"
        className="shrink-0 px-1 text-[12px] text-current/70 hover:text-current"
      >
        ×
      </button>
    </div>
  )
}
