import { render, screen } from '@testing-library/react'
import { describe, expect, it } from 'vitest'
import { ProviderTable } from '@/pages/usage/ProviderTable'
import type { ProviderUsage } from '@/api/usage'

function provider(id: string, label: string): ProviderUsage {
  return {
    provider: id,
    label,
    status: 'ok',
    source: 'cli',
    source_label: 'CLI',
    observed: {
      requests: 1,
      input_tokens: 10,
      output_tokens: 2,
      cache_read_tokens: 0,
      cache_write_tokens: 0,
      cost_usd: 0,
      accounting_missing_runs: 0,
    },
    windows: [],
    stale: false,
  }
}

describe('ProviderTable ordering', () => {
  it('exposes named keyboard-enabled reorder handles', () => {
    render(
      <ProviderTable
        providers={[provider('claude', 'Claude'), provider('codex', 'Codex')]}
      />,
    )

    const handles = screen.getAllByRole('button', { name: 'Reorder Claude' })
    expect(handles).toHaveLength(2) // mobile and desktop responsive renderings
    for (const handle of handles) {
      expect(handle).toHaveAttribute('tabindex', '0')
      expect(handle).toHaveAttribute('aria-describedby')
      expect(handle).toHaveAttribute('title', 'Drag to reorder. Space and arrow keys also work.')
    }
  })
})
