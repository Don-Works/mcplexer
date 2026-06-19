// MemoryAboutPage — the "tell me everything about X" surface at
// /memory/about/:entityKind/:entityId.
//
// Filters the memory store to rows linked to the named entity via the
// migration 076 join table, then renders the same MemoryTable + drawer
// pattern as /memory/all. Reuses searchMemories({ entities: [...] }) so
// the FTS5+vec0 RRF fusion still applies to the optional refine query.

import { useCallback, useEffect, useMemo, useState } from 'react'
import { Link, useParams, useSearchParams } from 'react-router-dom'
import { ArrowLeft, Brain } from 'lucide-react'
import { toast } from 'sonner'
import { Card, CardContent } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { searchMemories, getMemory, relatedEntities, spreadingActivation } from '@/api/memory'
import type { MemoryEntry, EntityCoLink } from '@/api/memory'
import { useMemoryMutations } from '@/hooks/use-memory'
import { MemoryDetailDrawer } from './MemoryDetailDrawer'
import { MemorySearchBar } from './MemorySearchBar'
import { MemoryTable } from './MemoryTable'

const SEARCH_DEBOUNCE_MS = 250
const LIMIT = 200

export function MemoryAboutPage() {
  const { entityKind = '', entityId = '' } = useParams<{
    entityKind: string
    entityId: string
  }>()
  const [searchParams, setSearchParams] = useSearchParams()

  const [searchInput, setSearchInput] = useState('')
  const [searchQuery, setSearchQuery] = useState('')
  const [rows, setRows] = useState<MemoryEntry[] | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [related, setRelated] = useState<EntityCoLink[] | null>(null)
  const [adjacent, setAdjacent] = useState<EntityCoLink[] | null>(null)
  const mut = useMemoryMutations()

  // Associative recall: load co-occurring entities + spreading-activation
  // neighbours whenever the entity tuple changes. Both queries are cheap +
  // independent, so fire them in parallel.
  useEffect(() => {
    if (!entityKind || !entityId) return
    let cancelled = false
    relatedEntities(entityKind, entityId, 10)
      .then((r) => {
        if (!cancelled) setRelated(r)
      })
      .catch(() => {
        if (!cancelled) setRelated([])
      })
    spreadingActivation(entityKind, entityId, 10)
      .then((r) => {
        if (!cancelled) setAdjacent(r)
      })
      .catch(() => {
        if (!cancelled) setAdjacent([])
      })
    return () => {
      cancelled = true
    }
  }, [entityKind, entityId])

  // Debounce the user's text input — matches the all-list page rhythm.
  useEffect(() => {
    const t = setTimeout(
      () => setSearchQuery(searchInput.trim()),
      searchInput ? SEARCH_DEBOUNCE_MS : 0,
    )
    return () => clearTimeout(t)
  }, [searchInput])

  // Re-fetch whenever the entity tuple OR refine query changes.
  useEffect(() => {
    let cancelled = false
    setLoading(true)
    setError(null)
    searchMemories({
      query: searchQuery,
      limit: LIMIT,
      entities: [{ kind: entityKind, id: entityId }],
    })
      .then((hits) => {
        if (cancelled) return
        setRows(hits.map((h) => h.entry))
      })
      .catch((err: unknown) => {
        if (cancelled) return
        setError(err instanceof Error ? err.message : 'Recall failed')
        setRows([])
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [entityKind, entityId, searchQuery])

  const source = useMemo(() => rows ?? [], [rows])

  // URL-backed drawer selection (mirrors MemoryListPage).
  const selectedId = searchParams.get('selected')
  const [fallback, setFallback] = useState<MemoryEntry | null>(null)
  useEffect(() => {
    if (!selectedId) {
      setFallback(null)
      return
    }
    if (source.some((r) => r.id === selectedId)) {
      setFallback(null)
      return
    }
    let cancelled = false
    getMemory(selectedId)
      .then((m) => {
        if (!cancelled) setFallback(m)
      })
      .catch(() => {
        if (!cancelled) setFallback(null)
      })
    return () => {
      cancelled = true
    }
  }, [selectedId, source])

  const selected = useMemo<MemoryEntry | null>(
    () =>
      selectedId ? source.find((r) => r.id === selectedId) ?? fallback : null,
    [selectedId, source, fallback],
  )
  const setSelected = useCallback(
    (entry: MemoryEntry | null) => {
      setSearchParams(
        (prev) => {
          const next = new URLSearchParams(prev)
          if (entry) next.set('selected', entry.id)
          else next.delete('selected')
          return next
        },
        { replace: true },
      )
    },
    [setSearchParams],
  )

  const selectedIndex = selected
    ? source.findIndex((r) => r.id === selected.id)
    : -1
  const hasPrev = selectedIndex > 0
  const hasNext = selectedIndex >= 0 && selectedIndex < source.length - 1

  async function handleInvalidate(id: string) {
    try {
      await mut.invalidate(id)
      toast.success('Memory invalidated')
      setRows((prev) => prev?.filter((m) => m.id !== id) ?? null)
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Invalidate failed')
    }
  }
  async function handleDelete(id: string) {
    try {
      await mut.delete(id)
      toast.success('Memory deleted')
      setRows((prev) => prev?.filter((m) => m.id !== id) ?? null)
    } catch (err) {
      toast.error(err instanceof Error ? err.message : 'Delete failed')
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
        <div className="flex items-center gap-2">
          <Brain className="h-5 w-5 text-primary" />
          <h1 className="text-2xl font-semibold tracking-tight">
            About this {entityKind || 'entity'}
          </h1>
          <Badge variant="outline" tone="muted" className="font-mono text-[10px]">
            {entityKind}:{entityId}
          </Badge>
        </div>
        <span className="font-mono text-[11px] tabular-nums text-muted-foreground">
          {source.length} memor{source.length === 1 ? 'y' : 'ies'}
        </span>
      </header>

      <MemorySearchBar
        value={searchInput}
        onChange={setSearchInput}
        onSubmit={() => setSearchQuery(searchInput.trim())}
        onClear={() => {
          setSearchInput('')
          setSearchQuery('')
        }}
        searching={loading}
        hitCount={searchQuery ? source.length : null}
      />

      {/* Associative-recall surfaces — these answer "what else?" without
          requiring the agent to know to ask. AR1 = structural co-link,
          AR2 = semantic neighbourhood. Two cards, side by side on wide
          screens. Hidden when both are empty so we don't show two empty
          panels for a brand-new entity. */}
      {(related && related.length > 0) || (adjacent && adjacent.length > 0) ? (
        <div className="grid grid-cols-1 gap-3 md:grid-cols-2">
          {related && related.length > 0 && (
            <Card>
              <CardContent className="space-y-2 pt-4">
                <div className="text-[11px] font-semibold uppercase tracking-[0.12em] text-muted-foreground">
                  Related entities
                </div>
                <p className="text-[11px] text-muted-foreground/70">
                  Co-link with this entity in shared memories.
                </p>
                <ul className="space-y-1">
                  {related.map((e) => (
                    <li
                      key={`${e.kind}:${e.id}`}
                      className="flex items-center justify-between text-[12px]"
                    >
                      <Link
                        to={`/memory/about/${encodeURIComponent(e.kind)}/${encodeURIComponent(e.id)}`}
                        className="inline-flex items-center gap-1.5 hover:text-primary"
                      >
                        <span className="font-mono text-[9px] uppercase tracking-wider text-muted-foreground">
                          {e.kind}
                        </span>
                        <span className="font-mono">{e.id}</span>
                      </Link>
                      <span className="font-mono text-[10px] tabular-nums text-muted-foreground">
                        {e.shared_count}×
                      </span>
                    </li>
                  ))}
                </ul>
              </CardContent>
            </Card>
          )}
          {adjacent && adjacent.length > 0 && (
            <Card>
              <CardContent className="space-y-2 pt-4">
                <div className="text-[11px] font-semibold uppercase tracking-[0.12em] text-muted-foreground">
                  Semantically adjacent
                </div>
                <p className="text-[11px] text-muted-foreground/70">
                  Entities surfaced via vec-neighbours of these memories.
                </p>
                <ul className="space-y-1">
                  {adjacent.map((e) => (
                    <li
                      key={`${e.kind}:${e.id}`}
                      className="flex items-center justify-between text-[12px]"
                    >
                      <Link
                        to={`/memory/about/${encodeURIComponent(e.kind)}/${encodeURIComponent(e.id)}`}
                        className="inline-flex items-center gap-1.5 hover:text-primary"
                      >
                        <span className="font-mono text-[9px] uppercase tracking-wider text-muted-foreground">
                          {e.kind}
                        </span>
                        <span className="font-mono">{e.id}</span>
                      </Link>
                      <span className="font-mono text-[10px] tabular-nums text-muted-foreground/70">
                        ~{e.shared_count}
                      </span>
                    </li>
                  ))}
                </ul>
              </CardContent>
            </Card>
          )}
        </div>
      ) : null}

      <Card>
        <CardContent className="pt-6">
          {loading && !rows && (
            <div className="flex items-center gap-2 text-muted-foreground">
              <div className="h-2 w-2 rounded-full bg-primary/60" />
              Loading…
            </div>
          )}
          {error && <p className="text-destructive">Error: {error}</p>}
          {!loading && source.length === 0 && (
            <p className="text-sm text-muted-foreground">
              No memories about {entityKind}:{entityId} yet. Link existing
              memories via the detail drawer, or save new ones with{' '}
              <code className="font-mono text-foreground/80">memory__save</code>{' '}
              passing{' '}
              <code className="font-mono text-foreground/80">
                entities=[{`{kind:"${entityKind}", id:"${entityId}"}`}]
              </code>
              .
            </p>
          )}
          {source.length > 0 && (
            <MemoryTable
              rows={source}
              loading={loading}
              selectedId={selected?.id}
              onSelect={setSelected}
              hasQuery={!!searchQuery}
            />
          )}
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
      />
    </div>
  )
}
