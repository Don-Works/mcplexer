// MemoryVisualisationPage — see your brain. A force-directed graph of every
// memory at /memory/graph.
//
// Why force-directed (option #1 from the design brief):
//   - Works on every install without an embedding model — co-tag + [[wikilink]]
//     edges are computed server-side from data we always have.
//   - Pairs cleanly with the future A-MEM auto-linking work — once that lands
//     we just add a "reason: a_mem" edge type and recolour.
//   - Constellation (option #2) needs UMAP/PCA over the embeddings store;
//     would have meant pulling in gonum or pure-Go UMAP, plus a precompute
//     step. Higher cost, slower ship — punted until embeddings are
//     universally indexed.
//
// This file owns data fetching, filtering, the drawer wiring, and the
// page chrome (header, chips, refresh). The actual SVG renderer +
// simulation lives in MemoryGraphCanvas so neither file exceeds the
// 300-line cap.

import { useCallback, useEffect, useMemo, useState } from 'react'
import { Link, useSearchParams } from 'react-router-dom'
import { ArrowLeft, Brain, RefreshCw } from 'lucide-react'
import { toast } from 'sonner'
import { Card, CardContent } from '@/components/ui/card'
import {
  getMemory,
  getMemoryGraph,
  type MemoryEntry,
  type MemoryGraph,
} from '@/api/memory'
import { useMemoryMutations } from '@/hooks/use-memory'
import { MemoryDetailDrawer } from './MemoryDetailDrawer'
import { MemoryGraphCanvas } from './MemoryGraphCanvas'

const KIND_COLOR_FACT = '#38bdf8'
const KIND_COLOR_NOTE = '#a78bfa'

export function MemoryVisualisationPage() {
  const [searchParams, setSearchParams] = useSearchParams()
  const [graph, setGraph] = useState<MemoryGraph | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [kindFilter, setKindFilter] = useState<'all' | 'fact' | 'note'>('all')
  const [includeInvalid, setIncludeInvalid] = useState(false)

  const refetch = useCallback(() => {
    setLoading(true)
    setError(null)
    getMemoryGraph({ include_invalid: includeInvalid })
      .then((g) => setGraph(g))
      .catch((e: unknown) => setError(e instanceof Error ? e.message : 'Failed'))
      .finally(() => setLoading(false))
  }, [includeInvalid])
  useEffect(() => {
    refetch()
  }, [refetch])

  const filtered = useMemo<MemoryGraph | null>(() => {
    if (!graph) return null
    if (kindFilter === 'all') return graph
    const keep = new Set(
      graph.nodes.filter((n) => n.kind === kindFilter).map((n) => n.id),
    )
    return {
      ...graph,
      nodes: graph.nodes.filter((n) => keep.has(n.id)),
      edges: graph.edges.filter((e) => keep.has(e.source) && keep.has(e.target)),
    }
  }, [graph, kindFilter])

  // URL-backed selection. Always fetch the full entry so we work for
  // deep-links from other pages too (the graph might not include the
  // selected id if filters have changed).
  const selectedId = searchParams.get('selected')
  const [drawerEntry, setDrawerEntry] = useState<MemoryEntry | null>(null)
  useEffect(() => {
    if (!selectedId) {
      setDrawerEntry(null)
      return
    }
    let cancelled = false
    getMemory(selectedId)
      .then((m) => {
        if (!cancelled) setDrawerEntry(m)
      })
      .catch(() => {
        if (!cancelled) setDrawerEntry(null)
      })
    return () => {
      cancelled = true
    }
  }, [selectedId])
  const setSelected = useCallback(
    (id: string | null) => {
      setSearchParams(
        (prev) => {
          const next = new URLSearchParams(prev)
          if (id) next.set('selected', id)
          else next.delete('selected')
          return next
        },
        { replace: true },
      )
    },
    [setSearchParams],
  )

  const mut = useMemoryMutations()
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

  return (
    <div className="space-y-4">
      <Link
        to="/memory"
        className="inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground transition-colors"
      >
        <ArrowLeft className="h-3 w-3" />
        Memory
      </Link>

      <header className="flex flex-wrap items-end justify-between gap-3">
        <div>
          <h1 className="flex items-center gap-2 text-2xl font-semibold tracking-tight">
            <Brain className="h-5 w-5 text-primary" />
            Memory graph
          </h1>
          <p className="mt-1 max-w-2xl text-sm text-muted-foreground">
            Force-directed view of every memory your gateway has learned.
            Edges link memories sharing tags (dim) or with explicit{' '}
            <code className="font-mono text-foreground/80">[[wikilinks]]</code>{' '}
            (bright). Hover for details, click to open.
          </p>
        </div>
        <button
          type="button"
          onClick={refetch}
          className="inline-flex items-center gap-1.5 border border-border px-2.5 py-1.5 font-mono text-[11px] text-muted-foreground hover:border-primary/40 hover:text-foreground"
        >
          <RefreshCw className={loading ? 'h-3 w-3 animate-spin' : 'h-3 w-3'} />
          Refresh
        </button>
      </header>

      <div className="flex flex-wrap items-center gap-1.5 text-[11px]">
        <FilterChip
          active={kindFilter === 'all'}
          onClick={() => setKindFilter('all')}
          label="All kinds"
        />
        <FilterChip
          active={kindFilter === 'fact'}
          onClick={() => setKindFilter('fact')}
          label="Facts"
          color={KIND_COLOR_FACT}
        />
        <FilterChip
          active={kindFilter === 'note'}
          onClick={() => setKindFilter('note')}
          label="Notes"
          color={KIND_COLOR_NOTE}
        />
        <FilterChip
          active={includeInvalid}
          onClick={() => setIncludeInvalid((v) => !v)}
          label={includeInvalid ? '+ invalidated' : 'Hide invalidated'}
        />
        {filtered && (
          <span className="ml-auto font-mono text-[11px] tabular-nums text-muted-foreground">
            {filtered.nodes.length} nodes · {filtered.edges.length} edges
            {filtered.truncated && (
              <span className="ml-2 text-amber-500">
                (truncated to {filtered.node_cap})
              </span>
            )}
          </span>
        )}
      </div>

      <Card>
        <CardContent className="p-0">
          {loading && !graph && (
            <div className="flex h-[560px] items-center justify-center text-sm text-muted-foreground">
              <span className="mr-2 h-2 w-2 animate-pulse rounded-full bg-primary/60" />
              Building graph…
            </div>
          )}
          {error && (
            <div className="flex h-[560px] items-center justify-center text-sm text-destructive">
              Error: {error}
            </div>
          )}
          {filtered && filtered.nodes.length === 0 && !loading && !error && (
            <div className="flex h-[560px] flex-col items-center justify-center gap-2 text-center text-sm text-muted-foreground">
              <Brain className="h-8 w-8 opacity-30" />
              <p>No memories yet — your brain is empty.</p>
            </div>
          )}
          {filtered && filtered.nodes.length > 0 && (
            <MemoryGraphCanvas graph={filtered} onSelect={setSelected} />
          )}
        </CardContent>
      </Card>

      <MemoryDetailDrawer
        entry={drawerEntry}
        onClose={() => setSelected(null)}
        onInvalidate={handleInvalidate}
        onDelete={handleDelete}
      />
    </div>
  )
}

function FilterChip({
  active,
  onClick,
  label,
  color,
}: {
  active: boolean
  onClick: () => void
  label: string
  color?: string
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={
        'inline-flex items-center gap-1 border px-2 py-1 font-mono text-[11px] transition-colors ' +
        (active
          ? 'border-primary/40 bg-primary/5 text-foreground'
          : 'border-dashed border-border text-muted-foreground hover:border-border/80 hover:text-foreground')
      }
    >
      {color && (
        <span
          className="inline-block h-1.5 w-1.5 rounded-full"
          style={{ background: color }}
        />
      )}
      {label}
    </button>
  )
}
