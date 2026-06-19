// MemoryGraphCanvas — the SVG rendering surface for the memory graph,
// split out of MemoryVisualisationPage.tsx so neither file exceeds the
// 300-line cap. Owns the simulation hook + DOM interactions; the parent
// owns data fetching + the drawer.

import { useEffect, useMemo, useRef, useState } from 'react'
import type {
  MemoryGraph,
  MemoryGraphEdge,
  MemoryGraphNode,
} from '@/api/memory'
import { useMemoryForceLayout } from './useMemoryForceLayout'

const KIND_COLOR: Record<string, string> = {
  fact: '#38bdf8', // sky-400
  note: '#a78bfa', // violet-400
}
const KIND_FALLBACK = '#94a3b8' // slate-400

function nodeColor(n: MemoryGraphNode): string {
  return KIND_COLOR[n.kind] ?? KIND_FALLBACK
}

interface Props {
  graph: MemoryGraph
  onSelect: (id: string) => void
}

export function MemoryGraphCanvas({ graph, onSelect }: Props) {
  const containerRef = useRef<HTMLDivElement | null>(null)
  const [viewport, setViewport] = useState({ width: 800, height: 560 })

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
    onPointerDown,
    onPointerMove,
    onPointerUp,
    hoveredId,
    setHoveredId,
    transform,
    onWheel,
    onCanvasDown,
  } = useMemoryForceLayout({
    nodes: graph.nodes,
    edges: graph.edges,
    width: viewport.width,
    height: viewport.height,
  })

  const hovered = useMemo(
    () => graph.nodes.find((n) => n.id === hoveredId) ?? null,
    [hoveredId, graph.nodes],
  )

  return (
    <div
      ref={containerRef}
      className="relative h-[560px] w-full select-none overflow-hidden bg-gradient-to-br from-background to-muted/30"
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
        <g transform={`translate(${transform.x},${transform.y}) scale(${transform.k})`}>
          {graph.edges.map((e, i) => (
            <GraphEdgeLine
              key={i}
              edge={e}
              positions={positions}
              hoveredId={hoveredId}
            />
          ))}
          {graph.nodes.map((n) => {
            const p = positions[n.id]
            if (!p) return null
            const r = nodeRadius(n)
            const isHover = hoveredId === n.id
            return (
              <g
                key={n.id}
                transform={`translate(${p.x},${p.y})`}
                onPointerDown={(ev) => onPointerDown(ev, n.id)}
                onMouseEnter={() => setHoveredId(n.id)}
                onMouseLeave={() => setHoveredId(null)}
                onClick={(ev) => {
                  ev.stopPropagation()
                  onSelect(n.id)
                }}
                style={{ cursor: 'pointer' }}
              >
                {n.pinned && (
                  <circle
                    r={r + 3}
                    fill="none"
                    stroke={nodeColor(n)}
                    strokeOpacity={0.35}
                    strokeWidth={1}
                  />
                )}
                <circle
                  r={r}
                  fill={nodeColor(n)}
                  fillOpacity={isHover ? 1 : 0.85}
                  stroke={isHover ? '#fff' : 'rgba(255,255,255,0.15)'}
                  strokeWidth={isHover ? 1.5 : 0.5}
                />
                {(isHover || r >= 6) && (
                  <text
                    x={r + 4}
                    y={3}
                    fontSize={10}
                    fontFamily="ui-monospace,SFMono-Regular,monospace"
                    fill="currentColor"
                    opacity={isHover ? 0.95 : 0.55}
                    style={{ pointerEvents: 'none' }}
                  >
                    {truncate(n.title, 40)}
                  </text>
                )}
              </g>
            )
          })}
        </g>
      </svg>

      {hovered && <HoverCard node={hovered} />}
    </div>
  )
}

function nodeRadius(n: MemoryGraphNode): number {
  // Degree-based — minimum 3px, log scaling above to keep hubs readable
  // without dominating.
  const base = 3 + Math.log2(1 + n.size) * 1.6
  return Math.min(11, base)
}

function truncate(s: string, n: number): string {
  if (s.length <= n) return s
  return s.slice(0, n - 1) + '…'
}

function GraphEdgeLine({
  edge,
  positions,
  hoveredId,
}: {
  edge: MemoryGraphEdge
  positions: Record<string, { x: number; y: number }>
  hoveredId: string | null
}) {
  const a = positions[edge.source]
  const b = positions[edge.target]
  if (!a || !b) return null
  const wikilink = edge.reason === 'wikilink'
  const dim = hoveredId && hoveredId !== edge.source && hoveredId !== edge.target
  return (
    <line
      x1={a.x}
      y1={a.y}
      x2={b.x}
      y2={b.y}
      stroke={wikilink ? '#38bdf8' : 'currentColor'}
      strokeOpacity={dim ? 0.05 : wikilink ? 0.55 : 0.15 + edge.weight * 0.2}
      strokeWidth={wikilink ? 1.2 : 0.6 + edge.weight * 0.8}
    />
  )
}

function HoverCard({ node }: { node: MemoryGraphNode }) {
  return (
    <div className="pointer-events-none absolute right-3 top-3 max-w-[260px] border border-border bg-background/95 p-2 text-xs shadow-md backdrop-blur">
      <div className="font-semibold leading-tight">{node.title}</div>
      <div className="mt-0.5 font-mono text-[10px] uppercase tracking-wider text-muted-foreground">
        {node.kind} · {node.size} link{node.size === 1 ? '' : 's'}
      </div>
      {node.tags && node.tags.length > 0 && (
        <div className="mt-1 flex flex-wrap gap-0.5">
          {node.tags.slice(0, 6).map((t) => (
            <span
              key={t}
              className="border border-border/60 px-1 py-0.5 font-mono text-[9px] text-muted-foreground"
            >
              {t}
            </span>
          ))}
        </div>
      )}
    </div>
  )
}
