import { CornerDownLeft, Loader2, Search, X } from 'lucide-react'
import { cn } from '@/lib/utils'
import type { SkillSearchHit } from '@/api/client'

// Search — primary action, given workspace-grade visual weight. This is the
// strongest single design moment in the app per /impeccable; the rest of the
// skills surface converges on this terminal-bar treatment.

interface Props {
  value: string
  onChange: (v: string) => void
  onSubmit: (v: string) => void
  searching: boolean
  hits: SkillSearchHit[] | null
  onClear: () => void
}

export function SearchBlock({ value, onChange, onSubmit, searching, hits, onClear }: Props) {
  // One unified terminal-bar: outer container owns the border + bg + focus
  // ring, the children sit flush so there's no nested-card double frame.
  // Native <input> on purpose — shadcn's <Input> ships dark:bg-input/30
  // and a 3px focus ring that fight the outer container.
  const canSubmit = !searching && value.trim().length > 0
  return (
    <div
      className={cn(
        'group/search flex items-stretch border border-border bg-card transition-colors',
        'focus-within:border-primary/60 focus-within:ring-1 focus-within:ring-primary/30',
      )}
    >
      <div className="flex items-center pl-3 pr-2 text-muted-foreground/70 transition-colors group-focus-within/search:text-primary sm:pl-4 sm:pr-3">
        <Search className="h-4 w-4" aria-hidden />
      </div>
      <input
        value={value}
        placeholder="Ask in plain English..."
        onChange={(e) => onChange(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === 'Enter') onSubmit(value)
          if (e.key === 'Escape') onClear()
        }}
        className={cn(
          'h-11 min-w-0 flex-1 border-0 bg-transparent p-0 pr-2 text-[15px] text-foreground placeholder:text-muted-foreground/60 sm:h-12 sm:pr-3',
          // Suppress the global :focus-visible ring on the input so the
          // single focus indicator is the outer container's border tint.
          'outline-none focus:outline-none focus-visible:outline-none focus:ring-0 focus-visible:ring-0 focus-visible:ring-offset-0',
        )}
        data-testid="skill-search"
        aria-label="Ask the skills registry"
      />
      <div className="flex shrink-0 items-center gap-1.5 pr-2 text-[11px] text-muted-foreground/80 sm:gap-2 sm:pr-3">
        {hits && (
          <span className="hidden uppercase tracking-wider sm:inline">
            {hits.length} match{hits.length === 1 ? '' : 'es'}
          </span>
        )}
        {value && (
          <button
            type="button"
            onClick={onClear}
            className="grid h-7 w-7 place-items-center text-muted-foreground/70 transition-colors hover:text-foreground focus-visible:ring-0 focus-visible:ring-offset-0"
            aria-label="Clear search"
          >
            <X className="h-3.5 w-3.5" />
          </button>
        )}
      </div>
      <button
        type="button"
        onClick={() => onSubmit(value)}
        disabled={!canSubmit}
        className={cn(
          'flex shrink-0 items-center gap-2 self-stretch border-l border-border px-3 sm:px-4',
          'text-[11px] uppercase tracking-wider transition-colors',
          'focus-visible:ring-0 focus-visible:ring-offset-0',
          canSubmit
            ? 'text-primary/90 hover:bg-primary/10 hover:text-primary'
            : 'cursor-not-allowed text-muted-foreground/40',
        )}
        aria-label="Run search"
      >
        {searching ? (
          <Loader2 className="h-3.5 w-3.5 animate-spin" />
        ) : (
          <CornerDownLeft className="h-3.5 w-3.5" aria-hidden />
        )}
        <span className="hidden font-semibold sm:inline">Ask</span>
      </button>
    </div>
  )
}
