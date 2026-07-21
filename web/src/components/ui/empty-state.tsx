import { cn } from '@/lib/utils'

// EmptyState is the single shape every "nothing here yet" surface
// renders. The codebase used to have three competing patterns
// (centered icon + heading + paragraph, inline table-cell variant, plain
// muted paragraph). One primitive replaces all three.
//
// Use `density='inline'` when rendering inside a table row that needs
// to span columns; the wrapper gets neutral padding and no card chrome.
// Default `density='card'` renders a dashed-border card suitable for
// dropping above or below other content as a hero empty state.

type Density = 'card' | 'inline'

interface Props {
  icon?: React.ReactNode
  title: string
  description?: React.ReactNode
  action?: React.ReactNode
  density?: Density
  className?: string
  // testid is wired so tests can find the empty state by intent
  // (e.g. `mesh-empty`, `audit-empty`) without leaking layout details.
  testid?: string
}

export function EmptyState({
  icon,
  title,
  description,
  action,
  density = 'card',
  className,
  testid,
}: Props) {
  const isCard = density === 'card'
  return (
    <div
      data-testid={testid}
      className={cn(
        'flex flex-col items-center justify-center text-center',
        isCard ? 'border border-dashed border-border bg-card/30 px-6 py-12' : 'px-3 py-6',
        className,
      )}
    >
      {icon && (
        <div className="mb-3 text-muted-foreground/40" aria-hidden>
          {icon}
        </div>
      )}
      <h3 className="text-sm font-semibold text-foreground">{title}</h3>
      {description && (
        <div className="mt-1.5 max-w-md text-xs leading-relaxed text-muted-foreground">
          {description}
        </div>
      )}
      {action && <div className="mt-4">{action}</div>}
    </div>
  )
}
