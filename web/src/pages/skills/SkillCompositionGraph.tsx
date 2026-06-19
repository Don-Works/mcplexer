// SkillCompositionGraph (W6) — force-directed SVG view of the skill
// registry's produces/consumes topology. Routed at /skills/graph.
//
// Edge colour-coding: each unique artifact_type gets a deterministic
// hue (string hash → 360° band) so the eye can follow "what flows
// where" without a legend. Hovering a node fades unrelated edges.
//
// Subcomponents (HoverCard, EdgeLine, GraphLegend) + utility fns live in
// skill-graph-parts.tsx so this file stays under the 300-line cap.

import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Link } from 'react-router-dom'
import { Loader2, Network, RefreshCcw, Sparkles } from 'lucide-react'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { EmptyState } from '@/components/ui/empty-state'
import { useApi } from '@/hooks/use-api'
import { getSkillGraph, type SkillGraph } from '@/api/skill-graph'
import { useSkillForceLayout } from './useSkillForceLayout'
import {
  EdgeLine,
  GraphLegend,
  HoverCard,
  nodeFill,
  nodeRadius,
} from './skill-graph-parts'

export function SkillCompositionGraph() {
  const fetcher = useCallback(() => getSkillGraph(), [])
  const { data, loading, error, refetch } = useApi(fetcher)

  return (
    <div className="space-y-6">
      <header className="flex flex-col gap-4 sm:flex-row sm:items-end sm:justify-between">
        <div>
          <h1 className="flex items-center gap-3 text-2xl font-bold tracking-tight">
            <Network className="h-5 w-5 text-primary/70" />
            Composition graph
          </h1>
          <p className="mt-1 max-w-2xl text-sm text-muted-foreground">
            Skills wired together via their{' '}
            <code className="text-[11px] text-foreground/80">produces:</code> +{' '}
            <code className="text-[11px] text-foreground/80">consumes:</code> manifest fields.
            Edges show "A's output type matches B's input type" — the runner can chain along
            them automatically. Cycles are allowed.
          </p>
        </div>
        <div className="flex shrink-0 gap-2">
          <Link to="/skills">
            <Button variant="ghost" size="sm">
              <Sparkles className="mr-1.5 h-3 w-3" />
              Library
            </Button>
          </Link>
          <Button variant="ghost" size="sm" onClick={refetch}>
            <RefreshCcw className="mr-1.5 h-3 w-3" />
            Refresh
          </Button>
        </div>
      </header>

      {error && (
        <p className="border border-destructive/40 bg-destructive/5 px-3 py-2 text-sm text-destructive">
          {error}
        </p>
      )}
      {loading && !data && (
        <div className="flex items-center gap-2 text-sm text-muted-foreground">
          <Loader2 className="h-3.5 w-3.5 animate-spin" />
          Reading the registry…
        </div>
      )}
      {data && (data.graph.nodes.length === 0 ? <GraphEmpty /> : <GraphCanvas graph={data.graph} />)}
      {data && data.graph.nodes.length > 0 && <GraphLegend graph={data.graph} />}
    </div>
  )
}

function GraphEmpty() {
  return (
    <EmptyState
      icon={<Network className="h-6 w-6" />}
      title="No skills to graph yet"
      description={
        <>
          Publish a skill with{' '}
          <code className="text-[11px] text-foreground/80">produces:</code> or{' '}
          <code className="text-[11px] text-foreground/80">consumes:</code> in its frontmatter
          and it'll appear here.
        </>
      }
    />
  )
}

function GraphCanvas({ graph }: { graph: SkillGraph }) {
  const containerRef = useRef<HTMLDivElement | null>(null)
  const [viewport, setViewport] = useState({ width: 900, height: 600 })

  useEffect(() => {
    if (!containerRef.current) return
    const ro = new ResizeObserver((entries) => {
      const e = entries[0]
      if (!e) return
      const { width, height } = e.contentRect
      setViewport({ width: Math.max(360, width), height: Math.max(360, height) })
    })
    ro.observe(containerRef.current)
    return () => ro.disconnect()
  }, [])

  const {
    positions,
    hoveredId,
    setHoveredId,
    transform,
    onPointerDown,
    onPointerMove,
    onPointerUp,
    onWheel,
    onCanvasDown,
  } = useSkillForceLayout({
    nodes: graph.nodes,
    edges: graph.edges,
    width: viewport.width,
    height: viewport.height,
  })

  const hovered = useMemo(
    () => graph.nodes.find((n) => n.name === hoveredId) ?? null,
    [hoveredId, graph.nodes],
  )

  return (
    <Card>
      <CardContent className="p-0">
        <div
          ref={containerRef}
          className="relative h-[600px] w-full select-none overflow-hidden bg-gradient-to-br from-background to-muted/30"
        >
          <svg
            width={viewport.width}
            height={viewport.height}
            onMouseDown={onCanvasDown}
            onWheel={onWheel}
            onPointerMove={onPointerMove}
            onPointerUp={onPointerUp}
            onPointerLeave={onPointerUp}
            style={{ cursor: 'grab' }}
          >
            <defs>
              <marker
                id="arrowhead"
                markerWidth="6"
                markerHeight="6"
                refX="9"
                refY="3"
                orient="auto"
                markerUnits="strokeWidth"
              >
                <path d="M0,0 L0,6 L6,3 z" fill="currentColor" />
              </marker>
            </defs>
            <g transform={`translate(${transform.x},${transform.y}) scale(${transform.k})`}>
              {graph.edges.map((e, i) => (
                <EdgeLine
                  key={`${e.from}->${e.to}@${e.artifact_type}-${i}`}
                  edge={e}
                  positions={positions}
                  hoveredId={hoveredId}
                />
              ))}
              {graph.nodes.map((n) => {
                const p = positions[n.name]
                if (!p) return null
                const r = nodeRadius(n)
                const isHover = hoveredId === n.name
                return (
                  <g
                    key={n.name}
                    transform={`translate(${p.x},${p.y})`}
                    onPointerDown={(ev) => onPointerDown(ev, n.name)}
                    onMouseEnter={() => setHoveredId(n.name)}
                    onMouseLeave={() => setHoveredId(null)}
                    style={{ cursor: 'pointer' }}
                  >
                    <circle
                      r={r}
                      fill={nodeFill(n)}
                      fillOpacity={isHover ? 1 : 0.85}
                      stroke={isHover ? '#fff' : 'rgba(255,255,255,0.15)'}
                      strokeWidth={isHover ? 1.5 : 0.5}
                    />
                    {(isHover || r >= 7) && (
                      <text
                        x={r + 4}
                        y={3}
                        fontSize={10}
                        fontFamily="ui-monospace,SFMono-Regular,monospace"
                        fill="currentColor"
                        opacity={isHover ? 0.95 : 0.65}
                        style={{ pointerEvents: 'none' }}
                      >
                        {n.name}
                      </text>
                    )}
                  </g>
                )
              })}
            </g>
          </svg>
          {hovered && <HoverCard node={hovered} />}
        </div>
      </CardContent>
    </Card>
  )
}
