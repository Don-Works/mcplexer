import { useCallback, useEffect, useRef, useState } from 'react'
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

// useSecretPromptStream merges the initial /pending fetch with live events
// from the 'secrets' channel of the multiplexed event hub
// (hooks/use-event-stream.ts) so the modal queue stays in sync. file_path is
// never received here — the SSE channel is metadata-only by design.
export function useSecretPromptStream() {
  const [pending, setPending] = useState<SecretPrompt[]>([])
  const streamStatus = useEventStreamStatus()
  const mountedRef = useRef(false)
  const resolvedIdsRef = useRef(new Set<string>())
  const eventVersionsRef = useRef(new Map<string, number>())
  const eventVersionRef = useRef(0)
  const reconcileVersionRef = useRef(0)
  const previousStreamStatusRef = useRef(streamStatus)

  const applyEvent = useCallback((evt: SecretPromptEvent) => {
    const eventVersion = ++eventVersionRef.current
    eventVersionsRef.current.set(evt.id, eventVersion)

    if (evt.type === 'pending') {
      if (resolvedIdsRef.current.has(evt.id)) return
      const next: SecretPrompt = {
        id: evt.id,
        reason: evt.reason ?? '',
        label: evt.label ?? '',
        requester: evt.requester ?? '',
        status: evt.status ?? 'pending',
        expires_at: evt.expires_at ?? '',
        created_at: evt.created_at ?? '',
      }
      setPending((prev) => [...prev.filter((p) => p.id !== next.id), next])
      return
    }

    resolvedIdsRef.current.add(evt.id)
    setPending((prev) => prev.filter((p) => p.id !== evt.id))
  }, [])

  const reconcilePending = useCallback(async () => {
    const reconcileVersion = ++reconcileVersionRef.current
    const startedAtEventVersion = eventVersionRef.current
    try {
      const apiBase = import.meta.env.VITE_API_BASE_URL?.replace(/\/api\/v1$/, '') || ''
      const res = await fetch(`${apiBase}/api/v1/secrets/prompts/pending`, {
        signal: AbortSignal.timeout(30_000),
      })
      if (!res.ok) return
      const rows = (await res.json()) as SecretPrompt[]
      if (!mountedRef.current || reconcileVersion !== reconcileVersionRef.current) return

      setPending((prev) => {
        const next = new Map<string, SecretPrompt>()
        for (const row of rows) {
          if (row.status !== 'pending') {
            resolvedIdsRef.current.add(row.id)
          } else if (!resolvedIdsRef.current.has(row.id)) {
            next.set(row.id, row)
          }
        }
        for (const prompt of prev) {
          const eventVersion = eventVersionsRef.current.get(prompt.id) ?? 0
          if (
            eventVersion > startedAtEventVersion &&
            !resolvedIdsRef.current.has(prompt.id)
          ) {
            next.set(prompt.id, prompt)
          }
        }
        return [...next.values()]
      })
    } catch {
      return
    }
  }, [])

  useEffect(() => {
    mountedRef.current = true
    const unsub = subscribeEvent('secrets', (data) => {
      applyEvent(data as SecretPromptEvent)
    })
    void reconcilePending()

    return () => {
      mountedRef.current = false
      unsub()
    }
  }, [applyEvent, reconcilePending])

  useEffect(() => {
    const previous = previousStreamStatusRef.current
    previousStreamStatusRef.current = streamStatus
    if (streamStatus === 'open' && previous !== 'open' && previous !== 'idle') {
      void reconcilePending()
    }
  }, [reconcilePending, streamStatus])

  return { pending, connected: streamStatus === 'open' }
}
