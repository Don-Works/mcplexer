import { afterEach, describe, expect, it, vi } from 'vitest'

const request = vi.fn()
vi.mock('@/api/client', () => ({
  request: (...args: unknown[]) => request(...args),
}))

import { getUsage, refreshUsage } from '@/api/usage'

describe('usage api client', () => {
  afterEach(() => request.mockReset())

  it('loads the selected observed window', async () => {
    request.mockResolvedValueOnce({ providers: [] })

    await getUsage(14)

    expect(request).toHaveBeenCalledWith('/usage?days=14')
  })

  it('forces a refresh with an extended timeout', async () => {
    request.mockResolvedValueOnce({ providers: [] })

    await refreshUsage(30)

    expect(request).toHaveBeenCalledWith(
      '/usage/refresh?days=30',
      { method: 'POST' },
      { timeoutMs: 60_000 },
    )
  })
})
