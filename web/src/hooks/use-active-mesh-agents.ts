// useActiveMeshAgents — returns the set of currently-active mesh
// session ids. An agent is "active" when the mesh manager has heard
// from it within ACTIVITY_WINDOW_MS. This is the canonical signal for
// "is this AI actually still working?" — strictly better than
// time-since-task-updated, which gives false abandonment for any
// agent doing slow work without writing to the task in question.
//
// Powers the tasks dashboard's abandoned-task detection: a task is
// abandoned ⇔ status=doing + has assignee + assignee NOT in this set.
// The set refreshes every 30s + immediately on mesh SSE events.

import { useCallback, useEffect, useMemo, useState } from 'react'

import { getMeshStatus } from '@/api/client'
import { useApi } from './use-api'

// 5 minutes mirrors the mesh manager's own "active agent" window. An
// agent that hasn't heartbeated within this window is treated as gone
// by both the mesh registry and our abandoned-task heuristic.
const ACTIVITY_WINDOW_MS = 5 * 60 * 1000

// REFRESH_INTERVAL_MS — how often we re-poll the mesh status when no
// SSE event has fired. Mesh agents typically heartbeat far more often
// than this, so we mostly piggyback on the existing data; this is the
// safety-net poll for when the mesh stream is idle.
const REFRESH_INTERVAL_MS = 30_000

export interface ActiveMeshAgents {
  // sessionIds — every session_id currently considered active.
  sessionIds: Set<string>
  // ready — false until the first fetch completes. Callers should
  // treat absent as "unknown, assume active" to avoid flashing
  // "abandoned" badges during initial load.
  ready: boolean
}

export function useActiveMeshAgents(): ActiveMeshAgents {
  const fetcher = useCallback(() => getMeshStatus(), [])
  const { data, refetch } = useApi(fetcher)
  const [tick, setTick] = useState(0)

  // Safety-net poll. SSE-driven refresh would be tighter, but a 30s
  // interval is cheap and avoids the "mesh tab never refreshes" case.
  useEffect(() => {
    const id = setInterval(() => {
      refetch()
      setTick((n) => n + 1)
    }, REFRESH_INTERVAL_MS)
    return () => clearInterval(id)
  }, [refetch])

  return useMemo(() => {
    if (!data) return { sessionIds: new Set<string>(), ready: false }
    const now = Date.now()
    const active = new Set<string>()
    for (const a of data.agents ?? []) {
      const seen = new Date(a.last_seen_at).getTime()
      if (Number.isNaN(seen)) continue
      if (now - seen <= ACTIVITY_WINDOW_MS) active.add(a.session_id)
    }
    return { sessionIds: active, ready: true }
    // tick is intentionally a dep so the memo recomputes when the
    // safety-net poll fires even if `data` is referentially stable.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [data, tick])
}
