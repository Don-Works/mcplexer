// Skill stats API client (W6) — thin typed wrappers over
// /api/v1/skills/{name}/stats and /api/v1/skills/stats batch.
//
// Backend source of truth: internal/api/skill_stats_handler.go +
// internal/skillregistry/stats.go (SkillStats struct). Keep these
// shapes in lockstep when either side changes.

import { request } from './client'

export interface SkillStatsToolUse {
  name: string
  count: number
}

export interface SkillStats {
  invocations: number
  success_rate: number
  failure_rate: number
  cancelled_rate: number
  p50_duration_ms: number
  p95_duration_ms: number
  last_run_at?: string | null
  top_tools_used: SkillStatsToolUse[]
  window_days: number
}

export interface SkillStatsResponse {
  skill: string
  stats: SkillStats
  generated: string
}

export interface SkillStatsBatchResponse {
  stats: Record<string, SkillStats>
  window_days: number
  generated: string
}

export function getSkillStats(
  name: string,
  windowDays?: number,
): Promise<SkillStatsResponse> {
  const qs = windowDays ? `?window_days=${windowDays}` : ''
  return request(`/skills/${encodeURIComponent(name)}/stats${qs}`)
}

export function getSkillStatsBatch(
  names: string[],
  windowDays?: number,
): Promise<SkillStatsBatchResponse> {
  const params = new URLSearchParams({ names: names.join(',') })
  if (windowDays) params.set('window_days', String(windowDays))
  return request(`/skills/stats?${params.toString()}`)
}
