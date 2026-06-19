import { useCallback, useEffect, useState } from 'react'

import { listWorkers, type WorkerRun, type WorkerRunStatus, type WorkerSummary } from '@/api/workers'
import { subscribeEvent } from '@/hooks/use-event-stream'

const FALLBACK_REFRESH_MS = 60_000
const UNKNOWN_WORKER_REFRESH_MS = 1_500

interface Snapshot {
  rows: WorkerSummary[]
  loading: boolean
  error: string | null
  connected: boolean
  lastRefreshAt: number | null
  lastEventAt: number | null
}

interface WorkerRunEvent {
  kind?: 'status' | 'tool_call' | 'usage' | string
  worker_id?: string
  run_id?: string
  run?: WorkerRun
  input_tokens?: number
  output_tokens?: number
  cost_usd?: number
  tool_calls?: number
}

let snapshot: Snapshot = {
  rows: [],
  loading: false,
  error: null,
  connected: false,
  lastRefreshAt: null,
  lastEventAt: null,
}

const listeners = new Set<() => void>()
let unsubscribeWorkers: (() => void) | null = null
let refreshTimer: ReturnType<typeof setInterval> | undefined
let inFlight = false
let lastUnknownRefresh = 0

function emit() {
  for (const listener of listeners) listener()
}

function setSnapshot(patch: Partial<Snapshot>) {
  snapshot = { ...snapshot, ...patch }
  emit()
}

async function refreshWorkers() {
  if (inFlight) return
  if (typeof document !== 'undefined' && document.visibilityState !== 'visible') return
  inFlight = true
  setSnapshot({ loading: snapshot.rows.length === 0, error: null })
  try {
    const rows = await listWorkers()
    setSnapshot({
      rows,
      loading: false,
      error: null,
      lastRefreshAt: Date.now(),
    })
  } catch (err) {
    setSnapshot({
      loading: false,
      error: err instanceof Error ? err.message : 'Failed to load workers',
    })
  } finally {
    inFlight = false
  }
}

function refreshSoonForUnknownWorker() {
  const now = Date.now()
  if (now - lastUnknownRefresh < UNKNOWN_WORKER_REFRESH_MS) return
  lastUnknownRefresh = now
  window.setTimeout(() => void refreshWorkers(), UNKNOWN_WORKER_REFRESH_MS)
}

function applyRunStatus(run: WorkerRun) {
  let found = false
  const rows = snapshot.rows.map((row) => {
    if (row.id !== run.worker_id) return row
    found = true
    return {
      ...row,
      last_run_status: run.status,
      last_run_at: run.started_at || row.last_run_at,
    }
  })
  if (!found) {
    refreshSoonForUnknownWorker()
    return
  }
  setSnapshot({ rows, lastEventAt: Date.now() })
}

function markWorkerRunning(workerID: string) {
  let found = false
  const rows = snapshot.rows.map((row) => {
    if (row.id !== workerID) return row
    found = true
    if (row.last_run_status === 'running') return row
    return { ...row, last_run_status: 'running' as WorkerRunStatus }
  })
  if (!found) {
    refreshSoonForUnknownWorker()
    return
  }
  setSnapshot({ rows, lastEventAt: Date.now() })
}

function handleWorkerEvent(data: unknown) {
  const evt = data as WorkerRunEvent
  if (evt.kind === 'status' && evt.run) {
    applyRunStatus(evt.run)
    return
  }
  const workerID = evt.worker_id || evt.run?.worker_id
  if (!workerID) return
  if (evt.kind === 'usage' || evt.kind === 'tool_call') {
    markWorkerRunning(workerID)
  }
}

function onVisibility() {
  if (document.visibilityState === 'visible') void refreshWorkers()
}

function start() {
  if (unsubscribeWorkers) return
  unsubscribeWorkers = subscribeEvent('workers', handleWorkerEvent)
  setSnapshot({ connected: true })
  void refreshWorkers()
  refreshTimer = setInterval(() => void refreshWorkers(), FALLBACK_REFRESH_MS)
  document.addEventListener('visibilitychange', onVisibility)
}

function stop() {
  unsubscribeWorkers?.()
  unsubscribeWorkers = null
  if (refreshTimer) {
    clearInterval(refreshTimer)
    refreshTimer = undefined
  }
  document.removeEventListener('visibilitychange', onVisibility)
  setSnapshot({ connected: false })
}

function subscribe(listener: () => void): () => void {
  listeners.add(listener)
  if (listeners.size === 1) start()
  return () => {
    listeners.delete(listener)
    if (listeners.size === 0) stop()
  }
}

export function useWorkersRealtime() {
  const [, force] = useState(0)
  useEffect(() => subscribe(() => force((n) => n + 1)), [])
  const refetch = useCallback(() => void refreshWorkers(), [])
  return { ...snapshot, refetch }
}
