import { describe, expect, it } from 'vitest'
import {
  formatCountdown,
  isLiveDelegationWorker,
  liveDelegationCount,
  nextRunCountdown,
} from '@/pages/workers/worker-utils'
import type { WorkerSummary } from '@/api/workers'

const NOW = new Date('2024-01-01T12:00:00Z')

describe('nextRunCountdown — manual-trigger threshold', () => {
  it('returns "manual / mesh trigger" for 8760h (1 year)', () => {
    const result = nextRunCountdown('8760h', undefined, NOW)
    expect(result.humanCountdown).toBe('manual / mesh trigger')
    expect(result.nextRunDate).toBeNull()
  })

  it('returns "manual / mesh trigger" for exactly 30d (720h)', () => {
    const result = nextRunCountdown('720h', undefined, NOW)
    expect(result.humanCountdown).toBe('manual / mesh trigger')
    expect(result.nextRunDate).toBeNull()
  })

  it('returns "manual / mesh trigger" for 43200m (30 days in minutes)', () => {
    const result = nextRunCountdown('43200m', undefined, NOW)
    expect(result.humanCountdown).toBe('manual / mesh trigger')
    expect(result.nextRunDate).toBeNull()
  })

  it('does NOT return "manual / mesh trigger" for a normal 1h interval', () => {
    const result = nextRunCountdown('1h', undefined, NOW)
    expect(result.humanCountdown).not.toBe('manual / mesh trigger')
    expect(result.nextRunDate).not.toBeNull()
  })

  it('does NOT return "manual / mesh trigger" for 5m', () => {
    const result = nextRunCountdown('5m', undefined, NOW)
    expect(result.humanCountdown).not.toBe('manual / mesh trigger')
    expect(result.nextRunDate).not.toBeNull()
  })

  it('does NOT return "manual / mesh trigger" for 29d (696h)', () => {
    // 29 days = 696h < threshold
    const result = nextRunCountdown('696h', undefined, NOW)
    expect(result.humanCountdown).not.toBe('manual / mesh trigger')
    expect(result.nextRunDate).not.toBeNull()
  })
})

describe('nextRunCountdown — interval anchoring', () => {
  it('anchors off lastRunAt when provided', () => {
    const lastRunAt = new Date(NOW.getTime() - 30_000).toISOString() // 30s ago
    const result = nextRunCountdown('1m', lastRunAt, NOW)
    // next fire is 30s from now (1m after last run)
    expect(result.humanCountdown).toBe('30s')
    expect(result.nextRunDate).not.toBeNull()
  })

  it('fires at now+interval when no lastRunAt', () => {
    const result = nextRunCountdown('5m', undefined, NOW)
    expect(result.humanCountdown).toBe('5m')
    expect(result.nextRunDate?.getTime()).toBe(NOW.getTime() + 5 * 60_000)
  })

  it('returns — for empty scheduleSpec', () => {
    const result = nextRunCountdown('', undefined, NOW)
    expect(result.humanCountdown).toBe('—')
    expect(result.nextRunDate).toBeNull()
  })
})

describe('formatCountdown', () => {
  it('formats sub-minute as seconds', () => {
    expect(formatCountdown(45_000)).toBe('45s')
  })

  it('formats minutes', () => {
    expect(formatCountdown(5 * 60_000)).toBe('5m')
  })

  it('formats minutes with remaining seconds', () => {
    expect(formatCountdown(5 * 60_000 + 30_000)).toBe('5m 30s')
  })

  it('formats hours and minutes', () => {
    expect(formatCountdown(2 * 3_600_000 + 15 * 60_000)).toBe('2h 15m')
  })

  it('formats days', () => {
    expect(formatCountdown(3 * 86_400_000)).toBe('3d 0h')
  })

  it('returns "now" for zero or negative ms', () => {
    expect(formatCountdown(0)).toBe('now')
    expect(formatCountdown(-1000)).toBe('now')
  })
})

describe('live delegation helpers', () => {
  it('does not treat a delegation worker with no run status as live', () => {
    expect(isLiveDelegationWorker(worker({ id: 'w-queued', delegation_id: 'd-queued' }))).toBe(false)
  })

  it('counts distinct live delegations, not live worker rows', () => {
    const rows = [
      worker({ id: 'w-1', delegation_id: 'd-1', last_run_status: 'running' }),
      worker({ id: 'w-2', delegation_id: 'd-1', last_run_status: 'awaiting_approval' }),
      worker({ id: 'w-3', delegation_id: 'd-2', last_run_status: 'success' }),
      worker({ id: 'w-4', delegation_id: 'd-3' }),
      worker({ id: 'w-durable', ephemeral: false, last_run_status: 'running' }),
    ]

    expect(liveDelegationCount(rows)).toBe(1)
  })
})

function worker(overrides: Partial<WorkerSummary>): WorkerSummary {
  return {
    id: overrides.id ?? 'worker-1',
    name: overrides.name ?? 'worker',
    model_provider: overrides.model_provider ?? 'openai',
    model_id: overrides.model_id ?? 'model',
    schedule_spec: overrides.schedule_spec ?? '',
    enabled: overrides.enabled ?? true,
    created_at: overrides.created_at ?? NOW.toISOString(),
    workspace_id: overrides.workspace_id ?? 'ws-1',
    ...overrides,
  }
}
