// MemorySearchBar — terminal-bar natural-language search for /memory/all.
// Mirrors pages/skills/SearchBlock.tsx visually so muscle memory carries
// across the two surfaces.

import { CornerDownLeft, Loader2, Search, X } from 'lucide-react'
import { cn } from '@/lib/utils'

interface Props {
  value: string
  onChange: (v: string) => void
  onSubmit: () => void
  onClear: () => void
  searching: boolean
  hitCount: number | null
}

export function MemorySearchBar({
  value,
  onChange,
  onSubmit,
  onClear,
  searching,
  hitCount,
}: Props) {
  const canSubmit = !searching && value.trim().length > 0
  return (
    <div
      className={cn(
        'group/search flex items-stretch border border-border bg-card transition-colors',
        'focus-within:border-primary/60 focus-within:ring-1 focus-within:ring-primary/30',
      )}
    >
      <div className="flex items-center pl-4 pr-3 text-muted-foreground/70 transition-colors group-focus-within/search:text-primary">
        <Search className="h-4 w-4" aria-hidden />
      </div>
      <input
        value={value}
        placeholder="Search memories: agent name, tag, context…"
        onChange={(e) => onChange(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === 'Enter') {
            e.preventDefault()
            onSubmit()
          }
          if (e.key === 'Escape') {
            e.preventDefault()
            onClear()
          }
        }}
        className={cn(
          'h-12 min-w-0 flex-1 border-0 bg-transparent p-0 pr-3 text-[15px] text-foreground placeholder:text-muted-foreground/60',
          'outline-none focus:outline-none focus-visible:outline-none focus:ring-0 focus-visible:ring-0 focus-visible:ring-offset-0',
        )}
        data-testid="memory-search"
        aria-label="Search memories"
      />
      <div className="flex shrink-0 items-center gap-2 pr-3 text-[11px] text-muted-foreground/80">
        {hitCount !== null && (
          <span className="uppercase tracking-wider">
            {hitCount} match{hitCount === 1 ? '' : 'es'}
          </span>
        )}
        {value && (
          <button
            type="button"
            onClick={onClear}
            className="grid h-7 w-7 place-items-center text-muted-foreground/70 transition-colors hover:text-foreground focus-visible:ring-0 focus-visible:ring-offset-0"
            aria-label="Clear search"
            data-testid="memory-search-clear"
          >
            <X className="h-3.5 w-3.5" />
          </button>
        )}
      </div>
      <button
        type="button"
        onClick={onSubmit}
        disabled={!canSubmit}
        className={cn(
          'flex shrink-0 items-center gap-2 self-stretch border-l border-border px-4',
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
        <span className="font-semibold">Search</span>
      </button>
    </div>
  )
}
