import { afterEach, describe, expect, it, vi } from 'vitest'

// Mock the shared request() helper so we assert the brain client hits the
// right path + method without touching fetch / the network.
const request = vi.fn()
vi.mock('@/api/client', () => ({
  request: (...args: unknown[]) => request(...args),
}))

import {
  getBrainStatus,
  listBrainErrors,
  pushBrain,
  syncBrain,
} from '@/api/brain'
import type { BrainStatus, BrainVerifyResult } from '@/api/brain'

describe('brain api client', () => {
  afterEach(() => request.mockReset())

  it('getBrainStatus GETs /brain/status and returns the payload', async () => {
    const payload: BrainStatus = {
      enabled: true,
      dir: '/home/x/.mcplexer/brain',
      error_count: 0,
      git: {
        initialized: true,
        dirty: false,
        ahead: 0,
        behind: 0,
        has_remote: true,
        has_upstream: true,
        branch: 'main',
        last_commit: 'abc123 init',
      },
    }
    request.mockResolvedValueOnce(payload)

    const got = await getBrainStatus()

    expect(request).toHaveBeenCalledWith('/brain/status')
    expect(got).toEqual(payload)
  })

  it('listBrainErrors GETs /brain/errors', async () => {
    request.mockResolvedValueOnce([])
    await listBrainErrors()
    expect(request).toHaveBeenCalledWith('/brain/errors')
  })

  it('pushBrain POSTs /brain/push', async () => {
    request.mockResolvedValueOnce({ pushed: true, conflict: false })
    const res = await pushBrain()
    expect(request).toHaveBeenCalledWith('/brain/push', { method: 'POST' })
    expect(res.pushed).toBe(true)
  })

  it('syncBrain POSTs /brain/sync and returns the verify result', async () => {
    const verify: BrainVerifyResult = { ok: true, files_checked: 3, drifts: [] }
    request.mockResolvedValueOnce(verify)
    const res = await syncBrain()
    expect(request).toHaveBeenCalledWith('/brain/sync', { method: 'POST' })
    expect(res.ok).toBe(true)
    expect(res.files_checked).toBe(3)
  })
})
