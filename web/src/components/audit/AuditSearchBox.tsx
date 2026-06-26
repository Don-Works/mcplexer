import { Search, X } from 'lucide-react'
import { Input } from '@/components/ui/input'
import type { AuditCapabilities, AuditSearchMode } from '@/api/types'
import { cn } from '@/lib/utils'

// Map the active/available ranking mode to its badge label. We badge by what
// actually answered (mode prop) when known, otherwise by the best capability
// the install advertises — so the operator always knows how fuzzy results are.
const MODE_LABEL: Record<AuditSearchMode, string> = {
  vector: 'Semantic',
  tfidf: 'Smart',
  fts: 'Text',
}

function bestMode(caps?: AuditCapabilities): AuditSearchMode {
  if (caps?.search.vector) return 'vector'
  if (caps?.search.tfidf) return 'tfidf'
  return 'fts'
}

function placeholderFor(mode: AuditSearchMode): string {
  switch (mode) {
    case 'vector':
      return 'Search audit log by meaning…'
    case 'tfidf':
      return 'Search audit log (smart ranking)…'
    default:
      return 'Search audit log…'
  }
}

/**
 * AuditSearchBox — free-text audit search input. Controlled (`value` /
 * `onChange`); `onSubmit` fires on Enter. A trailing badge advertises the
 * ranking mode: the resolved `mode` once a search has run, otherwise the best
 * mode the install supports (`capabilities`). Mono, electric-blue focus ring,
 * sharp corners — house style.
 */
export function AuditSearchBox({
  value,
  onChange,
  onSubmit,
  mode,
  capabilities,
  placeholder,
  autoFocus,
  className,
}: {
  value: string
  onChange: (value: string) => void
  onSubmit?: (value: string) => void
  mode?: AuditSearchMode | null
  capabilities?: AuditCapabilities
  placeholder?: string
  autoFocus?: boolean
  className?: string
}) {
  const shownMode = mode ?? bestMode(capabilities)
  const resolvedPlaceholder = placeholder ?? placeholderFor(shownMode)

  return (
    <div
      className={cn(
        'group flex min-w-0 items-center gap-2 border border-border bg-background px-2.5 transition-colors focus-within:border-primary/60 focus-within:ring-1 focus-within:ring-primary/30',
        className,
      )}
    >
      <Search className="h-3.5 w-3.5 shrink-0 text-muted-foreground" />
      <Input
        type="search"
        value={value}
        onChange={(e) => onChange(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === 'Enter') {
            e.preventDefault()
            onSubmit?.(value)
          } else if (e.key === 'Escape' && value) {
            e.preventDefault()
            onChange('')
          }
        }}
        autoFocus={autoFocus}
        placeholder={resolvedPlaceholder}
        aria-label="Search audit log"
        data-testid="audit-search-input"
        className="h-9 min-w-0 flex-1 border-0 bg-transparent px-0 font-mono text-sm shadow-none focus-visible:ring-0 focus-visible:ring-offset-0 [&::-webkit-search-cancel-button]:appearance-none"
      />
      {value && (
        <button
          type="button"
          aria-label="Clear search"
          onClick={() => onChange('')}
          className="shrink-0 text-muted-foreground transition-colors hover:text-foreground focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-primary/40"
        >
          <X className="h-3.5 w-3.5" />
        </button>
      )}
      <span className="shrink-0 border border-primary/30 bg-primary/5 px-1.5 py-0.5 font-mono text-[10px] uppercase tracking-wider text-primary">
        {MODE_LABEL[shownMode]}
      </span>
    </div>
  )
}
