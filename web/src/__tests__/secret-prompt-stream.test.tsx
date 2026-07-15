import { act, renderHook, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { SecretPrompt } from '@/hooks/use-secret-prompt-stream'
import { useSecretPromptStream } from '@/hooks/use-secret-prompt-stream'

const hub = vi.hoisted(() => {
  const handlers = new Set<(data: unknown) => void>()
  const subscribe = vi.fn((_channel: string, handler: (data: unknown) => void) => {
    handlers.add(handler)
    return () => handlers.delete(handler)
  })
  return {
    handlers,
    status: 'open' as 'idle' | 'connecting' | 'open' | 'reconnecting',
    subscribe,
  }
})

vi.mock('@/hooks/use-event-stream', () => ({
  subscribeEvent: hub.subscribe,
  useEventStreamStatus: () => hub.status,
}))

function prompt(id: string, requester = 'api-requester'): SecretPrompt {
  return {
    id,
    reason: `reason-${id}`,
    label: `LABEL_${id}`,
    requester,
    status: 'pending',
    expires_at: '2026-07-15T12:02:00Z',
    created_at: '2026-07-15T12:00:00Z',
  }
}

function response(rows: SecretPrompt[]): Response {
  return {
    ok: true,
    json: async () => rows,
  } as Response
}

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((done) => {
    resolve = done
  })
  return { promise, resolve }
}

function emit(data: unknown) {
  for (const handler of hub.handlers) handler(data)
}

describe('useSecretPromptStream', () => {
  beforeEach(() => {
    hub.handlers.clear()
    hub.subscribe.mockClear()
    hub.status = 'open'
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('merges live arrivals with the pending snapshot without resurrecting resolved IDs', async () => {
    const pendingFetch = deferred<Response>()
    const fetchMock = vi.fn().mockReturnValueOnce(pendingFetch.promise)
    vi.stubGlobal('fetch', fetchMock)

    const { result } = renderHook(() => useSecretPromptStream())

    act(() => {
      emit({
        type: 'pending',
        id: 'live',
        reason: 'live reason',
        label: 'LIVE_KEY',
        requester: 'sse-requester',
        status: 'pending',
      })
      emit({ type: 'resolved', id: 'resolved', status: 'timeout' })
    })

    await act(async () => {
      pendingFetch.resolve(
        response([prompt('server'), prompt('live'), prompt('resolved')]),
      )
      await pendingFetch.promise
    })

    await waitFor(() => {
      expect(result.current.pending.map((item) => item.id)).toEqual(['server', 'live'])
    })
    expect(result.current.pending[1].requester).toBe('sse-requester')
    expect(result.current.connected).toBe(true)
  })

  it('reconciles after reconnect while keeping mid-fetch events and one hub listener', async () => {
    const reconnectFetch = deferred<Response>()
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(response([prompt('stale')]))
      .mockReturnValueOnce(reconnectFetch.promise)
    vi.stubGlobal('fetch', fetchMock)

    const { result, rerender } = renderHook(() => useSecretPromptStream())

    await waitFor(() => {
      expect(result.current.pending.map((item) => item.id)).toEqual(['stale'])
    })

    act(() => {
      hub.status = 'reconnecting'
      rerender()
    })
    expect(result.current.connected).toBe(false)

    act(() => {
      hub.status = 'open'
      rerender()
    })
    expect(fetchMock).toHaveBeenCalledTimes(2)

    act(() => {
      emit({
        type: 'pending',
        id: 'fresh',
        requester: 'live-requester',
        status: 'pending',
      })
    })

    await act(async () => {
      reconnectFetch.resolve(response([prompt('server')]))
      await reconnectFetch.promise
    })

    await waitFor(() => {
      expect(result.current.pending.map((item) => item.id)).toEqual(['server', 'fresh'])
    })
    expect(result.current.pending[1].requester).toBe('live-requester')
    expect(result.current.connected).toBe(true)
    expect(hub.subscribe).toHaveBeenCalledTimes(1)
  })
})
