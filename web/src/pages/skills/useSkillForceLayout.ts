// useSkillForceLayout (W6) — minimal in-house force simulation for the
// skill composition graph. Self-contained: NO import from the memory
// graph code per W6 brief ("don't touch web/src/pages/memory/*"). When
// the memory machinery is extracted into a shared component later, this
// file is the inheritor candidate to delete.
//
// Forces per tick: link spring, Coulomb repulsion, centring, velocity
// damping. Same skeleton as the memory implementation but with weights
// dialled for composition graphs (lower fan-out → tighter clusters).

import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import type { SkillGraphEdge, SkillGraphNode } from '@/api/skill-graph'

interface Pt { x: number; y: number }
interface Vel { vx: number; vy: number }

interface UseSkillForceLayoutArgs {
  nodes: SkillGraphNode[]
  edges: SkillGraphEdge[]
  width: number
  height: number
}

const TICKS_BEFORE_COOL = 280
const ALPHA_DECAY = 0.992
const LINK_DISTANCE = 110
const LINK_STRENGTH = 0.08
const CHARGE = -260
const CENTRE_STRENGTH = 0.018
const DAMPING = 0.84

export interface SkillForceLayoutReturn {
  positions: Record<string, Pt>
  hoveredId: string | null
  setHoveredId: (id: string | null) => void
  transform: { x: number; y: number; k: number }
  onPointerDown: (e: React.PointerEvent, id: string) => void
  onPointerMove: (e: React.PointerEvent) => void
  onPointerUp: () => void
  onWheel: (e: React.WheelEvent) => void
  onCanvasDown: (e: React.MouseEvent) => void
}

export function useSkillForceLayout(args: UseSkillForceLayoutArgs): SkillForceLayoutReturn {
  const { nodes, edges, width, height } = args
  const [positions, setPositions] = useState<Record<string, Pt>>({})
  const [hoveredId, setHoveredId] = useState<string | null>(null)
  const [transform, setTransform] = useState({ x: 0, y: 0, k: 1 })

  const posRef = useRef<Record<string, Pt>>({})
  const velRef = useRef<Record<string, Vel>>({})
  const pinnedRef = useRef<Set<string>>(new Set())
  const dragRef = useRef<{ id: string; offsetX: number; offsetY: number } | null>(null)
  const panRef = useRef<{ x: number; y: number } | null>(null)
  const alphaRef = useRef(1)
  const tickRef = useRef(0)
  const rafRef = useRef<number | null>(null)

  // (Re)initialise on node-set change. Spiral-place new nodes around centre.
  useEffect(() => {
    const nextPos: Record<string, Pt> = {}
    const nextVel: Record<string, Vel> = {}
    nodes.forEach((n, i) => {
      const prior = posRef.current[n.name]
      if (prior) {
        nextPos[n.name] = prior
        nextVel[n.name] = velRef.current[n.name] ?? { vx: 0, vy: 0 }
        return
      }
      const angle = i * 0.55
      const radius = 30 + Math.sqrt(i) * 12
      nextPos[n.name] = {
        x: width / 2 + Math.cos(angle) * radius,
        y: height / 2 + Math.sin(angle) * radius,
      }
      nextVel[n.name] = { vx: 0, vy: 0 }
    })
    posRef.current = nextPos
    velRef.current = nextVel
    alphaRef.current = 1
    tickRef.current = 0
    setPositions(nextPos)
  }, [nodes, width, height])

  useEffect(() => {
    if (nodes.length === 0) return
    const skipRepulsion = nodes.length > 1500

    const tick = () => {
      const alpha = alphaRef.current
      const pos = posRef.current
      const vel = velRef.current
      const pinned = pinnedRef.current
      const cx = width / 2
      const cy = height / 2

      for (const id in pos) {
        if (pinned.has(id)) continue
        const p = pos[id]
        const v = vel[id]
        v.vx += (cx - p.x) * CENTRE_STRENGTH * alpha
        v.vy += (cy - p.y) * CENTRE_STRENGTH * alpha
      }

      if (!skipRepulsion) {
        const ids = Object.keys(pos)
        for (let i = 0; i < ids.length; i++) {
          const pi = pos[ids[i]]
          const vi = vel[ids[i]]
          for (let j = i + 1; j < ids.length; j++) {
            const pj = pos[ids[j]]
            const vj = vel[ids[j]]
            let dx = pi.x - pj.x
            let dy = pi.y - pj.y
            let d2 = dx * dx + dy * dy
            if (d2 < 1) {
              d2 = 1
              dx = (Math.random() - 0.5) * 2
              dy = (Math.random() - 0.5) * 2
            }
            const f = (CHARGE * alpha) / d2
            const d = Math.sqrt(d2)
            const fx = (dx / d) * f
            const fy = (dy / d) * f
            if (!pinned.has(ids[i])) {
              vi.vx -= fx
              vi.vy -= fy
            }
            if (!pinned.has(ids[j])) {
              vj.vx += fx
              vj.vy += fy
            }
          }
        }
      }

      for (const e of edges) {
        const a = pos[e.from]
        const b = pos[e.to]
        if (!a || !b) continue
        const dx = b.x - a.x
        const dy = b.y - a.y
        const d = Math.sqrt(dx * dx + dy * dy) || 1
        const diff = (d - LINK_DISTANCE) * LINK_STRENGTH * alpha
        const fx = (dx / d) * diff
        const fy = (dy / d) * diff
        const va = vel[e.from]
        const vb = vel[e.to]
        if (!pinned.has(e.from)) {
          va.vx += fx
          va.vy += fy
        }
        if (!pinned.has(e.to)) {
          vb.vx -= fx
          vb.vy -= fy
        }
      }

      for (const id in pos) {
        if (pinned.has(id)) continue
        const v = vel[id]
        v.vx *= DAMPING
        v.vy *= DAMPING
        pos[id].x += v.vx
        pos[id].y += v.vy
      }

      tickRef.current++
      if (tickRef.current > TICKS_BEFORE_COOL) {
        alphaRef.current *= ALPHA_DECAY
      }

      setPositions({ ...pos })

      if (alphaRef.current > 0.01 || dragRef.current) {
        rafRef.current = requestAnimationFrame(tick)
      } else {
        rafRef.current = null
      }
    }
    rafRef.current = requestAnimationFrame(tick)
    return () => {
      if (rafRef.current) cancelAnimationFrame(rafRef.current)
      rafRef.current = null
    }
  }, [nodes, edges, width, height])

  const screenToWorld = useCallback(
    (sx: number, sy: number, container: DOMRect): Pt => ({
      x: (sx - container.left - transform.x) / transform.k,
      y: (sy - container.top - transform.y) / transform.k,
    }),
    [transform],
  )

  const onPointerDown = useCallback(
    (e: React.PointerEvent, id: string) => {
      e.stopPropagation()
      const target = e.currentTarget as SVGGElement
      target.setPointerCapture?.(e.pointerId)
      const svg = target.ownerSVGElement
      if (!svg) return
      const rect = svg.getBoundingClientRect()
      const world = screenToWorld(e.clientX, e.clientY, rect)
      const p = posRef.current[id]
      if (!p) return
      pinnedRef.current.add(id)
      dragRef.current = {
        id,
        offsetX: p.x - world.x,
        offsetY: p.y - world.y,
      }
      alphaRef.current = Math.max(alphaRef.current, 0.6)
    },
    [screenToWorld],
  )

  const onPointerMove = useCallback(
    (e: React.PointerEvent) => {
      if (dragRef.current) {
        const svg = e.currentTarget as SVGSVGElement
        const rect = svg.getBoundingClientRect()
        const world = screenToWorld(e.clientX, e.clientY, rect)
        const { id, offsetX, offsetY } = dragRef.current
        const p = posRef.current[id]
        if (p) {
          p.x = world.x + offsetX
          p.y = world.y + offsetY
        }
        return
      }
      if (panRef.current) {
        const dx = e.clientX - panRef.current.x
        const dy = e.clientY - panRef.current.y
        panRef.current = { x: e.clientX, y: e.clientY }
        setTransform((t) => ({ ...t, x: t.x + dx, y: t.y + dy }))
      }
    },
    [screenToWorld],
  )

  const onPointerUp = useCallback(() => {
    if (dragRef.current) {
      pinnedRef.current.delete(dragRef.current.id)
      dragRef.current = null
    }
    panRef.current = null
  }, [])

  const onCanvasDown = useCallback((e: React.MouseEvent) => {
    const tag = (e.target as Element).tagName
    if (tag === 'svg' || tag === 'g') {
      panRef.current = { x: e.clientX, y: e.clientY }
    }
  }, [])

  const onWheel = useCallback((e: React.WheelEvent) => {
    const svg = e.currentTarget as SVGSVGElement
    const rect = svg.getBoundingClientRect()
    const mx = e.clientX - rect.left
    const my = e.clientY - rect.top
    setTransform((t) => {
      const factor = e.deltaY < 0 ? 1.1 : 1 / 1.1
      const k = Math.max(0.2, Math.min(4, t.k * factor))
      const x = mx - (mx - t.x) * (k / t.k)
      const y = my - (my - t.y) * (k / t.k)
      return { x, y, k }
    })
  }, [])

  return useMemo(
    () => ({
      positions,
      hoveredId,
      setHoveredId,
      transform,
      onPointerDown,
      onPointerMove,
      onPointerUp,
      onWheel,
      onCanvasDown,
    }),
    [
      positions,
      hoveredId,
      transform,
      onPointerDown,
      onPointerMove,
      onPointerUp,
      onWheel,
      onCanvasDown,
    ],
  )
}
