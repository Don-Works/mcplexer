import { cn } from '@/lib/utils'
import type { NotificationFilter } from './store'

interface Props {
  filter: NotificationFilter
  counts: Record<NotificationFilter, number>
  onChange: (f: NotificationFilter) => void
}

const TABS: { id: NotificationFilter; key: string }[] = [
  { id: 'all', key: '1' },
  { id: 'mesh', key: '2' },
  { id: 'approval', key: '3' },
  { id: 'system', key: '4' },
  { id: 'secret', key: '5' },
]

export function SignalFilterRail({ filter, counts, onChange }: Props) {
  return (
    <nav
      role="tablist"
      aria-label="Signal filter"
      className="flex shrink-0 items-center gap-1 border-b border-sidebar-border px-2 py-1.5 font-mono text-[11px]"
    >
      {TABS.map((t) => {
        const active = t.id === filter
        const count = counts[t.id]
        return (
          <button
            key={t.id}
            role="tab"
            aria-selected={active}
            onClick={() => onChange(t.id)}
            data-testid={`signal-filter-${t.id}`}
            className={cn(
              'relative px-2 py-1 transition-colors',
              active
                ? 'text-foreground'
                : 'text-muted-foreground/70 hover:text-foreground',
            )}
          >
            <span className="uppercase tracking-wider">{t.id}</span>
            {count > 0 && (
              <span
                className={cn(
                  'ml-1 tabular-nums',
                  active ? 'text-primary' : 'text-muted-foreground/50',
                )}
              >
                {count}
              </span>
            )}
            {active && (
              <span
                aria-hidden
                className="absolute inset-x-1 -bottom-1.5 h-px bg-primary"
              />
            )}
          </button>
        )
      })}
    </nav>
  )
}
