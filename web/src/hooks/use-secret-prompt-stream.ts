import { useEffect, useState } from 'react'
import { subscribeEvent } from '@/hooks/use-event-stream'

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
  const [connected, setConnected] = useState(false)

  useEffect(() => {
    let cancelled = false

    async function loadInitial() {
      try {
        const apiBase =
          import.meta.env.VITE_API_BASE_URL?.replace(/\/api\/v1$/, '') || ''
        const res = await fetch(`${apiBase}/api/v1/secrets/prompts/pending`, {
          signal: AbortSignal.timeout(30_000),
        })
        if (!res.ok) return
        const rows = (await res.json()) as SecretPrompt[]
        if (!cancelled) setPending(rows)
      } catch {
        // best-effort initial fetch
      }
    }

    function applyEvent(evt: SecretPromptEvent) {
      if (evt.type === 'pending') {
        const next: SecretPrompt = {
          id: evt.id,
          reason: evt.reason ?? '',
          label: evt.label ?? '',
          requester: '',
          status: evt.status ?? 'pending',
          expires_at: evt.expires_at ?? '',
          created_at: evt.created_at ?? '',
        }
        setPending((prev) => {
          if (prev.some((p) => p.id === next.id)) return prev
          return [...prev, next]
        })
        return
      }
      if (evt.type === 'resolved') {
        setPending((prev) => prev.filter((p) => p.id !== evt.id))
      }
    }

    void loadInitial()
    const unsub = subscribeEvent('secrets', (data) => {
      applyEvent(data as SecretPromptEvent)
    })
    setConnected(true)

    return () => {
      cancelled = true
      unsub()
      setConnected(false)
    }
  }, [])

  return { pending, connected }
}
