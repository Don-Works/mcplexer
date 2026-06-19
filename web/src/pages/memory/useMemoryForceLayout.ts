// useMemoryForceLayout — a tiny in-house force simulation powering the
// memory graph view. No d3 / cyto — keeps the bundle slim.
//
// Forces (per tick):
//   - link spring  → toward edge.weight-modulated target length
//   - repulsion    → Coulomb-style 1/r² between all node pairs
//                    (skipped above 1500 nodes to bound O(n²) cost)
//   - centring     → pull toward viewport centre
//   - drag         → velocity damping
//
// Integration is velocity-Verlet-ish (vel += force; pos += vel; vel *= damp).
// We cool down (alpha→0) after ~300 ticks so the loop sleeps once the
// layout has settled — CPU goes idle until filter / data changes wake it.
//
// Pan/zoom + node drag are handled inline because they need to interact with
// the simulation's `positions` map directly — dragging pins a node by
// adding it to a Set the integrator skips.

import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import type { MemoryGraphEdge, MemoryGraphNode } from '@/api/memory'

interface Pt { x: number; y: number }
interface Vel { vx: number; vy: number }

interface UseMemoryForceLayoutArgs {
  nodes: MemoryGraphNode[]
  edges: MemoryGraphEdge[]
  width: number
  height: number
}

const TICKS_BEFORE_COOL = 300
const ALPHA_DECAY = 0.992
const LINK_DISTANCE = 60
const LINK_STRENGTH = 0.05
const CHARGE = -200
const CENTRE_STRENGTH = 0.02
const DAMPING = 0.85

export function useMemoryForceLayout(args: UseMemoryForceLayoutArgs) {
  const { nodes, edges, width, height } = args
  const [positions, setPositions] = useState<Record<string, Pt>>({})
  const [hoveredId, setHoveredId] = useState<string | null>(null)
  const [transform, setTransform] = useState({ x: 0, y: 0, k: 1 })

  // Mutable state lives in refs so the rAF loop doesn't re-create itself.
  const posRef = useRef<Record<string, Pt>>({})
  const velRef = useRef<Record<string, Vel>>({})
  const pinnedRef = useRef<Set<string>>(new Set())
  const dragRef = useRef<{ id: string; offsetX: number; offsetY: number } | null>(null)
  const panRef = useRef<{ x: number; y: number } | null>(null)
  const alphaRef = useRef(1)
  const tickRef = useRef(0)
  const rafRef = useRef<number | null>(null)

  // (Re)initialise positions when the node set changes. Place new nodes
  // in a spiral around the centre — deterministic + visually pleasant.
  useEffect(() => {
    const nextPos: Record<string, Pt> = {}
    const nextVel: Record<string, Vel> = {}
    nodes.forEach((n, i) => {
      const prior = posRef.current[n.id]
      if (prior) {
        nextPos[n.id] = prior
        nextVel[n.id] = velRef.current[n.id] ?? { vx: 0, vy: 0 }
        return
      }
      const angle = i * 0.5
      const radius = 20 + Math.sqrt(i) * 8
      nextPos[n.id] = {
        x: width / 2 + Math.cos(angle) * radius,
        y: height / 2 + Math.sin(angle) * radius,
      }
      nextVel[n.id] = { vx: 0, vy: 0 }
    })
    posRef.current = nextPos
    velRef.current = nextVel
    alphaRef.current = 1
    tickRef.current = 0
    setPositions(nextPos)
  }, [nodes, height, width])

  // Force simulation loop.
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
        const a = pos[e.source]
        const b = pos[e.target]
        if (!a || !b) continue
        const dx = b.x - a.x
        const dy = b.y - a.y
        const d = Math.sqrt(dx * dx + dy * dy) || 1
        const target = LINK_DISTANCE * (1 - Math.min(0.6, e.weight * 0.4))
        const diff = (d - target) * LINK_STRENGTH * alpha * (0.5 + e.weight * 0.5)
        const fx = (dx / d) * diff
        const fy = (dy / d) * diff
        const va = vel[e.source]
        const vb = vel[e.target]
        if (!pinned.has(e.source)) {
          va.vx += fx
          va.vy += fy
        }
        if (!pinned.has(e.target)) {
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

  // Coordinate conversion: screen → world (inverse of svg transform).
  const screenToWorld = useCallback(
    (sx: number, sy: number, container: DOMRect): Pt => {
      const x = (sx - container.left - transform.x) / transform.k
      const y = (sy - container.top - transform.y) / transform.k
      return { x, y }
    },
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
    if ((e.target as Element).tagName === 'svg' || (e.target as Element).tagName === 'g') {
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
