/**
 * a11y smoke tests: render the busiest pages and assert axe-core finds no
 * violations of impact `serious` or `critical`. We mock `fetch` with empty
 * responses so the components render their empty states.
 */
import { describe, it, expect, beforeEach, vi } from 'vitest'
import { render, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import axe, { type Result as AxeResult } from 'axe-core'

import { DashboardPage } from '@/pages/DashboardPage'
import { AuditPage } from '@/pages/AuditPage'
import { ApprovalsPage } from '@/pages/ApprovalsPage'
import { TooltipProvider } from '@/components/ui/tooltip'

type FetchInput = string | URL | Request

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'content-type': 'application/json' },
  })
}

// Match request URL and return canned data; default empty list / object.
function mockFetch() {
  return vi.fn(async (input: FetchInput) => {
    const url = typeof input === 'string' ? input : input.toString()
    if (url.includes('/dashboard')) {
      return jsonResponse({
        timeseries: [],
        active_session_list: [],
        recent_calls: [],
        recent_errors: [],
        active_downstreams: [],
        tool_leaderboard: [],
        error_breakdown: [],
        server_health: [],
        route_hit_map: [],
        approval_metrics: null,
        cache_stats: {
          tool_call: { hits: 0, misses: 0, evictions: 0, entries: 0, hit_rate: 0 },
          route_resolution: { hits: 0, misses: 0, evictions: 0, entries: 0, hit_rate: 0 },
        },
        stats: {
          total_requests: 0, error_count: 0, blocked_count: 0,
          avg_latency_ms: 0, p95_latency_ms: 0,
        },
      })
    }
    if (url.includes('/audit/query')) {
      return jsonResponse({ data: [], total: 0 })
    }
    if (url.includes('/approvals')) {
      return jsonResponse([])
    }
    if (url.includes('/audit/stream') || url.includes('/approvals/stream') || url.includes('/sessions/stream')) {
      // SSE endpoints — return an empty stream.
      return new Response('', {
        status: 200,
        headers: { 'content-type': 'text/event-stream' },
      })
    }
    // Fallback empty list for everything else (workspaces, auth_scopes, etc.)
    return jsonResponse([])
  })
}

async function runAxe(container: HTMLElement): Promise<AxeResult[]> {
  const results = await axe.run(container, {
    runOnly: { type: 'tag', values: ['wcag2a', 'wcag2aa'] },
    resultTypes: ['violations'],
  })
  return results.violations.filter(
    (v) => v.impact === 'serious' || v.impact === 'critical',
  )
}

function wrap(node: React.ReactNode) {
  return (
    <TooltipProvider>
      <MemoryRouter>{node}</MemoryRouter>
    </TooltipProvider>
  )
}

describe('a11y: busiest pages have no serious/critical axe violations', () => {
  beforeEach(() => {
    globalThis.fetch = mockFetch() as unknown as typeof fetch
    // EventSource is used by stream hooks — stub minimal interface.
    class EventSourceStub {
      url: string
      onmessage: ((e: MessageEvent) => void) | null = null
      onerror: ((e: Event) => void) | null = null
      onopen: ((e: Event) => void) | null = null
      readyState = 0
      constructor(url: string) { this.url = url }
      close() { /* no-op */ }
      addEventListener() { /* no-op */ }
      removeEventListener() { /* no-op */ }
      dispatchEvent() { return false }
    }
    ;(globalThis as unknown as { EventSource: typeof EventSourceStub }).EventSource = EventSourceStub
  })

  it('DashboardPage', async () => {
    const { container } = render(wrap(<DashboardPage />))
    await waitFor(() => {
      expect(container.querySelector('h1')).toBeTruthy()
    })
    const violations = await runAxe(container)
    if (violations.length > 0) {
      console.error('Dashboard violations:', JSON.stringify(violations, null, 2))
    }
    expect(violations).toEqual([])
  })

  it('AuditPage', async () => {
    const { container } = render(wrap(<AuditPage />))
    await waitFor(() => {
      expect(container.querySelector('h1')).toBeTruthy()
    })
    const violations = await runAxe(container)
    if (violations.length > 0) {
      console.error('Audit violations:', JSON.stringify(violations, null, 2))
    }
    expect(violations).toEqual([])
  })

  it('ApprovalsPage', async () => {
    const { container } = render(wrap(<ApprovalsPage />))
    await waitFor(() => {
      expect(container.querySelector('h1')).toBeTruthy()
    })
    const violations = await runAxe(container)
    if (violations.length > 0) {
      console.error('Approvals violations:', JSON.stringify(violations, null, 2))
    }
    expect(violations).toEqual([])
  })
})
