import type {
  DelegationContext,
  DelegationModelCapacity,
  DelegationModelStat,
  DelegationWorkerContext,
} from '@/api/workers'

export type AnalysisPeriod = '1h' | '12h' | '24h' | '7d' | 'all'

export const ANALYSIS_PERIODS: Array<{ key: AnalysisPeriod; label: string; ms?: number }> = [
  { key: '1h', label: '1h', ms: 60 * 60 * 1000 },
  { key: '12h', label: '12h', ms: 12 * 60 * 60 * 1000 },
  { key: '24h', label: '24h', ms: 24 * 60 * 60 * 1000 },
  { key: '7d', label: '7d', ms: 7 * 24 * 60 * 60 * 1000 },
  { key: 'all', label: 'All' },
]

export type CapacityRow = DelegationModelCapacity & { duplicate_count?: number }

export type ModelRankRow = {
  modelKey: string
  modelProvider: string
  modelID: string
  runs: number
  success: number
  failure: number
  running: number
  totalTokens: number
  costUSD: number
  unknownCostRuns: number
  durationMS: number
  unknownDurationMS: number
  avgDurationMS: number
  costKnown: boolean
  reviewCount: number
  avgScore: number
  successRate: number
  capabilityScores: Record<string, number>
}

export function periodLabel(period: AnalysisPeriod) {
  return ANALYSIS_PERIODS.find((row) => row.key === period)?.label ?? period
}

export function filterDelegationsByPeriod(rows: DelegationContext[], period: AnalysisPeriod) {
  const spec = ANALYSIS_PERIODS.find((row) => row.key === period)
  if (!spec?.ms) return rows
  const cutoff = Date.now() - spec.ms
  return rows.filter((row) => delegationActivityTime(row) >= cutoff)
}

export function dedupeCapacityRows(rows: DelegationModelCapacity[]): CapacityRow[] {
  const byKey = new Map<string, CapacityRow>()
  const counts = new Map<string, number>()
  for (const row of rows) {
    const key = capacityRowKey(row)
    counts.set(key, (counts.get(key) || 0) + 1)
    const current = byKey.get(key)
    if (
      !current ||
      (row.available && !current.available) ||
      row.capacity_score > current.capacity_score
    ) {
      byKey.set(key, { ...row })
    }
  }
  return Array.from(byKey.values())
    .map((row) => {
      const key = capacityRowKey(row)
      return {
        ...row,
        duplicate_count: counts.get(key) || 1,
      }
    })
    .sort((a, b) => {
      if (a.available !== b.available) return a.available ? -1 : 1
      if (a.capacity_score !== b.capacity_score) return b.capacity_score - a.capacity_score
      return capacityRowKey(a).localeCompare(capacityRowKey(b))
    })
    .map((row, index) => ({ ...row, rank: index + 1 }))
}

export function rankDelegationModels(rows: DelegationContext[]): ModelRankRow[] {
  const byKey = new Map<
    string,
    ModelRankRow & {
      scoreTotal: number
      capabilityTotals: Record<string, number>
      capabilityCounts: Record<string, number>
    }
  >()
  for (const d of rows) {
    const stats = d.model_stats?.length ? d.model_stats : fallbackModelStats(d)
    for (const stat of stats) {
      const key = stat.model_key || `${stat.model_provider}/${stat.model_id}`
      const row = byKey.get(key) ?? {
        modelKey: key,
        modelProvider: stat.model_provider,
        modelID: stat.model_id,
        runs: 0,
        success: 0,
        failure: 0,
        running: 0,
        totalTokens: 0,
        costUSD: 0,
        unknownCostRuns: 0,
        durationMS: 0,
        unknownDurationMS: 0,
        avgDurationMS: 0,
        costKnown: false,
        reviewCount: 0,
        avgScore: 0,
        successRate: 0,
        capabilityScores: {},
        scoreTotal: 0,
        capabilityTotals: {},
        capabilityCounts: {},
      }
      row.runs += stat.runs || 0
      row.success += stat.success || 0
      row.failure += stat.failure || 0
      row.running += stat.running || 0
      row.totalTokens += stat.total_tokens || 0
      row.costUSD += stat.cost_usd || 0
      row.unknownCostRuns += stat.unknown_cost_runs || 0
      row.durationMS += stat.duration_ms || 0
      row.unknownDurationMS += stat.unknown_duration_ms || 0
      if ((stat.review_count || 0) > 0) {
        row.reviewCount += stat.review_count || 0
        row.scoreTotal += (stat.review_score || 0) * (stat.review_count || 0)
        addCapabilityScores(row, stat.capability_scores)
      } else if (d.review?.reviewed && typeof d.review.score === 'number') {
        row.reviewCount += 1
        row.scoreTotal += d.review.score
        addCapabilityScores(row, d.review.scores)
      }
      byKey.set(key, row)
    }
  }
  return Array.from(byKey.values())
    .map((row) => {
      const knownRuns = Math.max(0, row.runs - row.unknownCostRuns)
      const knownSuccess = Math.max(0, row.success - row.unknownCostRuns)
      const knownDurationMS = Math.max(0, row.durationMS - row.unknownDurationMS)
      return {
        ...row,
        avgScore: row.reviewCount ? row.scoreTotal / row.reviewCount : 0,
        successRate: knownRuns ? knownSuccess / knownRuns : 0,
        avgDurationMS: knownRuns ? Math.round(knownDurationMS / knownRuns) : 0,
        costKnown: knownRuns > 0,
        capabilityScores: averageCapabilityScores(row.capabilityTotals, row.capabilityCounts),
      }
    })
    .filter((row) => row.reviewCount > 0 || row.runs > 0)
    .sort((a, b) => {
      if (b.avgScore !== a.avgScore) return b.avgScore - a.avgScore
      if (b.reviewCount !== a.reviewCount) return b.reviewCount - a.reviewCount
      if (b.successRate !== a.successRate) return b.successRate - a.successRate
      if (a.costKnown && b.costKnown && a.costUSD !== b.costUSD) return a.costUSD - b.costUSD
      return a.avgDurationMS - b.avgDurationMS
    })
}

export function formatTokens(n: number) {
  if (!n) return '0'
  if (Math.abs(n) >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}m`
  if (Math.abs(n) >= 1_000) return `${(n / 1_000).toFixed(1)}k`
  return String(n)
}

export function formatCost(n: number) {
  return `$${(n || 0).toFixed(4)}`
}

export function formatDuration(ms: number) {
  if (!ms) return '0s'
  if (ms < 1000) return `${ms}ms`
  if (ms < 60_000) return `${Math.round(ms / 1000)}s`
  return `${Math.round(ms / 60_000)}m`
}

function capacityRowKey(row: Pick<DelegationModelCapacity, 'model_key' | 'model_provider' | 'model_id'>) {
  return row.model_key || `${row.model_provider}/${row.model_id}`
}

function delegationActivityTime(row: DelegationContext) {
  const times = [
    row.created_at,
    row.updated_at,
    row.review?.reviewed_at,
    ...row.workers.flatMap((worker) => [
      worker.latest_run?.started_at,
      worker.latest_run?.finished_at,
      worker.worker.updated_at,
      worker.worker.created_at,
    ]),
  ]
    .map((value) => (value ? Date.parse(value) : NaN))
    .filter((value) => Number.isFinite(value))
  return times.length ? Math.max(...times) : 0
}

export function fallbackModelStats(d: DelegationContext): DelegationModelStat[] {
  const byKey = new Map<string, DelegationModelStat>()
  for (const row of d.workers) {
    const run = row.latest_run
    const provider = run?.model_provider || row.worker.model_provider
    const modelID = run?.model_id || row.worker.model_id
    const key = `${provider}/${modelID}`
    const stat = byKey.get(key) ?? {
      model_provider: provider,
      model_id: modelID,
      model_key: key,
      runs: 0,
      success: 0,
      failure: 0,
      running: 0,
      input_tokens: 0,
      output_tokens: 0,
      total_tokens: 0,
      cost_usd: 0,
      unknown_cost_runs: 0,
      duration_ms: 0,
      avg_duration_ms: 0,
      unknown_duration_ms: 0,
      review_count: 0,
      review_score: 0,
      worker_ids: [],
    }
    stat.worker_ids = [...(stat.worker_ids || []), row.worker.id]
    if (run) {
      stat.runs += 1
      if (run.status === 'success') stat.success += 1
      if (['failure', 'cap_exceeded', 'paused', 'rejected'].includes(run.status)) stat.failure += 1
      if (['running', 'awaiting_approval'].includes(run.status)) stat.running += 1
      stat.input_tokens += run.input_tokens || 0
      stat.output_tokens += run.output_tokens || 0
      stat.total_tokens += (run.input_tokens || 0) + (run.output_tokens || 0)
      stat.cost_usd += run.cost_usd || 0
      stat.duration_ms += run.duration_ms || 0
      if (run.accounting_missing || isRunAccountingMissing(run)) {
        stat.unknown_cost_runs = (stat.unknown_cost_runs || 0) + 1
        stat.unknown_duration_ms = (stat.unknown_duration_ms || 0) + (run.duration_ms || 0)
      }
    }
    if (d.review?.reviewed && typeof d.review.score === 'number') {
      stat.review_count = 1
      stat.review_score = d.review.score
      stat.task_kind = d.review.task_kind || d.task_kind
      stat.capability_scores = d.review.scores
    }
    byKey.set(key, stat)
  }
  return Array.from(byKey.values()).map((stat) => ({
    ...stat,
    avg_duration_ms:
      stat.runs && stat.runs > (stat.unknown_cost_runs || 0)
        ? Math.round(
            (stat.duration_ms - (stat.unknown_duration_ms || 0)) /
              (stat.runs - (stat.unknown_cost_runs || 0)),
          )
        : 0,
  }))
}

function isRunAccountingMissing(run: DelegationWorkerContext['latest_run']) {
  if (!run) return false
  return run.status === 'success' && !run.input_tokens && !run.output_tokens && !run.cost_usd
}

function addCapabilityScores(
  row: { capabilityTotals: Record<string, number>; capabilityCounts: Record<string, number> },
  scores?: Record<string, number>,
) {
  if (!scores) return
  for (const [key, value] of Object.entries(scores)) {
    if (!Number.isFinite(value)) continue
    row.capabilityTotals[key] = (row.capabilityTotals[key] || 0) + value
    row.capabilityCounts[key] = (row.capabilityCounts[key] || 0) + 1
  }
}

function averageCapabilityScores(
  totals: Record<string, number>,
  counts: Record<string, number>,
) {
  const out: Record<string, number> = {}
  for (const [key, value] of Object.entries(totals)) {
    const count = counts[key] || 0
    if (count > 0) out[key] = value / count
  }
  return out
}
