import { describe, expect, it } from 'vitest'
import type { Task } from '@/api/tasks'
import type { TaskEvent } from '@/hooks/use-tasks-stream'
import {
  buildTaskHistoryEvents,
  mergeTaskActivityEvents,
} from '@/pages/tasks/task-activity'

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

describe('task activity hydrate', () => {
  it('builds newest-first activity from durable status history', () => {
    const rows = buildTaskHistoryEvents([
      task({
        status: 'done',
        closed_at: '2026-06-12T10:05:00Z',
        status_history: [
          { at: '2026-06-12T10:00:00Z', evt: 'created', to: 'open' },
          { at: '2026-06-12T10:05:00Z', evt: 'status_changed', from: 'open', to: 'done' },
          { at: '2026-06-12T10:05:00Z', evt: 'closed', to: 'done' },
        ],
      }),
    ])

    expect(rows.map((r) => r.history?.evt)).toEqual([
      'status_changed',
      'closed',
      'created',
    ])
    expect(rows.every((r) => r.workspace_id === 'ws-1')).toBe(true)
    expect(rows.map((r) => r.kind)).toEqual([
      'task_updated',
      'task_updated',
      'task_created',
    ])
  })

  it('prefers durable history rows over old generic cached events', () => {
    const t = task({
      status_history: [
        { at: '2026-06-12T10:05:00Z', evt: 'status_changed', from: 'open', to: 'done' },
      ],
    })
    const staleCached: TaskEvent = {
      kind: 'task_updated',
      workspace_id: 'ws-1',
      task: t,
      at: '2026-06-12T10:05:00Z',
    }

    const merged = mergeTaskActivityEvents(
      [staleCached],
      buildTaskHistoryEvents([t]),
    )

    expect(merged).toHaveLength(1)
    expect(merged[0].history?.evt).toBe('status_changed')
  })
})
