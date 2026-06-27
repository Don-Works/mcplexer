import { useCallback, useMemo, useState } from 'react'
import { Button } from '@/components/ui/button'
import { Rows2, Rows3, SlidersHorizontal, X } from 'lucide-react'
import { useApi } from '@/hooks/use-api'
import { useAuditStream } from '@/hooks/use-audit-stream'
import { useAuditFeed } from '@/hooks/use-audit-feed'
import { useAuditCapabilities } from '@/hooks/use-audit-capabilities'
import { useAuditSearch } from '@/hooks/use-audit-search'
import {
  listAuthScopes,
  listDownstreams,
  listRoutes,
  listWorkspaces,
} from '@/api/client'
import type { AuditFilter } from '@/api/types'
import { AuditDetailDialog } from '@/components/AuditDetailDialog'
import { AuditInspector } from '@/components/audit/AuditInspector'
import { type FacetOption } from '@/components/audit/AuditFacetRail'
import { AuditLeftRail } from '@/pages/audit/AuditLeftRail'
import { AuditFeed } from '@/pages/audit/AuditFeed'
import { useAuditUrlFilter } from '@/pages/audit/use-audit-url-filter'
import { useAuditSelection } from '@/pages/audit/use-audit-selection'
import { cn } from '@/lib/utils'

const PAGE_SIZE = 50

export function AuditPage() {
  const { filter, setFilter, searchParams, setSearchParams } = useAuditUrlFilter(PAGE_SIZE)

  const workspacesFetcher = useCallback(() => listWorkspaces(), [])
  const { data: workspaces } = useApi(workspacesFetcher)
  const authScopesFetcher = useCallback(() => listAuthScopes(), [])
  const { data: authScopes } = useApi(authScopesFetcher)
  const downstreamsFetcher = useCallback(() => listDownstreams(), [])
  const { data: downstreams } = useApi(downstreamsFetcher)
  const routesFetcher = useCallback(() => listRoutes(), [])
  const { data: routes } = useApi(routesFetcher)

  const wsName = useCallback(
    (id: string) => workspaces?.find((w) => w.id === id)?.name ?? id,
    [workspaces],
  )
  const asName = useCallback(
    (id: string) => authScopes?.find((a) => a.id === id)?.name ?? id,
    [authScopes],
  )

  const wsOptions = useMemo<FacetOption[]>(
    () => (workspaces ?? []).map((w) => ({ value: w.id, label: w.name })),
    [workspaces],
  )
  const serverOptions = useMemo<FacetOption[]>(
    () => (downstreams ?? []).map((s) => ({ value: s.id, label: s.name })),
    [downstreams],
  )
  const routeOptions = useMemo<FacetOption[]>(
    () => (routes ?? []).map((r) => ({ value: r.id, label: r.name || r.path_glob })),
    [routes],
  )

  const { capabilities } = useAuditCapabilities()
  const search = useAuditSearch()

  // --- Live stream (dedupe against the loaded keyset feed) ---
  const streamFilter = useMemo(
    () => ({
      workspace_id: filter.workspace_id,
      tool_name: filter.tool_name,
      status: filter.status,
      execution_id: filter.execution_id,
      session_id: filter.session_id,
    }),
    [filter.workspace_id, filter.tool_name, filter.status, filter.execution_id, filter.session_id],
  )
  const { records: liveRecords, connected, clear, paused, pause, resume, bufferedCount } =
    useAuditStream(streamFilter)

  // --- Keyset feed: cursor-paginated, accumulating, reset on query change ---
  const {
    records: pages,
    total,
    loading: feedLoading,
    error: feedError,
    hasMore,
    loadMore,
  } = useAuditFeed(filter)
  const loadedPages = pages ?? []

  // Live events not yet in the keyset feed, prepended to the top.
  const uniqueLive = useMemo(() => {
    const ids = new Set(loadedPages.map((r) => r.id))
    return liveRecords.filter((r) => !ids.has(r.id))
  }, [loadedPages, liveRecords])

  // When a search is active, its ranked results replace the feed entirely.
  const searchActive = search.results !== null

  // Any active filter dimension (drives an accurate empty state — "no matching
  // records" vs the live-idle "waiting for events").
  const filtered = useMemo(
    () =>
      Boolean(
        filter.workspace_id || filter.status || filter.tool_name ||
          filter.actor_kind || filter.actor_id || filter.downstream_server_id ||
          filter.route_rule_id || filter.client_type || filter.error_code ||
          filter.tier || filter.cache_hit !== undefined ||
          filter.min_latency_ms !== undefined || filter.after || filter.before ||
          filter.q || filter.session_id || filter.execution_id,
      ),
    [filter],
  )
  const feedRecords = useMemo(
    () => (searchActive ? search.results ?? [] : [...uniqueLive, ...loadedPages]),
    [searchActive, search.results, uniqueLive, loadedPages],
  )

  // --- Selection (URL-backed, deep-link fallback + keyboard nav) ---
  const { selected, setSelected, goPrev, goNext, hasPrev, hasNext, isWide } =
    useAuditSelection(feedRecords, searchParams, setSearchParams)

  // --- Local UI state ---
  const [dense, setDense] = useState(false)
  const [filtersOpen, setFiltersOpen] = useState(false) // mobile facet sheet
  const showInlineInspector = isWide && selected !== null

  const onFilterPatch = useCallback(
    (patch: Partial<AuditFilter>) => {
      search.clear()
      setFilter((f) => ({ ...f, ...patch }))
    },
    [search, setFilter],
  )

  const onQueryChange = useCallback(
    (q: string) => {
      // Typing updates the lexical feed filter; clearing exits ranked search.
      if (search.results !== null) search.clear()
      setFilter((f) => ({ ...f, q: q || undefined }))
    },
    [search, setFilter],
  )
  const onSearchSubmit = useCallback(
    (q: string) => {
      if (q.trim()) search.run(q, filter)
      else search.clear()
    },
    [search, filter],
  )

  const onApplySaved = useCallback(
    (q: string, f: AuditFilter) => {
      search.clear()
      setFilter({ ...f, limit: PAGE_SIZE, q: q || undefined })
    },
    [search, setFilter],
  )

  const leftRail = (
    <AuditLeftRail
      filter={filter}
      capabilities={capabilities}
      workspaces={wsOptions}
      servers={serverOptions}
      routes={routeOptions}
      onFilterPatch={onFilterPatch}
      onApplySaved={onApplySaved}
    />
  )

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <h1 className="text-xl font-semibold tracking-tight">Audit Logs</h1>
        <div className="flex shrink-0 items-center gap-2">
          {uniqueLive.length > 0 && !searchActive && (
            <Button variant="ghost" size="sm" onClick={clear} data-testid="audit-clear-live">
              Clear live
            </Button>
          )}
          <Button
            variant="ghost"
            size="sm"
            className="h-8 w-8 p-0"
            aria-label={dense ? 'Comfortable rows' : 'Dense rows'}
            data-testid="audit-density-toggle"
            onClick={() => setDense((v) => !v)}
          >
            {dense ? <Rows3 className="h-4 w-4" /> : <Rows2 className="h-4 w-4" />}
          </Button>
          <Button
            variant="outline"
            size="sm"
            className="gap-1.5 lg:hidden"
            data-testid="audit-filters-toggle"
            onClick={() => setFiltersOpen((v) => !v)}
          >
            <SlidersHorizontal className="h-3.5 w-3.5" />
            Filters
          </Button>
        </div>
      </div>

      {/* Mobile filter drawer */}
      {filtersOpen && (
        <div className="border border-border bg-card p-4 lg:hidden">
          <div className="mb-3 flex items-center justify-between">
            <span className="text-sm font-medium">Filters</span>
            <button type="button" aria-label="Close filters" onClick={() => setFiltersOpen(false)}>
              <X className="h-4 w-4 text-muted-foreground" />
            </button>
          </div>
          {leftRail}
        </div>
      )}

      <div
        className={cn(
          'grid grid-cols-1 gap-4 lg:grid-cols-[15rem_minmax(0,1fr)]',
          showInlineInspector && '2xl:grid-cols-[15rem_minmax(0,1fr)_24rem]',
        )}
      >
        {/* LEFT rail (desktop) */}
        <aside className="hidden lg:block">
          <div className="sticky top-4 max-h-[calc(100vh-2rem)] overflow-y-auto pr-1">{leftRail}</div>
        </aside>

        {/* CENTER feed */}
        <AuditFeed
          records={feedRecords}
          selectedId={selected?.id}
          sort={filter.sort}
          dense={dense}
          liveCount={uniqueLive.length}
          total={total}
          searchActive={searchActive}
          searchTotal={search.total}
          filtered={filtered}
          loading={searchActive ? search.loading : feedLoading}
          feedError={feedError}
          searchError={search.error}
          hasMore={hasMore}
          query={filter.q ?? ''}
          searchMode={search.mode}
          capabilities={capabilities}
          connected={connected}
          paused={paused}
          bufferedCount={bufferedCount}
          wsName={wsName}
          asName={asName}
          onSelect={setSelected}
          onSort={(s) => onFilterPatch({ sort: s })}
          onFilter={onFilterPatch}
          onQueryChange={onQueryChange}
          onSearchSubmit={onSearchSubmit}
          onTogglePause={() => (paused ? resume() : pause())}
          onFlush={resume}
          onLoadMore={loadMore}
        />

        {/* RIGHT inline inspector (desktop 2xl+) — replaces the modal drawer */}
        {showInlineInspector && selected && (
          <aside className="hidden 2xl:block">
            <div className="sticky top-4 max-h-[calc(100vh-2rem)] min-w-0 overflow-y-auto border border-border bg-card p-4 [scrollbar-gutter:stable]">
              <AuditInspector
                record={selected}
                wsName={wsName}
                asName={asName}
                onFilter={onFilterPatch}
                onClose={() => setSelected(null)}
              />
            </div>
          </aside>
        )}
      </div>

      {/* Mobile / tablet / laptop: Sheet drawer (below 2xl, where there's no inline pane).
          Only mounted when narrow so its built-in j/k nav can't double-fire
          alongside the inline inspector's keyboard handler. */}
      {!isWide && (
        <AuditDetailDialog
          record={selected}
          onClose={() => setSelected(null)}
          wsName={wsName}
          asName={asName}
          onPrev={goPrev}
          onNext={goNext}
          hasPrev={hasPrev}
          hasNext={hasNext}
        />
      )}
    </div>
  )
}
