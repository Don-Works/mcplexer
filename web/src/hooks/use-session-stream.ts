import { useEffect, useRef, useState } from 'react'
import type { SessionInfo } from '@/api/types'
import { subscribeEvent } from '@/hooks/use-event-stream'

interface SessionEvent {
  type: 'connected' | 'disconnected'
  session: SessionInfo
}

// useSessionStream tracks the live set of connected sessions. It subscribes
// to the 'sessions' channel of the multiplexed event hub
// (hooks/use-event-stream.ts) rather than opening its own EventSource — one
// shared connection for all always-on streams keeps us under the browser's
// HTTP/1.1 per-origin cap.
export function useSessionStream() {
  const [sessions, setSessions] = useState<SessionInfo[]>([])
  const [connected, setConnected] = useState(false)
  const initializedRef = useRef(false)

  // Seed the stream state from an initial snapshot (e.g. dashboard API).
  function seed(initial: SessionInfo[]) {
    if (initializedRef.current) return
    initializedRef.current = true
    setSessions(initial)
  }

  useEffect(() => {
    const unsub = subscribeEvent('sessions', (data) => {
      const evt = data as SessionEvent
      if (evt.type === 'connected') {
        setSessions((prev) => {
          if (prev.some((s) => s.id === evt.session.id)) return prev
          return [evt.session, ...prev]
        })
      } else if (evt.type === 'disconnected') {
        setSessions((prev) => prev.filter((s) => s.id !== evt.session.id))
      }
    })
    setConnected(true)
    return () => {
      unsub()
      setConnected(false)
    }
  }, [])

  return { sessions, connected, seed }
}
