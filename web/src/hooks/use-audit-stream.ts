import { useCallback, useEffect, useRef, useState } from 'react'
import type { AuditRecord } from '@/api/types'
import { getBackoffDelay } from '@/lib/sse-backoff'

interface AuditStreamFilter {
  workspace_id?: string
  tool_name?: string
  status?: string
  execution_id?: string
  session_id?: string
}

const MAX_RECORDS = 200

// useAuditStream subscribes to the live audit SSE feed and keeps the most
// recent MAX_RECORDS in memory (newest first). The connection stays open
// regardless of `paused` — pausing only stops new events from flowing into the
// visible `records` list, buffering them instead so the Mission Control feed
// can hold a stable scroll position while still tracking how many landed. This
// is purely additive over the original hook: `records`, `connected`, and
// `clear` keep their exact prior semantics, so existing callers are untouched.
export function useAuditStream(filter: AuditStreamFilter) {
  const [records, setRecords] = useState<AuditRecord[]>([])
  const [connected, setConnected] = useState(false)
  const [paused, setPaused] = useState(false)
  // Count of events that arrived while paused and have not yet been flushed
  // into `records`. Drives the "N new" pill.
  const [bufferedCount, setBufferedCount] = useState(0)
  const retryRef = useRef(0)
  const esRef = useRef<EventSource | null>(null)
  // Events received while paused, held back from `records` until resume().
  const bufferRef = useRef<AuditRecord[]>([])
  // Mirror `paused` into a ref so the long-lived SSE onmessage closure reads
  // the current value without re-subscribing on every pause toggle.
  const pausedRef = useRef(paused)
  pausedRef.current = paused

  const clear = useCallback(() => {
    bufferRef.current = []
    setBufferedCount(0)
    setRecords([])
  }, [])

  const pause = useCallback(() => setPaused(true), [])

  const resume = useCallback(() => {
    setPaused(false)
    setRecords((prev) => {
      if (bufferRef.current.length === 0) return prev
      const next = [...bufferRef.current, ...prev].slice(0, MAX_RECORDS)
      bufferRef.current = []
      return next
    })
    setBufferedCount(0)
  }, [])

  useEffect(() => {
    let cancelled = false
    let retryTimeout: ReturnType<typeof setTimeout>

    function connect() {
      if (cancelled) return

      const params = new URLSearchParams()
      if (filter.workspace_id) params.set('workspace_id', filter.workspace_id)
      if (filter.tool_name) params.set('tool_name', filter.tool_name)
      if (filter.status) params.set('status', filter.status)
      if (filter.execution_id) params.set('execution_id', filter.execution_id)
      if (filter.session_id) params.set('session_id', filter.session_id)

      const qs = params.toString()
      const apiBase = import.meta.env.VITE_API_BASE_URL?.replace(/\/api\/v1$/, '') || ''
      const url = `${apiBase}/api/v1/audit/stream${qs ? `?${qs}` : ''}`

      const es = new EventSource(url)
      esRef.current = es

      es.onopen = () => {
        if (cancelled) return
        setConnected(true)
        retryRef.current = 0
      }

      es.onmessage = (event) => {
        if (cancelled) return
        try {
          const record = JSON.parse(event.data) as AuditRecord
          if (pausedRef.current) {
            // Buffer (newest first) and surface the count; don't disturb the
            // visible list while paused.
            bufferRef.current = [record, ...bufferRef.current].slice(0, MAX_RECORDS)
            setBufferedCount(bufferRef.current.length)
          } else {
            setRecords((prev) => [record, ...prev].slice(0, MAX_RECORDS))
          }
        } catch {
          // skip malformed events
        }
      }

      es.onerror = () => {
        if (cancelled) return
        es.close()
        esRef.current = null
        setConnected(false)

        const delay = getBackoffDelay(retryRef.current)
        retryRef.current++
        retryTimeout = setTimeout(connect, delay)
      }
    }

    connect()

    return () => {
      cancelled = true
      clearTimeout(retryTimeout)
      esRef.current?.close()
      esRef.current = null
      setConnected(false)
    }
  }, [filter.workspace_id, filter.tool_name, filter.status, filter.execution_id, filter.session_id])

  return { records, connected, clear, paused, pause, resume, bufferedCount }
}
