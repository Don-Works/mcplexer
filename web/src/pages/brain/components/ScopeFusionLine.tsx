import { cn } from '@/lib/utils'

// ScopeFusionLine renders the agent's literal scope-fusion string verbatim
// (e.g. `acme-api ∪ acme ∪ global`) as a mono footer — the "what would my
// agent see right now" POV (DESIGN §3.1, §4.1). Mono, never decorated; this
// is machine output, not prose. Empty scope renders nothing.
interface Props {
  scope: string
  className?: string
}

export function ScopeFusionLine({ scope, className }: Props) {
  if (!scope) return null
  return (
    <div
      className={cn(
        'border-t border-border px-3 py-1.5 font-mono text-[11px] text-muted-foreground',
        className,
      )}
    >
      <span className="text-muted-foreground/70">scope:</span> {scope}
    </div>
  )
}
