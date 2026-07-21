import { useCallback, useEffect, useReducer, useRef } from 'react'
import type { Dispatch } from 'react'
import { subscribeEvent, useEventStreamStatus } from '@/hooks/use-event-stream'

export interface SecretPrompt {
  id: string
  reason: string
  label: string
  requester: string
  status: string
  expires_at: string
  created_at: string
}

interface SecretPromptEvent {
  type: 'pending' | 'resolved'
  id: string
  reason?: string
  label?: string
  requester?: string
  status?: string
  expires_at?: string
  created_at?: string
}

interface VersionedEvent {
  event: SecretPromptEvent
  version: number
}

interface PromptState {
  pending: SecretPrompt[]
  journal: Map<string, VersionedEvent>
}

type PromptAction =
  | { type: 'event'; item: VersionedEvent; record: boolean }
  | { type: 'snapshot'; rows: SecretPrompt[]; startedAt: number }
  | { type: 'clear-journal' }

function promptFromEvent(event: SecretPromptEvent): SecretPrompt {
  return {
    id: event.id,
    reason: event.reason ?? '',
    label: event.label ?? '',
    requester: event.requester ?? '',
    status: event.status ?? 'pending',
    expires_at: event.expires_at ?? '',
    created_at: event.created_at ?? '',
  }
}

function applyPromptEvent(
  pending: SecretPrompt[],
  event: SecretPromptEvent,
): SecretPrompt[] {
  if (event.type === 'resolved') return pending.filter((item) => item.id !== event.id)
  const next = promptFromEvent(event)
  return [...pending.filter((item) => item.id !== next.id), next]
}

function promptReducer(state: PromptState, action: PromptAction): PromptState {
  if (action.type === 'clear-journal') return { ...state, journal: new Map() }
  if (action.type === 'event') {
    const journal = action.record ? new Map(state.journal) : state.journal
    if (action.record) journal.set(action.item.event.id, action.item)
    return { pending: applyPromptEvent(state.pending, action.item.event), journal }
  }
  let pending = action.rows.filter((row) => row.status === 'pending')
  const replay = [...state.journal.values()]
    .filter((item) => item.version > action.startedAt)
    .sort((a, b) => a.version - b.version)
  for (const item of replay) pending = applyPromptEvent(pending, item.event)
  return { pending, journal: new Map() }
}

async function fetchPendingPrompts(): Promise<SecretPrompt[] | null> {
  const apiBase = import.meta.env.VITE_API_BASE_URL?.replace(/\/api\/v1$/, '') || ''
  const response = await fetch(`${apiBase}/api/v1/secrets/prompts/pending`, {
    signal: AbortSignal.timeout(30_000),
  })
  return response.ok ? (response.json() as Promise<SecretPrompt[]>) : null
}

function usePromptReconciler(dispatch: Dispatch<PromptAction>) {
  const mounted = useRef(false)
  const eventVersion = useRef(0)
  const requestVersion = useRef(0)
  const reconciling = useRef(false)

  const applyEvent = useCallback((event: SecretPromptEvent) => {
    const item = { event, version: ++eventVersion.current }
    dispatch({ type: 'event', item, record: reconciling.current })
  }, [dispatch])

  const reconcile = useCallback(async () => {
    const request = ++requestVersion.current
    const startedAt = eventVersion.current
    reconciling.current = true
    try {
      const rows = await fetchPendingPrompts()
      if (rows && mounted.current && request === requestVersion.current) {
        dispatch({ type: 'snapshot', rows, startedAt })
      }
    } catch {
      // Best effort: live events still keep the panel current.
    } finally {
      if (request === requestVersion.current) {
        reconciling.current = false
        if (mounted.current) dispatch({ type: 'clear-journal' })
      }
    }
  }, [dispatch])

  return { applyEvent, reconcile, mounted }
}

// Merges the initial pending snapshot with live SSE events. Only events that
// race an in-flight snapshot are journalled, so a long-lived page cannot retain
// metadata for every prompt it has ever observed.
export function useSecretPromptStream() {
  const [state, dispatch] = useReducer(promptReducer, { pending: [], journal: new Map() })
  const streamStatus = useEventStreamStatus()
  const previousStatus = useRef(streamStatus)
  const { applyEvent, reconcile, mounted } = usePromptReconciler(dispatch)

  useEffect(() => {
    mounted.current = true
    const unsubscribe = subscribeEvent('secrets', (data) => applyEvent(data as SecretPromptEvent))
    void reconcile()
    return () => {
      mounted.current = false
      unsubscribe()
    }
  }, [applyEvent, mounted, reconcile])

  useEffect(() => {
    const previous = previousStatus.current
    previousStatus.current = streamStatus
    if (streamStatus === 'open' && previous !== 'open' && previous !== 'idle') {
      void reconcile()
    }
  }, [reconcile, streamStatus])

  return { pending: state.pending, connected: streamStatus === 'open' }
}
