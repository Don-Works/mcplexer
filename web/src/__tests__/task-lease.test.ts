import { describe, expect, it } from 'vitest'
import { leaseStaleness } from '@/pages/tasks/task-utils'

describe('task lease state', () => {
  it('does not mark human-owned tasks abandoned when a device session is absent from mesh', () => {
    expect(
      leaseStaleness('doing', 'dashboard-old', null, new Set(), undefined, 'user-max'),
    ).toEqual({ state: 'idle' })
  })

  it('marks working agent tasks abandoned when the assignee session left mesh', () => {
    expect(
      leaseStaleness('doing', 'agent-gone', null, new Set()),
    ).toEqual({ state: 'abandoned' })
  })
})
