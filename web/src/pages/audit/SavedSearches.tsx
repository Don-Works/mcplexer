import { useCallback, useState } from 'react'
import { Bookmark, Plus, Trash2 } from 'lucide-react'
import { Input } from '@/components/ui/input'
import { useApi } from '@/hooks/use-api'
import {
  createSavedSearch,
  deleteSavedSearch,
  listSavedSearches,
} from '@/api/client'
import type { AuditFilter, SavedSearch } from '@/api/types'
import { cn } from '@/lib/utils'

// FacetLabel mirror — keeps the left rail headings visually identical to the
// facet rail without importing a private helper.
function RailLabel({ children }: { children: React.ReactNode }) {
  return (
    <span className="text-[10px] font-semibold uppercase tracking-wider text-muted-foreground/70">
      {children}
    </span>
  )
}

/**
 * SavedSearches — left-rail list of persisted query + facet sets. Re-applying
 * one merges its stored {q, filter} back into the page's filter; the "+" saves
 * the current query/filter under a typed name. Mono, sharp corners, dashed
 * inactive borders — same house style as the facet rail.
 */
export function SavedSearches({
  currentQuery,
  currentFilter,
  workspaceId,
  onApply,
}: {
  currentQuery: string
  currentFilter: AuditFilter
  workspaceId?: string
  onApply: (q: string, filter: AuditFilter) => void
}) {
  const fetcher = useCallback(() => listSavedSearches(), [])
  const { data, loading, refetch } = useApi(fetcher)
  const searches = data ?? []

  const [adding, setAdding] = useState(false)
  const [name, setName] = useState('')
  const [busy, setBusy] = useState(false)

  const save = useCallback(async () => {
    const trimmed = name.trim()
    if (!trimmed || busy) return
    setBusy(true)
    try {
      await createSavedSearch({
        name: trimmed,
        q: currentQuery,
        filter: currentFilter,
        threshold_count: 0,
        window_sec: 0,
        workspace_id: workspaceId ?? '',
        enabled: false,
      })
      setName('')
      setAdding(false)
      refetch()
    } finally {
      setBusy(false)
    }
  }, [name, busy, currentQuery, currentFilter, workspaceId, refetch])

  const remove = useCallback(
    async (id: string) => {
      await deleteSavedSearch(id)
      refetch()
    },
    [refetch],
  )

  return (
    <div className="space-y-2">
      <div className="flex items-center justify-between">
        <RailLabel>Saved</RailLabel>
        <button
          type="button"
          data-testid="audit-saved-add"
          aria-label="Save current search"
          onClick={() => setAdding((v) => !v)}
          className="text-muted-foreground transition-colors hover:text-foreground"
        >
          <Plus className="h-3.5 w-3.5" />
        </button>
      </div>

      {adding && (
        <div className="flex items-center gap-1">
          <Input
            value={name}
            autoFocus
            placeholder="search name…"
            data-testid="audit-saved-name"
            onChange={(e) => setName(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter') void save()
              else if (e.key === 'Escape') {
                setAdding(false)
                setName('')
              }
            }}
            className="h-8 rounded-none border-border bg-background font-mono text-xs focus-visible:ring-1 focus-visible:ring-primary/30"
          />
          <button
            type="button"
            onClick={() => void save()}
            disabled={!name.trim() || busy}
            className="shrink-0 border border-primary/40 bg-primary/5 px-2 py-1.5 font-mono text-[11px] text-primary transition-colors hover:bg-primary/10 disabled:opacity-40"
          >
            Save
          </button>
        </div>
      )}

      {loading && searches.length === 0 ? (
        <p className="font-mono text-[11px] text-muted-foreground/60">Loading…</p>
      ) : searches.length === 0 ? (
        <p className="font-mono text-[11px] text-muted-foreground/60">
          No saved searches
        </p>
      ) : (
        <ul className="space-y-1">
          {searches.map((s: SavedSearch) => (
            <li key={s.id} className="group flex items-center gap-1">
              <button
                type="button"
                onClick={() => onApply(s.q, s.filter)}
                title={s.q || s.name}
                className={cn(
                  'flex min-w-0 flex-1 items-center gap-1.5 border border-dashed border-border px-2 py-1 text-left font-mono text-[11px] text-muted-foreground transition-colors',
                  'hover:border-primary/40 hover:text-foreground',
                )}
              >
                <Bookmark className="h-3 w-3 shrink-0 opacity-60" />
                <span className="truncate">{s.name}</span>
              </button>
              <button
                type="button"
                aria-label={`Delete saved search ${s.name}`}
                onClick={() => void remove(s.id)}
                className="shrink-0 text-muted-foreground/50 opacity-0 transition-opacity hover:text-destructive group-hover:opacity-100"
              >
                <Trash2 className="h-3 w-3" />
              </button>
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}
