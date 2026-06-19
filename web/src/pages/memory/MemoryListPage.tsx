// MemoryListPage — the browse + search surface at /memory/all.
//
// Mirrors AuditPage's list+drawer pattern: URL-backed selection
// (?selected=<id>), j/k arrow keyboard nav for cycling through the visible
// rows. The terminal-bar search is debounced 250ms and submits on Enter.

import { useCallback, useEffect, useMemo, useState } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { ArrowLeft, Brain, Network } from 'lucide-react'
import { toast } from 'sonner'
import { Card, CardContent } from '@/components/ui/card'
import { useMemoryList, useMemoryMutations } from '@/hooks/use-memory'
import type { MemoryEntry } from '@/api/memory'
import { getMemory, searchMemories } from '@/api/memory'
import { MemoryDetailDrawer } from './MemoryDetailDrawer'
import {
  MemoryListFilters,
  type KindFilter,
  type ScopeFilter,
} from './MemoryListFilters'
import { MemorySearchBar } from './MemorySearchBar'
import { MemoryTable } from './MemoryTable'
import { parseTags, scopeOf } from './memory-utils'

const LIST_LIMIT = 200
const SEARCH_DEBOUNCE_MS = 250
const STALE_AFTER_MS = 180 * 24 * 60 * 60 * 1000

function isStaleMemory(m: MemoryEntry): boolean {
  if (m.pinned || m.t_valid_end) return false
  const updated = new Date(m.updated_at).getTime()
  return Number.isFinite(updated) && Date.now() - updated > STALE_AFTER_MS
}

export function MemoryListPage() {
  const [searchParams, setSearchParams] = useSearchParams()

  // Filter state. Kind is forwarded to the backend; scope+tags+invalid are
  // applied client-side so chip toggles feel instant.
  const [scope, setScope] = useState<ScopeFilter>('all')
  const [kind, setKind] = useState<KindFilter>('all')
  const [selectedTags, setSelectedTags] = useState<string[]>([])
  const [includeInvalid, setIncludeInvalid] = useState(false)
  const [staleOnly, setStaleOnly] = useState(
    () => searchParams.get('stale') === '1',
  )

  useEffect(() => {
    setStaleOnly(searchParams.get('stale') === '1')
  }, [searchParams])

  // Search state
  const [searchInput, setSearchInput] = useState('')
  const [searchQuery, setSearchQuery] = useState('')
  const [searchHits, setSearchHits] = useState<MemoryEntry[] | null>(null)
  const [searching, setSearching] = useState(false)

  // Debounced auto-submit. Enter is also wired in the bar. Always go through
  // setTimeout so we never call setState synchronously in the effect body.
  useEffect(() => {
    const t = setTimeout(
      () => setSearchQuery(searchInput.trim()),
      searchInput ? SEARCH_DEBOUNCE_MS : 0,
    )
    return () => clearTimeout(t)
  }, [searchInput])

  // Run the search whenever query OR filters change while a query is active.
  useEffect(() => {
    let cancelled = false
    if (!searchQuery) {
      queueMicrotask(() => {
        if (cancelled) return
        setSearchHits(null)
        setSearching(false)
      })
      return () => {
        cancelled = true
      }
    }
    queueMicrotask(() => {
      if (cancelled) return
      setSearching(true)
      searchMemories({
        query: searchQuery,
        kind: kind === 'all' ? undefined : kind,
        tags: selectedTags.length > 0 ? selectedTags : undefined,
        include_invalid: includeInvalid,
        limit: 100,
      })
        .then((hits) => {
          if (cancelled) return
          setSearchHits(hits.map((h) => h.entry))
        })
        .catch((err: unknown) => {
          if (cancelled) return
          toast.error(err instanceof Error ? err.message : 'Search failed')
          setSearchHits([])
        })
        .finally(() => {
          if (!cancelled) setSearching(false)
        })
    })
    return () => {
      cancelled = true
    }
  }, [searchQuery, kind, selectedTags, includeInvalid])

  // Browse fetch (when no query). Uses kind only — scope + tags + invalid
  // are applied client-side.
  const listParams = useMemo(
    () => ({
      kind: kind === 'all' ? undefined : kind,
      limit: LIST_LIMIT,
      include_invalid: includeInvalid,
    }),
    [kind, includeInvalid],
  )
  const { data: browseRows, loading, error, refetch } = useMemoryList(listParams)
  const mut = useMemoryMutations()

  const source: MemoryEntry[] = useMemo(() => {
    const base = searchQuery ? searchHits ?? [] : browseRows ?? []
    return base
      .filter((m) => {
        const actualScope = scopeOf(m)
        if (scope !== 'all' && actualScope !== scope) return false
        return true
      })
      .filter((m) => !staleOnly || isStaleMemory(m))
      .filter((m) => {
        if (selectedTags.length === 0) return true
        const tags = parseTags(m.tags)
        return selectedTags.every((t) => tags.includes(t))
      })
  }, [searchQuery, searchHits, browseRows, scope, staleOnly, selectedTags])

  // Build the available tag set from the *scope-filtered but not tag-filtered*
  // view, so toggling a tag never makes its sibling tags disappear.
  const availableTags = useMemo(() => {
    const base = searchQuery ? searchHits ?? [] : browseRows ?? []
    const scoped = base.filter((m) => {
      const actualScope = scopeOf(m)
      if (scope !== 'all' && actualScope !== scope) return false
      if (staleOnly && !isStaleMemory(m)) return false
      return true
    })
    const set = new Set<string>()
    for (const m of scoped) for (const t of parseTags(m.tags)) set.add(t)
    return Array.from(set).sort()
  }, [searchQuery, searchHits, browseRows, scope, staleOnly])

  // URL-backed selection. When the row is in the current filtered view
  // we reuse the in-memory entry; otherwise (deep-link from dashboard /
  // signal-tray hitting a filter-hidden row) we fetch the entry by id
  // so the drawer opens regardless of filters — UI-4 fix.
  // Accept ?id= as an alias for ?selected= — memory-event deep links (from
  // the activity timeline + signal tray, via memoryEventLink) use ?id=, so
  // honour both or the drawer never opens on click-through.
  const selectedId = searchParams.get('selected') ?? searchParams.get('id')
  const [fallbackEntry, setFallbackEntry] = useState<MemoryEntry | null>(null)
  useEffect(() => {
    if (!selectedId) {
      setFallbackEntry(null)
      return
    }
    if (source.some((r) => r.id === selectedId)) {
      setFallbackEntry(null)
      return
    }
    let cancelled = false
    getMemory(selectedId)
      .then((m) => {
        if (!cancelled) setFallbackEntry(m)
      })
      .catch(() => {
        if (!cancelled) setFallbackEntry(null)
      })
    return () => {
      cancelled = true
    }
  }, [selectedId, source])
  const selected = useMemo<MemoryEntry | null>(
    () =>
      selectedId
        ? source.find((r) => r.id === selectedId) ?? fallbackEntry
        : null,
    [selectedId, source, fallbackEntry],
  )
  const setSelected = useCallback(
    (entry: MemoryEntry | null) => {
      setSearchParams(
        (prev) => {
          const next = new URLSearchParams(prev)
          if (entry) {
            next.set('selected', entry.id)
            next.delete('id')
          } else {
            next.delete('selected')
            next.delete('id')
          }
          return next
        },
        { replace: true },
      )
    },
    [setSearchParams],
  )

  const selectedIndex = selected ? source.findIndex((r) => r.id === selected.id) : -1
  const hasPrev = selectedIndex > 0
  const hasNext = selectedIndex >= 0 && selectedIndex < source.length - 1

  function toggleTag(t: string) {
    setSelectedTags((prev) =>
      prev.includes(t) ? prev.filter((x) => x !== t) : [...prev, t],
    )
  }

  function toggleStale() {
    setSearchParams(
      (prev) => {
        const next = new URLSearchParams(prev)
        if (staleOnly) next.delete('stale')
        else next.set('stale', '1')
        return next
      },
      { replace: true },
    )
  }

  async function handleInvalidate(id: string) {
    try {
      await mut.invalidate(id)
      toast.success('Memory invalidated')
      refetch()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Invalidate failed')
    }
  }
  async function handleDelete(id: string) {
    try {
      await mut.delete(id)
      toast.success('Memory deleted')
      refetch()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Delete failed')
    }
  }
  async function handleTogglePin(id: string, next: boolean) {
    try {
      await mut.setPinned(id, next)
      toast.success(next ? 'Memory pinned' : 'Memory unpinned')
      refetch()
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Pin update failed')
    }
  }

  return (
    <div className="space-y-5">
      <Link
        to="/memory"
        className="inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground transition-colors"
      >
        <ArrowLeft className="h-3 w-3" />
        Memory
      </Link>
      <header className="flex items-center justify-between">
        <h1 className="flex items-center gap-2 text-2xl font-semibold tracking-tight">
          <Brain className="h-5 w-5 text-primary" />
          All memories
        </h1>
        <div className="flex items-center gap-3">
          <Link
            to="/memory/graph"
            className="inline-flex items-center gap-1 border border-border px-2 py-1 font-mono text-[11px] text-muted-foreground transition-colors hover:border-primary/40 hover:text-foreground"
          >
            <Network className="h-3 w-3" />
            Graph view
          </Link>
          <span className="font-mono text-[11px] tabular-nums text-muted-foreground">
            {source.length} shown
          </span>
        </div>
      </header>

      <MemorySearchBar
        value={searchInput}
        onChange={setSearchInput}
        onSubmit={() => setSearchQuery(searchInput.trim())}
        onClear={() => {
          setSearchInput('')
          setSearchQuery('')
        }}
        searching={searching}
        hitCount={searchHits ? searchHits.length : null}
      />

      <MemoryListFilters
        scope={scope}
        onScope={setScope}
        kind={kind}
        onKind={setKind}
        selectedTags={selectedTags}
        availableTags={availableTags}
        onToggleTag={toggleTag}
        onClearTags={() => setSelectedTags([])}
        includeInvalid={includeInvalid}
        onToggleInvalid={() => setIncludeInvalid((v) => !v)}
        staleOnly={staleOnly}
        onToggleStale={toggleStale}
      />

      <Card>
        <CardContent className="pt-6">
          {loading && !browseRows && (
            <div className="flex items-center gap-2 text-muted-foreground">
              <div className="h-2 w-2 rounded-full bg-primary/60" />
              Loading…
            </div>
          )}
          {error && <p className="text-destructive">Error: {error}</p>}

          <MemoryTable
            rows={source}
            loading={loading}
            selectedId={selected?.id}
            onSelect={setSelected}
            hasQuery={!!searchQuery}
          />
        </CardContent>
      </Card>

      <MemoryDetailDrawer
        entry={selected}
        onClose={() => setSelected(null)}
        onPrev={() => {
          if (hasPrev) setSelected(source[selectedIndex - 1])
        }}
        onNext={() => {
          if (hasNext) setSelected(source[selectedIndex + 1])
        }}
        hasPrev={hasPrev}
        hasNext={hasNext}
        onInvalidate={handleInvalidate}
        onDelete={handleDelete}
        onTogglePin={handleTogglePin}
      />
    </div>
  )
}
