import { beforeEach, describe, expect, it } from 'vitest'
import {
  moveProvider,
  PROVIDER_ORDER_STORAGE_KEY,
  readProviderOrder,
  sortProviders,
  writeProviderOrder,
} from '@/pages/usage/providerOrder'
import type { ProviderUsage } from '@/api/usage'

function provider(provider: string): ProviderUsage {
  return { provider, label: provider, status: 'ok', source: 'cli', source_label: 'CLI', observed: {
    requests: 0, input_tokens: 0, output_tokens: 0, cache_read_tokens: 0,
    cache_write_tokens: 0, cost_usd: 0, accounting_missing_runs: 0,
  }, windows: [], stale: false }
}

describe('provider ordering', () => {
  beforeEach(() => localStorage.clear())

  it('moves and persists provider IDs', () => {
    const next = moveProvider(['claude', 'codex', 'grok'], 'grok', 'claude')
    expect(next).toEqual(['grok', 'claude', 'codex'])
    writeProviderOrder(next)
    expect(readProviderOrder()).toEqual(next)
    expect(localStorage.getItem(PROVIDER_ORDER_STORAGE_KEY)).toBe(JSON.stringify(next))
  })

  it('moves a provider downward to the sortable target position', () => {
    expect(moveProvider(['claude', 'codex', 'grok'], 'claude', 'codex'))
      .toEqual(['codex', 'claude', 'grok'])
    expect(moveProvider(['claude', 'codex', 'grok'], 'claude', 'grok'))
      .toEqual(['codex', 'grok', 'claude'])
  })

  it('keeps new providers after the persisted order', () => {
    const sorted = sortProviders(
      [provider('claude'), provider('codex'), provider('mimo')],
      ['mimo', 'claude'],
    )
    expect(sorted.map((item) => item.provider)).toEqual(['mimo', 'claude', 'codex'])
  })

  it('ignores malformed persisted state', () => {
    localStorage.setItem(PROVIDER_ORDER_STORAGE_KEY, '{bad json')
    expect(readProviderOrder()).toEqual([])
  })
})
