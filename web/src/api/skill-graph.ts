// Skill composition graph API client (W6) — thin typed wrapper over
// GET /api/v1/skills/graph. Backend source of truth:
// internal/api/skill_graph_handler.go + internal/skillregistry/graph.go.

import { request } from './client'

export interface SkillStatsSummary {
  invocations: number
  success_rate: number
  p95_duration_ms?: number
  last_run_at?: string | null
}

export interface SkillGraphNode {
  name: string
  version: number
  description?: string
  produces?: string[]
  consumes?: string[]
  workspace_id?: string | null
  stats_summary?: SkillStatsSummary | null
}

export interface SkillGraphEdge {
  from: string
  to: string
  artifact_type: string
}

export interface SkillGraph {
  nodes: SkillGraphNode[]
  edges: SkillGraphEdge[]
}

export interface SkillGraphResponse {
  graph: SkillGraph
  window_days: number
  generated: string
}

export function getSkillGraph(windowDays?: number): Promise<SkillGraphResponse> {
  const qs = windowDays ? `?window_days=${windowDays}` : ''
  return request(`/skills/graph${qs}`)
}
