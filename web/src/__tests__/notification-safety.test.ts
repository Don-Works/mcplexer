import { describe, expect, it } from 'vitest'
import { safeNotificationPath } from '@/components/notifications/safe-link'
import { destinationForSignal } from '@/components/notifications/use-os-notifications'
import { nextSyntheticNotificationID } from '@/components/notifications/use-signal'
import { hasTaskRef } from '@/pages/tasks/task-utils'

describe('notification navigation safety', () => {
  it.each([
    'https://example.test/phish',
    '//example.test/phish',
    'javascript:alert(1)',
    'data:text/html,boom',
    '/\\example.test/phish',
  ])('rejects untrusted destination %s', (link) => {
    expect(safeNotificationPath(link)).toBeNull()
  })

  it('preserves a same-origin path, query, and fragment', () => {
    expect(safeNotificationPath('/approvals?selected=abc#details')).toBe(
      '/approvals?selected=abc#details',
    )
  })

  it('falls back by signal kind when an explicit link is unsafe', () => {
    expect(
      destinationForSignal({
        message_id: 'message-1',
        title: 'Approval required',
        kind: 'approval_pending',
        link: 'javascript:alert(1)',
      }),
    ).toBe('/approvals')
  })
})

describe('notification identity safety', () => {
  it('creates unique negative ids for events in the same millisecond', () => {
    const first = nextSyntheticNotificationID(1_700_000_000_000)
    const second = nextSyntheticNotificationID(1_700_000_000_000)
    expect(first).toBeLessThan(0)
    expect(second).toBeLessThan(first)
  })
})

describe('task reference detection', () => {
  it('is stable across repeated calls', () => {
    const text = 'see task:01ARZ3NDEKTSV4RRFFQ69G5FAV'
    for (let i = 0; i < 10; i += 1) {
      expect(hasTaskRef(text)).toBe(true)
    }
  })
})
