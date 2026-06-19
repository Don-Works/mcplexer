// skill-graph-parts.tsx (W6) — small subcomponents + utility functions
// for SkillCompositionGraph. Split out to keep the parent file under
// the 300-line cap.

import { useMemo } from 'react'
import { Card, CardContent } from '@/components/ui/card'
import type {
  SkillGraph,
  SkillGraphEdge,
  SkillGraphNode,
} from '@/api/skill-graph'

export interface EdgeLineProps {
  edge: SkillGraphEdge
  positions: Record<string, { x: number; y: number }>
  hoveredId: string | null
}

export function EdgeLine({ edge, positions, hoveredId }: EdgeLineProps) {
  const a = positions[edge.from]
  const b = positions[edge.to]
  if (!a || !b) return null
  const dim = hoveredId && hoveredId !== edge.from && hoveredId !== edge.to
  return (
    <line
      x1={a.x}
      y1={a.y}
      x2={b.x}
      y2={b.y}
      stroke={artifactColor(edge.artifact_type)}
      strokeOpacity={dim ? 0.08 : 0.55}
      strokeWidth={1.2}
      markerEnd="url(#arrowhead)"
    >
      <title>{`${edge.from} → ${edge.to} (${edge.artifact_type})`}</title>
    </line>
  )
}

export function HoverCard({ node }: { node: SkillGraphNode }) {
  return (
    <div className="pointer-events-none absolute right-3 top-3 max-w-[300px] border border-border bg-background/95 p-3 text-xs shadow-md backdrop-blur">
      <div className="font-semibold leading-tight">{node.name}</div>
      <div className="mt-0.5 font-mono text-[10px] uppercase tracking-wider text-muted-foreground">
        v{node.version}
      </div>
      {node.description && (
        <p className="mt-2 leading-snug text-muted-foreground">{node.description}</p>
      )}
      {node.produces && node.produces.length > 0 && (
        <div className="mt-2">
          <span className="font-mono text-[10px] uppercase tracking-wider text-emerald-500/80">
            produces
          </span>
          <div className="mt-0.5 font-mono text-[10px] text-foreground/80">
            {node.produces.join(', ')}
          </div>
        </div>
      )}
      {node.consumes && node.consumes.length > 0 && (
        <div className="mt-1.5">
          <span className="font-mono text-[10px] uppercase tracking-wider text-sky-500/80">
            consumes
          </span>
          <div className="mt-0.5 font-mono text-[10px] text-foreground/80">
            {node.consumes.join(', ')}
          </div>
        </div>
      )}
      {node.stats_summary && node.stats_summary.invocations > 0 && (
        <div className="mt-2 border-t border-border/40 pt-2 font-mono text-[10px] text-muted-foreground">
          {node.stats_summary.invocations} runs ·{' '}
          {Math.round(node.stats_summary.success_rate * 100)}% success
        </div>
      )}
    </div>
  )
}

export function GraphLegend({ graph }: { graph: SkillGraph }) {
  const artifactTypes = useMemo(() => {
    const set = new Set<string>()
    for (const e of graph.edges) set.add(e.artifact_type)
    return Array.from(set).sort()
  }, [graph.edges])
  if (artifactTypes.length === 0) return null
  return (
    <Card>
      <CardContent className="flex flex-wrap items-center gap-x-4 gap-y-2 px-5 py-3">
        <span className="font-mono text-[10px] uppercase tracking-wider text-muted-foreground">
          artifact types
        </span>
        {artifactTypes.map((t) => (
          <span key={t} className="inline-flex items-center gap-1.5 text-xs">
            <span
              className="inline-block h-0.5 w-6"
              style={{ background: artifactColor(t) }}
            />
            <code className="font-mono text-[11px] text-foreground/80">{t}</code>
          </span>
        ))}
      </CardContent>
    </Card>
  )
}

export function nodeRadius(n: SkillGraphNode): number {
  const degree = (n.produces?.length ?? 0) + (n.consumes?.length ?? 0)
  return Math.min(11, 4 + Math.log2(1 + degree) * 1.8)
}

export function nodeFill(n: SkillGraphNode): string {
  // Green-tinted for high-rep, slate fallback otherwise.
  const s = n.stats_summary
  if (!s || s.invocations === 0) return '#94a3b8'
  if (s.success_rate >= 0.9) return '#10b981'
  if (s.success_rate >= 0.6) return '#f59e0b'
  return '#ef4444'
}

// artifactColor — deterministic hue per artifact type. djb2 hash → 360°
// band, fixed S/L for legible contrast on both light + dark themes.
export function artifactColor(type: string): string {
  let h = 5381
  for (let i = 0; i < type.length; i++) {
    h = ((h << 5) + h + type.charCodeAt(i)) & 0xffffffff
  }
  const hue = Math.abs(h) % 360
  return `hsl(${hue}, 65%, 55%)`
}
