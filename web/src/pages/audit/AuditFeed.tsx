import { useEffect, useRef } from 'react'
import { Button } from '@/components/ui/button'
import { AuditTable } from '@/components/audit/AuditTable'
import { AuditTopBar } from '@/pages/audit/AuditTopBar'
import type {
  AuditCapabilities,
  AuditFilter,
  AuditRecord,
  AuditSearchMode,
  AuditSort,
} from '@/api/types'

/**
 * AuditFeed — the Mission Control center column: search/live top bar, the
 * keyset-paginated table, the total + "Load more" footer, and an
 * IntersectionObserver sentinel that auto-loads the next window as it scrolls
 * into view. When a ranked search is active, sorting + pagination are disabled
 * (the search owns the result order) and the total reflects the search.
 */
export function AuditFeed({
  records,
  selectedId,
  sort,
  dense,
  liveCount,
  total,
  searchActive,
  searchTotal,
  filtered,
  loading,
  feedError,
  searchError,
  hasMore,
  query,
  searchMode,
  capabilities,
  connected,
  paused,
  bufferedCount,
  wsName,
  asName,
  onSelect,
  onSort,
  onFilter,
  onQueryChange,
  onSearchSubmit,
  onTogglePause,
  onFlush,
  onLoadMore,
}: {
  records: AuditRecord[]
  selectedId?: string | null
  sort?: AuditSort
  dense: boolean
  liveCount: number
  total: number
  searchActive: boolean
  searchTotal: number
  filtered: boolean
  loading: boolean
  feedError: string | null
  searchError: string | null
  hasMore: boolean
  query: string
  searchMode: AuditSearchMode | null
  capabilities: AuditCapabilities
  connected: boolean
  paused: boolean
  bufferedCount: number
  wsName: (id: string) => string
  asName: (id: string) => string
  onSelect: (record: AuditRecord) => void
  onSort: (sort: AuditSort) => void
  onFilter: (patch: Partial<AuditFilter>) => void
  onQueryChange: (v: string) => void
  onSearchSubmit: (v: string) => void
  onTogglePause: () => void
  onFlush: () => void
  onLoadMore: () => void
}) {
  const sentinelRef = useRef<HTMLDivElement | null>(null)
  useEffect(() => {
    const el = sentinelRef.current
    if (!el || searchActive || !hasMore) return
    const io = new IntersectionObserver(
      (entries) => entries[0]?.isIntersecting && onLoadMore(),
      { rootMargin: '400px' },
    )
    io.observe(el)
    return () => io.disconnect()
  }, [onLoadMore, hasMore, searchActive])

  return (
    <main className="min-w-0 space-y-3">
      <AuditTopBar
        query={query}
        onQueryChange={onQueryChange}
        onSearchSubmit={onSearchSubmit}
        searchMode={searchMode}
        capabilities={capabilities}
        connected={connected}
        paused={paused}
        onTogglePause={onTogglePause}
        bufferedCount={bufferedCount}
        onFlush={onFlush}
      />

      {feedError && <p className="text-sm text-destructive">Error: {feedError}</p>}
      {searchError && <p className="text-sm text-destructive">Search error: {searchError}</p>}

      <div className="border border-border bg-card">
        <AuditTable
          records={records}
          selectedId={selectedId}
          sort={sort}
          dense={dense}
          liveCount={searchActive ? 0 : liveCount}
          loading={loading}
          emptyTitle={
            searchActive ? 'No matches' : filtered ? 'No matching records' : undefined
          }
          emptyHint={
            searchActive
              ? 'No matches for this search'
              : filtered
                ? 'Try widening or clearing filters'
                : undefined
          }
          wsName={wsName}
          asName={asName}
          onSelect={onSelect}
          onSort={searchActive ? undefined : onSort}
          onFilter={onFilter}
        />
      </div>

      <div className="flex items-center justify-between text-sm text-muted-foreground">
        <span className="font-mono tabular-nums">
          {searchActive ? `${searchTotal} results` : `${total} total`}
        </span>
        {!searchActive && hasMore && (
          <Button
            variant="outline"
            size="sm"
            data-testid="audit-load-more"
            disabled={loading}
            onClick={onLoadMore}
          >
            {loading ? 'Loading…' : 'Load more'}
          </Button>
        )}
      </div>
      {!searchActive && hasMore && <div ref={sentinelRef} className="h-px" />}
    </main>
  )
}
