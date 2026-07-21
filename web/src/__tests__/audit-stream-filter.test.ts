import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { renderHook } from '@testing-library/react'
import { useAuditStream } from '@/hooks/use-audit-stream'

// jsdom has no EventSource. This stub records the URL the hook opens so we can
// assert which filter dimensions get forwarded to /api/v1/audit/stream.
const opened: string[] = []

class EventSourceStub {
  url: string
  onopen: (() => void) | null = null
  onmessage: ((e: MessageEvent) => void) | null = null
  onerror: (() => void) | null = null
  constructor(url: string) {
    this.url = url
    opened.push(url)
  }
  close() {
    /* no-op */
  }
}

function paramsOf(url: string): URLSearchParams {
  return new URL(url, 'http://localhost').searchParams
}

describe('useAuditStream — SSE filter forwarding', () => {
  beforeEach(() => {
    opened.length = 0
    ;(globalThis as unknown as { EventSource: unknown }).EventSource = EventSourceStub
  })
  afterEach(() => {
    delete (globalThis as { EventSource?: unknown }).EventSource
  })

  it('forwards every server-matched dimension the SSE handler accepts', () => {
    renderHook(() =>
      useAuditStream({
        workspace_id: 'ws1',
        tool_name: 'github__list_issues',
        status: 'error',
        execution_id: 'exec1',
        session_id: 'sess1',
        actor_kind: 'worker',
        actor_id: 'agent-7',
        downstream_server_id: 'srv1',
        route_rule_id: 'route1',
        client_type: 'claude-code',
        error_code: 'E_DENIED',
        tier: 'verbose',
      }),
    )

    expect(opened).toHaveLength(1)
    expect(opened[0]).toContain('/api/v1/audit/stream')
    const p = paramsOf(opened[0])
    // The five legacy dims plus the seven Mission Control facets the gateway's
    // audit_sse_handler.go matches server-side.
    expect(p.get('workspace_id')).toBe('ws1')
    expect(p.get('tool_name')).toBe('github__list_issues')
    expect(p.get('status')).toBe('error')
    expect(p.get('execution_id')).toBe('exec1')
    expect(p.get('session_id')).toBe('sess1')
    expect(p.get('actor_kind')).toBe('worker')
    expect(p.get('actor_id')).toBe('agent-7')
    expect(p.get('downstream_server_id')).toBe('srv1')
    expect(p.get('route_rule_id')).toBe('route1')
    expect(p.get('client_type')).toBe('claude-code')
    expect(p.get('error_code')).toBe('E_DENIED')
    expect(p.get('tier')).toBe('verbose')
  })

  it('omits unset dimensions from the query string', () => {
    renderHook(() => useAuditStream({ workspace_id: 'ws1', status: 'success' }))

    const p = paramsOf(opened[0])
    expect(p.get('workspace_id')).toBe('ws1')
    expect(p.get('status')).toBe('success')
    expect(p.has('tool_name')).toBe(false)
    expect(p.has('actor_kind')).toBe(false)
    expect(p.has('downstream_server_id')).toBe(false)
    expect(p.has('tier')).toBe(false)
  })

  it('opens an unfiltered stream when the filter is empty', () => {
    renderHook(() => useAuditStream({}))

    expect(opened[0]).toContain('/api/v1/audit/stream')
    expect(opened[0]).not.toContain('?')
  })
})
