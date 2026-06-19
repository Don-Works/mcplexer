import { describe, expect, it } from 'vitest'
import { nextRunCountdown, formatCountdown } from '@/pages/workers/worker-utils'

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
