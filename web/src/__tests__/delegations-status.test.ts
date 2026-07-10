import { describe, expect, it } from 'vitest'

import { delegationStatusBadgeClass } from '@/pages/workers/DelegationsPage'
import { statusBadgeClass } from '@/pages/workers/worker-utils'

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
})