import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { ApiClientError, apiURL, request } from '@/api/transport'

const fetchMock = vi.fn()

describe('API transport', () => {
  beforeEach(() => {
    vi.stubGlobal('fetch', fetchMock)
  })

  afterEach(() => {
    fetchMock.mockReset()
    vi.unstubAllGlobals()
  })

  it('uses the API base without adding cross-origin auth state', async () => {
    fetchMock.mockResolvedValueOnce(
      new Response(JSON.stringify({ ok: true }), {
        headers: { 'Content-Type': 'application/json' },
      }),
    )

    await request(
      '/health',
      {
        method: 'POST',
        headers: { 'X-Test': 'kept' },
        body: '{}',
      },
      { timeoutMs: 0 },
    )

    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('/api/v1/health')
    expect(init.method).toBe('POST')
    expect(init.body).toBe('{}')
    expect(new Headers(init.headers).get('X-Test')).toBe('kept')
    expect(new Headers(init.headers).get('Authorization')).toBeNull()
    expect(init.credentials).toBeUndefined()
    expect(init.signal).toBeUndefined()
  })

  it('keeps the default timeout and caller cancellation semantics', async () => {
    fetchMock.mockResolvedValueOnce(
      new Response(JSON.stringify({ ok: true }), {
        headers: { 'Content-Type': 'application/json' },
      }),
    )
    const controller = new AbortController()

    await request('/health', { signal: controller.signal })

    const [, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(init.signal).toBeInstanceOf(AbortSignal)
    expect(init.signal).not.toBe(controller.signal)
    controller.abort()
    expect(init.signal?.aborted).toBe(true)
  })

  it('returns undefined for empty success responses', async () => {
    fetchMock.mockResolvedValueOnce(new Response(null, { status: 204 }))

    await expect(request<void>('/settings', undefined, { timeoutMs: 0 })).resolves.toBeUndefined()
  })

  it('throws the stable API error with status and response body', async () => {
    fetchMock.mockResolvedValueOnce(new Response('denied', { status: 403 }))

    const result = request('/protected', undefined, { timeoutMs: 0 })

    await expect(result).rejects.toEqual(
      expect.objectContaining<ApiClientError>({
        name: 'ApiClientError',
        message: 'API error 403: denied',
        status: 403,
        body: 'denied',
      }),
    )
  })

  it('builds direct endpoint URLs from the same base', () => {
    expect(apiURL('/backups/example/download')).toBe(
      '/api/v1/backups/example/download',
    )
  })
})
