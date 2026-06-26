import { describe, expect, it } from 'vitest'
import type { Task } from '@/api/tasks'
import {
  buildTaskChildParentIds,
  matchesTaskCompositionFilter,
  taskCompositionFlags,
} from '@/pages/tasks/task-utils'

function task(overrides: Partial<Task>): Task {
  return {
    id: 'task-1',
    workspace_id: 'ws-1',
    title: 'Task one',
    description: '',
    status: 'open',
    priority: 'normal',
    assignee_origin_kind: 'local',
    source_kind: 'agent',
    created_at: '2026-06-12T10:00:00Z',
    updated_at: '2026-06-12T10:00:00Z',
    ...overrides,
  }
}

describe('task composition filters', () => {
  it('detects epics from their composes metadata', () => {
    const epic = task({ id: 'epic-1', meta: '{"composes":["child-1","child-2"]}' })
    const ids = buildTaskChildParentIds([epic])

    expect(taskCompositionFlags(epic, ids)).toEqual({ isEpic: true, isChild: false })
    expect(matchesTaskCompositionFilter(epic, 'epics', ids)).toBe(true)
    expect(matchesTaskCompositionFilter(epic, 'standalone', ids)).toBe(false)
  })

  it('detects epics from loaded children that point at a parent', () => {
    const parent = task({ id: 'parent-1' })
    const child = task({ id: 'child-1', meta: '{"composed_by":"parent-1"}' })
    const ids = buildTaskChildParentIds([parent, child])

    expect(taskCompositionFlags(parent, ids)).toEqual({ isEpic: true, isChild: false })
    expect(taskCompositionFlags(child, ids)).toEqual({ isEpic: false, isChild: true })
    expect(matchesTaskCompositionFilter(parent, 'epics', ids)).toBe(true)
    expect(matchesTaskCompositionFilter(child, 'children', ids)).toBe(true)
  })

  it('keeps legacy frontmatter rows filterable', () => {
    const child = task({ id: 'child-legacy', meta: 'composed_by: parent-legacy' })
    const standalone = task({ id: 'standalone' })
    const ids = buildTaskChildParentIds([child, standalone])

    expect(matchesTaskCompositionFilter(child, 'children', ids)).toBe(true)
    expect(matchesTaskCompositionFilter(standalone, 'standalone', ids)).toBe(true)
  })
})
