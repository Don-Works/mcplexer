import { describe, expect, it } from 'vitest'

import type { DelegationContext } from '@/api/workers'
import {
  delegationBlocked,
  delegationNeedsReview,
  delegationStatusBadgeClass,
  statusBadgeClass,
} from '@/pages/workers/worker-utils'

describe('delegationStatusBadgeClass', () => {
  it('maps needs_review to neutral slate styling, not failure red', () => {
    const needsReview = delegationStatusBadgeClass('needs_review')
    const failure = delegationStatusBadgeClass('failure')
    const rejected = delegationStatusBadgeClass('rejected')

    expect(needsReview).toBe(statusBadgeClass('cancelled'))
    expect(needsReview).not.toContain('destructive')
    expect(needsReview).not.toContain('red')

    expect(failure).toBe(statusBadgeClass('failure'))
    expect(failure).toContain('destructive')

    expect(rejected).toBe(statusBadgeClass('failure'))
    expect(rejected).toContain('destructive')
  })

  it('keeps backend blocked status distinct from the review gate', () => {
    const blocked = {
      status: 'blocked',
      review_required: true,
      review: { reviewed: false },
    } as DelegationContext

    expect(delegationBlocked(blocked)).toBe(true)
    expect(delegationNeedsReview(blocked)).toBe(false)
    expect(delegationStatusBadgeClass('blocked')).toBe(statusBadgeClass('blocked'))
  })

  it('styles detached dispatch failures as failures', () => {
    expect(statusBadgeClass('dispatch_failed')).toBe(statusBadgeClass('failure'))
  })
})
