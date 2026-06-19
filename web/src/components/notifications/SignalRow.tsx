import { cn } from '@/lib/utils'
import type { StoredNotification } from '@/api/notifications'
import { glyphFor, priorityGlyphTone } from './source-glyph'

interface Props {
  evt: StoredNotification
  dataIndex: number
  onMarkRead: (id: number) => void
  onOpen: (e: StoredNotification) => void
}

// One row in the Signal feed. Mono throughout. Source = ASCII glyph.
// Priority = glyph color. Unread = solid dot on the right.
// Critical priority gets a 1px left border in destructive — the only
// exception to the side-stripe ban; semantic, not decorative.

export function SignalRow({ evt, dataIndex, onMarkRead, onOpen }: Props) {
  const isUnread = !evt.read_at
  const isCritical = evt.priority === 'critical'

  return (
    <button
      type="button"
      tabIndex={0}
      data-signal-index={dataIndex}
      data-testid={`signal-row-${evt.id}`}
      onClick={() => onOpen(evt)}
      className={cn(
        'group block w-full px-3 py-2 text-left transition-colors',
        isCritical && 'border-l border-destructive',
        isUnread
          ? 'bg-card/60 hover:bg-accent/30 focus:bg-accent/40'
          : 'hover:bg-accent/20 focus:bg-accent/30 opacity-80',
      )}
    >
      <div className="flex items-baseline gap-2">
        <span
          aria-label={`source-${evt.source || 'mesh'}`}
          className={cn('shrink-0 font-mono text-[12px] leading-none', priorityGlyphTone(evt.priority))}
        >
          {glyphFor(evt.source || 'mesh')}
        </span>
        <span
          className="shrink-0 font-mono text-[10px] tabular-nums text-muted-foreground/70"
          title={new Date(evt.created_at).toLocaleString()}
        >
          {formatHM(evt.created_at)}
        </span>
        {evt.agent_name && (
          <span className="shrink truncate text-[11px] text-muted-foreground/80">
            · {evt.agent_name}
          </span>
        )}
        {isUnread && (
          <span
            aria-label="unread"
            className="ml-auto h-1.5 w-1.5 shrink-0 rounded-full bg-primary"
          />
        )}
      </div>
      <div
        className={cn(
          'mt-0.5 line-clamp-1 font-mono text-[12.5px]',
          isUnread ? 'font-medium text-foreground' : 'text-foreground/80',
        )}
      >
        {evt.title}
      </div>
      {evt.body && (
        <div className="mt-0.5 line-clamp-1 font-mono text-[11px] text-muted-foreground/80">
          {evt.body}
        </div>
      )}
      {isUnread && (
        <div className="mt-1 flex items-center gap-2 opacity-0 transition-opacity group-hover:opacity-100 group-focus-within:opacity-100">
          <span
            role="button"
            tabIndex={-1}
            onClick={(e) => {
              e.stopPropagation()
              onMarkRead(evt.id)
            }}
            className="cursor-pointer font-mono text-[10px] uppercase tracking-wider text-muted-foreground hover:text-foreground"
          >
            r mark read
          </span>
        </div>
      )}
    </button>
  )
}

function formatHM(iso: string): string {
  try {
    const d = new Date(iso)
    return `${pad(d.getHours())}:${pad(d.getMinutes())}`
  } catch {
    return '--:--'
  }
}

function pad(n: number): string {
  return n < 10 ? `0${n}` : `${n}`
}
