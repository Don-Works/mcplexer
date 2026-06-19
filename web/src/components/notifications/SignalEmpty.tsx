import type { NotificationFilter } from './store'

interface Props {
  filter: NotificationFilter
}

// Empty state teaches the producer-side API. Same idiom as the cmd+K
// palette empty — instructive, not blank.

export function SignalEmpty({ filter }: Props) {
  if (filter !== 'all') {
    return (
      <div className="px-4 py-12 text-center font-mono text-[12px] text-muted-foreground">
        <p>no {filter} notifications</p>
        <p className="mt-2 text-[11px] text-muted-foreground/60">
          showing zero results for filter <span className="text-foreground/70">{filter}</span>
        </p>
      </div>
    )
  }
  return (
    <div className="px-4 py-12 text-center font-mono text-muted-foreground">
      <pre className="mx-auto mb-3 text-[10px] leading-tight text-muted-foreground/40" aria-hidden>
        {`.··.
(    )
 \`--'`}
      </pre>
      <p className="text-[12px] text-foreground/70">Signal is quiet.</p>
      <p className="mx-auto mt-2 max-w-xs text-[11px] text-muted-foreground/60">
        Agents will surface here when they need you — set{' '}
        <code className="border border-border bg-muted/40 px-1 py-px text-[10px] text-foreground/80">
          notify_user: true
        </code>{' '}
        on a <code className="border border-border bg-muted/40 px-1 py-px text-[10px] text-foreground/80">mesh.send</code>.
      </p>
    </div>
  )
}
