import { useEffect, useRef, useState } from 'react'
import type { CompressionStatsResponse } from '@/api/types'
import { getBackoffDelay } from '@/lib/sse-backoff'

// useCompressionStream subscribes to the live compression stats SSE feed
// (/api/v1/compression/stream) and keeps the latest payload in state, so the
// settings page updates as savings accrue without polling. Reconnects with
// backoff, mirroring useAuditStream.
export function useCompressionStream() {
  const [stats, setStats] = useState<CompressionStatsResponse | null>(null)
  const [connected, setConnected] = useState(false)
  const retryRef = useRef(0)
  const esRef = useRef<EventSource | null>(null)

  useEffect(() => {
    let cancelled = false
    let retryTimeout: ReturnType<typeof setTimeout>

    function connect() {
      if (cancelled) return
      const apiBase = import.meta.env.VITE_API_BASE_URL?.replace(/\/api\/v1$/, '') || ''
      const es = new EventSource(`${apiBase}/api/v1/compression/stream`)
      esRef.current = es

      es.onopen = () => {
        if (cancelled) return
        setConnected(true)
        retryRef.current = 0
      }

      es.onmessage = (event) => {
        if (cancelled) return
        try {
          setStats(JSON.parse(event.data) as CompressionStatsResponse)
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
  }, [])

  return { stats, connected }
}
