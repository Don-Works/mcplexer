import { describe, expect, it } from 'vitest'
import { lineageStatusLabel, observedTokens } from '@/pages/usage/usageFormat'

describe('usage dashboard labels', () => {
  it('maps allowance lineage statuses without implying connection', () => {
    expect(lineageStatusLabel('ok')).toBe('Available')
    expect(lineageStatusLabel('partial')).toBe('Partial')
    expect(lineageStatusLabel('unconfigured')).toBe('Unconfigured')
    expect(lineageStatusLabel('unavailable')).toBe('Unavailable')
    expect(lineageStatusLabel('error')).toBe('Error')
  })

  it('prefers an authoritative total when token directions are unavailable', () => {
    expect(observedTokens({
      requests: 0,
      total_tokens: 1200,
      input_tokens: 0,
      output_tokens: 0,
      cache_read_tokens: 0,
      cache_write_tokens: 0,
      cost_usd: 0,
      accounting_missing_runs: 0,
    })).toBe(1200)
  })
})
