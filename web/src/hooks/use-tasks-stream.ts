import { useEffect, useRef } from 'react'
import type { Task, TaskNote, TaskOffer, TaskStatusHistoryEntry } from '@/api/tasks'
import { subscribeEvent } from '@/hooks/use-event-stream'

// TaskEvent — the JSON shape emitted by the backend tasks bus. Kind strings
// mirror internal/tasks/bus.go constants.
export type TaskEventKind =
  | 'task_created'
  | 'task_updated'
  | 'task_claimed'
  | 'task_deleted'
  | 'task_note_appended'
  | 'task_offer_updated'

export interface TaskEvent {
  kind: TaskEventKind
  workspace_id: string
  task?: Task
  note?: TaskNote
  offer?: TaskOffer
  history?: TaskStatusHistoryEntry
  at: string
}

interface UseTasksStreamOptions {
  workspaceId?: string
  onEvent: (evt: TaskEvent) => void
  // disabled — skip the subscription entirely (useful while the upstream
  // identity is still loading).
  disabled?: boolean
}

// useTasksStream invokes onEvent for each task event. It subscribes to the
// 'tasks' channel of the multiplexed event hub (hooks/use-event-stream.ts)
// rather than opening its own /tasks/stream EventSource — one shared
// connection for all always-on streams keeps us under the browser's
// HTTP/1.1 per-origin cap. The multiplexed stream is unfiltered, so the
// optional workspaceId filter is applied client-side.
export function useTasksStream({ workspaceId, onEvent, disabled }: UseTasksStreamOptions) {
  // Stash the latest callback in a ref so subscribers don't need to memoize
  // onEvent to avoid re-subscribing.
  const cbRef = useRef(onEvent)
  cbRef.current = onEvent

  useEffect(() => {
    if (disabled) return
    return subscribeEvent('tasks', (data) => {
      const evt = data as TaskEvent
      if (workspaceId && evt.workspace_id !== workspaceId) return
      cbRef.current(evt)
    })
  }, [workspaceId, disabled])
}
