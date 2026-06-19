import { afterEach, describe, expect, it, vi } from 'vitest'

// apiURL just composes the path; a passthrough keeps the assist client's
// fetch URLs assertable without the real base.
vi.mock('@/api/client', () => ({
  request: vi.fn(),
  apiURL: (p: string) => `/api/v1${p}`,
  ApiClientError: class ApiClientError extends Error {},
}))

import {
  streamAssistComplete,
  fetchMemoryCandidates,
  fetchGuidance,
} from '@/api/brainBrowser'
import { nextWordBoundary } from '@/pages/brain/components/useGhostText'

// sseBody builds a ReadableStream emitting the given SSE frames (already
// terminated with the blank-line separators) as one chunk.
function sseBody(frames: string): ReadableStream<Uint8Array> {
  const enc = new TextEncoder()
  return new ReadableStream({
    start(controller) {
      controller.enqueue(enc.encode(frames))
      controller.close()
    },
  })
}

describe('nextWordBoundary (graded accept)', () => {
  it('accepts one leading-space word at a time', () => {
    // " so the timer" -> first boundary covers " so".
    expect(nextWordBoundary(' so the timer')).toBe(3)
    // "so the" with no leading space -> covers "so".
    expect(nextWordBoundary('so the')).toBe(2)
    // empty -> 0.
    expect(nextWordBoundary('')).toBe(0)
  })
})

describe('streamAssistComplete', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('returns degraded on a 204 (no model profile)', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue({ status: 204, ok: false, body: null }),
    )
    const tokens: string[] = []
    const res = await streamAssistComplete({ context: 'hi' }, (t) => tokens.push(t))
    expect(res).toEqual({ profile: null, degraded: true })
    expect(tokens).toHaveLength(0)
  })

  it('streams token frames and resolves the profile from done', async () => {
    const frames =
      'event: token\ndata: re-compute\n\n' +
      'event: token\ndata:  next_run_at\n\n' +
      'event: done\ndata: {"profile":"openai_compat"}\n\n'
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue({ status: 200, ok: true, body: sseBody(frames) }),
    )
    const tokens: string[] = []
    const res = await streamAssistComplete({ context: 'x' }, (t) => tokens.push(t))
    expect(tokens.join('')).toBe('re-compute next_run_at')
    expect(res.profile).toBe('openai_compat')
    expect(res.degraded).toBe(false)
  })

  it('decodes escaped newlines in a token frame', async () => {
    const frames = 'event: token\ndata: line1\\nline2\n\nevent: done\ndata: {"profile":"p"}\n\n'
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue({ status: 200, ok: true, body: sseBody(frames) }),
    )
    const tokens: string[] = []
    await streamAssistComplete({ context: 'x' }, (t) => tokens.push(t))
    expect(tokens.join('')).toBe('line1\nline2')
  })
})

describe('fetchMemoryCandidates', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('returns empty on a 204', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ status: 204, ok: false }))
    const res = await fetchMemoryCandidates({ body: 'a decision because reasons' })
    expect(res.candidates).toHaveLength(0)
    expect(res.profile).toBeNull()
  })

  it('parses the candidate list + profile', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue({
        status: 200,
        ok: true,
        json: async () => ({
          candidates: [
            {
              text: 'Never deploy dirty because traceability.',
              kind: 'note',
              signal: 'decision-with-rationale',
              content_hash: 'abc',
            },
          ],
          profile: 'openai_compat',
        }),
      }),
    )
    const res = await fetchMemoryCandidates({ body: 'x', record_id: 'r1' })
    expect(res.candidates).toHaveLength(1)
    expect(res.candidates[0].content_hash).toBe('abc')
    expect(res.profile).toBe('openai_compat')
  })
})

describe('fetchGuidance', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('parses the nudge list (never 204s — deterministic nudges with no model)', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn().mockResolvedValue({
        status: 200,
        ok: true,
        json: async () => ({
          nudges: [
            {
              kind: 'auto-tag',
              message: 'looks like a #scheduler task - add tag?',
              apply: { add_tag: 'scheduler' },
            },
          ],
          profile: '',
        }),
      }),
    )
    const res = await fetchGuidance({ body: 'cron scheduler', status: 'open' })
    expect(res.nudges).toHaveLength(1)
    expect(res.nudges[0].apply.add_tag).toBe('scheduler')
    expect(res.profile).toBe('')
  })

  it('returns empty on a 204 (assist not wired)', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ status: 204, ok: false }))
    const res = await fetchGuidance({ body: 'x' })
    expect(res.nudges).toHaveLength(0)
  })
})
