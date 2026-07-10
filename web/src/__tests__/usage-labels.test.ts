import { describe, expect, it } from 'vitest'
import { lineageStatusLabel } from '@/pages/usage/usageFormat'

describe('usage dashboard labels', () => {
  it('maps allowance lineage statuses without implying connection', () => {
    expect(lineageStatusLabel('ok')).toBe('Available')
    expect(lineageStatusLabel('partial')).toBe('Partial')
    expect(lineageStatusLabel('unconfigured')).toBe('Unconfigured')
    expect(lineageStatusLabel('unavailable')).toBe('Unavailable')
    expect(lineageStatusLabel('error')).toBe('Error')
  })
})