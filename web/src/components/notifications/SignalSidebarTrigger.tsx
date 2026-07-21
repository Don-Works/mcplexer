import { cn } from '@/lib/utils'
import { useSignal } from './use-signal'

// Sidebar row: `○ notifications: 3 new   ⌘J` — sibling of the daemon-status
// row right above it. Hollow dot at zero, solid cyan with ping-pulse
// when any unread is critical/high (matches the live-daemon dot
// grammar exactly).
//
// Single-click toggles the tray (the "right now" surface). Right-click
// / shift-click navigates to the full /signals page instead. Plain
// click is the fast path the user wants 95% of the time.

export function SignalSidebarTrigger() {
  const isMac = typeof navigator !== 'undefined' && /Mac|iPhone|iPad/.test(navigator.platform)
  const { unreadCount, events, toggleTray, openLink } = useSignal()
  const hasUrgent = unreadCount > 0 && events.some(
    (e) => !e.read_at && (e.priority === 'critical' || e.priority === 'high'),
  )
  const labelText = unreadCount === 0 ? 'notifications: quiet' : `notifications: ${unreadCount} new`

  return (
    <button
      type="button"
      onClick={(e) => {
        // Shift-click navigates to /signals; plain click toggles the tray.
        if (e.shiftKey) {
          openLink('/signals')
          return
        }
        toggleTray()
      }}
      onAuxClick={(e) => {
        // Middle/right click also routes to /signals.
        if (e.button === 1 || e.button === 2) {
          e.preventDefault()
          openLink('/signals')
        }
      }}
      onContextMenu={(e) => {
        e.preventDefault()
        openLink('/signals')
      }}
      data-testid="signal-sidebar-trigger"
      aria-label={`Open notifications tray (${unreadCount} unread); shift-click for full page`}
      title="Click: tray · Shift-click: full notifications page"
      className="flex w-full items-center gap-2 px-1 py-0.5 text-left transition-colors hover:text-foreground"
    >
      <span className="relative flex h-2 w-2 shrink-0" aria-hidden>
        {hasUrgent && (
          <span
            className={cn(
              'absolute inline-flex h-full w-full animate-ping rounded-full opacity-75',
              'bg-amber-400',
            )}
          />
        )}
        <span
          className={cn(
            'relative inline-flex h-2 w-2 rounded-full transition-colors',
            unreadCount === 0
              ? 'border border-muted-foreground/40 bg-transparent'
              : hasUrgent
                ? 'bg-amber-400'
                : 'bg-primary',
          )}
        />
      </span>
      <span
        aria-live="polite"
        className={cn(
          'flex-1 truncate font-mono text-[11px] tabular-nums',
          unreadCount === 0 ? 'text-muted-foreground/70' : 'text-foreground',
        )}
      >
        {labelText}
      </span>
      <kbd className="border border-border bg-background/40 px-1 py-px font-mono text-[10px] text-muted-foreground">
        {isMac ? '⌘J' : 'ctrl+J'}
      </kbd>
    </button>
  )
}
