// MemoryEntityGraphPage — the entity-to-entity graph view (AR3).
//
// Renders distinct entities (across the current memory scope) as nodes,
// co-link counts as weighted edges. Click a node to jump to /memory/about
// for that entity. Reuses MemoryGraphCanvas's force simulation by
// adapting the entity payload to the node/edge shape the canvas expects.

import { useCallback, useEffect, useMemo, useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { ArrowLeft, Network } from 'lucide-react'
import { Card, CardContent } from '@/components/ui/card'
import { entityGraph, type EntityGraph as EntityGraphData } from '@/api/memory'
import type {
  MemoryGraph,
  MemoryGraphNode,
  MemoryGraphEdge,
} from '@/api/memory'
import { MemoryGraphCanvas } from './MemoryGraphCanvas'

// adaptToMemoryGraph projects the entity-graph payload onto the shape
// the existing force-layout + canvas expects. We synthesise a
// "kind:id" string as the node id (matches what the backend already
// emits on edges) and map memory_count → size for the radius pass.
function adaptToMemoryGraph(g: EntityGraphData): {
  graph: MemoryGraph
  kindByNodeID: Map<string, string>
  idByNodeID: Map<string, string>
} {
  const kindByNodeID = new Map<string, string>()
  const idByNodeID = new Map<string, string>()
  const nodes: MemoryGraphNode[] = g.nodes.map((n) => {
    const nodeID = `${n.kind}:${n.id}`
    kindByNodeID.set(nodeID, n.kind)
    idByNodeID.set(nodeID, n.id)
    return {
      id: nodeID,
      title: `${n.kind}:${n.id}`,
      // canvas colours by kind — repurpose: we want a per-entity-kind
      // colour. Map kinds to existing palette buckets so the canvas's
      // KIND_COLOR fallback path lights up.
      kind: n.kind,
      tags: [],
      created_at: n.last_linked_at,
      size: n.memory_count,
      pinned: false,
    }
  })
  const edges: MemoryGraphEdge[] = g.edges.map((e) => ({
    source: e.source,
    target: e.target,
    weight: e.weight,
    reason: 'co_tag', // recycle the existing edge-type slot
  }))
  return {
    graph: { nodes, edges, truncated: g.truncated, node_cap: g.node_cap },
    kindByNodeID,
    idByNodeID,
  }
}

export function MemoryEntityGraphPage() {
  const navigate = useNavigate()
  const [data, setData] = useState<EntityGraphData | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)
  const [minWeight, setMinWeight] = useState(1)

  useEffect(() => {
    let cancelled = false
    setLoading(true)
    entityGraph({ node_cap: 200, min_weight: minWeight })
      .then((g) => {
        if (!cancelled) setData(g)
      })
      .catch((err: unknown) => {
        if (!cancelled) {
          setError(err instanceof Error ? err.message : 'Load failed')
          setData(null)
        }
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [minWeight])

  const adapted = useMemo(() => (data ? adaptToMemoryGraph(data) : null), [data])

  const handleSelect = useCallback(
    (nodeID: string) => {
      if (!adapted) return
      const kind = adapted.kindByNodeID.get(nodeID)
      const id = adapted.idByNodeID.get(nodeID)
      if (!kind || !id) return
      navigate(
        `/memory/about/${encodeURIComponent(kind)}/${encodeURIComponent(id)}`,
      )
    },
    [adapted, navigate],
  )

  return (
    <div className="flex h-full flex-col space-y-4">
      <Link
        to="/memory"
        className="inline-flex items-center gap-1 text-xs text-muted-foreground hover:text-foreground transition-colors"
      >
        <ArrowLeft className="h-3 w-3" />
        Memory
      </Link>
      <header className="flex items-center justify-between">
        <h1 className="flex items-center gap-2 text-2xl font-semibold tracking-tight">
          <Network className="h-5 w-5 text-primary" />
          Entity graph
        </h1>
        <div className="flex items-center gap-4 font-mono text-[11px] text-muted-foreground">
          <label className="inline-flex items-center gap-2">
            <span>min weight</span>
            <input
              type="number"
              min={0}
              max={20}
              value={minWeight}
              onChange={(e) => setMinWeight(Number(e.target.value) || 0)}
              className="w-14 border border-border bg-background/60 px-1.5 py-0.5 text-foreground"
            />
          </label>
          {data && (
            <span className="tabular-nums">
              {data.nodes.length} nodes · {data.edges.length} edges
              {data.truncated && ' · capped'}
            </span>
          )}
        </div>
      </header>

      <p className="max-w-2xl text-sm text-muted-foreground">
        Each node is an entity that at least one memory is about. Edges
        connect entities that co-link in the same memory; edge weight =
        number of shared memories. Click a node to pivot into "everything
        about ⟨that entity⟩".
      </p>

      <Card className="flex-1 overflow-hidden">
        <CardContent className="h-full p-0">
          {loading && !data && (
            <div className="flex h-full items-center justify-center gap-2 text-muted-foreground">
              <div className="h-2 w-2 rounded-full bg-primary/60" />
              Loading graph…
            </div>
          )}
          {error && (
            <div className="flex h-full items-center justify-center text-destructive">
              Error: {error}
            </div>
          )}
          {!loading && data && data.nodes.length === 0 && (
            <div className="flex h-full items-center justify-center text-sm text-muted-foreground">
              No entities linked to memories yet. Save memories with{' '}
              <code className="mx-1 font-mono text-foreground/80">
                entities=[
                {`{kind, id}`}]
              </code>{' '}
              to populate this view.
            </div>
          )}
          {adapted && adapted.graph.nodes.length > 0 && (
            <MemoryGraphCanvas
              graph={adapted.graph}
              onSelect={handleSelect}
            />
          )}
        </CardContent>
      </Card>
    </div>
  )
}
