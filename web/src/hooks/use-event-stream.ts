import { useSyncExternalStore } from 'react'
import { getBackoffDelay } from '@/lib/sse-backoff'

// Single multiplexed SSE hub. The gateway serves GET /api/v1/events/stream,
// which fans several always-on event sources (notifications, approvals,
// sessions, secret-prompts, tasks, workers) onto ONE connection, each tagged as a
// named SSE event. The browser talks HTTP/1.1 over plain http:// and is
// capped at ~6 connections per origin, so collapsing these EventSources
// into one is what keeps the pool from exhausting (see api/client.ts for the
// request-timeout half of the fix, and internal/api/events_sse_handler.go for
// the server side).
//
// Every consumer (useSignalStream, useApprovalStream, useSessionStream,
// useSecretPromptStream, useTasksStream) subscribes to a channel here instead
// of opening its own connection. The underlying EventSource is opened on the
// first subscriber and torn down when the last one leaves; reconnect uses the
// shared backoff helper.

export type EventChannel =
  | 'notifications'
  | 'approvals'
  | 'sessions'
  | 'secrets'
  | 'tasks'
  | 'workers'

type Handler = (data: unknown) => void

const CHANNELS: EventChannel[] = [
  'notifications',
  'approvals',
  'sessions',
  'secrets',
  'tasks',
  'workers',
]

const listeners: Record<EventChannel, Set<Handler>> = {
  notifications: new Set(),
  approvals: new Set(),
  sessions: new Set(),
  secrets: new Set(),
  tasks: new Set(),
  workers: new Set(),
}

let es: EventSource | null = null
let retry = 0
let retryTimer: ReturnType<typeof setTimeout> | undefined
let refcount = 0
let connectionState: EventStreamState = 'idle'
const statusListeners = new Set<() => void>()

export type EventStreamState = 'idle' | 'connecting' | 'open' | 'reconnecting'

function emitStatus() {
  for (const listener of statusListeners) listener()
}

function setConnectionState(next: EventStreamState) {
  if (connectionState === next) return
  connectionState = next
  emitStatus()
}

function connect() {
  if (es) return
  setConnectionState(retry > 0 ? 'reconnecting' : 'connecting')
  const apiBase = import.meta.env.VITE_API_BASE_URL?.replace(/\/api\/v1$/, '') || ''
  const source = new EventSource(`${apiBase}/api/v1/events/stream`)
  es = source

  source.onopen = () => {
    retry = 0
    setConnectionState('open')
  }

  for (const channel of CHANNELS) {
    source.addEventListener(channel, (event: MessageEvent) => {
      let data: unknown
      try {
        data = JSON.parse(event.data)
      } catch {
        return // skip malformed events
      }
      for (const handler of listeners[channel]) handler(data)
    })
  }

  source.onerror = () => {
    source.close()
    if (es === source) es = null
    setConnectionState('reconnecting')
    const delay = getBackoffDelay(retry)
    retry++
    retryTimer = setTimeout(() => {
      if (refcount > 0) connect()
    }, delay)
  }
}

function teardown() {
  if (retryTimer) {
    clearTimeout(retryTimer)
    retryTimer = undefined
  }
  es?.close()
  es = null
  setConnectionState('idle')
}

// subscribeEvent registers a handler for one channel and returns an
// unsubscribe function. The shared EventSource opens on the first subscriber
// across all channels and closes when the last one unsubscribes.
export function subscribeEvent(channel: EventChannel, handler: Handler): () => void {
  listeners[channel].add(handler)
  refcount++
  if (refcount === 1) connect()
  return () => {
    listeners[channel].delete(handler)
    refcount--
    if (refcount === 0) teardown()
  }
}

function subscribeStatus(listener: () => void): () => void {
  statusListeners.add(listener)
  return () => statusListeners.delete(listener)
}

export function useEventStreamStatus(): EventStreamState {
  return useSyncExternalStore(subscribeStatus, () => connectionState, () => connectionState)
}
