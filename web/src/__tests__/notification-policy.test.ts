import { describe, expect, it } from 'vitest'

import { isForegroundNotificationEligible } from '@/components/notifications/use-os-notifications'

describe('foreground notification policy', () => {
  it.each([
    ['pending approval', { message_id: '1', title: 'Approval', source: 'approval', kind: 'approval_pending' }, true],
    ['task assignment', { message_id: '2', title: 'Assigned', source: 'task', kind: 'task_assigned' }, true],
    ['task due', { message_id: '3', title: 'Due', source: 'task', kind: 'task_due' }, true],
    ['memory offer', { message_id: '4', title: 'Offer', source: 'memory', kind: 'memory_offer_received' }, true],
    ['high alert', { message_id: '5', title: 'Alert', source: 'system', kind: 'alert', priority: 'high' }, true],
    ['routine task update', { message_id: '6', title: 'Updated', source: 'task', kind: 'task_updated' }, false],
    ['memory write', { message_id: '7', title: 'Saved', source: 'memory', kind: 'memory_write' }, false],
    ['delegation completion', { message_id: '8', title: 'Done', source: 'worker', kind: 'delegation_completed' }, false],
  ] as const)('%s', (_name, event, expected) => {
    expect(isForegroundNotificationEligible(event)).toBe(expected)
  })
})
