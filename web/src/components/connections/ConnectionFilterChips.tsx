import type { ConnectionFilter } from './connection-model'

interface ConnectionCounts {
  all: number
  connected: number
  needsAuth: number
  available: number
}

export function ConnectionFilterChips({
  counts,
  filter,
  onChange,
}: {
  counts: ConnectionCounts
  filter: ConnectionFilter
  onChange: (filter: ConnectionFilter) => void
}) {
  const options: Array<{ key: ConnectionFilter; label: string; count: number }> = [
    { key: 'all', label: 'All servers', count: counts.all },
    { key: 'connected', label: 'Connected', count: counts.connected },
    { key: 'needs-auth', label: 'Needs auth', count: counts.needsAuth },
    { key: 'available', label: 'Not connected', count: counts.available },
  ]

  return (
    <div className="flex flex-nowrap items-center gap-1.5 overflow-x-auto pb-1" data-testid="connections-filter-chips">
      {options.map((option) => {
        const active = filter === option.key
        return (
          <button
            key={option.key}
            type="button"
            onClick={() => onChange(option.key)}
            className={
              'inline-flex shrink-0 items-center gap-1.5 border px-2.5 py-1 text-xs transition-colors ' +
              (active
                ? 'border-primary/60 bg-primary/10 text-primary'
                : 'border-border bg-card/40 text-muted-foreground hover:text-foreground')
            }
            data-testid={`connections-filter-${option.key}`}
          >
            <span>{option.label}</span>
            <span className={active ? 'text-primary' : 'text-muted-foreground/60'}>
              ({option.count})
            </span>
          </button>
        )
      })}
    </div>
  )
}
