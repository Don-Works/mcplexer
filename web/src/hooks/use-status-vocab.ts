// use-status-vocab — fetches /api/v1/task-status-vocabulary?workspace_id=
// and returns a Map<status_text, kind> the rest of the tasks UI uses
// to classify freeform statuses into one of the canonical buckets
// (open|working|blocked|done|cancelled). See migration 070 +
// task-utils.tsx kindOfStatus / isWorkingStatus.
//
// Caching strategy: useApi already provides per-component
// stale-while-revalidate; we additionally normalise the result into a
// Map ONCE per fetch (vs every render) via useMemo, so the consumer
// gets a referentially-stable handle they can pass into the various
// status-classification helpers in task-utils without re-deriving on
// every tick of useNow.
//
// Empty / missing workspace ID returns an empty map (and skips the
// fetch) — components calling this from a page that doesn't yet have
// a workspace context still render cleanly without an error toast.

import { useCallback, useMemo } from 'react'
import { listTaskVocab, type StatusKind, type TaskStatusVocab } from '@/api/tasks'
import { useApi } from './use-api'
import type { StatusKindMap } from '@/pages/tasks/task-utils'

export interface UseStatusVocabReturn {
  vocab: StatusKindMap
  raw: TaskStatusVocab[]
  loading: boolean
  error: string | null
  refetch: () => void
}

const EMPTY_VOCAB: StatusKindMap = new Map()

export function useStatusVocab(workspaceId: string | null | undefined): UseStatusVocabReturn {
  const wsID = workspaceId ?? ''
  const fetcher = useCallback(async (): Promise<TaskStatusVocab[]> => {
    if (!wsID) return []
    return listTaskVocab(wsID)
  }, [wsID])
  const { data, loading, error, refetch } = useApi(fetcher)

  const vocab = useMemo<StatusKindMap>(() => {
    if (!data || data.length === 0) return EMPTY_VOCAB
    const m = new Map<string, StatusKind | string>()
    for (const row of data) {
      if (!row.status_text) continue
      // Default to "open" when the row predates migration 070 self-heal
      // — matches the server-side fallback in scanTaskVocab.
      m.set(row.status_text, row.kind ?? 'open')
    }
    return m
  }, [data])

  return {
    vocab,
    raw: data ?? [],
    loading,
    error,
    refetch,
  }
}
